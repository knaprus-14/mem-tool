package mem

import (
	"context"
	"encoding/json"
	"fmt"
)

type classicMindMapAICheckpoint struct {
	Ready        bool
	Proposals    []ClassicMindMapAIProposal
	Insufficient bool
	Corrections  int
}

// Reuse only an entire, completed extraction with the exact same request,
// document revisions, target revision, model and deterministic batch plan.
// A new run receives copies; the failed run and its diagnostic record stay intact.
func (s *Store) loadCompletedClassicMindMapAICheckpoints(ctx context.Context, runID, requestDigest string, batches []classicMindMapAIBatch) ([]classicMindMapAICheckpoint, error) {
	return s.loadClassicMindMapAICheckpoints(ctx, runID, requestDigest, batches, false)
}

func (s *Store) loadClassicMindMapAICheckpoints(ctx context.Context, runID, requestDigest string, batches []classicMindMapAIBatch, partial bool) ([]classicMindMapAICheckpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fail := func() ([]classicMindMapAICheckpoint, error) {
		return nil, fmt.Errorf("%w: saved generation checkpoints differ or are incomplete", ErrClassicMindMapAIChanged)
	}
	var digest, status string
	if err := s.db.QueryRowContext(ctx, `SELECT request_digest,status FROM mind_map_generation_runs WHERE id=?`, runID).Scan(&digest, &status); err != nil {
		return nil, err
	}
	if digest != requestDigest || (!partial && status != string(ClassicMindMapAIFailed)) || (partial && status != "failed" && status != "cancelled" && status != "running") {
		return fail()
	}
	var count int
	var size int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(result_json AS BLOB))+length(CAST(evidence_json AS BLOB))),0) FROM mind_map_generation_batches WHERE run_id=?`, runID).Scan(&count, &size); err != nil {
		return nil, err
	}
	if count != len(batches) || count == 0 || size > 64<<20 {
		return fail()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal,batch_id,prompt_digest,evidence_json,status,result_json,result_digest,correction_retries FROM mind_map_generation_batches WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]classicMindMapAICheckpoint, 0, count)
	for rows.Next() {
		var ordinal, corrections int
		var id, promptDigest, evidenceJSON, status, resultJSON, resultDigest string
		if err := rows.Scan(&ordinal, &id, &promptDigest, &evidenceJSON, &status, &resultJSON, &resultDigest, &corrections); err != nil {
			return nil, err
		}
		ready := status == "completed" || status == "insufficient"
		if ordinal != len(result) || ordinal >= count || id != batches[ordinal].ID || corrections < 0 || (!ready && (!partial || (status != "pending" && status != "failed" && status != "cancelled"))) {
			return fail()
		}
		wantPrompt, _ := classicMindMapAIDigest(struct {
			System string `json:"system"`
			User   string `json:"user"`
		}{batches[ordinal].System, batches[ordinal].User})
		if promptDigest != wantPrompt {
			return fail()
		}
		var evidence []GroundedEvidence
		if json.Unmarshal([]byte(evidenceJSON), &evidence) != nil {
			return fail()
		}
		actual, _ := classicMindMapAIDigest(evidence)
		want, _ := classicMindMapAIDigest(batches[ordinal].Evidence)
		if actual != want {
			return fail()
		}
		checkpoint := classicMindMapAICheckpoint{Ready: ready, Insufficient: status == "insufficient", Corrections: corrections}
		if status == "completed" {
			if json.Unmarshal([]byte(resultJSON), &checkpoint.Proposals) != nil || len(checkpoint.Proposals) == 0 || len(checkpoint.Proposals) > MaxClassicMindMapAIProposals {
				return fail()
			}
			actual, _ := classicMindMapAIDigest(checkpoint.Proposals)
			if actual != resultDigest {
				return fail()
			}
			for _, p := range checkpoint.Proposals {
				if p.SynthesisRole != "" || len(p.Evidence) == 0 {
					return fail()
				}
			}
			if validateStoredClassicMindMapAIPreview(ClassicMindMapAIPreview{Evidence: evidence, Proposals: checkpoint.Proposals}) != nil {
				return fail()
			}
		} else if resultJSON != "" || resultDigest != "" {
			return fail()
		}
		result = append(result, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != count {
		return fail()
	}
	return result, nil
}
