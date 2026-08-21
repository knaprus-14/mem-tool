package mem

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeCoverageReportMeasuresCurrentChunksPagesAndObjects(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	chunks := coverageTestChunks("coverage revision", []string{"alpha", "beta", "gamma"})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	first, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EvidenceAnchorForEntry(entries[1], entries[1].Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "coverage-draft", Kind: KnowledgeNodeClaim, Label: "Draft", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.8, Evidence: []EvidenceAnchor{first}},
		{ID: "coverage-active", Kind: KnowledgeNodeClaim, Label: "Active", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Confidence: 0.9, Evidence: []EvidenceAnchor{second}},
	}}); err != nil {
		t.Fatal(err)
	}

	options := KnowledgeCoverageOptions{Tag: "Manual", LowConfidence: 65}
	report, err := store.BuildKnowledgeCoverageReport(options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Documents != 1 || report.Summary.ChunksWithText != 3 || report.Summary.PagesWithText != 2 {
		t.Fatalf("unexpected coverage denominator: %#v", report.Summary)
	}
	if report.Summary.CoveredChunks != 2 || report.Summary.UncoveredChunks != 1 || report.Summary.FullyCoveredPages != 1 || report.Summary.UncoveredPages != 1 {
		t.Fatalf("unexpected chunk/page coverage: %#v", report.Summary)
	}
	if report.Summary.ProcessedChunks != 2 || report.Summary.UnprocessedChunks != 1 || report.Summary.ProcessingPercent <= 66 || report.Summary.ProcessingPercent >= 67 {
		t.Fatalf("unexpected processing coverage: %#v", report.Summary)
	}
	if report.Summary.ExtractedNodes != 2 || report.Summary.ActiveObjects != 1 || report.Summary.DraftObjects != 1 {
		t.Fatalf("unexpected object coverage: %#v", report.Summary)
	}
	if report.Summary.LowConfidenceOCRChunks != 1 || report.Summary.LowConfidenceOCRPages != 1 || report.Summary.WarningChunks != 1 {
		t.Fatalf("OCR quality was not reported: %#v", report.Summary)
	}
	if len(report.Documents) != 1 || len(report.Documents[0].UncoveredPages) != 1 || report.Documents[0].UncoveredPages[0] != 2 ||
		len(report.Documents[0].LowConfidenceOCRPages) != 1 || report.Documents[0].LowConfidenceOCRPages[0] != 2 ||
		len(report.Documents[0].Warnings) != 1 || report.Documents[0].Warnings[0].Page != 2 ||
		len(report.Documents[0].Warnings[0].Messages) != 1 {
		t.Fatalf("document detail is incomplete: %#v", report.Documents)
	}
	if !strings.HasPrefix(report.SnapshotDigest, "sha256:") || len(report.Limitations) < 3 {
		t.Fatalf("snapshot contract is incomplete: %#v", report)
	}
	repeated, err := store.BuildKnowledgeCoverageReport(options)
	if err != nil || repeated.SnapshotDigest != report.SnapshotDigest {
		t.Fatalf("coverage snapshot is not deterministic: first=%q second=%q err=%v", report.SnapshotDigest, repeated.SnapshotDigest, err)
	}

	pageTwo, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: chunks[0].Provenance.SourcePath, PageFrom: 2, PageTo: 2, LowConfidence: 65})
	if err != nil {
		t.Fatal(err)
	}
	if pageTwo.Summary.ChunksWithText != 1 || pageTwo.Summary.CoveredChunks != 0 || pageTwo.Summary.UncoveredPages != 1 {
		t.Fatalf("physical page scope was ignored: %#v", pageTwo.Summary)
	}
	if _, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: "C:/docs/missing.pdf"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing document selector was not rejected: %v", err)
	}
}

func TestKnowledgeCoverageReportDoesNotCountChangedEvidenceAsCovered(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := coverageTestChunks("old revision", []string{"old text"})
	if err := store.ReplaceDocumentChunks(original[0].Provenance.SourcePath, original); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(original[0].Provenance.SourcePath)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "changed-evidence", Kind: KnowledgeNodeClaim, Label: "Old claim", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Confidence: 0.8, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	changed := coverageTestChunks("new revision", []string{"new text"})
	if err := store.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatal(err)
	}
	report, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.CoveredChunks != 0 || report.Summary.UncoveredChunks != 1 || report.Summary.ExtractedNodes != 0 ||
		report.Summary.StaleEvidenceObjects+report.Summary.MissingEvidenceObjects != 1 {
		t.Fatalf("changed evidence was counted as current coverage: %#v", report.Summary)
	}
}

func coverageTestChunks(revisionSeed string, texts []string) []DocumentChunk {
	const source = "C:/docs/coverage.pdf"
	chunks := make([]DocumentChunk, len(texts))
	for i, text := range texts {
		page := 1
		method, confidence := "text", -1.0
		var warnings []string
		if i == len(texts)-1 && len(texts) > 1 {
			page = 2
			method, confidence = "ocr", 42
			warnings = []string{"low OCR confidence"}
		}
		chunks[i] = DocumentChunk{
			Text: text, Title: "Coverage PDF", Tags: []string{"Manual", "RF"},
			Backend: "test", Embedding: []float32{1, 0}, ChunkIndex: i, TotalChunks: len(texts),
			Provenance: Provenance{
				DocumentID: "doc-coverage", DocumentRevision: ChunkContentHash(revisionSeed), ChunkHash: ChunkContentHash(text),
				SourcePath: source, MediaType: "application/pdf", Page: page, BlockIndex: i,
				BlockMarker: "page", BlockChunkIndex: 0, BlockTotalChunks: 1,
				ExtractionMethod: method, OCRConfidence: confidence, Warnings: warnings,
			},
		}
	}
	return chunks
}
