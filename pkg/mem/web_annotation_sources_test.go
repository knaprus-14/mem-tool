package mem

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type webAnnotationSourceFixture struct {
	dir, mapID, sourcePath string
	store                  *Store
	annotation             DocumentAnnotation
	document               WebMapDocument
}

func newWebAnnotationSourceFixture(t *testing.T) webAnnotationSourceFixture {
	t.Helper()
	dir, store, mapID, nodeID, sourceID, sourcePath := mapDocumentFixture(t)
	ctx := context.Background()
	document, err := ReadWebMapDocumentIn(ctx, dir, mapID, nodeID, sourceID, 2)
	if err != nil || document.State != "current" || len(document.Passages) != 2 {
		t.Fatal(document, err)
	}
	var selected WebMapDocumentPassage
	for _, passage := range document.Passages {
		if passage.Match {
			selected = passage
		}
	}
	runes := []rune(selected.Text)
	if selected.EntryID <= 0 || selected.Pin == "" || len(runes) < 6 {
		t.Fatal("missing selectable passage", selected)
	}
	created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", DocumentAnnotationCreate{
		EntryID: selected.EntryID, Pin: selected.Pin, Start: 2, End: 5,
		Quote: string(runes[2:5]), Note: "Комментарий к части цитаты", Color: "blue", RequestID: strings.Repeat("a", 32),
	})
	if err != nil || created.Annotation.State != EvidenceCurrent || created.Annotation.Prefix != string(runes[:2]) || created.Annotation.Suffix != string(runes[5:]) {
		t.Fatal(created, err)
	}
	return webAnnotationSourceFixture{dir: dir, mapID: mapID, sourcePath: sourcePath, store: store, annotation: created.Annotation, document: document}
}

func assertWebAnnotationSourcePrivacy(t *testing.T, f webAnnotationSourceFixture, values ...any) {
	t.Helper()
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{f.dir, f.sourcePath, filepath.Dir(f.dir)} {
			path, _ := json.Marshal(private)
			if strings.Contains(string(encoded), string(path[1:len(path)-1])) {
				t.Fatalf("private path exposed: %s", encoded)
			}
		}
		if strings.Contains(string(encoded), `"source_path"`) || strings.Contains(string(encoded), `"embedding"`) {
			t.Fatalf("internal source metadata exposed: %s", encoded)
		}
	}
}

