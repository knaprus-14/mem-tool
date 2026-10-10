package mem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebGraphOperationPublishesAnchoredExtractionAndDuplicates(t *testing.T) {
	s, _ := newKnowledgeExtractionJobStore(t, []string{"Pumps supply water under pressure."})
	defer s.Close()
	dir := filepath.Dir(s.Path())
	ctx := context.Background()
	plan, e := s.BuildKnowledgeExtractionJobPlan("pumps", KnowledgeCoverageOptions{}, 12000, 8)
	if e != nil || len(plan.Batches) == 0 {
		t.Fatal(plan, e)
	}
	citation := plan.Batches[0].Prompt.Evidence[0].CitationID
	reply := `{"nodes":[{"ref":"topic","kind":"topic","label":"Pumps","confidence":0.9,"citations":["` + citation + `"]}],"edges":[]}`
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embeddings" {
			json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 0}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}, "done": true, "done_reason": "stop"})
	}))
	defer model.Close()
	cfg := DefaultLocalConfig()
	cfg.Answer.Model = "test"
	cfg.Answer.BaseURL = model.URL
	cfg.Ollama.BaseURL = model.URL
	if e = SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	for i, kind := range []string{"graph-extract", "graph-duplicates"} {
		in := WebOperationInput{RequestID: strings.Repeat(string(rune('a'+i)), 32), Kind: kind, ConfigRevision: cfg.Revision(), Focus: "pumps", Batches: 8, ContextChars: 12000}
		op, e := StartWebOperationIn(ctx, dir, "local", in, true)
		if e != nil {
			t.Fatal(e)
		}
		if worked, e := ProcessWebOperationIn(ctx, dir, "local"); e != nil || !worked {
			t.Fatal(worked, e)
		}
		jobs, e := ListWebOperationsIn(ctx, dir, "local")
		if e != nil {
			t.Fatal(e)
		}
		for _, job := range jobs {
			if job.ID == op.ID && job.Status != "completed" {
				t.Fatal(job)
			}
		}
		if kind == "graph-duplicates" {
			if _, e = ReadWebOperationResultIn(ctx, dir, "local", op.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	graph, e := s.LoadKnowledgeGraph()
	if e != nil || len(graph.Nodes) != 1 || len(graph.Nodes[0].Evidence) != 1 {
		t.Fatal(graph, e)
	}
	if s.ResolveEvidenceAnchor(graph.Nodes[0].Evidence[0]).State != EvidenceCurrent {
		t.Fatal("published ungrounded node")
	}
}

func TestWebCorpusAnalysisRecordsInsufficientBatch(t *testing.T) {
	s := corpusAnalysisStoreWithClaims(t, 2)
	defer s.Close()
	dir := filepath.Dir(s.Path())
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": `{"insufficient_evidence":"No supported cross-document relation."}`}, "done": true, "done_reason": "stop"})
	}))
	defer model.Close()
	cfg := DefaultLocalConfig()
	cfg.Answer.Model = "test"
	cfg.Answer.BaseURL = model.URL
	if e := SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	in := WebOperationInput{RequestID: strings.Repeat("c", 32), Kind: "graph-analyze", ConfigRevision: cfg.Revision(), Focus: "pressure", Batches: 8, ContextChars: 48000}
	if _, e := StartWebOperationIn(context.Background(), dir, "local", in, true); e != nil {
		t.Fatal(e)
	}
	if worked, e := ProcessWebOperationIn(context.Background(), dir, "local"); e != nil || !worked {
		t.Fatal(worked, e)
	}
	jobs, e := ListWebOperationsIn(context.Background(), dir, "local")
	if e != nil || len(jobs) != 1 || jobs[0].Status != "completed" || jobs[0].RunID == "" {
		t.Fatal(jobs, e)
	}
	run, e := s.LoadCorpusAnalysisRun(jobs[0].RunID)
	if e != nil || len(run.Batches) == 0 || run.Batches[0].Status != CorpusAnalysisBatchInsufficient {
		t.Fatal(run, e)
	}
}
