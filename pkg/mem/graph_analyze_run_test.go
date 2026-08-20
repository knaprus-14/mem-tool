package mem

import (
	"strings"
	"testing"
)

func TestCorpusAnalysisRunPersistsValidatedBatchesAndResumesIdempotently(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()

	answer := corpusAnalysisRunAnswerConfig()
	run, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.Status != CorpusAnalysisRunRunning || len(run.Batches) != 3 {
		t.Fatalf("unexpected initial run: %#v", run)
	}
	firstGraph := decodeCorpusRunBatch(t, plan.Batches[0], "Persisted gap")
	if err := store.SaveCorpusAnalysisBatchGraph(run.ID, plan.Batches[0].BatchID, firstGraph); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCorpusAnalysisBatchInsufficient(run.ID, plan.Batches[1].BatchID, "no comparison"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCorpusAnalysisBatchFailure(run.ID, plan.Batches[2].BatchID, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCorpusAnalysisRun(run.ID); err == nil || !strings.Contains(err.Error(), "unfinished") {
		t.Fatalf("run completed with a failed batch: %v", err)
	}

	resumed, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != run.ID || resumed.Batches[0].Status != CorpusAnalysisBatchCompleted ||
		len(resumed.Batches[0].Graph.Nodes) != 1 || resumed.Batches[1].Status != CorpusAnalysisBatchInsufficient ||
		resumed.Batches[2].Status != CorpusAnalysisBatchFailed {
		t.Fatalf("durable batch state was not restored: %#v", resumed)
	}
	if err := store.SaveCorpusAnalysisBatchInsufficient(run.ID, plan.Batches[2].BatchID, "retry found nothing"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := store.LoadCorpusAnalysisRun(run.ID)
	if err != nil || completed.Status != CorpusAnalysisRunCompleted {
		t.Fatalf("completed run was not durable: %#v err=%v", completed, err)
	}
}

func TestCorpusAnalysisRunResumeRejectsChangedClaimPrompt(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()
	answer := corpusAnalysisRunAnswerConfig()
	run, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}

	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for i := range graph.Nodes {
		if graph.Nodes[i].ID == "plan-claim-a" {
			graph.Nodes[i].Label = "Changed pressure claim"
			if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{graph.Nodes[i]}}); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	changedPlan, err := store.BuildCorpusAnalysisPlan("pressure", budget, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, changedPlan, answer, run.ID); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("stale run resumed after claim prompt changed: %v", err)
	}
}

func TestCorpusAnalysisRunResumeRejectsChangedGenerationSettings(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()
	answer := corpusAnalysisRunAnswerConfig()
	run, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	answer.Model = "another-chat-model"
	if _, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, run.ID); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("run resumed with changed generation settings: %v", err)
	}
}

func TestCorpusAnalysisRunResumeRejectsChangedEvidence(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()
	answer := corpusAnalysisRunAnswerConfig()
	run, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}

	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	var anchor EvidenceAnchor
	for _, node := range graph.Nodes {
		if node.ID == "plan-claim-a" {
			anchor = node.Evidence[0]
			break
		}
	}
	entries := store.GetBySourceFile(anchor.SourcePath)
	if len(entries) != 1 {
		t.Fatalf("got %d source entries, want 1", len(entries))
	}
	entry := entries[0]
	changedText := entry.Text + " Updated."
	if err := store.ReplaceDocumentChunks(anchor.SourcePath, []DocumentChunk{{
		Text: changedText, Title: entry.Title, Tags: entry.Tags, Backend: entry.Backend,
		Embedding: entry.Embedding, ChunkLabel: entry.ChunkLabel, ChunkIndex: 0, TotalChunks: 1,
		Provenance: Provenance{
			DocumentID: entry.DocumentID, DocumentRevision: ChunkContentHash("changed analysis revision"),
			ChunkHash: ChunkContentHash(changedText), SourcePath: anchor.SourcePath, MediaType: entry.MediaType,
			Page: entry.Page, BlockIndex: entry.BlockIndex, BlockMarker: entry.BlockMarker,
			BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: entry.ExtractionMethod,
			OCRConfidence: entry.OCRConfidence,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	changedPlan, err := store.BuildCorpusAnalysisPlan("pressure", budget, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, changedPlan, answer, run.ID); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("stale run resumed after evidence changed: %v", err)
	}
}

func TestCorpusAnalysisRunRejectsTamperedBatchResult(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()
	run, err := store.PrepareCorpusAnalysisRun("pressure", budget, 3, plan, corpusAnalysisRunAnswerConfig(), "")
	if err != nil {
		t.Fatal(err)
	}
	graph := decodeCorpusRunBatch(t, plan.Batches[0], "Digest gap")
	if err := store.SaveCorpusAnalysisBatchGraph(run.ID, plan.Batches[0].BatchID, graph); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_analysis_batches SET result_json = result_json || ' '
        WHERE run_id = ? AND batch_id = ?`, run.ID, plan.Batches[0].BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadCorpusAnalysisRun(run.ID); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered result was accepted: %v", err)
	}
}

func corpusAnalysisRunFixture(t *testing.T) (*Store, CorpusAnalysisPlan, int) {
	t.Helper()
	store := corpusAnalysisStoreWithClaims(t, 5)
	candidates, _, _, err := store.loadCorpusAnalysisCandidates("pressure")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, budget, err := buildCorpusAnalysisPromptPayload("pressure", []CorpusAnalysisClaim{candidates[0].claim, candidates[1].claim})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	plan, err := store.BuildCorpusAnalysisPlan("pressure", budget, 3)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if len(plan.Batches) != 3 {
		store.Close()
		t.Fatalf("fixture produced %d batches", len(plan.Batches))
	}
	return store, plan, budget
}

func decodeCorpusRunBatch(t *testing.T, prompt CorpusAnalysisPrompt, label string) KnowledgeGraph {
	t.Helper()
	raw := `{"findings":[{"kind":"gap","label":"` + label + `","confidence":0.8,"claim_refs":["c1","c2"],"citations":["` +
		prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Graph
}

func corpusAnalysisRunAnswerConfig() AnswerConfig {
	return AnswerConfig{BaseURL: "http://127.0.0.1:11434", Model: "test-chat", MaxTokens: 1000, Temperature: 0.1}
}