func TestWebAnnotationSourcePassagesSelectionAndMapIndependence(t *testing.T) {
	f := newWebAnnotationSourceFixture(t)
	ctx := context.Background()
	for _, passage := range f.document.Passages {
		hit, err := ReadDocumentAnnotationEntryIn(ctx, f.dir, passage.EntryID, passage.Pin)
		if err != nil || hit.Pin != passage.Pin || hit.Text != passage.Text || hit.Document != f.document.DocumentID || hit.Truncated {
			t.Fatal("passage cannot be reopened using its selection pin", passage, hit, err)
		}
		assertWebAnnotationSourcePrivacy(t, f, hit)
	}
	source, err := ReadDocumentAnnotationSourceIn(ctx, f.dir, f.annotation.ID)
	if err != nil || source.State != "current" || source.Excerpt != f.annotation.Quote || source.Title != "source.pdf" || !source.OriginalPDF || len(source.Passages) != 2 {
		t.Fatal(source, err)
	}
	if _, err = DeleteWebMapIn(ctx, f.dir, "alice", f.mapID, WebMapDelete{Revision: 2, RequestID: strings.Repeat("b", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadWebMapIn(ctx, f.dir, f.mapID); !errors.Is(err, ErrClassicMindMapNotFound) {
		t.Fatal("fixture map was not deleted", err)
	}
	after, err := ReadDocumentAnnotationSourceIn(ctx, f.dir, f.annotation.ID)
	if err != nil || !reflect.DeepEqual(source, after) {
		t.Fatal("annotation source depends on the deleted map", after, err)
	}
	saved, err := ReadDocumentAnnotationIn(ctx, f.dir, f.annotation.ID)
	if err != nil || saved != f.annotation {
		t.Fatal("deleting the map changed the annotation", saved, err)
	}
	assertWebAnnotationSourcePrivacy(t, f, f.document, source, after, saved)
}

func TestWebAnnotationSourceVerifiedPDFRevisionAndHistoricalQuote(t *testing.T) {
	f := newWebAnnotationSourceFixture(t)
	ctx := context.Background()
	original, err := os.ReadFile(f.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{false, true} {
		file, closeFile, err := OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, 1, local)
		if err != nil {
			t.Fatal("verified PDF unavailable", local, err)
		}
		data, readErr := io.ReadAll(file)
		snapshotPath := file.Name()
		closeFile()
		if readErr != nil || string(data) != string(original) || !strings.HasPrefix(string(data), "%PDF-") {
			t.Fatal("incorrect verified PDF", readErr, string(data))
		}
		if _, err = os.Stat(snapshotPath); !os.IsNotExist(err) {
			t.Fatal("temporary verified snapshot was not removed", err)
		}
	}
	for _, version := range []int64{0, 2} {
		file, closeFile, err := OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, version, false)
		if err == nil {
			closeFile()
			t.Fatal("incorrect annotation revision opened a PDF", version, file)
		}
		want := ErrDocumentAnnotationInvalid
		if version == 2 {
			want = ErrDocumentAnnotationConflict
		}
		if !errors.Is(err, want) {
			t.Fatal("wrong revision error", version, err)
		}
	}
	edited, err := UpdateDocumentAnnotationIn(ctx, f.dir, "alice", f.annotation.ID, DocumentAnnotationEdit{Revision: 1, Note: "Уточнено", Color: "pink", RequestID: strings.Repeat("b", 32)})
	if err != nil || edited.Annotation.Revision != 2 {
		t.Fatal(edited, err)
	}
	if _, closeFile, err := OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, 1, false); !errors.Is(err, ErrDocumentAnnotationConflict) {
		if closeFile != nil {
			closeFile()
		}
		t.Fatal("stale annotation revision opened a PDF", err)
	}
	file, closeFile, err := OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, 2, false)
	if err != nil {
		t.Fatal("current annotation revision did not open a PDF", err)
	}
	closeFile()
	if err = os.WriteFile(f.sourcePath, []byte("%PDF-1.4\nchanged bytes\n%%EOF"), 0600); err != nil {
		t.Fatal(err)
	}
	file, closeFile, err = OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, 2, false)
	if err == nil {
		closeFile()
		t.Fatal("changed physical PDF was served", file)
	}
	if !errors.Is(err, ErrSourceFileChanged) {
		t.Fatal("changed PDF error", err)
	}
	// Another document copy with matching logical coordinates remains in this
	// fixture. It must not stand in for the annotation's exact pinned path.
	if _, err = f.store.db.Exec(`DELETE FROM entries WHERE source_path=?`, f.sourcePath); err != nil {
		t.Fatal(err)
	}
	source, err := ReadDocumentAnnotationSourceIn(ctx, f.dir, f.annotation.ID)
	if err != nil || source.State != string(EvidenceMissing) || source.Excerpt != f.annotation.Quote || len(source.Passages) != 0 || source.OriginalPDF || source.Notice == "" {
		t.Fatal("missing source lost its saved quote or used another document copy", source, err)
	}
	saved, err := ReadDocumentAnnotationIn(ctx, f.dir, f.annotation.ID)
	if err != nil || saved.State != EvidenceMissing || saved.Quote != f.annotation.Quote || saved.Note != "Уточнено" || saved.Revision != 2 {
		t.Fatal(saved, err)
	}
	if _, closeFile, err = OpenDocumentAnnotationPDFIn(ctx, f.dir, f.annotation.ID, 2, false); !errors.Is(err, ErrDocumentAnnotationStale) {
		if closeFile != nil {
			closeFile()
		}
		t.Fatal("missing indexed source opened PDF", err)
	}
	assertWebAnnotationSourcePrivacy(t, f, source, saved)
}
