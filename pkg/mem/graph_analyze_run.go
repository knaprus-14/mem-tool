package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const corpusAnalysisRunSchema = `
CREATE TABLE IF NOT EXISTS knowledge_analysis_runs (
    id TEXT PRIMARY KEY,
    focus TEXT NOT NULL,
    context_chars INTEGER NOT NULL,
    max_batches INTEGER NOT NULL,
    generation_digest TEXT NOT NULL,
    plan_digest TEXT NOT NULL,
    batch_count INTEGER NOT NULL,
    eligible_claims INTEGER NOT NULL,
    covered_claims INTEGER NOT NULL,
    eligible_documents INTEGER NOT NULL,
    covered_documents INTEGER NOT NULL,
    status TEXT NOT NULL,
    created TEXT NOT NULL,
    updated TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_analysis_batches (
    run_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    batch_id TEXT NOT NULL,
    prompt_digest TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '',
    result_digest TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    updated TEXT NOT NULL,
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, batch_id)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_analysis_runs_status
    ON knowledge_analysis_runs(status, updated);
CREATE INDEX IF NOT EXISTS idx_knowledge_analysis_batches_status
    ON knowledge_analysis_batches(run_id, status, ordinal);
`

type CorpusAnalysisRunStatus string

const (
	CorpusAnalysisRunRunning   CorpusAnalysisRunStatus = "running"
	CorpusAnalysisRunCompleted CorpusAnalysisRunStatus = "completed"
)

type CorpusAnalysisBatchStatus string

const (
	CorpusAnalysisBatchPending      CorpusAnalysisBatchStatus = "pending"
	CorpusAnalysisBatchCompleted    CorpusAnalysisBatchStatus = "completed"
	CorpusAnalysisBatchInsufficient CorpusAnalysisBatchStatus = "insufficient"
	CorpusAnalysisBatchFailed       CorpusAnalysisBatchStatus = "failed"
)

type CorpusAnalysisRun struct {
	ID                string                   `json:"id"`
	Focus             string                   `json:"focus"`
	ContextChars      int                      `json:"context_chars"`
	MaxBatches        int                      `json:"max_batches"`
	GenerationDigest  string                   `json:"generation_digest"`
	PlanDigest        string                   `json:"plan_digest"`
	EligibleClaims    int                      `json:"eligible_claims"`
	CoveredClaims     int                      `json:"covered_claims"`
	EligibleDocuments int                      `json:"eligible_documents"`
	CoveredDocuments  int                      `json:"covered_documents"`
	Status            CorpusAnalysisRunStatus  `json:"status"`
	Created           string                   `json:"created"`
	Updated           string                   `json:"updated"`
	Batches           []CorpusAnalysisRunBatch `json:"batches"`
}

type CorpusAnalysisRunBatch struct {
	Ordinal      int                       `json:"ordinal"`
	BatchID      string                    `json:"batch_id"`
	PromptDigest string                    `json:"prompt_digest"`
	Status       CorpusAnalysisBatchStatus `json:"status"`
	Graph        KnowledgeGraph            `json:"graph,omitempty"`
	Reason       string                    `json:"reason,omitempty"`
	Updated      string                    `json:"updated"`
}

// CorpusAnalysisRunSummary is the lightweight, result-free history view used
// by list and cleanup operations. Failed batches still belong to a running,
// resumable run and are deliberately reported separately.
type CorpusAnalysisRunSummary struct {
	ID                  string                  `json:"id"`
	Focus               string                  `json:"focus"`
	ContextChars        int                     `json:"context_chars"`
	MaxBatches          int                     `json:"max_batches"`
	EligibleClaims      int                     `json:"eligible_claims"`
	CoveredClaims       int                     `json:"covered_claims"`
	EligibleDocuments   int                     `json:"eligible_documents"`
	CoveredDocuments    int                     `json:"covered_documents"`
	Status              CorpusAnalysisRunStatus `json:"status"`
	BatchCount          int                     `json:"batch_count"`
	PendingBatches      int                     `json:"pending_batches"`
	CompletedBatches    int                     `json:"completed_batches"`
	InsufficientBatches int                     `json:"insufficient_batches"`
	FailedBatches       int                     `json:"failed_batches"`
	Created             string                  `json:"created"`
	Updated             string                  `json:"updated"`
}

