package mem

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKnowledgeExtractionJobPlanSelectsOnlyUncoveredUnprocessedChunks(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{
		strings.Repeat("alpha ", 90), strings.Repeat("beta ", 90), strings.Repeat("gamma ", 90),
	})
	defer store.Close()
	anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "job-covered", Kind: KnowledgeNodeClaim, Label: "Covered", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Confidence: 0.8, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	secondCitation, _ := CitationForEntry(entries[1])
	if _, err := store.db.Exec(`INSERT INTO knowledge_extraction_coverage
        (citation_id, document_id, document_revision, chunk_hash, source_path, page, block_index,
         block_chunk_index, run_id, batch_id, outcome, created)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'fixture-run', 'fixture-batch', 'insufficient', '2026-01-01T00:00:00Z')`,
		secondCitation, entries[1].DocumentID, entries[1].DocumentRevision, entries[1].ChunkHash,
		entries[1].SourcePath, entries[1].Page, entries[1].BlockIndex, entries[1].BlockChunkIndex); err != nil {
		t.Fatal(err)
	}

	plan, err := store.BuildKnowledgeExtractionJobPlan("complete extraction", KnowledgeCoverageOptions{}, MaxAnswerContextChars, 4)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScopedChunks != 3 || plan.AlreadyCoveredChunks != 1 || plan.PreviouslyProcessedChunks != 1 ||
		plan.EligibleChunks != 1 || plan.SelectedChunks != 1 || plan.RemainingChunks != 0 || len(plan.Batches) != 1 {
		t.Fatalf("unexpected extraction plan: %#v", plan)
	}
	thirdCitation, _ := CitationForEntry(entries[2])
	if len(plan.Batches[0].Prompt.Evidence) != 1 || plan.Batches[0].Prompt.Evidence[0].CitationID != thirdCitation ||
		plan.Batches[0].Prompt.Evidence[0].Text != entries[2].Text {
		t.Fatalf("planner selected or truncated the wrong chunk: %#v", plan.Batches[0])
	}
	repeated, err := store.BuildKnowledgeExtractionJobPlan("complete extraction", KnowledgeCoverageOptions{}, MaxAnswerContextChars, 4)
	if err != nil || repeated.SnapshotDigest != plan.SnapshotDigest || repeated.Batches[0].BatchID != plan.Batches[0].BatchID {
		t.Fatalf("extraction plan is not deterministic: first=%#v repeated=%#v err=%v", plan, repeated, err)
	}
}

