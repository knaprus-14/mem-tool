package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const knowledgeExtractionRunSchema = `
CREATE TABLE IF NOT EXISTS knowledge_extraction_runs (
    id TEXT PRIMARY KEY,
    focus TEXT NOT NULL,
    scope_json TEXT NOT NULL,
    snapshot_digest TEXT NOT NULL,
    context_chars INTEGER NOT NULL,
    max_batches INTEGER NOT NULL,
    generation_digest TEXT NOT NULL,
    plan_digest TEXT NOT NULL,
    batch_count INTEGER NOT NULL,
    scoped_chunks INTEGER NOT NULL,
    eligible_chunks INTEGER NOT NULL,
    selected_chunks INTEGER NOT NULL,
    remaining_chunks INTEGER NOT NULL,
    status TEXT NOT NULL,
    created TEXT NOT NULL,
    updated TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_extraction_batches (
    run_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    batch_id TEXT NOT NULL,
    prompt_digest TEXT NOT NULL,
    evidence_count INTEGER NOT NULL,
    evidence_json TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '',
    result_digest TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    updated TEXT NOT NULL,
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, batch_id)
);

CREATE TABLE IF NOT EXISTS knowledge_extraction_coverage (
    citation_id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    chunk_hash TEXT NOT NULL,
    source_path TEXT NOT NULL,
    page INTEGER NOT NULL,
    block_index INTEGER NOT NULL,
    block_chunk_index INTEGER NOT NULL,
    run_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    created TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_knowledge_extraction_runs_status
    ON knowledge_extraction_runs(status, updated);
CREATE INDEX IF NOT EXISTS idx_knowledge_extraction_batches_status
    ON knowledge_extraction_batches(run_id, status, ordinal);
CREATE INDEX IF NOT EXISTS idx_knowledge_extraction_coverage_document
    ON knowledge_extraction_coverage(document_id, document_revision, page);
`

type KnowledgeExtractionRunStatus string

const (
	KnowledgeExtractionRunRunning   KnowledgeExtractionRunStatus = "running"
	KnowledgeExtractionRunCompleted KnowledgeExtractionRunStatus = "completed"
)

type KnowledgeExtractionBatchStatus string

const (
	KnowledgeExtractionBatchPending      KnowledgeExtractionBatchStatus = "pending"
	KnowledgeExtractionBatchCompleted    KnowledgeExtractionBatchStatus = "completed"
	KnowledgeExtractionBatchInsufficient KnowledgeExtractionBatchStatus = "insufficient"
	KnowledgeExtractionBatchFailed       KnowledgeExtractionBatchStatus = "failed"
)

type KnowledgeExtractionRun struct {
	ID               string                        `json:"id"`
	Focus            string                        `json:"focus"`
	Scope            KnowledgeCoverageOptions      `json:"scope"`
	SnapshotDigest   string                        `json:"snapshot_digest"`
	ContextChars     int                           `json:"context_chars"`
	MaxBatches       int                           `json:"max_batches"`
	GenerationDigest string                        `json:"generation_digest"`
	PlanDigest       string                        `json:"plan_digest"`
	ScopedChunks     int                           `json:"scoped_chunks"`
	EligibleChunks   int                           `json:"eligible_chunks"`
	SelectedChunks   int                           `json:"selected_chunks"`
	RemainingChunks  int                           `json:"remaining_chunks"`
	Status           KnowledgeExtractionRunStatus  `json:"status"`
	Created          string                        `json:"created"`
	Updated          string                        `json:"updated"`
	Batches          []KnowledgeExtractionRunBatch `json:"batches"`
}

type KnowledgeExtractionRunBatch struct {
	Ordinal       int                            `json:"ordinal"`
	BatchID       string                         `json:"batch_id"`
	PromptDigest  string                         `json:"prompt_digest"`
	EvidenceCount int                            `json:"evidence_count"`
	Citations     []string                       `json:"citations"`
	Status        KnowledgeExtractionBatchStatus `json:"status"`
	Graph         KnowledgeGraph                 `json:"graph,omitempty"`
	Reason        string                         `json:"reason,omitempty"`
	Updated       string                         `json:"updated"`
}

