package mem

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func TestDocumentAnnotationRegionsPersistentPinsAndReplay(t *testing.T) {
	f := newWebAnnotationSourceFixture(t)
	ctx := context.Background()
	var hash string
	if err := f.store.db.QueryRow(`SELECT file_hash FROM document_source_files LIMIT 1`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	input := DocumentAnnotationRegionCreate{Revision: 1, FileHash: hash, X: .1, Y: .2, Width: .25, Height: .3, RequestID: strings.Repeat("1", 32)}
	var createdIDs []string
	for i := 0; i < 5; i++ {
		in := input
		in.RequestID = fmt.Sprintf("%032d", i+1)
		in.X += float64(i) * .05
		got, err := CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, in, true)
		if err != nil || got.Replayed || got.Region.State != EvidenceCurrent || got.Region.Page != 4 || got.Region.X != in.X {
			t.Fatal(got, err)
		}
		createdIDs = append(createdIDs, got.Region.ID)
	}
	regions, err := ListDocumentAnnotationRegionsIn(ctx, f.dir, f.annotation.ID)
	if err != nil || len(regions.Items) != 5 {
		t.Fatal(regions, err)
	}
	for i, r := range regions.Items {
		if r.FileHash != hash || r.AnnotationRevision != 1 || r.ID != createdIDs[i] {
			t.Fatal(r)
		}
	}
	assertWebAnnotationSourcePrivacy(t, f, regions)
	// Editing a comment never changes the saved page coordinates.
	if _, err = UpdateDocumentAnnotationIn(ctx, f.dir, "alice", f.annotation.ID, DocumentAnnotationEdit{Revision: 1, Note: "new note", Color: "pink", RequestID: strings.Repeat("2", 32)}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.sourcePath, []byte("%PDF-1.4 changed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	input.RequestID = fmt.Sprintf("%032d", 1)
	got, err := CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, input, true)
	if err != nil || !got.Replayed || got.Region.Width != input.Width {
		t.Fatal("late replay", got, err)
	}
	input.RequestID = strings.Repeat("3", 32)
	input.Revision = 2
	if _, err = CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, input, true); err == nil {
		t.Fatal("changed PDF accepted")
	}
	if _, err = f.store.db.Exec(`DELETE FROM entries WHERE source_path=?`, f.sourcePath); err != nil {
		t.Fatal(err)
	}
	regions, err = ListDocumentAnnotationRegionsIn(ctx, f.dir, f.annotation.ID)
	if err != nil || len(regions.Items) != 5 {
		t.Fatal(regions, err)
	}
	for _, r := range regions.Items {
		if r.State != EvidenceMissing {
			t.Fatal(r)
		}
	}
}

func TestDocumentAnnotationRegionsValidationImportAndRollback(t *testing.T) {
	f := newWebAnnotationSourceFixture(t)
	ctx := context.Background()
	var hash string
	f.store.db.QueryRow(`SELECT file_hash FROM document_source_files LIMIT 1`).Scan(&hash)
	base := DocumentAnnotationRegionCreate{Revision: 1, FileHash: hash, X: .1, Y: .2, Width: .2, Height: .3, RequestID: strings.Repeat("3", 32)}
	for _, n := range []float64{-1, 1.01, math.NaN(), math.Inf(1)} {
		in := base
		in.Width = n
		if _, err := CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, in, true); !errors.Is(err, ErrDocumentAnnotationInvalid) {
			t.Fatal(n, err)
		}
	}
	wrong := base
	wrong.FileHash = "sha256:" + strings.Repeat("0", 64)
	if _, err := CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, wrong, true); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal(err)
	}
	if _, err := CreateDocumentAnnotationRegionIn(ctx, f.dir, "alice", f.annotation.ID, base, true); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	parts, err := exportDocumentAnnotationRegionsTx(ctx, tx, f.annotation.ID)
	if err != nil || len(parts) != 1 {
		t.Fatal(parts, err)
	}
	parts[0].FileHash = "sha256:" + strings.Repeat("0", 64)
	if err = importDocumentAnnotationRegionsTx(ctx, tx, f.annotation.ID, parts, "bob"); err != nil {
		t.Fatal(err)
	}
	items, err := readAnnotationRegionsTx(ctx, tx, f.annotation.ID)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	var historical bool
	for _, r := range items {
		if r.FileHash == parts[0].FileHash {
			historical = true
			if r.State != EvidenceStale || r.ImportedBy != "bob" || r.OriginID == "" {
				t.Fatal(r)
			}
		}
	}
	if !historical {
		t.Fatal("lost historical region")
	}
	if _, err = tx.Exec(`UPDATE document_annotation_regions SET payload='{}'`); err == nil {
		t.Fatal("immutable region changed")
	}
	tx.Rollback()
	list, err := ListDocumentAnnotationRegionsIn(ctx, f.dir, f.annotation.ID)
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
}
