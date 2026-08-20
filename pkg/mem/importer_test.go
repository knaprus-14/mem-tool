package mem

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestMarkdownImportStoresSearchablePageProvenance(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "book.md")
	doc, err := ingest.ParseMarkdown(source, "# Book\n\n<!-- page: 7 -->\n\nThe hydraulic answer.\n\n<!-- page: 8 -->\n\nAnother section.")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	embed := func(_ *Config, text string) ([]float32, error) {
		if text == "The hydraulic answer." {
			return []float32{1, 0}, nil
		}
		return []float32{0, 1}, nil
	}
	result, err := importExtractedDocumentWithEmbedder(testConfig(1000, "paragraph"), store, doc,
		ImportOptions{Tags: []string{"book"}}, embed)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePath != doc.SourcePath || result.DocumentID != doc.ID || result.DocumentRevision != doc.Revision || result.Chunks != 3 {
		t.Fatalf("unexpected import result: %#v", result)
	}

	search, err := store.Search([]float32{1, 0}, "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(search) != 1 {
		t.Fatalf("got %d search results", len(search))
	}
	got := search[0]
	if got.SourceFile != doc.SourcePath || got.SourcePath != doc.SourcePath || got.DocumentID != doc.ID || got.Page != 7 {
		t.Fatalf("search result lost provenance: %#v", got)
	}
	if got.BlockMarker != "<!-- page: 7 -->" || got.BlockIndex != 1 || got.MediaType != "text/markdown" {
		t.Fatalf("search result has incomplete block metadata: %#v", got)
	}
	if got.BlockChunkIndex != 0 || got.BlockTotalChunks != 1 {
		t.Fatalf("search result lost block-local chunk coordinates: %#v", got)
	}
	if got.DocumentRevision != doc.Revision || got.ChunkHash != ChunkContentHash(got.Text) {
		t.Fatalf("search result lost versioned content provenance: %#v", got)
	}
	if got.ExtractionMethod != "text" || got.OCRConfidence != -1 {
		t.Fatalf("Markdown extraction provenance changed: %#v", got)
	}
}

func TestDocumentImportRejectsInvalidProvenanceBeforeEmbedding(t *testing.T) {
	root := t.TempDir()
	doc, err := ingest.ParseMarkdown(filepath.Join(root, "book.md"), "first\n\n<!-- page: 2 -->\n\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	doc.Blocks[1].Index = 7
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	embedCalls := 0
	_, err = importExtractedDocumentWithEmbedder(testConfig(100, "paragraph"), store, doc, ImportOptions{}, func(*Config, string) ([]float32, error) {
		embedCalls++
		return []float32{1}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "indices must be contiguous") {
		t.Fatalf("invalid block provenance was accepted: %v", err)
	}
	if embedCalls != 0 || len(store.GetBySourceFile(doc.SourcePath)) != 0 {
		t.Fatalf("invalid provenance caused work or writes: embed calls=%d", embedCalls)
	}
}

func TestReimportKeepsLaterBlockCitationWhenEarlierBlockGrows(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "book.md")
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testConfig(10, "fixed")

	first, err := ingest.ParseMarkdown(source, "short\n\n<!-- page: 2 -->\n\ntarget text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(cfg, store, first, ImportOptions{}, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	before := entryForPage(t, store.GetBySourceFile(first.SourcePath), 2)
	beforeID, _ := CitationForEntry(before)
	beforeRevision, beforeChunkHash := before.DocumentRevision, before.ChunkHash

	second, err := ingest.ParseMarkdown(source, "a much longer earlier block that creates several chunks\n\n<!-- page: 2 -->\n\ntarget text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(cfg, store, second, ImportOptions{}, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	after := entryForPage(t, store.GetBySourceFile(second.SourcePath), 2)
	afterID, _ := CitationForEntry(after)
	if after.ChunkIndex == before.ChunkIndex {
		t.Fatalf("test setup did not shift global chunk index: before=%d after=%d", before.ChunkIndex, after.ChunkIndex)
	}
	if afterID != beforeID {
		t.Fatalf("later source anchor changed after earlier block grew: %q vs %q", beforeID, afterID)
	}
	if after.DocumentID != before.DocumentID || after.DocumentRevision == beforeRevision {
		t.Fatalf("reimport should preserve path identity and change content revision: before=%#v after=%#v", before, after)
	}
	if after.ChunkHash != beforeChunkHash {
		t.Fatalf("unchanged target evidence changed chunk hash: %q vs %q", beforeChunkHash, after.ChunkHash)
	}
}

func entryForPage(t *testing.T, entries []Entry, page int) Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.Page == page {
			return entry
		}
	}
	t.Fatalf("no entry for page %d in %#v", page, entries)
	return Entry{}
}

func TestDocumentImportRollsBackAllChunksOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "book.md")
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testConfig(100, "paragraph")
	stable, err := ingest.ParseMarkdown(source, "stable original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(cfg, store, stable, ImportOptions{}, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_second_document_chunk
BEFORE INSERT ON entries WHEN NEW.source_file != '' AND NEW.chunk_index = 1
BEGIN SELECT RAISE(ABORT, 'synthetic second-chunk failure'); END;`); err != nil {
		t.Fatal(err)
	}

	updated, err := ingest.ParseMarkdown(source, strings.Repeat("changed text ", 20))
	if err != nil {
		t.Fatal(err)
	}
	_, err = importExtractedDocumentWithEmbedder(testConfig(30, "fixed"), store, updated, ImportOptions{}, fakeEmbedding)
	if err == nil || !strings.Contains(err.Error(), "document not updated") {
		t.Fatalf("transactional write failure was hidden: %v", err)
	}
	entries := store.GetBySourceFile(stable.SourcePath)
	if len(entries) != 1 || entries[0].Text != "stable original" || entries[0].TotalChunks != 1 {
		t.Fatalf("failed replacement changed the previous document: %#v", entries)
	}
}

func TestOCRWarningsAndConfidenceSurviveImport(t *testing.T) {
	root := t.TempDir()
	doc, err := ingest.ParseMarkdown(filepath.Join(root, "scan.md"), "<!-- page: 4 -->\n\nRecognized text")
	if err != nil {
		t.Fatal(err)
	}
	doc.Format, doc.MediaType = ingest.FormatPDF, "application/pdf"
	doc.Blocks[0].Extraction = "ocr"
	doc.Blocks[0].OCRConfidence = 42.5
	doc.Blocks[0].Warnings = []string{"OCR confidence 42.5 is below 65.0"}
	doc.Warnings = append([]string(nil), doc.Blocks[0].Warnings...)
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err := importExtractedDocumentWithEmbedder(testConfig(1000, "paragraph"), store, doc, ImportOptions{}, func(*Config, string) ([]float32, error) { return []float32{1}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("import warnings lost: %#v", result)
	}
	entries := store.GetBySourceFile(doc.SourcePath)
	if len(entries) != 1 || entries[0].ExtractionMethod != "ocr" || entries[0].OCRConfidence != 42.5 || len(entries[0].Warnings) != 1 {
		t.Fatalf("stored OCR provenance lost: %#v", entries)
	}
	entries[0].Warnings[0] = "mutated by caller"
	again := store.GetBySourceFile(doc.SourcePath)
	if again[0].Warnings[0] == "mutated by caller" {
		t.Fatal("caller mutated Store warning metadata through a shallow copy")
	}
}
