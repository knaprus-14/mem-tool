package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestHandleMapEvalLabeledHumanJSONAndFailingExit(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const source = "C:/docs/labeled-cli.pdf"
	text := "Рабочее давление равно 10 бар"
	chunk := mem.DocumentChunk{
		Text: text, Title: "Labeled CLI", Tags: []string{"eval"}, Backend: "test",
		Embedding: []float32{1, 0}, ChunkIndex: 0, TotalChunks: 1,
		Provenance: mem.Provenance{
			DocumentID: "doc-labeled-cli", DocumentRevision: mem.ChunkContentHash("labeled cli revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
			Page: 7, BlockIndex: 0, BlockMarker: "page", BlockChunkIndex: 0, BlockTotalChunks: 1,
			ExtractionMethod: "text", OCRConfidence: -1,
		},
	}
	if err := store.ReplaceDocumentChunks(source, []mem.DocumentChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(source)[0]
	anchor, err := mem.EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "claim-pressure", Kind: mem.KnowledgeNodeClaim, Label: text,
		Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginSource, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "labeled.json")
	writeMapEvalLabeledManifest(t, manifestPath, map[string]any{
		"schema_version": 1,
		"name":           "CLI labeled benchmark",
		"scope":          map[string]any{"document": source},
		"matching":       map[string]any{"claim_similarity_threshold": 0.8, "statuses": []string{"active"}},
		"claims":         []any{map[string]any{"id": "expected-pressure", "text": text, "accepted_citation_ids": []string{anchor.CitationID}}},
		"contradictions": []any{},
		"requirements":   map[string]any{"min_claim_precision": 100, "min_claim_recall": 100, "min_claim_f1": 100},
	})
	stdout, _, err := captureCLIStreams(func() error { return handleMapEvalLabeled(store, []string{manifestPath}) })
	if err != nil || !strings.Contains(stdout, "Результат: PASS") || !strings.Contains(stdout, "Claims: ожидается 1") {
		t.Fatalf("human labeled eval failed: err=%v output=%q", err, stdout)
	}
	stdout, _, err = captureCLIStreams(func() error { return handleMapEvalLabeled(store, []string{manifestPath, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var report mem.KnowledgeLabeledEvalReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || !report.Passed || report.Claims.TruePositive != 1 {
		t.Fatalf("JSON labeled eval is invalid: err=%v report=%#v output=%q", err, report, stdout)
	}

	writeMapEvalLabeledManifest(t, manifestPath, map[string]any{
		"schema_version": 1, "name": "CLI failing benchmark", "scope": map[string]any{"document": source},
		"matching":       map[string]any{"statuses": []string{"active"}},
		"claims":         []any{map[string]any{"id": "expected-missing", "text": "Температура равна 40 градусам"}},
		"contradictions": []any{}, "requirements": map[string]any{"min_claim_recall": 100},
	})
	stdout, _, err = captureCLIStreams(func() error { return handleMapEvalLabeled(store, []string{manifestPath}) })
	if !errors.Is(err, mem.ErrKnowledgeLabeledEvalFailed) || !strings.Contains(stdout, "Результат: FAIL") || !strings.Contains(stdout, "Пропущенные claims") {
		t.Fatalf("failing labeled eval did not fail closed: err=%v output=%q", err, stdout)
	}
	if err := handleMapEvalLabeled(store, []string{manifestPath, "--unknown"}); err == nil {
		t.Fatal("unknown labeled eval flag was accepted")
	}
}

func writeMapEvalLabeledManifest(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
