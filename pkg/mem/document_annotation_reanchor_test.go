package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func annotationReanchorSelection(t *testing.T, store *Store, path, text, quote string, revision int64) DocumentAnnotationReanchorPreviewInput {
	t.Helper()
	var entry Entry
	for _, row := range store.GetBySourceFile(path) {
		if row.Text == text {
			entry = row
		}
	}
	if entry.ID < 1 {
		t.Fatal("fixture entry not found")
	}
	pin, err := documentRestoreEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	index := strings.LastIndex(text, quote)
	if index < 0 {
		t.Fatal("fixture quotation not found")
	}
	start := utf8.RuneCountInString(text[:index])
	return DocumentAnnotationReanchorPreviewInput{Revision: revision, EntryID: entry.ID, Pin: pin, Start: start, End: start + utf8.RuneCountInString(quote), Quote: quote}
}
func annotationReanchorApplyInput(in DocumentAnnotationReanchorPreviewInput, digest, nonce string) DocumentAnnotationReanchorInput {
	return DocumentAnnotationReanchorInput{Revision: in.Revision, EntryID: in.EntryID, Pin: in.Pin, Start: in.Start, End: in.End, Quote: in.Quote, Digest: digest, RequestID: nonce}
}
func annotationReanchorCurrentFixture(t *testing.T) (string, *Store, DocumentAnnotation, DocumentAnnotationReanchorPreviewInput) {
	t.Helper()
	dir, store, original, _ := annotationTransferFixture(t)
	path := filepath.Join(dir, "private", "source.pdf")
	return dir, store, original, annotationReanchorSelection(t, store, path, annotationTransferTestText, annotationTransferTestQuote, original.Revision)
}

