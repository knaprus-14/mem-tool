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
	"unicode/utf8"
)

func annotationCandidateReplace(t *testing.T, s *Store, path string, texts ...string) {
	t.Helper()
	chunks := make([]DocumentChunk, len(texts))
	for i, text := range texts {
		chunks[i] = validStructuredChunks()[0]
		chunks[i].Text, chunks[i].ChunkIndex, chunks[i].TotalChunks = text, i, len(texts)
		p := &chunks[i].Provenance
		p.SourcePath, p.DocumentRevision, p.ChunkHash = path, ChunkContentHash(strings.Join(texts, "\n")), ChunkContentHash(text)
		p.BlockChunkIndex, p.BlockTotalChunks = i, len(texts)
	}
	if err := s.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
}

func candidateSelection(a DocumentAnnotation, c DocumentAnnotationReanchorTarget) DocumentAnnotationReanchorPreviewInput {
	return DocumentAnnotationReanchorPreviewInput{Revision: a.Revision, EntryID: c.EntryID, Pin: c.Pin, Start: c.Start, End: c.End, Quote: c.Quote}
}

func candidateReason(out DocumentAnnotationCandidates, reason string) bool {
	for _, r := range out.TruncatedReasons {
		if r == reason {
			return true
		}
	}
	return false
}

func TestDocumentAnnotationCandidatesInsertedParagraphExplicitChoiceAndStalePin(t *testing.T) {
	ctx := context.Background()
	quote := "давление 🌡️ 5 бар"
	before := "😀 Введение: " + quote + ". Конец."
	dir, store, create := documentAnnotationFixture(t, before, quote)
	created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", create)
	if err != nil {
		t.Fatal(err)
	}
	a := created.Annotation
	query := DocumentAnnotationCandidateQuery{Revision: a.Revision}
	current, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || len(current.Items) != 1 || current.Items[0].Start != a.Start || !current.Complete || current.Limit != 20 {
		t.Fatal(current, err)
	}
	path := filepath.Join(dir, "private", "source.pdf")
	prefix := "Вставленный абзац.\n\n"
	annotationCandidateReplace(t, store, path, prefix+before)
	unique, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || !unique.Complete || len(unique.Items) != 1 || unique.Items[0].Start != a.Start+utf8.RuneCountInString(prefix) || unique.Items[0].Quote != quote || unique.Items[0].Pin == create.Pin || unique.Items[0].DocumentRevision == a.DocumentRevision {
		t.Fatal("unique candidate after paragraph insertion", unique, err)
	}
	lineage, err := ListDocumentAnnotationLineageIn(ctx, dir, a.ID, 0, 30)
	if err != nil || lineage.Total != 0 {
		t.Fatal("unique candidate was applied without explicit choice", lineage, err)
	}
	unchanged, err := ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || unchanged.State != EvidenceStale || unchanged.Start != a.Start || unchanged.End != a.End || unchanged.DocumentRevision != a.DocumentRevision || unchanged.Revision != a.Revision {
		t.Fatal("search changed old anchor", unchanged, err)
	}
	text := prefix + before + " Второй случай: " + quote + "!"
	annotationCandidateReplace(t, store, path, text)
	old, err := ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || old.State != EvidenceStale {
		t.Fatal(old, err)
	}
	out, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || !out.Complete || out.TotalEntries != 1 || out.ScannedEntries != 1 || out.ScannedBytes != int64(len(text)) || len(out.Items) != 2 || out.AnnotationID != a.ID || out.Quote != quote {
		t.Fatal(out, err)
	}
	if out.Items[0].Start == a.Start || out.Items[1].Start != utf8.RuneCountInString(text[:strings.LastIndex(text, quote)]) || out.Items[0].Prefix == out.Items[1].Prefix {
		t.Fatal(out.Items)
	}
	for _, target := range out.Items {
		if target.DocumentID != a.DocumentID || target.DocumentRevision == a.DocumentRevision || target.Pin == create.Pin || target.SourceName != "source.pdf" || target.End-target.Start != utf8.RuneCountInString(quote) || utf8.RuneCountInString(target.Prefix) > 48 || utf8.RuneCountInString(target.Suffix) > 48 {
			t.Fatal(target)
		}
	}
	afterSearch, err := ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || afterSearch != old {
		t.Fatal("search changed annotation", afterSearch, err)
	}
	history, err := ReadDocumentAnnotationHistoryIn(ctx, dir, a.ID, 0, 50)
	if err != nil || history.Total != 1 {
		t.Fatal(history, err)
	}
	selection := candidateSelection(a, out.Items[1])
	preview, err := PreviewDocumentAnnotationReanchorIn(ctx, dir, a.ID, selection)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReanchorDocumentAnnotationIn(ctx, dir, "reviewer", a.ID, annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("b", 32)))
	if err != nil || result.Annotation.State != EvidenceCurrent || result.Annotation.Start != out.Items[1].Start || result.Annotation.ID == a.ID {
		t.Fatal(result, err)
	}
	oldAgain, err := ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || oldAgain != old {
		t.Fatal("apply changed old anchor", oldAgain, err)
	}
	annotationCandidateReplace(t, store, path, "Ещё абзац. "+text)
	if _, err = PreviewDocumentAnnotationReanchorIn(ctx, dir, a.ID, selection); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal("stale candidate pin accepted", err)
	}
	apply := annotationReanchorApplyInput(selection, preview.Digest, strings.Repeat("c", 32))
	if _, err = ReanchorDocumentAnnotationIn(ctx, dir, "reviewer", a.ID, apply); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal("stale apply", err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "source_path") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), dir) {
		t.Fatal("path leaked", string(raw))
	}
}