type CorpusAnalysisRunPruneResult struct {
	DryRun          bool                       `json:"dry_run"`
	CompletedBefore string                     `json:"completed_before"`
	KeepLatest      int                        `json:"keep_latest"`
	DeletedRuns     int                        `json:"deleted_runs"`
	DeletedBatches  int                        `json:"deleted_batches"`
	Runs            []CorpusAnalysisRunSummary `json:"runs"`
}

type corpusAnalysisPlanBatchManifest struct {
	BatchID      string `json:"batch_id"`
	PromptDigest string `json:"prompt_digest"`
}

type corpusAnalysisPlanManifest struct {
	Focus             string                            `json:"focus"`
	ContextChars      int                               `json:"context_chars"`
	MaxBatches        int                               `json:"max_batches"`
	GenerationDigest  string                            `json:"generation_digest"`
	EligibleClaims    int                               `json:"eligible_claims"`
	CoveredClaims     int                               `json:"covered_claims"`
	EligibleDocuments int                               `json:"eligible_documents"`
	CoveredDocuments  int                               `json:"covered_documents"`
	Batches           []corpusAnalysisPlanBatchManifest `json:"batches"`
}

// PrepareCorpusAnalysisRun creates an idempotent durable run for an exact
// deterministic plan. A supplied resume ID pins the plan and is rejected if
// focus, options, claim text, or evidence changed since the earlier attempt.
func (s *Store) PrepareCorpusAnalysisRun(focus string, contextChars, maxBatches int, plan CorpusAnalysisPlan, answer AnswerConfig, resumeID string) (CorpusAnalysisRun, error) {
	manifest, planDigest, runID, err := buildCorpusAnalysisRunIdentity(focus, contextChars, maxBatches, plan, answer)
	if err != nil {
		return CorpusAnalysisRun{}, err
	}
	resumeID = strings.TrimSpace(resumeID)
	if resumeID != "" && resumeID != runID {
		return CorpusAnalysisRun{}, fmt.Errorf("analysis run %q no longer matches the current plan (expected %s); evidence, claims, focus, or options changed", resumeID, runID)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return CorpusAnalysisRun{}, fmt.Errorf("begin analysis run: %w", err)
	}
	rollback := func(cause error) (CorpusAnalysisRun, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return CorpusAnalysisRun{}, fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return CorpusAnalysisRun{}, cause
	}
	var existingDigest string
	err = tx.QueryRow(`SELECT plan_digest FROM knowledge_analysis_runs WHERE id = ?`, runID).Scan(&existingDigest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err = tx.Exec(`INSERT INTO knowledge_analysis_runs
			(id, focus, context_chars, max_batches, generation_digest, plan_digest, batch_count,
			 eligible_claims, covered_claims, eligible_documents, covered_documents,
			 status, created, updated)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, manifest.Focus, contextChars, maxBatches, manifest.GenerationDigest, planDigest, len(manifest.Batches),
			plan.EligibleClaims, plan.CoveredClaims, plan.EligibleDocuments, plan.CoveredDocuments,
			CorpusAnalysisRunRunning, now, now); err != nil {
			return rollback(fmt.Errorf("create analysis run: %w", err))
		}
		for ordinal, batch := range manifest.Batches {
			if _, err = tx.Exec(`INSERT INTO knowledge_analysis_batches
                    (run_id, ordinal, batch_id, prompt_digest, status, updated)
                    VALUES (?, ?, ?, ?, ?, ?)`,
				runID, ordinal, batch.BatchID, batch.PromptDigest, CorpusAnalysisBatchPending, now); err != nil {
				return rollback(fmt.Errorf("create analysis batch %d: %w", ordinal+1, err))
			}
		}
	case err != nil:
		return rollback(fmt.Errorf("inspect analysis run: %w", err))
	case existingDigest != planDigest:
		return rollback(errors.New("stored analysis run plan digest disagrees with its stable ID"))
	}
	if err := tx.Commit(); err != nil {
		return CorpusAnalysisRun{}, fmt.Errorf("commit analysis run: %w", err)
	}
	run, err := loadCorpusAnalysisRun(s.db, runID)
	if err != nil {
		return CorpusAnalysisRun{}, err
	}
	if err := validateCorpusAnalysisRunAgainstManifest(run, manifest, planDigest); err != nil {
		return CorpusAnalysisRun{}, err
	}
	return run, nil
}

func buildCorpusAnalysisRunIdentity(focus string, contextChars, maxBatches int, plan CorpusAnalysisPlan, answer AnswerConfig) (corpusAnalysisPlanManifest, string, string, error) {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return corpusAnalysisPlanManifest{}, "", "", errors.New("analysis run focus is empty")
	}
	if contextChars < 1 || contextChars > MaxAnswerContextChars {
		return corpusAnalysisPlanManifest{}, "", "", fmt.Errorf("analysis run context chars must be between 1 and %d", MaxAnswerContextChars)
	}
	if maxBatches < 1 || maxBatches > MaxCorpusAnalysisBatches {
		return corpusAnalysisPlanManifest{}, "", "", fmt.Errorf("analysis run batches must be between 1 and %d", MaxCorpusAnalysisBatches)
	}
	if len(plan.Batches) == 0 || len(plan.Batches) > maxBatches {
		return corpusAnalysisPlanManifest{}, "", "", errors.New("analysis run plan has an invalid batch count")
	}
	answer = answer.WithDefaults()
	if strings.TrimSpace(answer.Model) == "" {
		return corpusAnalysisPlanManifest{}, "", "", errors.New("analysis run answer model is empty")
	}
	generationJSON, err := json.Marshal(struct {
		BaseURL     string  `json:"base_url"`
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}{strings.TrimSpace(answer.BaseURL), strings.TrimSpace(answer.Model), answer.MaxTokens, answer.Temperature})
	if err != nil {
		return corpusAnalysisPlanManifest{}, "", "", fmt.Errorf("encode analysis generation settings: %w", err)
	}
	manifest := corpusAnalysisPlanManifest{
		Focus: focus, ContextChars: contextChars, MaxBatches: maxBatches,
		GenerationDigest: digestCorpusAnalysisBytes(generationJSON),
		EligibleClaims:   plan.EligibleClaims, CoveredClaims: plan.CoveredClaims,
		EligibleDocuments: plan.EligibleDocuments, CoveredDocuments: plan.CoveredDocuments,
		Batches: make([]corpusAnalysisPlanBatchManifest, 0, len(plan.Batches)),
	}
	seen := make(map[string]bool, len(plan.Batches))
	for _, batch := range plan.Batches {
		if batch.BatchID == "" || seen[batch.BatchID] || len(batch.Claims) < 2 || batch.DocumentCount < 2 {
			return corpusAnalysisPlanManifest{}, "", "", errors.New("analysis run plan contains an invalid batch")
		}
		seen[batch.BatchID] = true
		manifest.Batches = append(manifest.Batches, corpusAnalysisPlanBatchManifest{
			BatchID: batch.BatchID, PromptDigest: digestCorpusAnalysisBytes([]byte(batch.System + "\x00" + batch.User)),
		})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return corpusAnalysisPlanManifest{}, "", "", fmt.Errorf("encode analysis run manifest: %w", err)
	}
	planDigest := digestCorpusAnalysisBytes(encoded)
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-corpus-analysis-run-v1")
	writeKnowledgeIDField(h, planDigest)
	runID := "kar-" + hex.EncodeToString(h.Sum(nil)[:16])
	return manifest, planDigest, runID, nil
}

func digestCorpusAnalysisBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validateCorpusAnalysisRunAgainstManifest(run CorpusAnalysisRun, manifest corpusAnalysisPlanManifest, planDigest string) error {
	if run.Focus != manifest.Focus || run.ContextChars != manifest.ContextChars || run.MaxBatches != manifest.MaxBatches ||
		run.GenerationDigest != manifest.GenerationDigest ||
		run.PlanDigest != planDigest || run.EligibleClaims != manifest.EligibleClaims || run.CoveredClaims != manifest.CoveredClaims ||
		run.EligibleDocuments != manifest.EligibleDocuments || run.CoveredDocuments != manifest.CoveredDocuments ||
		len(run.Batches) != len(manifest.Batches) {
		return errors.New("stored analysis run metadata does not match the current deterministic plan")
	}
	for i, batch := range run.Batches {
		if batch.Ordinal != i || batch.BatchID != manifest.Batches[i].BatchID || batch.PromptDigest != manifest.Batches[i].PromptDigest {
			return fmt.Errorf("stored analysis batch %d does not match the current deterministic plan", i+1)
		}
	}
	return nil
}

func (s *Store) LoadCorpusAnalysisRun(runID string) (CorpusAnalysisRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return CorpusAnalysisRun{}, errors.New("analysis run ID is empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return loadCorpusAnalysisRun(s.db, runID)
}

// ListCorpusAnalysisRuns returns newest runs first without loading stored graph
// fragments. An empty status includes both running and completed runs.
func (s *Store) ListCorpusAnalysisRuns(limit int, status CorpusAnalysisRunStatus) ([]CorpusAnalysisRunSummary, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("analysis run limit must be between 1 and 1000")
	}
	if status != "" && status != CorpusAnalysisRunRunning && status != CorpusAnalysisRunCompleted {
		return nil, fmt.Errorf("unsupported analysis run status %q", status)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return listCorpusAnalysisRuns(s.db, limit, status)
}

func listCorpusAnalysisRuns(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, limit int, status CorpusAnalysisRunStatus) ([]CorpusAnalysisRunSummary, error) {
	query := `SELECT r.id, r.focus, r.context_chars, r.max_batches,
		r.eligible_claims, r.covered_claims, r.eligible_documents, r.covered_documents,
		r.status, r.batch_count,
		COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN b.status = ? THEN 1 ELSE 0 END), 0),
		r.created, r.updated
		FROM knowledge_analysis_runs r
		LEFT JOIN knowledge_analysis_batches b ON b.run_id = r.id`
	args := []any{
		CorpusAnalysisBatchPending, CorpusAnalysisBatchCompleted,
		CorpusAnalysisBatchInsufficient, CorpusAnalysisBatchFailed,
	}
	if status != "" {
		query += ` WHERE r.status = ?`
		args = append(args, status)
	}
	query += ` GROUP BY r.id ORDER BY r.updated DESC, r.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list analysis runs: %w", err)
	}
	defer rows.Close()
	runs := make([]CorpusAnalysisRunSummary, 0)
	for rows.Next() {
		var run CorpusAnalysisRunSummary
		if err := rows.Scan(
			&run.ID, &run.Focus, &run.ContextChars, &run.MaxBatches,
			&run.EligibleClaims, &run.CoveredClaims, &run.EligibleDocuments, &run.CoveredDocuments,
			&run.Status, &run.BatchCount, &run.PendingBatches, &run.CompletedBatches,
			&run.InsufficientBatches, &run.FailedBatches, &run.Created, &run.Updated,
		); err != nil {
			return nil, fmt.Errorf("scan analysis run summary: %w", err)
		}
		if run.Status != CorpusAnalysisRunRunning && run.Status != CorpusAnalysisRunCompleted {
			return nil, fmt.Errorf("analysis run %q has invalid status %q", run.ID, run.Status)
		}
		if run.PendingBatches+run.CompletedBatches+run.InsufficientBatches+run.FailedBatches != run.BatchCount {
			return nil, fmt.Errorf("analysis run %q batch count mismatch", run.ID)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read analysis run summaries: %w", err)
	}
	return runs, nil
}