func TestDocumentAnnotationReanchorCreatesImmutableLineageAndPreservesOldObjects(t *testing.T) {
	ctx := context.Background()
	dir, store, old, _ := annotationReanchorCurrentFixture(t)
	mapDoc, err := store.CreateClassicMindMap("Аннотации", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AttachDocumentAnnotationToWebMapIn(ctx, dir, "reviewer", mapDoc.Map.ID, mapDoc.Nodes[0].ID, WebMapAnnotationAttach{Revision: mapDoc.Map.Revision, AnnotationID: old.ID, AnnotationRevision: old.Revision, RequestID: strings.Repeat("2", 32)}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	area := DocumentAnnotationRegionTransfer{OriginID: "area-" + strings.Repeat("1", 32), Page: old.Page, X: .1, Y: .2, Width: .3, Height: .4, FileHash: ChunkContentHash("original PDF"), Author: "area-author", Created: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = importDocumentAnnotationRegionsTx(ctx, tx, old.ID, []DocumentAnnotationRegionTransfer{area}, "fixture"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private", "source.pdf")
	text, quote := "😀 Новая редакция. Давление 6 бар. Повтор: Давление 6 бар.", "Давление 6 бар"
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
		chunks[i].Provenance.DocumentRevision = ChunkContentHash(text)
	}
	chunks[0].Text, chunks[0].Provenance.ChunkHash = text, ChunkContentHash(text)
	if err = store.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	selection := annotationReanchorSelection(t, store, path, text, quote, old.Revision)
	preview, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, selection)
	if err != nil || preview.Old.State != EvidenceStale || preview.Old.Quote != old.Quote || preview.Target.Quote != quote || preview.Target.Start != selection.Start || preview.Target.DocumentRevision == old.DocumentRevision || preview.RegionCount != 1 || preview.MapLinkCount != 1 || len(preview.Warnings) < 3 {
		t.Fatal(preview, err)
	}
	request := annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("3", 32))
	result, err := ReanchorDocumentAnnotationIn(ctx, dir, "operator", old.ID, request)
	if err != nil || result.Replayed || result.Annotation.ID == old.ID || result.Annotation.Revision != 1 || result.Annotation.State != EvidenceCurrent || result.Annotation.Archived || result.Annotation.Author != "operator" || result.Annotation.Note != old.Note || result.Annotation.Color != old.Color || result.Annotation.Quote != quote || result.Annotation.Start != selection.Start || result.Lineage.OldID != old.ID || result.Lineage.OldRevision != 1 || result.Lineage.NewID != result.Annotation.ID || result.Lineage.Actor != "operator" {
		t.Fatal(result, err)
	}
	oldAfter, err := ReadDocumentAnnotationIn(ctx, dir, old.ID)
	if err != nil || oldAfter != preview.Old {
		t.Fatal("old annotation was changed", oldAfter, err)
	}
	oldHistory, err := ReadDocumentAnnotationHistoryIn(ctx, dir, old.ID, 0, 50)
	if err != nil || oldHistory.Total != 1 {
		t.Fatal(oldHistory, err)
	}
	newHistory, err := ReadDocumentAnnotationHistoryIn(ctx, dir, result.Annotation.ID, 0, 50)
	if err != nil || newHistory.Total != 1 || newHistory.Items[0].Action != "reanchor" {
		t.Fatal(newHistory, err)
	}
	oldAreas, err := ListDocumentAnnotationRegionsIn(ctx, dir, old.ID)
	if err != nil || len(oldAreas.Items) != 1 || oldAreas.Items[0].FileHash != area.FileHash {
		t.Fatal(oldAreas, err)
	}
	newAreas, err := ListDocumentAnnotationRegionsIn(ctx, dir, result.Annotation.ID)
	if err != nil || len(newAreas.Items) != 0 {
		t.Fatal("areas silently moved", newAreas, err)
	}
	oldLinks, err := ListDocumentAnnotationMapLinksIn(ctx, dir, old.ID)
	if err != nil || oldLinks.Total != 1 {
		t.Fatal(oldLinks, err)
	}
	newLinks, err := ListDocumentAnnotationMapLinksIn(ctx, dir, result.Annotation.ID)
	if err != nil || newLinks.Total != 0 {
		t.Fatal("links silently moved", newLinks, err)
	}
	for _, id := range []string{old.ID, result.Annotation.ID} {
		lineage, err := ListDocumentAnnotationLineageIn(ctx, dir, id, 0, 50)
		if err != nil || lineage.Total != 1 || len(lineage.Items) != 1 || lineage.Items[0] != result.Lineage {
			t.Fatal(lineage, err)
		}
	}
	for _, value := range []any{preview, result, newHistory} {
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "source_path") || strings.Contains(string(raw), "private") {
			t.Fatal("path leak", string(raw), err)
		}
	}
	if _, err = store.db.Exec(`UPDATE document_annotation_lineage SET old_id=new_id`); err == nil {
		t.Fatal("mutable lineage")
	}
	if _, err = store.db.Exec(`DELETE FROM document_annotation_lineage`); err == nil {
		t.Fatal("deletable lineage")
	}
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "editor", result.Annotation.ID, DocumentAnnotationEdit{Revision: 1, Note: "edited new note", Color: "pink", RequestID: strings.Repeat("4", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "editor", old.ID, DocumentAnnotationEdit{Revision: 1, Note: "edited old note", Color: "green", RequestID: strings.Repeat("5", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DELETE FROM entries`); err != nil {
		t.Fatal(err)
	}
	retry, err := ReanchorDocumentAnnotationIn(ctx, dir, "operator", old.ID, request)
	if err != nil || !retry.Replayed || retry.Annotation.ID != result.Annotation.ID || retry.Annotation.Revision != 1 || retry.Annotation.Note != old.Note || retry.Annotation.State != EvidenceMissing || retry.Lineage != result.Lineage {
		t.Fatal("retry lost confirmed result", retry, err)
	}
	current, err := ReadDocumentAnnotationIn(ctx, dir, result.Annotation.ID)
	if err != nil || current.Revision != 2 || current.Note != "edited new note" {
		t.Fatal(current, err)
	}
	if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "another-actor", old.ID, request); !errors.Is(err, ErrDocumentAnnotationRequest) {
		t.Fatal(err)
	}
}

func TestDocumentAnnotationReanchorUnboundNeedsExplicitSelection(t *testing.T) {
	ctx := context.Background()
	_, _, _, content := annotationTransferFixture(t)
	dir, store, path := annotationTransferDestination(t, "")
	_, imported := annotationTransferApply(t, dir, content, strings.Repeat("c", 32))
	oldID := imported.Items[0].ID
	old, err := ReadDocumentAnnotationIn(ctx, dir, oldID)
	if err != nil || old.State != EvidenceMissing || !old.Archived {
		t.Fatal(old, err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
		chunks[i].Provenance.DocumentRevision = ChunkContentHash(annotationTransferTestText)
	}
	chunks[0].Text, chunks[0].Provenance.ChunkHash = annotationTransferTestText, ChunkContentHash(annotationTransferTestText)
	if err = store.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	unchanged, err := ReadDocumentAnnotationIn(ctx, dir, oldID)
	if err != nil || unchanged != old {
		t.Fatal("automatic reanchor", unchanged, err)
	}
	selection := annotationReanchorSelection(t, store, path, annotationTransferTestText, annotationTransferTestQuote, 1)
	preview, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, oldID, selection)
	if err != nil || len(preview.Warnings) < 4 {
		t.Fatal(preview, err)
	}
	result, err := ReanchorDocumentAnnotationIn(ctx, dir, "operator", oldID, annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("d", 32)))
	if err != nil || result.Annotation.State != EvidenceCurrent || result.Annotation.Archived || result.Annotation.DocumentID != LibraryDocumentID(path, "doc-book") || result.Annotation.Start == old.Start {
		t.Fatal(result, err)
	}
	// The first and second identical quotations have independent exact offsets.
	if result.Annotation.Quote != old.Quote || result.Annotation.Start <= old.Start || result.Annotation.Prefix == old.Prefix {
		t.Fatal(result)
	}
	still, err := ReadDocumentAnnotationIn(ctx, dir, oldID)
	if err != nil || still != old {
		t.Fatal(still, err)
	}
}

func TestDocumentAnnotationReanchorPreviewPinsOldTargetAndVisibleRelations(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"old-edit", "target-edit", "target-delete", "old-state", "add-region", "add-link", "add-knowledge-link", "digest", "duplicate-target"} {
		t.Run(kind, func(t *testing.T) {
			dir, store, old, selection := annotationReanchorCurrentFixture(t)
			if kind == "old-state" {
				path := filepath.Join(dir, "private", "source.pdf")
				selection = annotationReanchorSelection(t, store, path, "chunk-1", "chunk", old.Revision)
			}
			preview, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, selection)
			if err != nil {
				t.Fatal(err)
			}
			in := annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("b", 32))
			want := ErrDocumentAnnotationReanchorPreview
			switch kind {
			case "old-edit":
				_, err = UpdateDocumentAnnotationIn(ctx, dir, "editor", old.ID, DocumentAnnotationEdit{Revision: 1, Note: "changed", Color: "blue", RequestID: strings.Repeat("d", 32)})
				want = ErrDocumentAnnotationConflict
			case "target-edit":
				_, err = store.db.Exec(`UPDATE entries SET text=text||' changed' WHERE id=?`, selection.EntryID)
				want = ErrDocumentAnnotationStale
			case "target-delete":
				_, err = store.db.Exec(`DELETE FROM entries WHERE id=?`, selection.EntryID)
				want = ErrDocumentAnnotationStale
			case "old-state":
				_, err = store.db.Exec(`DELETE FROM entries WHERE block_chunk_index=0`)
			case "add-region":
				var txErr error
				tx, e := store.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				txErr = importDocumentAnnotationRegionsTx(ctx, tx, old.ID, []DocumentAnnotationRegionTransfer{{OriginID: "area-" + strings.Repeat("a", 32), Page: old.Page, X: 0, Y: 0, Width: 1, Height: 1, FileHash: ChunkContentHash("PDF"), Author: "author", Created: time.Now().UTC().Format(time.RFC3339Nano)}}, "actor")
				if txErr != nil {
					tx.Rollback()
					t.Fatal(txErr)
				}
				err = tx.Commit()
			case "add-link":
				doc, e := store.CreateClassicMindMap("Map", "")
				if e != nil {
					t.Fatal(e)
				}
				_, err = AttachDocumentAnnotationToWebMapIn(ctx, dir, "actor", doc.Map.ID, doc.Nodes[0].ID, WebMapAnnotationAttach{Revision: doc.Map.Revision, AnnotationID: old.ID, AnnotationRevision: old.Revision, RequestID: strings.Repeat("c", 32)})
			case "digest":
				in.Digest = ChunkContentHash("not previewed")
			case "add-knowledge-link":
				_, err = CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "actor", old.ID, DocumentAnnotationKnowledgeCreate{Revision: old.Revision, Kind: KnowledgeNodeNote, Label: "Linked note", Body: "Working thought", RequestID: strings.Repeat("f", 32)})
			case "duplicate-target":
				_, err = store.db.Exec(`UPDATE entries SET block_chunk_index=0 WHERE id!=?`, selection.EntryID)
				want = ErrDocumentAnnotationStale
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "operator", old.ID, in); !errors.Is(err, want) {
				t.Fatal(kind, err, want)
			}
			var annotations, lineage int
			if err = store.db.QueryRow(`SELECT COUNT(*) FROM document_annotations`).Scan(&annotations); err != nil || annotations != 1 {
				t.Fatal(annotations, err)
			}
			if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='document_annotation_lineage'`).Scan(&lineage); err != nil || lineage != 0 {
				t.Fatal("rejected apply wrote lineage", lineage, err)
			}
		})
	}
}

