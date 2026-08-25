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

func TestHandleMapEvalHumanJSONAndFailingExit(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const source = "C:/docs/eval.pdf"
	text := "Проверяемое утверждение"
	chunk := mem.DocumentChunk{
		Text: text, Title: "Eval", Tags: []string{"test"}, Backend: "test",
		Embedding: []float32{1, 0}, ChunkIndex: 0, TotalChunks: 1,
		Provenance: mem.Provenance{
			DocumentID: "doc-eval", DocumentRevision: mem.ChunkContentHash("eval revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
			Page: 1, BlockIndex: 0, BlockMarker: "page", BlockChunkIndex: 0, BlockTotalChunks: 1,
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
		ID: "eval-node", Kind: mem.KnowledgeNodeClaim, Label: "Eval claim", Status: mem.KnowledgeStatusDraft,
		Origin: mem.KnowledgeOriginGenerated, Confidence: 0.9, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(t.TempDir(), "quality.json")
	writeMapEvalManifest(t, manifestPath, map[string]any{
		"schema_version": 1,
		"name":           "CLI baseline",
		"scope":          map[string]any{"document": source},
		"requirements":   map[string]any{"min_knowledge_coverage_percent": 100, "min_extracted_nodes": 1, "max_stale_evidence_objects": 0},
	})
	cfg := &Config{Ingest: mem.IngestConfig{LowConfidence: 65}}
	stdout, _, err := captureCLIStreams(func() error { return handleMapEval(cfg, store, []string{manifestPath}) })
	if err != nil || !strings.Contains(stdout, "Результат: PASS") || !strings.Contains(stdout, "knowledge_coverage_percent") {
		t.Fatalf("human eval failed: err=%v output=%q", err, stdout)
	}

	stdout, _, err = captureCLIStreams(func() error { return handleMapEval(cfg, store, []string{manifestPath, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var report mem.KnowledgeQualityReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || !report.Passed || len(report.Checks) != 3 {
		t.Fatalf("JSON eval is invalid: err=%v report=%#v output=%q", err, report, stdout)
	}

	writeMapEvalManifest(t, manifestPath, map[string]any{
		"schema_version": 1,
		"name":           "Failing CLI baseline",
		"scope":          map[string]any{"document": source},
		"requirements":   map[string]any{"max_draft_objects": 0},
	})
	stdout, _, err = captureCLIStreams(func() error { return handleMapEval(cfg, store, []string{manifestPath}) })
	if !errors.Is(err, mem.ErrKnowledgeQualityGateFailed) || !strings.Contains(stdout, "Результат: FAIL") || !strings.Contains(stdout, "[FAIL]") {
		t.Fatalf("failing eval did not fail closed: err=%v output=%q", err, stdout)
	}
	if err := handleMapEval(cfg, store, []string{manifestPath, "--unknown"}); err == nil {
		t.Fatal("unknown eval flag was accepted")
	}
}

func writeMapEvalManifest(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