func TestDocumentAnnotationCandidatesExactUnicodeOverlapsAndChunkBoundary(t *testing.T) {
	for _, test := range []struct {
		name, quote, text string
		starts            []int
	}{
		{"overlaps", "ааа", "😀 аааа", []int{2, 3}},
		{"normalization", "Café", "Cafe\u0301 Café CAFÉ", []int{6}},
		{"emoji", "🌡️", "😀 🌡️ 🌡️", []int{2, 5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, _, create := documentAnnotationFixture(t, test.text, test.quote)
			made, err := CreateDocumentAnnotationIn(context.Background(), dir, "alice", create)
			if err != nil {
				t.Fatal(err)
			}
			out, err := FindDocumentAnnotationReanchorCandidatesIn(context.Background(), dir, made.Annotation.ID, DocumentAnnotationCandidateQuery{Revision: 1})
			var starts []int
			for _, item := range out.Items {
				starts = append(starts, item.Start)
			}
			if err != nil || !out.Complete || !reflect.DeepEqual(starts, test.starts) {
				t.Fatal(out, starts, err)
			}
		})
	}
	dir, store, created, _ := annotationTransferFixture(t)
	annotationCandidateReplace(t, store, filepath.Join(dir, "private", "source.pdf"), "Давление ", "5 бар")
	out, err := FindDocumentAnnotationReanchorCandidatesIn(context.Background(), dir, created.ID, DocumentAnnotationCandidateQuery{Revision: 1})
	if err != nil || !out.Complete || len(out.Items) != 0 || out.TotalEntries != 2 || !strings.Contains(out.Warnings[0], "границе") {
		t.Fatal(out, err)
	}
}