// PruneCompletedCorpusAnalysisRuns removes only completed runs older than the
// cutoff. The newest keepLatest completed runs are protected regardless of
// age. Running runs, including those with failed batches, are never candidates.
func (s *Store) PruneCompletedCorpusAnalysisRuns(completedBefore time.Time, keepLatest int, dryRun bool) (CorpusAnalysisRunPruneResult, error) {
	if completedBefore.IsZero() {
		return CorpusAnalysisRunPruneResult{}, errors.New("analysis run prune cutoff is required")
	}
	if keepLatest < 0 || keepLatest > 10000 {
		return CorpusAnalysisRunPruneResult{}, errors.New("analysis run keep count must be between 0 and 10000")
	}
	cutoff := completedBefore.UTC()
	result := CorpusAnalysisRunPruneResult{
		DryRun: dryRun, CompletedBefore: cutoff.Format(time.RFC3339), KeepLatest: keepLatest,
		Runs: make([]CorpusAnalysisRunSummary, 0),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return CorpusAnalysisRunPruneResult{}, fmt.Errorf("begin analysis run prune: %w", err)
	}
	rollback := func(cause error) (CorpusAnalysisRunPruneResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return CorpusAnalysisRunPruneResult{}, fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return CorpusAnalysisRunPruneResult{}, cause
	}
	completed, err := listCorpusAnalysisRuns(tx, 0, CorpusAnalysisRunCompleted)
	if err != nil {
		return rollback(err)
	}
	for index, run := range completed {
		if index < keepLatest {
			continue
		}
		updated, parseErr := time.Parse(time.RFC3339, run.Updated)
		if parseErr != nil {
			return rollback(fmt.Errorf("analysis run %q has invalid updated timestamp: %w", run.ID, parseErr))
		}
		if updated.Before(cutoff) {
			result.Runs = append(result.Runs, run)
		}
	}
	if dryRun {
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			return CorpusAnalysisRunPruneResult{}, fmt.Errorf("finish analysis run prune preview: %w", err)
		}
		return result, nil
	}
	for _, run := range result.Runs {
		batchResult, err := tx.Exec(`DELETE FROM knowledge_analysis_batches WHERE run_id = ?`, run.ID)
		if err != nil {
			return rollback(fmt.Errorf("delete analysis run %q batches: %w", run.ID, err))
		}
		deletedBatches, err := batchResult.RowsAffected()
		if err != nil {
			return rollback(fmt.Errorf("count analysis run %q deleted batches: %w", run.ID, err))
		}
		runResult, err := tx.Exec(`DELETE FROM knowledge_analysis_runs WHERE id = ? AND status = ?`, run.ID, CorpusAnalysisRunCompleted)
		if err != nil {
			return rollback(fmt.Errorf("delete analysis run %q: %w", run.ID, err))
		}
		deletedRuns, err := runResult.RowsAffected()
		if err != nil {
			return rollback(fmt.Errorf("count deleted analysis run %q: %w", run.ID, err))
		}
		if deletedRuns != 1 {
			return rollback(fmt.Errorf("completed analysis run %q changed concurrently", run.ID))
		}
		result.DeletedRuns += int(deletedRuns)
		result.DeletedBatches += int(deletedBatches)
	}
	if err := tx.Commit(); err != nil {
		return CorpusAnalysisRunPruneResult{}, fmt.Errorf("commit analysis run prune: %w", err)
	}
	return result, nil
}