func TestKnowledgeExtractionRunCheckpointsResumeFinalizeAndSkipProcessed(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{
		strings.Repeat("first evidence ", 80), strings.Repeat("second evidence ", 80),
	})
	defer store.Close()
	focus := "full document extraction"
	budget := 0
	for i := range entries {
		onePrompt, err := buildKnowledgeExtractionJobPrompt(focus, entries[i:i+1], MaxAnswerContextChars, 65)
		if err != nil {
			t.Fatal(err)
		}
		size := utf8.RuneCountInString(onePrompt.System) + utf8.RuneCountInString(onePrompt.User)
		if size > budget {
			budget = size
		}
	}
	plan, err := store.BuildKnowledgeExtractionJobPlan(focus, KnowledgeCoverageOptions{}, budget, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 2 || plan.SelectedChunks != 2 {
		t.Fatalf("fixture did not produce two complete batches: %#v", plan)
	}
	answer := AnswerConfig{BaseURL: "http://localhost:11434", Model: "test-chat", MaxTokens: 4096}
	run, err := store.PrepareKnowledgeExtractionRun(plan, focus, budget, 4, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.ID, "ker-") || run.Status != KnowledgeExtractionRunRunning || len(run.Batches) != 2 {
		t.Fatalf("invalid extraction run: %#v", run)
	}
	firstEntry := plan.Batches[0].Entries[0]
	anchor, err := EvidenceAnchorForEntry(firstEntry, firstEntry.Text)
	if err != nil {
		t.Fatal(err)
	}
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "job-result", Kind: KnowledgeNodeClaim, Label: "Extracted result", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Confidence: 0.9, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := store.SaveKnowledgeExtractionBatchGraph(run.ID, plan.Batches[0].BatchID, graph); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveKnowledgeExtractionBatchFailure(run.ID, plan.Batches[1].BatchID, errPlannedExtractionFailure{}); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.PrepareKnowledgeExtractionRun(plan, focus, budget, 4, answer, run.ID)
	if err != nil || resumed.Batches[0].Status != KnowledgeExtractionBatchCompleted || resumed.Batches[1].Status != KnowledgeExtractionBatchFailed {
		t.Fatalf("extraction checkpoints did not resume: run=%#v err=%v", resumed, err)
	}
	if err := store.SaveKnowledgeExtractionBatchInsufficient(run.ID, plan.Batches[1].BatchID, "no additional facts"); err != nil {
		t.Fatal(err)
	}
	merged, err := store.FinalizeKnowledgeExtractionRun(run.ID, plan)
	if err != nil || len(merged.Nodes) != 1 {
		t.Fatalf("finalize extraction run: graph=%#v err=%v", merged, err)
	}
	completed, err := store.LoadKnowledgeExtractionRun(run.ID)
	if err != nil || completed.Status != KnowledgeExtractionRunCompleted {
		t.Fatalf("completed extraction run was not persisted: run=%#v err=%v", completed, err)
	}
	processed, err := store.currentKnowledgeExtractionProcessedCitations(entries)
	if err != nil || len(processed) != 2 {
		t.Fatalf("processed chunk ledger is incomplete: processed=%#v err=%v", processed, err)
	}
	remaining, err := store.BuildKnowledgeExtractionJobPlan(focus, KnowledgeCoverageOptions{}, budget, 4)
	if err != nil || remaining.EligibleChunks != 0 || len(remaining.Batches) != 0 {
		t.Fatalf("completed chunks were selected again: plan=%#v err=%v", remaining, err)
	}
	if _, err := store.FinalizeKnowledgeExtractionRun(run.ID, plan); err != nil {
		t.Fatalf("finalization is not idempotent: %v", err)
	}
}

func TestKnowledgeExtractionResumeRejectsChangedRevision(t *testing.T) {
	store, _ := newKnowledgeExtractionJobStore(t, []string{"original evidence"})
	defer store.Close()
	focus := "revision pin"
	plan, err := store.BuildKnowledgeExtractionJobPlan(focus, KnowledgeCoverageOptions{}, MaxAnswerContextChars, 2)
	if err != nil {
		t.Fatal(err)
	}
	answer := AnswerConfig{BaseURL: "http://localhost:11434", Model: "test-chat", MaxTokens: 4096}
	run, err := store.PrepareKnowledgeExtractionRun(plan, focus, MaxAnswerContextChars, 2, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	changed := knowledgeExtractionJobChunks([]string{"changed evidence"}, "changed revision")
	if err := store.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatal(err)
	}
	changedPlan, err := store.BuildKnowledgeExtractionJobPlan(focus, KnowledgeCoverageOptions{}, MaxAnswerContextChars, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareKnowledgeExtractionRun(changedPlan, focus, MaxAnswerContextChars, 2, answer, run.ID); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("changed revision was accepted by resume: %v", err)
	}
}

type errPlannedExtractionFailure struct{}

func (errPlannedExtractionFailure) Error() string { return "planned extraction failure" }

func newKnowledgeExtractionJobStore(t *testing.T, texts []string) (*Store, []Entry) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	chunks := knowledgeExtractionJobChunks(texts, "job revision")
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, store.GetBySourceFile(chunks[0].Provenance.SourcePath)
}

func knowledgeExtractionJobChunks(texts []string, revision string) []DocumentChunk {
	const source = "C:/docs/extraction-job.pdf"
	chunks := make([]DocumentChunk, len(texts))
	for i, text := range texts {
		chunks[i] = DocumentChunk{
			Text: text, Title: "Extraction job", Tags: []string{"manual"}, Backend: "test",
			Embedding: []float32{1, 0}, ChunkIndex: i, TotalChunks: len(texts),
			Provenance: Provenance{
				DocumentID: "doc-extraction-job", DocumentRevision: ChunkContentHash(revision), ChunkHash: ChunkContentHash(text),
				SourcePath: source, MediaType: "application/pdf", Page: i + 1, BlockIndex: i,
				BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
			},
		}
	}
	return chunks
}
