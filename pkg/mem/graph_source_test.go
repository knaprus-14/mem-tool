package mem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKnowledgeMapPDFSourceRequiresCurrentGraphEvidence(t *testing.T) {
	store, anchor, source := knowledgeMapPDFSourceFixture(t)
	defer store.Close()

	resolved, err := store.ResolveKnowledgeMapPDFSource(anchor.CitationID)
	if err != nil || resolved.SourcePath != source || resolved.Page != anchor.Page {
		t.Fatalf("current PDF evidence did not resolve: anchor=%#v err=%v", resolved, err)
	}
	if _, err := store.ResolveKnowledgeMapPDFSource("cite-unknown"); !errors.Is(err, ErrKnowledgeMapSourceNotFound) {
		t.Fatalf("unknown citation returned %v", err)
	}

	entries := store.GetBySourceFile(source)
	chunks := make([]DocumentChunk, len(entries))
	for i, entry := range entries {
		chunks[i] = DocumentChunk{
			Text: entry.Text, Title: entry.Title, Tags: entry.Tags, Backend: entry.Backend,
			EmbeddingModel: entry.EmbeddingModel, EmbeddingSpace: entry.EmbeddingSpace,
			Embedding: entry.Embedding, ChunkLabel: entry.ChunkLabel,
			ChunkIndex: entry.ChunkIndex, TotalChunks: entry.TotalChunks,
			Provenance: Provenance{
				DocumentID: entry.DocumentID, DocumentRevision: entry.DocumentRevision,
				ChunkHash: entry.ChunkHash, SourcePath: entry.SourcePath, MediaType: entry.MediaType,
				Page: entry.Page, BlockIndex: entry.BlockIndex, BlockMarker: entry.BlockMarker,
				BlockChunkIndex: entry.BlockChunkIndex, BlockTotalChunks: entry.BlockTotalChunks,
				ExtractionMethod: entry.ExtractionMethod, OCRConfidence: entry.OCRConfidence,
				Warnings: entry.Warnings,
			},
		}
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("changed source revision")
	}
	if err := store.ReplaceDocumentChunks(source, chunks); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveKnowledgeMapPDFSource(anchor.CitationID); !errors.Is(err, ErrKnowledgeMapSourceNotCurrent) {
		t.Fatalf("stale citation was not blocked: %v", err)
	}
}

func TestResolveKnowledgeMapPDFSourceRejectsNonPDF(t *testing.T) {
	store, anchor, _ := knowledgeMapSourceFixture(t, ".md")
	defer store.Close()
	if _, err := store.ResolveKnowledgeMapPDFSource(anchor.CitationID); !errors.Is(err, ErrKnowledgeMapSourceUnsupported) {
		t.Fatalf("non-PDF source was accepted: %v", err)
	}
}

func knowledgeMapPDFSourceFixture(t *testing.T) (*Store, EvidenceAnchor, string) {
	t.Helper()
	return knowledgeMapSourceFixture(t, ".pdf")
}

func knowledgeMapSourceFixture(t *testing.T, extension string) (*Store, EvidenceAnchor, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "source"+extension)
	if err := os.WriteFile(source, []byte("%PDF-1.4\nmem-tool source fixture\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = source
		chunks[i].Provenance.Page = 12
	}
	if err := store.ReplaceDocumentChunks(source, chunks); err != nil {
		store.Close()
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(source)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "source-node", Kind: KnowledgeNodeClaim, Label: "Source node",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, anchor, source
}