func loadCorpusAnalysisRun(q interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}, runID string) (CorpusAnalysisRun, error) {
	var run CorpusAnalysisRun
	var batchCount int
	err := q.QueryRow(`SELECT id, focus, context_chars, max_batches, generation_digest, plan_digest, batch_count,
		eligible_claims, covered_claims, eligible_documents, covered_documents,
		status, created, updated FROM knowledge_analysis_runs WHERE id = ?`, runID).Scan(
		&run.ID, &run.Focus, &run.ContextChars, &run.MaxBatches, &run.GenerationDigest, &run.PlanDigest, &batchCount,
		&run.EligibleClaims, &run.CoveredClaims, &run.EligibleDocuments, &run.CoveredDocuments,
		&run.Status, &run.Created, &run.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return CorpusAnalysisRun{}, fmt.Errorf("analysis run %q not found", runID)
	}
	if err != nil {
		return CorpusAnalysisRun{}, fmt.Errorf("load analysis run: %w", err)
	}
	if run.Status != CorpusAnalysisRunRunning && run.Status != CorpusAnalysisRunCompleted {
		return CorpusAnalysisRun{}, fmt.Errorf("analysis run %q has invalid status %q", runID, run.Status)
	}
	rows, err := q.Query(`SELECT ordinal, batch_id, prompt_digest, status, result_json, result_digest, reason, updated
        FROM knowledge_analysis_batches WHERE run_id = ? ORDER BY ordinal`, runID)
	if err != nil {
		return CorpusAnalysisRun{}, fmt.Errorf("load analysis batches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var batch CorpusAnalysisRunBatch
		var resultJSON, resultDigest string
		if err := rows.Scan(&batch.Ordinal, &batch.BatchID, &batch.PromptDigest, &batch.Status,
			&resultJSON, &resultDigest, &batch.Reason, &batch.Updated); err != nil {
			return CorpusAnalysisRun{}, fmt.Errorf("scan analysis batch: %w", err)
		}
		switch batch.Status {
		case CorpusAnalysisBatchPending, CorpusAnalysisBatchFailed:
			if resultJSON != "" || resultDigest != "" {
				return CorpusAnalysisRun{}, fmt.Errorf("analysis batch %q has a result in status %q", batch.BatchID, batch.Status)
			}
		case CorpusAnalysisBatchInsufficient:
			if resultJSON != "" || resultDigest != "" {
				return CorpusAnalysisRun{}, fmt.Errorf("insufficient analysis batch %q contains a graph", batch.BatchID)
			}
		case CorpusAnalysisBatchCompleted:
			if resultJSON == "" || resultDigest == "" || digestCorpusAnalysisBytes([]byte(resultJSON)) != resultDigest {
				return CorpusAnalysisRun{}, fmt.Errorf("analysis batch %q result digest mismatch", batch.BatchID)
			}
			if err := json.Unmarshal([]byte(resultJSON), &batch.Graph); err != nil {
				return CorpusAnalysisRun{}, fmt.Errorf("decode analysis batch %q result: %w", batch.BatchID, err)
			}
			if err := validateCorpusAnalysisFragment(batch.Graph); err != nil {
				return CorpusAnalysisRun{}, fmt.Errorf("validate analysis batch %q result: %w", batch.BatchID, err)
			}
			if err := ValidateKnowledgeGraph(batch.Graph); err != nil {
				return CorpusAnalysisRun{}, fmt.Errorf("validate analysis batch %q graph: %w", batch.BatchID, err)
			}
		default:
			return CorpusAnalysisRun{}, fmt.Errorf("analysis batch %q has invalid status %q", batch.BatchID, batch.Status)
		}
		run.Batches = append(run.Batches, batch)
	}
	if err := rows.Err(); err != nil {
		return CorpusAnalysisRun{}, fmt.Errorf("read analysis batches: %w", err)
	}
	if len(run.Batches) != batchCount {
		return CorpusAnalysisRun{}, fmt.Errorf("analysis run %q batch count mismatch: got %d want %d", runID, len(run.Batches), batchCount)
	}
	if run.Status == CorpusAnalysisRunCompleted {
		for _, batch := range run.Batches {
			if batch.Status != CorpusAnalysisBatchCompleted && batch.Status != CorpusAnalysisBatchInsufficient {
				return CorpusAnalysisRun{}, fmt.Errorf("completed analysis run %q contains unfinished batch %q", runID, batch.BatchID)
			}
		}
	}
	return run, nil
}

func (s *Store) SaveCorpusAnalysisBatchGraph(runID, batchID string, graph KnowledgeGraph) error {
	graph = normalizeKnowledgeGraph(graph)
	if err := validateCorpusAnalysisFragment(graph); err != nil {
		return err
	}
	if err := ValidateKnowledgeGraph(graph); err != nil {
		return err
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		return fmt.Errorf("encode analysis batch graph: %w", err)
	}
	return s.saveCorpusAnalysisBatch(runID, batchID, CorpusAnalysisBatchCompleted, string(encoded), digestCorpusAnalysisBytes(encoded), "")
}

func (s *Store) SaveCorpusAnalysisBatchInsufficient(runID, batchID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Недостаточно подтверждённых данных для междокументного анализа."
	}
	return s.saveCorpusAnalysisBatch(runID, batchID, CorpusAnalysisBatchInsufficient, "", "", reason)
}