func TestDocumentAnnotationReanchorValidationReadOnlyPaginationAndCancellation(t *testing.T) {
	ctx := context.Background()
	dir, store, old, selection := annotationReanchorCurrentFixture(t)
	empty, err := ListDocumentAnnotationLineageIn(ctx, dir, old.ID, 0, 0)
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 || empty.Limit != 50 {
		t.Fatal(empty, err)
	}
	preview, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, selection)
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='document_annotation_lineage'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("read created schema", tables, err)
	}
	for _, change := range []func(*DocumentAnnotationReanchorPreviewInput){
		func(x *DocumentAnnotationReanchorPreviewInput) { x.Revision = 0 }, func(x *DocumentAnnotationReanchorPreviewInput) { x.EntryID = 0 }, func(x *DocumentAnnotationReanchorPreviewInput) { x.Pin = "bad" }, func(x *DocumentAnnotationReanchorPreviewInput) { x.Quote = string([]byte{255}) }, func(x *DocumentAnnotationReanchorPreviewInput) { x.Start = -1 }, func(x *DocumentAnnotationReanchorPreviewInput) { x.End = x.Start }, func(x *DocumentAnnotationReanchorPreviewInput) {
			x.Quote = strings.Repeat("я", 8001)
			x.End = x.Start + 8001
		},
	} {
		bad := selection
		change(&bad)
		if _, err = PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, bad); !errors.Is(err, ErrDocumentAnnotationReanchorInvalid) {
			t.Fatal(err)
		}
		if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "operator", old.ID, annotationReanchorApplyInput(bad, preview.Digest, strings.Repeat("b", 32))); !errors.Is(err, ErrDocumentAnnotationReanchorInvalid) {
			t.Fatal(err)
		}
	}
	bad := selection
	bad.Start++
	bad.End++
	if _, err = PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, bad); !errors.Is(err, ErrDocumentAnnotationReanchorInvalid) {
		t.Fatal("mismatched exact range", err)
	}
	for i := 0; i < 3; i++ {
		if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "operator", old.ID, annotationReanchorApplyInput(selection, preview.Digest, fmt.Sprintf("%032x", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	page, err := ListDocumentAnnotationLineageIn(ctx, dir, old.ID, 1, 1)
	if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Offset != 1 || page.Limit != 1 {
		t.Fatal(page, err)
	}
	if _, err = ListDocumentAnnotationLineageIn(ctx, dir, old.ID, -1, 50); !errors.Is(err, ErrDocumentAnnotationInvalid) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = PreviewDocumentAnnotationReanchorIn(cancelled, dir, old.ID, selection); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = ReanchorDocumentAnnotationIn(cancelled, dir, "operator", old.ID, annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("f", 32))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = ListDocumentAnnotationLineageIn(cancelled, dir, old.ID, 0, 50); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDocumentAnnotationReanchorQuotasRollbackAndReplay(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"records", "history", "receipts", "lineage-count", "lineage-bytes", "chunk"} {
		t.Run(kind, func(t *testing.T) {
			dir, store, old, selection := annotationReanchorCurrentFixture(t)
			p, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, old.ID, selection)
			if err != nil {
				t.Fatal(err)
			}
			in := annotationReanchorApplyInput(selection, p.Digest, strings.Repeat("b", 32))
			created, err := ReanchorDocumentAnnotationIn(ctx, dir, "actor", old.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "records":
				_, err = store.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9998) INSERT INTO document_annotations SELECT 'ann-'||printf('%032x',x),revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,1,author,created,updated FROM document_annotations,n WHERE id=?`, old.ID)
			case "history":
				_, err = store.db.Exec(`INSERT INTO document_annotation_history(annotation_id,revision,action,actor,created,payload,payload_hash) VALUES('quota',1,'quota','test','now',CAST(zeroblob(?) AS TEXT),'x')`, maxDocumentAnnotationHistoryBytes)
			case "receipts":
				_, err = store.db.Exec(`INSERT INTO document_annotation_receipts VALUES('quota',CAST(zeroblob(?) AS TEXT),'x',1,'x')`, maxDocumentAnnotationReceiptBytes)
			case "lineage-count":
				_, err = store.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO document_annotation_lineage SELECT printf('%032x',x),'quota',printf('%032x',x),'x','x' FROM n`)
			case "lineage-bytes":
				_, err = store.db.Exec(`INSERT INTO document_annotation_lineage VALUES('quota','quota','quota',CAST(zeroblob(?) AS TEXT),'x')`, maxAnnotationLineageBytes)
			case "chunk":
				_, err = store.db.Exec(`UPDATE entries SET text=CAST(zeroblob(?) AS TEXT) WHERE id=?`, maxDocumentAnnotationChunkBytes+1, selection.EntryID)
			}
			if err != nil {
				t.Fatal(err)
			}
			retry, err := ReanchorDocumentAnnotationIn(ctx, dir, "actor", old.ID, in)
			if err != nil || !retry.Replayed || retry.Annotation.ID != created.Annotation.ID || retry.Lineage != created.Lineage {
				t.Fatal(retry, err)
			}
			before := []int{}
			after := []int{}
			tables := []string{"document_annotations", "document_annotation_history", "document_annotation_receipts", "document_annotation_lineage"}
			for _, table := range tables {
				var n int
				if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				before = append(before, n)
			}
			other := in
			other.RequestID = strings.Repeat("c", 32)
			if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "actor", old.ID, other); !errors.Is(err, ErrDocumentAnnotationLimit) {
				t.Fatal("quota accepted", err)
			}
			for _, table := range tables {
				var n int
				if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				after = append(after, n)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial write", before, after)
			}
		})
	}
}