type KnowledgeExtractionRunSummary struct {
	ID                  string                       `json:"id"`
	Focus               string                       `json:"focus"`
	Scope               KnowledgeCoverageOptions     `json:"scope"`
	ScopedChunks        int                          `json:"scoped_chunks"`
	EligibleChunks      int                          `json:"eligible_chunks"`
	SelectedChunks      int                          `json:"selected_chunks"`
	RemainingChunks     int                          `json:"remaining_chunks"`
	Status              KnowledgeExtractionRunStatus `json:"status"`
	BatchCount          int                          `json:"batch_count"`
	PendingBatches      int                          `json:"pending_batches"`
	CompletedBatches    int                          `json:"completed_batches"`
	InsufficientBatches int                          `json:"insufficient_batches"`
	FailedBatches       int                          `json:"failed_batches"`
	Created             string                       `json:"created"`
	Updated             string                       `json:"updated"`
}

type knowledgeExtractionBatchManifest struct {
	BatchID       string   `json:"batch_id"`
	PromptDigest  string   `json:"prompt_digest"`
	EvidenceCount int      `json:"evidence_count"`
	Citations     []string `json:"citations"`
}

type knowledgeExtractionPlanManifest struct {
	Focus            string                             `json:"focus"`
	Scope            KnowledgeCoverageOptions           `json:"scope"`
	SnapshotDigest   string                             `json:"snapshot_digest"`
	ContextChars     int                                `json:"context_chars"`
	MaxBatches       int                                `json:"max_batches"`
	GenerationDigest string                             `json:"generation_digest"`
	ScopedChunks     int                                `json:"scoped_chunks"`
	EligibleChunks   int                                `json:"eligible_chunks"`
	SelectedChunks   int                                `json:"selected_chunks"`
	RemainingChunks  int                                `json:"remaining_chunks"`
	Batches          []knowledgeExtractionBatchManifest `json:"batches"`
}