func (s *Store) SaveCorpusAnalysisBatchFailure(runID, batchID string, cause error) error {
	reason := "analysis batch failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		reason = strings.TrimSpace(cause.Error())
	}
	return s.saveCorpusAnalysisBatch(runID, batchID, CorpusAnalysisBatchFailed, "", "", reason)
}

func (s *Store) saveCorpusAnalysisBatch(runID, batchID string, status CorpusAnalysisBatchStatus, resultJSON, resultDigest, reason string) error {
	if status != CorpusAnalysisBatchCompleted && status != CorpusAnalysisBatchInsufficient && status != CorpusAnalysisBatchFailed {
		return fmt.Errorf("unsupported analysis batch status %q", status)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	var current CorpusAnalysisBatchStatus
	var currentJSON, currentDigest, currentReason string
	err := s.db.QueryRow(`SELECT status, result_json, result_digest, reason
        FROM knowledge_analysis_batches WHERE run_id = ? AND batch_id = ?`, runID, batchID).Scan(
		&current, &currentJSON, &currentDigest, &currentReason)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("analysis batch %q does not belong to run %q", batchID, runID)
	}
	if err != nil {
		return fmt.Errorf("inspect analysis batch: %w", err)
	}
	if current == CorpusAnalysisBatchCompleted || current == CorpusAnalysisBatchInsufficient {
		if current == status && currentJSON == resultJSON && currentDigest == resultDigest && currentReason == reason {
			return nil
		}
		return fmt.Errorf("analysis batch %q already has immutable status %q", batchID, current)
	}
	result, err := s.db.Exec(`UPDATE knowledge_analysis_batches
        SET status = ?, result_json = ?, result_digest = ?, reason = ?, updated = ?
        WHERE run_id = ? AND batch_id = ? AND status IN (?, ?)`,
		status, resultJSON, resultDigest, reason, now, runID, batchID,
		CorpusAnalysisBatchPending, CorpusAnalysisBatchFailed)
	if err != nil {
		return fmt.Errorf("save analysis batch: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("save analysis batch rows: %w", err)
		}
		return fmt.Errorf("analysis batch %q changed concurrently", batchID)
	}
	if _, err := s.db.Exec(`UPDATE knowledge_analysis_runs SET status = ?, updated = ? WHERE id = ?`,
		CorpusAnalysisRunRunning, now, runID); err != nil {
		return fmt.Errorf("touch analysis run: %w", err)
	}
	return nil
}

func (s *Store) CompleteCorpusAnalysisRun(runID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	var unfinished int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_analysis_batches
        WHERE run_id = ? AND status NOT IN (?, ?)`, runID,
		CorpusAnalysisBatchCompleted, CorpusAnalysisBatchInsufficient).Scan(&unfinished); err != nil {
		return fmt.Errorf("inspect analysis run completion: %w", err)
	}
	if unfinished != 0 {
		return fmt.Errorf("analysis run %q still has %d unfinished batches", runID, unfinished)
	}
	result, err := s.db.Exec(`UPDATE knowledge_analysis_runs SET status = ?, updated = ? WHERE id = ?`,
		CorpusAnalysisRunCompleted, now, runID)
	if err != nil {
		return fmt.Errorf("complete analysis run: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("complete analysis run rows: %w", err)
		}
		return fmt.Errorf("analysis run %q not found", runID)
	}
	return nil
}
