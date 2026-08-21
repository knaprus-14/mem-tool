package mem

import (
	"errors"
	"strings"
	"testing"
	"time"
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

func TestCorpusAnalysisRunHistoryAndPruneProtectsRunningAndLatest(t *testing.T) {
	store, plan, budget := corpusAnalysisRunFixture(t)
	defer store.Close()
	answer := corpusAnalysisRunAnswerConfig()

	completeRun := func(focus string) CorpusAnalysisRun {
		t.Helper()
		run, err := store.PrepareCorpusAnalysisRun(focus, budget, 3, plan, answer, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, batch := range run.Batches {
			if err := store.SaveCorpusAnalysisBatchInsufficient(run.ID, batch.BatchID, "no cross-document finding"); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
			t.Fatal(err)
		}
		return run
	}
	oldCompleted := completeRun("old completed pressure analysis")
	latestCompleted := completeRun("latest completed pressure analysis")
	running, err := store.PrepareCorpusAnalysisRun("resumable pressure analysis", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCorpusAnalysisBatchFailure(running.ID, running.Batches[0].BatchID, errors.New("temporary model outage")); err != nil {
		t.Fatal(err)
	}

	updates := map[string]string{
		oldCompleted.ID:    "2020-01-01T00:00:00Z",
		latestCompleted.ID: "2022-01-01T00:00:00Z",
		running.ID:         "2019-01-01T00:00:00Z",
	}
	for id, updated := range updates {
		if _, err := store.db.Exec(`UPDATE knowledge_analysis_runs SET updated = ? WHERE id = ?`, updated, id); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := store.ListCorpusAnalysisRuns(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != latestCompleted.ID || runs[1].ID != oldCompleted.ID || runs[2].ID != running.ID {
		t.Fatalf("analysis history order is wrong: %#v", runs)
	}
	if runs[0].InsufficientBatches != 3 || runs[0].Status != CorpusAnalysisRunCompleted {
		t.Fatalf("completed summary lost batch counts: %#v", runs[0])
	}
	if runs[2].FailedBatches != 1 || runs[2].PendingBatches != 2 || runs[2].Status != CorpusAnalysisRunRunning {
		t.Fatalf("running summary lost resumable state: %#v", runs[2])
	}

	cutoff := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	preview, err := store.PruneCompletedCorpusAnalysisRuns(cutoff, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || len(preview.Runs) != 1 || preview.Runs[0].ID != oldCompleted.ID || preview.DeletedRuns != 0 {
		t.Fatalf("unexpected prune preview: %#v", preview)
	}
	if _, err := store.LoadCorpusAnalysisRun(oldCompleted.ID); err != nil {
		t.Fatalf("dry run deleted history: %v", err)
	}

	pruned, err := store.PruneCompletedCorpusAnalysisRuns(cutoff, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.DryRun || pruned.DeletedRuns != 1 || pruned.DeletedBatches != 3 || len(pruned.Runs) != 1 {
		t.Fatalf("unexpected prune result: %#v", pruned)
	}
	if _, err := store.LoadCorpusAnalysisRun(oldCompleted.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("old completed run survived prune: %v", err)
	}
	if _, err := store.LoadCorpusAnalysisRun(latestCompleted.ID); err != nil {
		t.Fatalf("latest completed run was not protected: %v", err)
	}
	resumable, err := store.LoadCorpusAnalysisRun(running.ID)
	if err != nil || resumable.Status != CorpusAnalysisRunRunning || resumable.Batches[0].Status != CorpusAnalysisBatchFailed {
		t.Fatalf("running run was not protected: run=%#v err=%v", resumable, err)
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