func TestDocumentAnnotationCandidatesScopeUnboundAndMissing(t *testing.T) {
	ctx := context.Background()
	dir, store, a, content := annotationTransferFixture(t)
	query := DocumentAnnotationCandidateQuery{Revision: 1}
	// A portable restore changes the physical file lookup, never the logical
	// locator used for this index-only search. The mapped file need not exist.
	if _, err := store.db.Exec(sourceLocationsSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO source_locations(document_id,document_revision,relative_path) VALUES(?,?,?)`, "doc-book", a.DocumentRevision, "portable/not-present.pdf"); err != nil {
		t.Fatal(err)
	}
	relocated, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || !relocated.Complete || len(relocated.Items) != 2 || relocated.Items[0].DocumentID != a.DocumentID {
		t.Fatal("physical relocation changed logical search", relocated, err)
	}
	// Corrupt a second in-scope fixture chunk into matching text under another
	// locator. This must not broaden the immutable source identity of the first.
	if _, err := store.db.Exec(`UPDATE entries SET text=?,chunk_hash=?,source_path=?,source_file=? WHERE document_id='doc-book' AND block_chunk_index=1`, a.Quote, ChunkContentHash(a.Quote), "C:/other/source.pdf", "C:/other/source.pdf"); err != nil {
		t.Fatal(err)
	}
	out, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || out.TotalEntries != 1 || len(out.Items) != 2 {
		t.Fatal(out, err)
	}
	if _, err = store.db.Exec(`UPDATE entries SET document_id='another-document' WHERE source_path=?`, filepath.Join(dir, "private", "source.pdf")); err != nil {
		t.Fatal(err)
	}
	out, err = FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
	if err != nil || !out.Complete || out.TotalEntries != 0 || len(out.Items) != 0 {
		t.Fatal(out, err)
	}
	other, _, _ := annotationTransferDestination(t, "")
	_, imported := annotationTransferApply(t, other, content, strings.Repeat("b", 32))
	out, err = FindDocumentAnnotationReanchorCandidatesIn(ctx, other, imported.Items[0].ID, query)
	if err != nil || out.Complete || out.TotalEntries != 0 || len(out.Items) != 0 || !candidateReason(out, "binding_unresolved") {
		t.Fatal(out, err)
	}
	still, err := ReadDocumentAnnotationIn(ctx, other, imported.Items[0].ID)
	if err != nil || still.State != EvidenceMissing || !still.Archived {
		t.Fatal(still, err)
	}
}

func TestDocumentAnnotationCandidatesBudgetsAndInvalidText(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"candidate", "exact-count", "chunk", "text", "entries", "metadata", "invalid-utf8", "nul", "invalid-hash", "invalid-coordinates"} {
		t.Run(name, func(t *testing.T) {
			dir, store, a, _ := annotationTransferFixture(t)
			path := filepath.Join(dir, "private", "source.pdf")
			query := DocumentAnnotationCandidateQuery{Revision: 1, Limit: 1}
			reason := ""
			switch name {
			case "candidate":
				reason = "candidate_limit"
			case "exact-count":
				annotationCandidateReplace(t, store, path, a.Quote)
			case "chunk":
				annotationCandidateReplace(t, store, path, strings.Repeat("x", maxDocumentAnnotationChunkBytes+1), a.Quote)
				reason = "chunk_limit"
			case "text":
				annotationCandidateReplace(t, store, path, strings.Repeat("x", maxDocumentAnnotationChunkBytes), strings.Repeat("x", maxDocumentAnnotationChunkBytes), strings.Repeat("x", maxDocumentAnnotationChunkBytes), strings.Repeat("x", maxDocumentAnnotationChunkBytes), a.Quote)
				reason = "text_budget"
			case "entries":
				texts := make([]string, maxAnnotationCandidateEntries+1)
				for i := range texts {
					texts[i] = fmt.Sprint("unrelated ", i)
				}
				texts[len(texts)-1] = a.Quote
				annotationCandidateReplace(t, store, path, texts...)
				reason = "entry_limit"
			case "metadata":
				if _, err := store.db.Exec(`UPDATE entries SET warnings=zeroblob(?) WHERE document_id='doc-book' AND block_chunk_index=0`, maxAnnotationCandidateStoredBytes+1); err != nil {
					t.Fatal(err)
				}
				reason = "metadata_budget"
			case "invalid-utf8", "nul":
				bad := "\xff" + a.Quote
				if name == "nul" {
					bad = "\x00" + a.Quote
				}
				if _, err := store.db.Exec(`UPDATE entries SET text=? WHERE document_id='doc-book' AND block_chunk_index=0`, bad); err != nil {
					t.Fatal(err)
				}
				reason = "invalid_entry"
			case "invalid-hash":
				if _, err := store.db.Exec(`UPDATE entries SET chunk_hash=? WHERE document_id='doc-book' AND block_chunk_index=0`, ChunkContentHash("different")); err != nil {
					t.Fatal(err)
				}
				reason = "invalid_entry"
			case "invalid-coordinates":
				if _, err := store.db.Exec(`UPDATE entries SET block_chunk_index=0 WHERE document_id='doc-book' AND block_chunk_index=1`); err != nil {
					t.Fatal(err)
				}
				reason = "invalid_entry"
			}
			out, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, query)
			if err != nil || out.Complete != (reason == "") || (reason != "" && !candidateReason(out, reason)) || out.ScannedBytes > maxAnnotationCandidateTextBytes || out.ScannedEntries > maxAnnotationCandidateEntries || len(out.Items) > 1 {
				t.Fatal(out, err)
			}
			if (name == "chunk" || name == "exact-count" || name == "candidate") && len(out.Items) != 1 {
				t.Fatal(out)
			}
			if (name == "text" || name == "entries" || name == "metadata" || strings.HasPrefix(name, "invalid") || name == "nul") && len(out.Items) != 0 {
				t.Fatal("invalid result", out)
			}
		})
	}
}

func TestDocumentAnnotationCandidatesReadOnlyValidationAndCancellation(t *testing.T) {
	ctx := context.Background()
	dir, store, a, _ := annotationTransferFixture(t)
	for _, in := range []DocumentAnnotationCandidateQuery{{}, {Revision: -1}, {Revision: 1, Limit: -1}, {Revision: 1, Limit: 51}} {
		if _, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, in); !errors.Is(err, ErrDocumentAnnotationReanchorInvalid) {
			t.Fatal(in, err)
		}
	}
	if _, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, DocumentAnnotationCandidateQuery{Revision: 2}); !errors.Is(err, ErrDocumentAnnotationConflict) {
		t.Fatal(err)
	}
	if _, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, "ann-"+strings.Repeat("f", 32), DocumentAnnotationCandidateQuery{Revision: 1}); !errors.Is(err, ErrDocumentAnnotationNotFound) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := FindDocumentAnnotationReanchorCandidatesIn(canceled, dir, a.ID, DocumentAnnotationCandidateQuery{Revision: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var receipts, history, records int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM document_annotation_receipts),(SELECT COUNT(*) FROM document_annotation_history),(SELECT COUNT(*) FROM document_annotations)`).Scan(&receipts, &history, &records); err != nil || receipts != 1 || history != 1 || records != 1 {
		t.Fatal(receipts, history, records, err)
	}
	if _, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 1, Note: "После поиска изменён комментарий", Color: "blue", RequestID: strings.Repeat("b", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, DocumentAnnotationCandidateQuery{Revision: 1}); !errors.Is(err, ErrDocumentAnnotationConflict) {
		t.Fatal("search accepted old annotation revision", err)
	}
	updated, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, dir, a.ID, DocumentAnnotationCandidateQuery{Revision: 2})
	if err != nil || updated.AnnotationRevision != 2 || len(updated.Items) != 2 || updated.Quote != a.Quote {
		t.Fatal(updated, err)
	}
	empty, emptyStore, _ := queryFixture(t)
	if _, err := FindDocumentAnnotationReanchorCandidatesIn(ctx, empty, a.ID, DocumentAnnotationCandidateQuery{Revision: 1}); !errors.Is(err, ErrDocumentAnnotationNotFound) {
		t.Fatal(err)
	}
	var tables int
	if err := emptyStore.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'document_annotation%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("read created schema", tables, err)
	}
}
