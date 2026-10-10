package mem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in diagnostic replay. Only a separately prepared copy beneath this
// checkout's build directory is accepted; never point this at a user's store.
func TestWebMapAIReassemblyCopiedDocument(t *testing.T) {
	dir := os.Getenv("MEM_MAP_REASSEMBLY_COPY")
	if dir == "" {
		t.Skip("requires an explicitly prepared document database copy")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(repo, "build", "mindmap-alpha11", "private-repro", ".mem")
	abs, err := filepath.Abs(dir)
	if err != nil || !strings.EqualFold(abs, expected) {
		t.Fatal("refusing non-fixture directory")
	}
	marker, err := os.ReadFile(filepath.Join(dir, "COPIED_DATABASE_ONLY"))
	if err != nil || string(marker) != "isolated copy" {
		t.Fatal("missing copy marker")
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	task, err := scanWebAITask(s.db.QueryRow(`SELECT ` + webAITaskColumns + ` FROM web_map_ai_tasks WHERE status='failed' AND phase='merging' AND total=24 ORDER BY created DESC LIMIT 1`))
	if err != nil {
		t.Fatal(err)
	}
	var originalCount int
	if err = s.db.QueryRow(`SELECT SUM(json_array_length(result_json)) FROM mind_map_generation_batches WHERE run_id=?`, task.RunID).Scan(&originalCount); err != nil || originalCount != 348 {
		t.Fatal(originalCount, err)
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	answer := cfg.Answer.WithMapGenerationDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 90)
	p := &synthesisTestProvider{t: t, budget: min(answer.ContextChars, 24000)}
	realModel := os.Getenv("MEM_MAP_REASSEMBLY_REAL_MODEL") == "1"
	if realModel {
		p.delegate, err = NewOllamaAnswerProvider(answer)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = ReassembleWebMapAIIn(ctx, dir, task.Owner, task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if worked, e := processWebMapAIIn(ctx, dir, task.Owner, p); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	tasks, err := ListWebMapAIIn(ctx, dir, task.Owner, task.MapID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "applied" {
		t.Fatal(tasks, err)
	}
	review, err := ReadWebMapAIReviewIn(ctx, dir, task.Owner, task.MapID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Synthesis == nil || review.Synthesis.InputProposals != 348 || review.Synthesis.Method != "hierarchical_whole_subtrees" {
		t.Fatal(review.Synthesis)
	}
	var equalCheckpoints int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_batches a JOIN mind_map_generation_batches b ON a.ordinal=b.ordinal WHERE a.run_id=? AND b.run_id=? AND a.result_json=b.result_json AND a.result_digest=b.result_digest AND a.evidence_json=b.evidence_json`, task.RunID, tasks[0].RunID).Scan(&equalCheckpoints); err != nil || equalCheckpoints != 24 {
		t.Fatal(equalCheckpoints, err)
	}
	doc, err := s.LoadClassicMindMap(task.MapID)
	if err != nil || len(doc.Nodes) != 349+review.Synthesis.Groups || doc.Map.Revision != 2 {
		t.Fatal(len(doc.Nodes), err)
	}
	out := filepath.Join(repo, "build", "mindmap-alpha11", "verification")
	result := map[string]any{"real_model": realModel, "model": answer.Model, "batches_reused": equalCheckpoints, "original_proposals": 348, "document_chunks": task.SourceChunks, "nodes": len(doc.Nodes), "groups": review.Synthesis.Groups, "model_calls": len(p.requests), "published_in_copy": true, "production_modified": false, "revision": doc.Map.Revision, "method": review.Synthesis.Method}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	if err = os.WriteFile(filepath.Join(out, "document-replay.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []ClassicMindMapExportFormat{ClassicMindMapExportJSON, ClassicMindMapExportHTML} {
		artifact, e := s.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: task.MapID, Format: format})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(repo, "build", "mindmap-alpha11", "SP10-recovered."+string(format)), artifact.Data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Log(string(encoded))
}

func TestWebMapAIReassemblyExportImport(t *testing.T) {
	dir := os.Getenv("MEM_MAP_REASSEMBLY_COPY")
	if dir == "" {
		t.Skip("requires an explicitly prepared document database copy")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !strings.EqualFold(abs, filepath.Join(repo, "build", "mindmap-alpha11", "private-repro", ".mem")) {
		t.Fatal("refusing non-fixture directory")
	}
	marker, err := os.ReadFile(filepath.Join(dir, "COPIED_DATABASE_ONLY"))
	if err != nil || string(marker) != "isolated copy" {
		t.Fatal("missing copy marker")
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data, err := os.ReadFile(filepath.Join(repo, "build", "mindmap-alpha11", "SP10-recovered.json"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewClassicMindMapJSON(context.Background(), data)
	if err != nil || preview.NodeCount != 355 || preview.SourceCount != 440 || preview.Current != 440 || preview.Missing != 0 || preview.Stale != 0 {
		t.Fatal(preview, err)
	}
	doc, err := s.ImportClassicMindMapJSON(context.Background(), data, preview.Digest, "diagnostic-copy", "Native export round trip")
	if err != nil || len(doc.Nodes) != 355 || doc.Map.ID == "mm-ff92ec9ecf2e9eb174a24a262df0627e" {
		t.Fatal(len(doc.Nodes), err)
	}
	encoded, _ := json.MarshalIndent(preview, "", "  ")
	if err = os.WriteFile(filepath.Join(repo, "build", "mindmap-alpha11", "verification", "native-import.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Native import: nodes=%d current sources=%d; independent map created in copy", len(doc.Nodes), preview.Current)
}
