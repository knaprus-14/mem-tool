package mem

import (
	"strings"
	"testing"
)

func TestDocumentImportManifestPersistsPhysicalPages(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	chunks := coverageTestChunks("manifest revision", []string{"alpha", "beta", "gamma"})
	manifest := manifestForCoverageChunks(chunks)
	if err := store.ReplaceDocumentChunksWithManifest(chunks[0].Provenance.SourcePath, chunks, manifest); err != nil {
		t.Fatal(err)
	}

	manifests, err := store.CurrentDocumentImportManifests(chunks[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !manifests[0].Available || manifests[0].PhysicalPageCount != 3 ||
		manifests[0].StoredPages != 2 || manifests[0].EmptyPages != 1 || len(manifests[0].Pages) != 3 {
		t.Fatalf("unexpected import manifest: %#v", manifests)
	}
	if page := manifests[0].Pages[2]; page.Page != 3 || page.Status != DocumentImportPageEmpty ||
		len(page.Warnings) != 1 || !strings.Contains(page.Warnings[0], "no text") {
		t.Fatalf("empty physical page detail was lost: %#v", page)
	}
}

func TestDocumentImportManifestAndChunksRollbackTogether(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := coverageTestChunks("original revision", []string{"old one", "old two"})
	if err := store.ReplaceDocumentChunks(original[0].Provenance.SourcePath, original); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_import_manifest BEFORE INSERT ON document_import_manifests
        BEGIN SELECT RAISE(ABORT, 'manifest write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	changed := coverageTestChunks("changed revision", []string{"new one", "new two", "new three"})
	manifest := manifestForCoverageChunks(changed)
	err = store.ReplaceDocumentChunksWithManifest(changed[0].Provenance.SourcePath, changed, manifest)
	if err == nil || !strings.Contains(err.Error(), "manifest write rejected") {
		t.Fatalf("manifest failure was not surfaced: %v", err)
	}
	entries := store.GetBySourceFile(original[0].Provenance.SourcePath)
	if len(entries) != 2 || entries[0].Text != "old one" || entries[1].Text != "old two" {
		t.Fatalf("chunk replacement escaped manifest rollback: %#v", entries)
	}
}

func TestCurrentDocumentImportManifestsMarksLegacyRevisionUnavailable(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := coverageTestChunks("legacy revision", []string{"legacy"})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	manifests, err := store.CurrentDocumentImportManifests("")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Available || manifests[0].DocumentID != chunks[0].Provenance.DocumentID {
		t.Fatalf("legacy manifest state is misleading: %#v", manifests)
	}
}

func TestKnowledgeCoverageIncludesManifestPagesWithoutChunks(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := coverageTestChunks("coverage manifest revision", []string{"alpha", "beta", "gamma"})
	if err := store.ReplaceDocumentChunksWithManifest(chunks[0].Provenance.SourcePath, chunks, manifestForCoverageChunks(chunks)); err != nil {
		t.Fatal(err)
	}
	report, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: chunks[0].Provenance.SourcePath})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.ManifestDocuments != 1 || report.Summary.PhysicalPagesInScope != 3 ||
		report.Summary.StoredPhysicalPages != 2 || report.Summary.EmptyPhysicalPages != 1 ||
		report.Summary.ImportCoveragePercent <= 66 || report.Summary.ImportCoveragePercent >= 67 {
		t.Fatalf("physical import coverage is wrong: %#v", report.Summary)
	}
	emptyPage, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{
		Document: chunks[0].Provenance.SourcePath, PageFrom: 3, PageTo: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyPage.Documents) != 1 || emptyPage.Summary.ChunksWithText != 0 ||
		emptyPage.Summary.PhysicalPagesInScope != 1 || emptyPage.Summary.EmptyPhysicalPages != 1 ||
		len(emptyPage.Documents[0].ImportPageIssues) != 1 {
		t.Fatalf("empty page scope disappeared from coverage: %#v", emptyPage)
	}
}

func manifestForCoverageChunks(chunks []DocumentChunk) DocumentImportManifest {
	manifest := DocumentImportManifest{
		Available: true, DocumentID: chunks[0].Provenance.DocumentID,
		DocumentRevision: chunks[0].Provenance.DocumentRevision,
		SourcePath:       chunks[0].Provenance.SourcePath, MediaType: chunks[0].Provenance.MediaType,
		Format: "pdf", PhysicalPageCount: 3, SelectedPageFirst: 1, SelectedPageLast: 3,
		StoredPages: 2, EmptyPages: 1, Blocks: len(chunks), Chunks: len(chunks),
	}
	pageChunks := map[int]int{}
	pageBlocks := map[int]int{}
	for _, chunk := range chunks {
		pageChunks[chunk.Provenance.Page]++
		pageBlocks[chunk.Provenance.Page]++
	}
	manifest.Pages = []DocumentImportPage{
		{Page: 1, Status: DocumentImportPageStored, ExtractionMethod: "text", TextRunes: 9,
			OCRConfidence: -1, BlockCount: pageBlocks[1], ChunkCount: pageChunks[1]},
		{Page: 2, Status: DocumentImportPageStored, ExtractionMethod: "ocr", TextRunes: 5,
			OCRConfidence: 42, BlockCount: pageBlocks[2], ChunkCount: pageChunks[2], Warnings: []string{"low OCR confidence"}},
		{Page: 3, Status: DocumentImportPageEmpty, OCRConfidence: -1, Warnings: []string{"page 3: no text was extracted"}},
	}
	return manifest
}
