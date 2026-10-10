package mem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const annotationTransferTestText = "😀 До цитаты. Давление 5 бар. После цитаты: Давление 5 бар."
const annotationTransferTestQuote = "Давление 5 бар"

func annotationTransferFixture(t *testing.T) (string, *Store, DocumentAnnotation, string) {
	t.Helper()
	dir, store, create := documentAnnotationFixture(t, annotationTransferTestText, annotationTransferTestQuote)
	created, err := CreateDocumentAnnotationIn(context.Background(), dir, "origin-author", create)
	if err != nil {
		t.Fatal(err)
	}
	export, err := ExportDocumentAnnotationsIn(context.Background(), dir, DocumentAnnotationExportRequest{IDs: []string{created.Annotation.ID}, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	return dir, store, created.Annotation, string(export.Data)
}

func annotationTransferDestination(t *testing.T, text string) (string, *Store, string) {
	t.Helper()
	dir, store, _ := queryFixture(t)
	path := filepath.Join(dir, "other-private-folder", "source.pdf")
	if text != "" {
		chunks := validStructuredChunks()
		for i := range chunks {
			chunks[i].Provenance.SourcePath = path
			chunks[i].Provenance.DocumentRevision = ChunkContentHash(text)
		}
		chunks[0].Text, chunks[0].Provenance.ChunkHash = text, ChunkContentHash(text)
		if err := store.ReplaceDocumentChunks(path, chunks); err != nil {
			t.Fatal(err)
		}
	}
	return dir, store, path
}

func annotationTransferApply(t *testing.T, dir, content, nonce string) (DocumentAnnotationImportPreview, DocumentAnnotationImportResult) {
	t.Helper()
	ctx := context.Background()
	preview, err := PreviewDocumentAnnotationImportIn(ctx, dir, DocumentAnnotationImportPreviewInput{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportDocumentAnnotationsIn(ctx, dir, "destination-actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: nonce})
	if err != nil {
		t.Fatal(err)
	}
	return preview, result
}

func TestDocumentAnnotationTransferCanonicalPrivacyRoundTripAndExactRetry(t *testing.T) {
	ctx := context.Background()
	dir, store, original, content := annotationTransferFixture(t)
	jsonAgain, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{original.ID}, Format: "json"})
	if err != nil || string(jsonAgain.Data) != content || !strings.Contains(jsonAgain.Filename, jsonAgain.Digest[7:19]) {
		t.Fatal(jsonAgain, err)
	}
	markdown, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{original.ID}, Format: "markdown"})
	if err != nil || !strings.Contains(string(markdown.Data), "> "+annotationTransferTestQuote) || !strings.Contains(string(markdown.Data), "Ручная заметка") || !strings.Contains(string(markdown.Data), original.CitationID) || markdown.Digest != jsonAgain.Digest {
		t.Fatal(markdown, err)
	}
	for _, data := range [][]byte{jsonAgain.Data, markdown.Data} {
		if bytes.Contains(data, []byte("source_path")) || bytes.Contains(data, []byte("private")) || bytes.Contains(data, []byte(filepath.ToSlash(dir))) || bytes.Contains(data, []byte(strings.ReplaceAll(dir, `\`, `\\`))) {
			t.Fatalf("locator leaked: %s", data)
		}
	}
	preview, imported := annotationTransferApply(t, dir, content, strings.Repeat("b", 32))
	if preview.Current != 1 || preview.Missing != 0 || preview.Count != 1 || len(preview.Items) != 1 || preview.Items[0].Quote != original.Quote || len(preview.Warnings) < 2 || imported.Imported != 1 || imported.Current != 1 || imported.Replayed || imported.Items[0].ID == original.ID {
		t.Fatal(preview, imported)
	}
	id := imported.Items[0].ID
	copy, err := ReadDocumentAnnotationIn(ctx, dir, id)
	if err != nil || copy.Author != original.Author || copy.Created != original.Created || copy.Updated != original.Updated || copy.Note != original.Note || copy.Quote != original.Quote || copy.Start != original.Start || copy.DocumentID != original.DocumentID || copy.State != EvidenceCurrent {
		t.Fatal(copy, err)
	}
	history, err := ReadDocumentAnnotationHistoryIn(ctx, dir, id, 0, 50)
	if err != nil || history.Total != 1 || history.Items[0].Actor != "destination-actor" || history.Items[0].Action != "import" || history.Items[0].Annotation.ID != id {
		t.Fatal(history, err)
	}
	var payload string
	if err = store.db.QueryRow(`SELECT payload FROM document_annotation_history WHERE annotation_id=?`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"transfer_origin"`) || !strings.Contains(payload, original.ID) || !strings.Contains(payload, jsonAgain.Digest) {
		t.Fatal(payload)
	}
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "reviewer", id, DocumentAnnotationEdit{Revision: 1, Note: "New local note", Color: "green", RequestID: strings.Repeat("c", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DELETE FROM entries`); err != nil {
		t.Fatal(err)
	}
	retry, err := ImportDocumentAnnotationsIn(ctx, dir, "destination-actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: strings.Repeat("b", 32)})
	if err != nil || !retry.Replayed || retry.Items[0] != imported.Items[0] || retry.Current != imported.Current {
		t.Fatal(retry, err)
	}
	read, err := ReadDocumentAnnotationIn(ctx, dir, id)
	if err != nil || read.Revision != 2 || read.Note != "New local note" || read.State != EvidenceMissing {
		t.Fatal(read, err)
	}
	unchanged, err := ReadDocumentAnnotationIn(ctx, dir, original.ID)
	if err != nil || unchanged.Revision != original.Revision || unchanged.Note != original.Note {
		t.Fatal(unchanged, err)
	}
	if _, err = ImportDocumentAnnotationsIn(ctx, dir, "another-actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: strings.Repeat("b", 32)}); !errors.Is(err, ErrDocumentAnnotationRequest) {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE document_annotation_import_receipts SET created='changed'`); err == nil {
		t.Fatal("mutable receipt")
	}
}

func TestDocumentAnnotationTransferCrossDatabaseCurrentStaleAndUnbound(t *testing.T) {
	ctx := context.Background()
	_, _, original, content := annotationTransferFixture(t)
	for _, kind := range []string{"current", "stale", "missing", "ambiguous", "corrupt", "duplicate-coordinates", "wrong-range", "wrong-context"} {
		t.Run(kind, func(t *testing.T) {
			text := annotationTransferTestText
			if kind == "stale" {
				text = "Новая редакция. " + annotationTransferTestQuote
			}
			if kind == "missing" {
				text = ""
			}
			dir, store, localPath := annotationTransferDestination(t, text)
			var err error
			switch kind {
			case "ambiguous":
				_, err = store.db.Exec(`UPDATE entries SET source_path=? WHERE block_chunk_index=1`, filepath.Join(dir, "ambiguous.pdf"))
			case "corrupt":
				_, err = store.db.Exec(`UPDATE entries SET text='corrupt same hash' WHERE block_chunk_index=0`)
			case "duplicate-coordinates":
				_, err = store.db.Exec(`UPDATE entries SET block_chunk_index=0`)
			}
			if err != nil {
				t.Fatal(err)
			}
			input := content
			if kind == "wrong-range" || kind == "wrong-context" {
				pack, e := parseAnnotationTransfer(content)
				if e != nil {
					t.Fatal(e)
				}
				if kind == "wrong-range" {
					pack.Annotations[0].Start++
					pack.Annotations[0].End++
				} else {
					pack.Annotations[0].Prefix = "Несовпавший контекст"
				}
				_, raw, e := canonicalAnnotationTransferPack(pack)
				if e != nil {
					t.Fatal(e)
				}
				input = string(raw)
			}
			preview, result := annotationTransferApply(t, dir, input, strings.Repeat("d", 32))
			read, err := ReadDocumentAnnotationIn(ctx, dir, result.Items[0].ID)
			if err != nil || read.Quote != original.Quote || read.Note != original.Note || read.DocumentRevision != original.DocumentRevision || read.Start != previewStart(t, input) {
				t.Fatal(read, err)
			}
			want := EvidenceMissing
			if kind == "current" {
				want = EvidenceCurrent
			}
			if kind == "stale" {
				want = EvidenceStale
			}
			if read.State != want || preview.Items[0].State != want || read.SourceName != original.SourceName {
				t.Fatal(read, preview)
			}
			if kind == "current" || kind == "stale" {
				if read.Archived || read.DocumentID != LibraryDocumentID(localPath, "doc-book") || read.DocumentID == original.DocumentID {
					t.Fatal(read)
				}
			} else {
				if !read.Archived || preview.Missing != 1 || preview.Archived != 1 || !strings.Contains(preview.Items[0].Reason, "без привязки") {
					t.Fatal(read, preview)
				}
				var rawAnchor string
				if err = store.db.QueryRow(`SELECT anchor_json FROM document_annotations WHERE id=?`, read.ID).Scan(&rawAnchor); err != nil {
					t.Fatal(err)
				}
				var anchor EvidenceAnchor
				if err = json.Unmarshal([]byte(rawAnchor), &anchor); err != nil || !strings.HasPrefix(anchor.SourcePath, documentAnnotationUnboundPrefix) {
					t.Fatal(anchor, err)
				}
				// Even later matching data cannot turn an unbound archived quote into
				// a real source. This guard precedes every source/file access.
				if text == "" {
					chunks := validStructuredChunks()
					for i := range chunks {
						chunks[i].Provenance.SourcePath = localPath
						chunks[i].Provenance.DocumentRevision = ChunkContentHash(annotationTransferTestText)
					}
					chunks[0].Text, chunks[0].Provenance.ChunkHash = annotationTransferTestText, ChunkContentHash(annotationTransferTestText)
					if err = store.ReplaceDocumentChunks(localPath, chunks); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = store.db.Exec(`UPDATE entries SET source_path=?,text=?,document_revision=?,chunk_hash=? WHERE block_chunk_index=0`, anchor.SourcePath, annotationTransferTestText, anchor.DocumentRevision, anchor.ChunkHash); err != nil {
					t.Fatal(err)
				}
				read, err = ReadDocumentAnnotationIn(ctx, dir, read.ID)
				if err != nil || read.State != EvidenceMissing {
					t.Fatal(read, err)
				}
				if file, release, err := OpenDocumentAnnotationPDFIn(ctx, dir, read.ID, read.Revision, true); !errors.Is(err, ErrDocumentAnnotationStale) {
					if release != nil {
						release()
					}
					t.Fatal(file, err)
				}
				again, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{read.ID}, Format: "json"})
				if err != nil || strings.Contains(string(again.Data), "annotation-unbound") {
					t.Fatal(string(again.Data), err)
				}
			}
		})
	}
}

func previewStart(t *testing.T, content string) int {
	t.Helper()
	pack, err := parseAnnotationTransfer(content)
	if err != nil {
		t.Fatal(err)
	}
	return pack.Annotations[0].Start
}

func TestDocumentAnnotationTransferRejectsChangedPreviewAndMalformedPackageWithoutWrites(t *testing.T) {
	ctx := context.Background()
	_, _, _, content := annotationTransferFixture(t)
	dir, store, _ := annotationTransferDestination(t, annotationTransferTestText)
	preview, err := PreviewDocumentAnnotationImportIn(ctx, dir, DocumentAnnotationImportPreviewInput{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'document_annotation%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("preview created schema", tables, err)
	}
	if _, err = store.db.Exec(`UPDATE entries SET document_revision=?`, ChunkContentHash("new revision")); err != nil {
		t.Fatal(err)
	}
	if _, err = ImportDocumentAnnotationsIn(ctx, dir, "actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: strings.Repeat("a", 32)}); !errors.Is(err, ErrDocumentAnnotationTransferPreview) {
		t.Fatal(err)
	}
	pack, err := parseAnnotationTransfer(content)
	if err != nil {
		t.Fatal(err)
	}
	malformed := []string{
		content + "{}", strings.Replace(content, `"format":`, `"extra":1,"format":`, 1), strings.Replace(content, `"format":`, `"format":"duplicate","format":`, 1), strings.Replace(content, `"format":`, `"Format":`, 1), strings.Replace(content, `"archived":false`, `"archived":null`, 1), strings.Replace(content, `"note":`, `"source_path":"C:/private/secret.pdf","note":`, 1), strings.Replace(content, `"Ручная заметка"`, `"\ud800"`, 1), strings.Repeat(" ", MaxDocumentAnnotationTransferBytes+1), strings.Replace(content, pack.Digest, ChunkContentHash("bad digest"), 1),
	}
	for _, mutate := range []func(*documentAnnotationTransferItem){
		func(x *documentAnnotationTransferItem) { x.SourceName = "../private.pdf" }, func(x *documentAnnotationTransferItem) { x.SourceName = `C:\private.pdf` }, func(x *documentAnnotationTransferItem) { x.SourceName = "line\nsecret.pdf" }, func(x *documentAnnotationTransferItem) { x.SourceName = strings.Repeat("я", 257) }, func(x *documentAnnotationTransferItem) { x.DocumentID = "C:/secret" }, func(x *documentAnnotationTransferItem) { x.Quote = "wrong quote" }, func(x *documentAnnotationTransferItem) { x.Note = strings.Repeat("я", 8001) }, func(x *documentAnnotationTransferItem) { x.Start = -1 }, func(x *documentAnnotationTransferItem) { x.OriginID = "../../origin" },
	} {
		bad, e := parseAnnotationTransfer(content)
		if e != nil {
			t.Fatal(e)
		}
		mutate(&bad.Annotations[0])
		raw, _ := json.Marshal(bad)
		malformed = append(malformed, string(raw))
	}
	bad := pack
	bad.Annotations = append(bad.Annotations, bad.Annotations[0])
	raw, _ := json.Marshal(bad)
	malformed = append(malformed, string(raw))
	bad.Annotations = make([]documentAnnotationTransferItem, 201)
	for i := range bad.Annotations {
		bad.Annotations[i] = pack.Annotations[0]
		bad.Annotations[i].OriginID = fmt.Sprintf("ann-%032x", i)
	}
	raw, _ = json.Marshal(bad)
	malformed = append(malformed, string(raw))
	for i, input := range malformed {
		if _, err = PreviewDocumentAnnotationImportIn(ctx, dir, DocumentAnnotationImportPreviewInput{Content: input}); !errors.Is(err, ErrDocumentAnnotationTransferInvalid) {
			t.Fatalf("preview malformed %d: %v", i, err)
		}
		if _, err = ImportDocumentAnnotationsIn(ctx, dir, "actor", DocumentAnnotationImportInput{Content: input, Digest: preview.Digest, RequestID: strings.Repeat("b", 32)}); !errors.Is(err, ErrDocumentAnnotationTransferInvalid) {
			t.Fatalf("apply malformed %d: %v", i, err)
		}
	}
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'document_annotation%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("failure left schema", tables, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = PreviewDocumentAnnotationImportIn(cancelled, dir, DocumentAnnotationImportPreviewInput{Content: content}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = ImportDocumentAnnotationsIn(cancelled, dir, "actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: strings.Repeat("f", 32)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDocumentAnnotationTransferRegionsPreserveHistoricalPinsAndOrigin(t *testing.T) {
	ctx := context.Background()
	sourceDir, sourceStore, original, _ := annotationTransferFixture(t)
	areas := []DocumentAnnotationRegionTransfer{{OriginID: "area-" + strings.Repeat("a", 32), Page: original.Page, X: .1, Y: .2, Width: .3, Height: .4, FileHash: ChunkContentHash("current PDF"), Author: "source-area-author", Created: time.Now().UTC().Format(time.RFC3339Nano)}, {OriginID: "area-" + strings.Repeat("b", 32), Page: original.Page, X: .05, Y: .05, Width: .1, Height: .1, FileHash: ChunkContentHash("historical PDF"), Author: "past-author", Created: time.Now().UTC().Format(time.RFC3339Nano)}}
	tx, err := sourceStore.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = importDocumentAnnotationRegionsTx(ctx, tx, original.ID, areas, "fixture-importer"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	export, err := ExportDocumentAnnotationsIn(ctx, sourceDir, DocumentAnnotationExportRequest{IDs: []string{original.ID}, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	dir, store, path := annotationTransferDestination(t, annotationTransferTestText)
	if _, err = store.db.Exec(`INSERT INTO document_source_files VALUES(?,?,?,?)`, "doc-book", original.DocumentRevision, path, areas[0].FileHash); err != nil {
		t.Fatal(err)
	}
	preview, result := annotationTransferApply(t, dir, string(export.Data), strings.Repeat("d", 32))
	if preview.RegionCount != 2 || len(preview.Warnings) < 3 {
		t.Fatal(preview)
	}
	got, err := ListDocumentAnnotationRegionsIn(ctx, dir, result.Items[0].ID)
	if err != nil || len(got.Items) != 2 {
		t.Fatal(got, err)
	}
	for _, r := range got.Items {
		if r.ImportedBy != "destination-actor" || r.ID == r.OriginID || r.OriginID == "" || r.Page != original.Page {
			t.Fatal(r)
		}
		if r.FileHash == areas[0].FileHash && (r.State != EvidenceCurrent || r.Author != areas[0].Author) {
			t.Fatal(r)
		}
		if r.FileHash == areas[1].FileHash && (r.State != EvidenceStale || r.Author != areas[1].Author) {
			t.Fatal(r)
		}
	}
	repeat, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{result.Items[0].ID}, Format: "json"})
	if err != nil || !bytes.Contains(repeat.Data, []byte(areas[1].FileHash)) || bytes.Contains(repeat.Data, []byte("source_path")) {
		t.Fatal(string(repeat.Data), err)
	}
	pack, err := parseAnnotationTransfer(string(export.Data))
	if err != nil {
		t.Fatal(err)
	}
	pack.Annotations[0].Regions[0].Page++
	if _, _, err = canonicalAnnotationTransferPack(pack); !errors.Is(err, ErrDocumentAnnotationTransferInvalid) {
		t.Fatal(err)
	}
}

func TestDocumentAnnotationTransferBatchQuotaRollbackAndReceiptBeforeLimits(t *testing.T) {
	ctx := context.Background()
	_, _, _, content := annotationTransferFixture(t)
	for _, kind := range []string{"records", "live-bytes", "history-bytes", "receipt-bytes", "receipt-count", "regions"} {
		t.Run(kind, func(t *testing.T) {
			dir, store, _ := annotationTransferDestination(t, annotationTransferTestText)
			preview, result := annotationTransferApply(t, dir, content, strings.Repeat("a", 32))
			id := result.Items[0].ID
			var err error
			switch kind {
			case "records":
				_, err = store.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO document_annotations SELECT 'ann-'||printf('%032x',x),revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,1,author,created,updated FROM document_annotations,n WHERE id=?`, id)
			case "live-bytes":
				_, err = store.db.Exec(`INSERT INTO document_annotations SELECT 'ann-'||printf('%032x',1),revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,CAST(zeroblob(?) AS TEXT),color,1,author,created,updated FROM document_annotations WHERE id=?`, maxDocumentAnnotationBytes, id)
			case "history-bytes":
				_, err = store.db.Exec(`INSERT INTO document_annotation_history(annotation_id,revision,action,actor,created,payload,payload_hash) VALUES('quota',1,'quota','test','now',CAST(zeroblob(?) AS TEXT),'x')`, maxDocumentAnnotationHistoryBytes)
			case "receipt-bytes":
				_, err = store.db.Exec(`INSERT INTO document_annotation_import_receipts VALUES('quota','x',CAST(zeroblob(?) AS TEXT),'x','x')`, maxDocumentAnnotationReceiptBytes)
			case "receipt-count":
				_, err = store.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<99999) INSERT INTO document_annotation_import_receipts SELECT printf('%032x',x),'x','x','x','x' FROM n`)
			case "regions":
				_, err = store.db.Exec(annotationRegionsSchema)
				if err == nil {
					_, err = store.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO document_annotation_regions SELECT printf('%032x',x),'quota','x','x' FROM n`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			retry, err := ImportDocumentAnnotationsIn(ctx, dir, "destination-actor", DocumentAnnotationImportInput{Content: content, Digest: preview.Digest, RequestID: strings.Repeat("a", 32)})
			if err != nil || !retry.Replayed || retry.Items[0].ID != id {
				t.Fatal(retry, err)
			}
			var before, after [3]int
			tables := []string{"document_annotations", "document_annotation_history", "document_annotation_import_receipts"}
			for i, table := range tables {
				if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&before[i]); err != nil {
					t.Fatal(err)
				}
			}
			pack, err := parseAnnotationTransfer(content)
			if err != nil {
				t.Fatal(err)
			}
			// Two-row batch exercises rollback after the first row was inserted:
			// the second region hits the independent global area quota.
			pack.Annotations = append(pack.Annotations, pack.Annotations[0])
			pack.Annotations[1].OriginID = "ann-" + strings.Repeat("f", 32)
			if kind == "regions" {
				pack.Annotations[1].Regions = []DocumentAnnotationRegionTransfer{{OriginID: "area-" + strings.Repeat("a", 32), Page: pack.Annotations[1].Page, X: 0, Y: 0, Width: 1, Height: 1, FileHash: ChunkContentHash("historical"), Author: "author", Created: time.Now().UTC().Format(time.RFC3339Nano)}}
			}
			_, raw, err := canonicalAnnotationTransferPack(pack)
			if err != nil {
				t.Fatal(err)
			}
			p, err := PreviewDocumentAnnotationImportIn(ctx, dir, DocumentAnnotationImportPreviewInput{Content: string(raw)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ImportDocumentAnnotationsIn(ctx, dir, "destination-actor", DocumentAnnotationImportInput{Content: string(raw), Digest: p.Digest, RequestID: strings.Repeat("b", 32)}); !errors.Is(err, ErrDocumentAnnotationLimit) {
				t.Fatal("quota accepted batch", err)
			}
			for i, table := range tables {
				if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&after[i]); err != nil {
					t.Fatal(err)
				}
			}
			if before != after {
				t.Fatal("partial batch committed", before, after)
			}
		})
	}
}

func TestDocumentAnnotationTransferUnicodeEscapesAndDeterministicOrder(t *testing.T) {
	ctx := context.Background()
	dir, _, original, content := annotationTransferFixture(t)
	// A valid surrogate pair represents the same Unicode quotation/context.
	escaped := strings.ReplaceAll(content, "😀", `\ud83d\ude00`)
	if _, err := PreviewDocumentAnnotationImportIn(ctx, dir, DocumentAnnotationImportPreviewInput{Content: escaped}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`"\ud800"`, `"\udc00"`, `"\ud800\u0041"`} {
		if validAnnotationTransferJSONUnicode([]byte(text)) {
			t.Fatal(text)
		}
	}
	for _, text := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"😀"`} {
		if !validAnnotationTransferJSONUnicode([]byte(text)) {
			t.Fatal(text)
		}
	}
	_, imported := annotationTransferApply(t, dir, content, strings.Repeat("1", 32))
	first, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{original.ID, imported.Items[0].ID}, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{imported.Items[0].ID, original.ID}, Format: "json"})
	if err != nil || !bytes.Equal(first.Data, second.Data) {
		t.Fatal(err)
	}
	if _, err = ExportDocumentAnnotationsIn(ctx, dir, DocumentAnnotationExportRequest{IDs: []string{original.ID, original.ID}, Format: "json"}); !errors.Is(err, ErrDocumentAnnotationTransferInvalid) {
		t.Fatal(err)
	}
}