func (s *Store) PrepareKnowledgeExtractionRun(plan KnowledgeExtractionJobPlan, focus string, contextChars, maxBatches int, answer AnswerConfig, resumeID string) (KnowledgeExtractionRun, error) {
	manifest, planDigest, runID, err := buildKnowledgeExtractionRunIdentity(plan, focus, contextChars, maxBatches, answer)
	if err != nil {
		return KnowledgeExtractionRun{}, err
	}
	resumeID = strings.TrimSpace(resumeID)
	if resumeID != "" && resumeID != runID {
		return KnowledgeExtractionRun{}, fmt.Errorf("extraction run %q no longer matches the current plan (expected %s); source chunks, scope, focus, or generation settings changed", resumeID, runID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	scopeJSON, err := json.Marshal(manifest.Scope)
	if err != nil {
		return KnowledgeExtractionRun{}, fmt.Errorf("encode extraction scope: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeExtractionRun{}, fmt.Errorf("begin extraction run: %w", err)
	}
	rollback := func(cause error) (KnowledgeExtractionRun, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeExtractionRun{}, fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeExtractionRun{}, cause
	}
	var existingDigest string
	err = tx.QueryRow(`SELECT plan_digest FROM knowledge_extraction_runs WHERE id = ?`, runID).Scan(&existingDigest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO knowledge_extraction_runs
            (id, focus, scope_json, snapshot_digest, context_chars, max_batches, generation_digest, plan_digest,
             batch_count, scoped_chunks, eligible_chunks, selected_chunks, remaining_chunks, status, created, updated)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, manifest.Focus, string(scopeJSON), manifest.SnapshotDigest, contextChars, maxBatches,
			manifest.GenerationDigest, planDigest, len(manifest.Batches), manifest.ScopedChunks,
			manifest.EligibleChunks, manifest.SelectedChunks, manifest.RemainingChunks,
			KnowledgeExtractionRunRunning, now, now); err != nil {
			return rollback(fmt.Errorf("create extraction run: %w", err))
		}
		for ordinal, batch := range manifest.Batches {
			citationsJSON, encodeErr := json.Marshal(batch.Citations)
			if encodeErr != nil {
				return rollback(fmt.Errorf("encode extraction batch %d evidence: %w", ordinal+1, encodeErr))
			}
			if _, err := tx.Exec(`INSERT INTO knowledge_extraction_batches
				(run_id, ordinal, batch_id, prompt_digest, evidence_count, evidence_json, status, updated)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, runID, ordinal, batch.BatchID, batch.PromptDigest,
				batch.EvidenceCount, string(citationsJSON), KnowledgeExtractionBatchPending, now); err != nil {
				return rollback(fmt.Errorf("create extraction batch %d: %w", ordinal+1, err))
			}
		}
	case err != nil:
		return rollback(fmt.Errorf("inspect extraction run: %w", err))
	case existingDigest != planDigest:
		return rollback(errors.New("stored extraction run plan digest disagrees with its stable ID"))
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeExtractionRun{}, fmt.Errorf("commit extraction run: %w", err)
	}
	run, err := loadKnowledgeExtractionRun(s.db, runID)
	if err != nil {
		return KnowledgeExtractionRun{}, err
	}
	if err := validateKnowledgeExtractionRunManifest(run, manifest, planDigest); err != nil {
		return KnowledgeExtractionRun{}, err
	}
	return run, nil
}

func buildKnowledgeExtractionRunIdentity(plan KnowledgeExtractionJobPlan, focus string, contextChars, maxBatches int, answer AnswerConfig) (knowledgeExtractionPlanManifest, string, string, error) {
	focus = strings.TrimSpace(focus)
	if contextChars < 1 || contextChars > MaxAnswerContextChars {
		return knowledgeExtractionPlanManifest{}, "", "", fmt.Errorf("extraction run context chars must be between 1 and %d", MaxAnswerContextChars)
	}
	if maxBatches < 1 || maxBatches > MaxKnowledgeExtractionJobBatches {
		return knowledgeExtractionPlanManifest{}, "", "", fmt.Errorf("extraction run batches must be between 1 and %d", MaxKnowledgeExtractionJobBatches)
	}
	if focus == "" || len(plan.Batches) == 0 || len(plan.Batches) > maxBatches || plan.SelectedChunks < 1 {
		return knowledgeExtractionPlanManifest{}, "", "", errors.New("extraction run plan is empty or invalid")
	}
	if !strings.HasPrefix(plan.SnapshotDigest, "sha256:") || plan.EligibleChunks < plan.SelectedChunks ||
		plan.RemainingChunks != plan.EligibleChunks-plan.SelectedChunks {
		return knowledgeExtractionPlanManifest{}, "", "", errors.New("extraction run coverage metadata is invalid")
	}
	answer = answer.WithDefaults()
	if strings.TrimSpace(answer.Model) == "" {
		return knowledgeExtractionPlanManifest{}, "", "", errors.New("extraction run answer model is empty")
	}
	generationJSON, err := json.Marshal(struct {
		BaseURL     string  `json:"base_url"`
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}{strings.TrimSpace(answer.BaseURL), strings.TrimSpace(answer.Model), answer.MaxTokens, answer.Temperature})
	if err != nil {
		return knowledgeExtractionPlanManifest{}, "", "", err
	}
	manifest := knowledgeExtractionPlanManifest{
		Focus: focus, Scope: plan.Scope, SnapshotDigest: plan.SnapshotDigest,
		ContextChars: contextChars, MaxBatches: maxBatches,
		GenerationDigest: digestCorpusAnalysisBytes(generationJSON),
		ScopedChunks:     plan.ScopedChunks, EligibleChunks: plan.EligibleChunks,
		SelectedChunks: plan.SelectedChunks, RemainingChunks: plan.RemainingChunks,
		Batches: make([]knowledgeExtractionBatchManifest, 0, len(plan.Batches)),
	}
	seen := make(map[string]bool)
	selectedChunks := 0
	for _, batch := range plan.Batches {
		if batch.BatchID == "" || seen[batch.BatchID] || len(batch.Prompt.Evidence) == 0 ||
			!knowledgeExtractionJobPromptFits(batch.Prompt, batch.Entries, contextChars) {
			return knowledgeExtractionPlanManifest{}, "", "", errors.New("extraction run contains an invalid batch")
		}
		seen[batch.BatchID] = true
		selectedChunks += len(batch.Entries)
		item := knowledgeExtractionBatchManifest{
			BatchID: batch.BatchID, PromptDigest: digestCorpusAnalysisBytes([]byte(batch.Prompt.System + "\x00" + batch.Prompt.User)),
			EvidenceCount: len(batch.Prompt.Evidence), Citations: make([]string, 0, len(batch.Prompt.Evidence)),
		}
		for _, evidence := range batch.Prompt.Evidence {
			item.Citations = append(item.Citations, evidence.CitationID)
		}
		manifest.Batches = append(manifest.Batches, item)
	}
	if selectedChunks != plan.SelectedChunks {
		return knowledgeExtractionPlanManifest{}, "", "", errors.New("extraction run selected chunk count disagrees with its batches")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return knowledgeExtractionPlanManifest{}, "", "", err
	}
	planDigest := digestCorpusAnalysisBytes(encoded)
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-extraction-run-v1")
	writeKnowledgeIDField(h, planDigest)
	return manifest, planDigest, "ker-" + hex.EncodeToString(h.Sum(nil)[:16]), nil
}

func validateKnowledgeExtractionRunManifest(run KnowledgeExtractionRun, manifest knowledgeExtractionPlanManifest, planDigest string) error {
	if run.Focus != manifest.Focus || run.SnapshotDigest != manifest.SnapshotDigest || run.ContextChars != manifest.ContextChars ||
		run.MaxBatches != manifest.MaxBatches || run.GenerationDigest != manifest.GenerationDigest || run.PlanDigest != planDigest ||
		run.ScopedChunks != manifest.ScopedChunks || run.EligibleChunks != manifest.EligibleChunks ||
		run.SelectedChunks != manifest.SelectedChunks || run.RemainingChunks != manifest.RemainingChunks || len(run.Batches) != len(manifest.Batches) {
		return errors.New("stored extraction run metadata does not match the current deterministic plan")
	}
	for i, batch := range run.Batches {
		want := manifest.Batches[i]
		if batch.Ordinal != i || batch.BatchID != want.BatchID || batch.PromptDigest != want.PromptDigest ||
			batch.EvidenceCount != want.EvidenceCount || !equalStringSlices(batch.Citations, want.Citations) {
			return fmt.Errorf("stored extraction batch %d does not match the current deterministic plan", i+1)
		}
	}
	return nil
}

func (s *Store) LoadKnowledgeExtractionRun(runID string) (KnowledgeExtractionRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return KnowledgeExtractionRun{}, errors.New("extraction run ID is empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return loadKnowledgeExtractionRun(s.db, runID)
}

// RebuildKnowledgeExtractionRunPlan reconstructs the exact immutable evidence
// batches stored for a run. It is used by -resume so a graph upsert from an
// interrupted finalization cannot make its own chunks disappear from the plan.
func (s *Store) RebuildKnowledgeExtractionRunPlan(runID, focus string) (KnowledgeExtractionJobPlan, error) {
	run, err := s.LoadKnowledgeExtractionRun(runID)
	if err != nil {
		return KnowledgeExtractionJobPlan{}, err
	}
	if strings.TrimSpace(focus) != run.Focus {
		return KnowledgeExtractionJobPlan{}, errors.New("resume focus does not match the stored extraction run")
	}
	report, err := s.BuildKnowledgeCoverageReport(run.Scope)
	if err != nil {
		return KnowledgeExtractionJobPlan{}, err
	}
	if report.SnapshotDigest != run.SnapshotDigest {
		return KnowledgeExtractionJobPlan{}, errors.New("extraction source snapshot changed; the stored run cannot be resumed")
	}
	entries := s.knowledgeCoverageEntries(run.Scope)
	byCitation := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		citationID, _ := CitationForEntry(entry)
		byCitation[citationID] = entry
	}
	plan := KnowledgeExtractionJobPlan{
		Scope: run.Scope, SnapshotDigest: run.SnapshotDigest, ScopedChunks: run.ScopedChunks,
		EligibleChunks: run.EligibleChunks, SelectedChunks: run.SelectedChunks, RemainingChunks: run.RemainingChunks,
		AlreadyCoveredChunks:      report.Summary.CoveredChunks,
		PreviouslyProcessedChunks: report.Summary.ProcessedChunks - report.Summary.CoveredChunks,
		Documents:                 report.Summary.Documents, Pages: report.Summary.PagesWithText,
		Batches: make([]KnowledgeExtractionJobBatch, 0, len(run.Batches)),
	}
	if plan.PreviouslyProcessedChunks < 0 {
		plan.PreviouslyProcessedChunks = 0
	}
	for _, stored := range run.Batches {
		batchEntries := make([]Entry, 0, len(stored.Citations))
		for _, citationID := range stored.Citations {
			entry, ok := byCitation[citationID]
			if !ok {
				return KnowledgeExtractionJobPlan{}, fmt.Errorf("extraction evidence %q is no longer current", citationID)
			}
			batchEntries = append(batchEntries, entry)
		}
		prompt, err := buildKnowledgeExtractionJobPrompt(run.Focus, batchEntries, run.ContextChars, run.Scope.LowConfidence)
		if err != nil {
			return KnowledgeExtractionJobPlan{}, err
		}
		batch := KnowledgeExtractionJobBatch{BatchID: stored.BatchID, Prompt: prompt, Entries: batchEntries}
		if !knowledgeExtractionJobPromptFits(prompt, batchEntries, run.ContextChars) ||
			stableKnowledgeExtractionJobBatchID(run.Focus, prompt.Evidence) != stored.BatchID ||
			digestCorpusAnalysisBytes([]byte(prompt.System+"\x00"+prompt.User)) != stored.PromptDigest {
			return KnowledgeExtractionJobPlan{}, fmt.Errorf("extraction batch %q no longer matches its stored prompt", stored.BatchID)
		}
		plan.Batches = append(plan.Batches, batch)
	}
	return plan, nil
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func loadKnowledgeExtractionRun(q interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}, runID string) (KnowledgeExtractionRun, error) {
	var run KnowledgeExtractionRun
	var scopeJSON string
	var batchCount int
	err := q.QueryRow(`SELECT id, focus, scope_json, snapshot_digest, context_chars, max_batches,
        generation_digest, plan_digest, batch_count, scoped_chunks, eligible_chunks, selected_chunks,
        remaining_chunks, status, created, updated FROM knowledge_extraction_runs WHERE id = ?`, runID).Scan(
		&run.ID, &run.Focus, &scopeJSON, &run.SnapshotDigest, &run.ContextChars, &run.MaxBatches,
		&run.GenerationDigest, &run.PlanDigest, &batchCount, &run.ScopedChunks, &run.EligibleChunks,
		&run.SelectedChunks, &run.RemainingChunks, &run.Status, &run.Created, &run.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return KnowledgeExtractionRun{}, fmt.Errorf("extraction run %q not found", runID)
	}
	if err != nil {
		return KnowledgeExtractionRun{}, fmt.Errorf("load extraction run: %w", err)
	}
	if err := json.Unmarshal([]byte(scopeJSON), &run.Scope); err != nil {
		return KnowledgeExtractionRun{}, fmt.Errorf("decode extraction run scope: %w", err)
	}
	if run.Status != KnowledgeExtractionRunRunning && run.Status != KnowledgeExtractionRunCompleted {
		return KnowledgeExtractionRun{}, fmt.Errorf("extraction run %q has invalid status %q", runID, run.Status)
	}
	rows, err := q.Query(`SELECT ordinal, batch_id, prompt_digest, evidence_count, evidence_json, status,
        result_json, result_digest, reason, updated FROM knowledge_extraction_batches
        WHERE run_id = ? ORDER BY ordinal`, runID)
	if err != nil {
		return KnowledgeExtractionRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var batch KnowledgeExtractionRunBatch
		var citationsJSON, resultJSON, resultDigest string
		if err := rows.Scan(&batch.Ordinal, &batch.BatchID, &batch.PromptDigest, &batch.EvidenceCount,
			&citationsJSON, &batch.Status, &resultJSON, &resultDigest, &batch.Reason, &batch.Updated); err != nil {
			return KnowledgeExtractionRun{}, err
		}
		if err := json.Unmarshal([]byte(citationsJSON), &batch.Citations); err != nil || len(batch.Citations) != batch.EvidenceCount {
			return KnowledgeExtractionRun{}, fmt.Errorf("extraction batch %q has invalid evidence manifest", batch.BatchID)
		}
		switch batch.Status {
		case KnowledgeExtractionBatchPending, KnowledgeExtractionBatchFailed:
			if resultJSON != "" || resultDigest != "" {
				return KnowledgeExtractionRun{}, fmt.Errorf("extraction batch %q has unexpected result data", batch.BatchID)
			}
		case KnowledgeExtractionBatchInsufficient:
			if resultJSON != "" || resultDigest != "" {
				return KnowledgeExtractionRun{}, fmt.Errorf("insufficient extraction batch %q contains a graph", batch.BatchID)
			}
		case KnowledgeExtractionBatchCompleted:
			if resultJSON == "" || resultDigest == "" || digestCorpusAnalysisBytes([]byte(resultJSON)) != resultDigest {
				return KnowledgeExtractionRun{}, fmt.Errorf("extraction batch %q result digest mismatch", batch.BatchID)
			}
			if err := json.Unmarshal([]byte(resultJSON), &batch.Graph); err != nil {
				return KnowledgeExtractionRun{}, err
			}
			if err := validateKnowledgeExtractionFragment(batch.Graph); err != nil {
				return KnowledgeExtractionRun{}, fmt.Errorf("extraction batch %q: %w", batch.BatchID, err)
			}
		default:
			return KnowledgeExtractionRun{}, fmt.Errorf("extraction batch %q has invalid status %q", batch.BatchID, batch.Status)
		}
		run.Batches = append(run.Batches, batch)
	}
	if err := rows.Err(); err != nil {
		return KnowledgeExtractionRun{}, err
	}
	if len(run.Batches) != batchCount {
		return KnowledgeExtractionRun{}, fmt.Errorf("extraction run %q batch count mismatch", runID)
	}
	if run.Status == KnowledgeExtractionRunCompleted {
		for _, batch := range run.Batches {
			if batch.Status != KnowledgeExtractionBatchCompleted && batch.Status != KnowledgeExtractionBatchInsufficient {
				return KnowledgeExtractionRun{}, fmt.Errorf("completed extraction run %q contains unfinished batch %q", runID, batch.BatchID)
			}
		}
	}
	return run, nil
}

func validateKnowledgeExtractionFragment(graph KnowledgeGraph) error {
	if len(graph.Nodes) == 0 {
		return errors.New("knowledge extraction graph has no nodes")
	}
	for _, node := range graph.Nodes {
		if node.Status != KnowledgeStatusDraft || node.Origin != KnowledgeOriginGenerated || len(node.Evidence) == 0 {
			return fmt.Errorf("extraction node %q is not a grounded generated draft", node.ID)
		}
	}
	for _, edge := range graph.Edges {
		if edge.Status != KnowledgeStatusDraft || edge.Origin != KnowledgeOriginGenerated || len(edge.Evidence) == 0 {
			return fmt.Errorf("extraction edge %q is not a grounded generated draft", edge.ID)
		}
	}
	return ValidateKnowledgeGraph(graph)
}

func (s *Store) SaveKnowledgeExtractionBatchGraph(runID, batchID string, graph KnowledgeGraph) error {
	graph = normalizeKnowledgeGraph(graph)
	if err := validateKnowledgeExtractionFragment(graph); err != nil {
		return err
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		return err
	}
	return s.saveKnowledgeExtractionBatch(runID, batchID, KnowledgeExtractionBatchCompleted, string(encoded), digestCorpusAnalysisBytes(encoded), "")
}

func (s *Store) SaveKnowledgeExtractionBatchInsufficient(runID, batchID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Недостаточно подтверждённых данных для извлечения объектов."
	}
	return s.saveKnowledgeExtractionBatch(runID, batchID, KnowledgeExtractionBatchInsufficient, "", "", reason)
}

func (s *Store) SaveKnowledgeExtractionBatchFailure(runID, batchID string, cause error) error {
	reason := "extraction batch failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		reason = strings.TrimSpace(cause.Error())
	}
	return s.saveKnowledgeExtractionBatch(runID, batchID, KnowledgeExtractionBatchFailed, "", "", reason)
}

func (s *Store) saveKnowledgeExtractionBatch(runID, batchID string, status KnowledgeExtractionBatchStatus, resultJSON, resultDigest, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	var current KnowledgeExtractionBatchStatus
	var currentJSON, currentDigest, currentReason string
	err := s.db.QueryRow(`SELECT status, result_json, result_digest, reason FROM knowledge_extraction_batches
        WHERE run_id = ? AND batch_id = ?`, runID, batchID).Scan(&current, &currentJSON, &currentDigest, &currentReason)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("extraction batch %q does not belong to run %q", batchID, runID)
	}
	if err != nil {
		return err
	}
	if current == KnowledgeExtractionBatchCompleted || current == KnowledgeExtractionBatchInsufficient {
		if current == status && currentJSON == resultJSON && currentDigest == resultDigest && currentReason == reason {
			return nil
		}
		return fmt.Errorf("extraction batch %q already has immutable status %q", batchID, current)
	}
	result, err := s.db.Exec(`UPDATE knowledge_extraction_batches SET status = ?, result_json = ?,
        result_digest = ?, reason = ?, updated = ? WHERE run_id = ? AND batch_id = ? AND status IN (?, ?)`,
		status, resultJSON, resultDigest, reason, now, runID, batchID,
		KnowledgeExtractionBatchPending, KnowledgeExtractionBatchFailed)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("extraction batch %q changed concurrently", batchID)
	}
	_, err = s.db.Exec(`UPDATE knowledge_extraction_runs SET status = ?, updated = ? WHERE id = ?`,
		KnowledgeExtractionRunRunning, now, runID)
	return err
}

// FinalizeKnowledgeExtractionRun is idempotent. The graph is upserted only
// after every batch has a validated terminal checkpoint. If the later ledger
// transaction fails, repeating the command safely replays the same stable graph.
func (s *Store) FinalizeKnowledgeExtractionRun(runID string, plan KnowledgeExtractionJobPlan) (KnowledgeGraph, error) {
	run, err := s.LoadKnowledgeExtractionRun(runID)
	if err != nil {
		return KnowledgeGraph{}, err
	}
	if len(run.Batches) != len(plan.Batches) {
		return KnowledgeGraph{}, errors.New("extraction run and current plan have different batch counts")
	}
	graphs := make([]KnowledgeGraph, 0, len(run.Batches))
	for i, batch := range run.Batches {
		if batch.BatchID != plan.Batches[i].BatchID {
			return KnowledgeGraph{}, errors.New("extraction run and current plan disagree on batch identity")
		}
		promptDigest := digestCorpusAnalysisBytes([]byte(plan.Batches[i].Prompt.System + "\x00" + plan.Batches[i].Prompt.User))
		citations := make([]string, 0, len(plan.Batches[i].Prompt.Evidence))
		for _, evidence := range plan.Batches[i].Prompt.Evidence {
			citations = append(citations, evidence.CitationID)
		}
		if promptDigest != batch.PromptDigest || !equalStringSlices(citations, batch.Citations) ||
			!knowledgeExtractionJobPromptFits(plan.Batches[i].Prompt, plan.Batches[i].Entries, run.ContextChars) {
			return KnowledgeGraph{}, errors.New("extraction run and current plan disagree on pinned evidence")
		}
		switch batch.Status {
		case KnowledgeExtractionBatchCompleted:
			graphs = append(graphs, batch.Graph)
		case KnowledgeExtractionBatchInsufficient:
		default:
			return KnowledgeGraph{}, fmt.Errorf("extraction run %q still has unfinished batch %q", runID, batch.BatchID)
		}
	}
	merged, err := MergeKnowledgeExtractionGraphs(graphs...)
	if err != nil {
		return KnowledgeGraph{}, err
	}
	if len(merged.Nodes) > 0 {
		if err := s.UpsertCurrentKnowledgeGraph(merged); err != nil {
			return KnowledgeGraph{}, fmt.Errorf("persist extraction graph: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeGraph{}, err
	}
	rollback := func(cause error) (KnowledgeGraph, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeGraph{}, fmt.Errorf("%v; finalize extraction rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeGraph{}, cause
	}
	for i, batch := range plan.Batches {
		cited := make(map[string]bool)
		if run.Batches[i].Status == KnowledgeExtractionBatchCompleted {
			for _, node := range run.Batches[i].Graph.Nodes {
				for _, anchor := range node.Evidence {
					cited[anchor.CitationID] = true
				}
			}
			for _, edge := range run.Batches[i].Graph.Edges {
				for _, anchor := range edge.Evidence {
					cited[anchor.CitationID] = true
				}
			}
		}
		for _, entry := range batch.Entries {
			citationID, _ := CitationForEntry(entry)
			outcome := "processed"
			if run.Batches[i].Status == KnowledgeExtractionBatchInsufficient {
				outcome = "insufficient"
			} else if cited[citationID] {
				outcome = "extracted"
			}
			if _, err := tx.Exec(`INSERT INTO knowledge_extraction_coverage
                (citation_id, document_id, document_revision, chunk_hash, source_path, page,
                 block_index, block_chunk_index, run_id, batch_id, outcome, created)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(citation_id) DO NOTHING`, citationID, entry.DocumentID, entry.DocumentRevision,
				entry.ChunkHash, entry.SourcePath, entry.Page, entry.BlockIndex, entry.BlockChunkIndex,
				runID, batch.BatchID, outcome, now); err != nil {
				return rollback(err)
			}
		}
	}
	var unfinished int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_extraction_batches WHERE run_id = ? AND status NOT IN (?, ?)`,
		runID, KnowledgeExtractionBatchCompleted, KnowledgeExtractionBatchInsufficient).Scan(&unfinished); err != nil {
		return rollback(err)
	}
	if unfinished != 0 {
		return rollback(fmt.Errorf("extraction run %q still has %d unfinished batches", runID, unfinished))
	}
	result, err := tx.Exec(`UPDATE knowledge_extraction_runs SET status = ?, updated = ? WHERE id = ?`,
		KnowledgeExtractionRunCompleted, now, runID)
	if err != nil {
		return rollback(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return rollback(fmt.Errorf("extraction run %q not found", runID))
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeGraph{}, err
	}
	return merged, nil
}

func MergeKnowledgeExtractionGraphs(graphs ...KnowledgeGraph) (KnowledgeGraph, error) {
	nodes, edges := make(map[string]KnowledgeNode), make(map[string]KnowledgeEdge)
	for index, graph := range graphs {
		if err := validateKnowledgeExtractionFragment(graph); err != nil {
			return KnowledgeGraph{}, fmt.Errorf("extraction batch %d: %w", index+1, err)
		}
		for _, node := range graph.Nodes {
			if existing, ok := nodes[node.ID]; ok {
				if !sameCorpusNode(existing, node) {
					return KnowledgeGraph{}, fmt.Errorf("extraction batches disagree on node %q", node.ID)
				}
				if node.Confidence > existing.Confidence {
					existing.Confidence = node.Confidence
					nodes[node.ID] = existing
				}
				continue
			}
			nodes[node.ID] = node
		}
		for _, edge := range graph.Edges {
			if existing, ok := edges[edge.ID]; ok {
				if !sameCorpusEdge(existing, edge) {
					return KnowledgeGraph{}, fmt.Errorf("extraction batches disagree on edge %q", edge.ID)
				}
				if edge.Confidence > existing.Confidence {
					existing.Confidence = edge.Confidence
					edges[edge.ID] = existing
				}
				continue
			}
			edges[edge.ID] = edge
		}
	}
	merged := KnowledgeGraph{Nodes: make([]KnowledgeNode, 0, len(nodes)), Edges: make([]KnowledgeEdge, 0, len(edges))}
	for _, node := range nodes {
		merged.Nodes = append(merged.Nodes, node)
	}
	for _, edge := range edges {
		merged.Edges = append(merged.Edges, edge)
	}
	sort.Slice(merged.Nodes, func(i, j int) bool { return merged.Nodes[i].ID < merged.Nodes[j].ID })
	sort.Slice(merged.Edges, func(i, j int) bool { return merged.Edges[i].ID < merged.Edges[j].ID })
	if len(merged.Nodes) > 0 {
		if err := validateKnowledgeExtractionFragment(merged); err != nil {
			return KnowledgeGraph{}, err
		}
	}
	return merged, nil
}

func (s *Store) currentKnowledgeExtractionProcessedCitations(entries []Entry) (map[string]bool, error) {
	selected := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		citationID, _ := CitationForEntry(entry)
		selected[citationID] = entry
	}
	result := make(map[string]bool)
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT citation_id, document_id, document_revision, chunk_hash FROM knowledge_extraction_coverage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var citationID, documentID, revision, chunkHash string
		if err := rows.Scan(&citationID, &documentID, &revision, &chunkHash); err != nil {
			return nil, err
		}
		entry, ok := selected[citationID]
		if ok && entry.DocumentID == documentID && entry.DocumentRevision == revision && entry.ChunkHash == chunkHash {
			result[citationID] = true
		}
	}
	return result, rows.Err()
}

func (s *Store) ListKnowledgeExtractionRuns(limit int) ([]KnowledgeExtractionRunSummary, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("extraction run limit must be between 1 and 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT r.id, r.focus, r.scope_json, r.scoped_chunks, r.eligible_chunks,
        r.selected_chunks, r.remaining_chunks, r.status, r.batch_count,
        COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
        COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0), r.created, r.updated
        FROM knowledge_extraction_runs r LEFT JOIN knowledge_extraction_batches b ON b.run_id = r.id
        GROUP BY r.id ORDER BY r.updated DESC, r.id DESC LIMIT ?`,
		KnowledgeExtractionBatchPending, KnowledgeExtractionBatchCompleted,
		KnowledgeExtractionBatchInsufficient, KnowledgeExtractionBatchFailed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []KnowledgeExtractionRunSummary
	for rows.Next() {
		var item KnowledgeExtractionRunSummary
		var scopeJSON string
		if err := rows.Scan(&item.ID, &item.Focus, &scopeJSON, &item.ScopedChunks, &item.EligibleChunks,
			&item.SelectedChunks, &item.RemainingChunks, &item.Status, &item.BatchCount,
			&item.PendingBatches, &item.CompletedBatches, &item.InsufficientBatches, &item.FailedBatches,
			&item.Created, &item.Updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(scopeJSON), &item.Scope); err != nil {
			return nil, err
		}
		if item.Status != KnowledgeExtractionRunRunning && item.Status != KnowledgeExtractionRunCompleted {
			return nil, fmt.Errorf("extraction run %q has invalid status %q", item.ID, item.Status)
		}
		if item.PendingBatches+item.CompletedBatches+item.InsufficientBatches+item.FailedBatches != item.BatchCount {
			return nil, fmt.Errorf("extraction run %q batch count mismatch", item.ID)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
