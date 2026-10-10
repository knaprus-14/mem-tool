package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const classicMindMapAIPartsSchema = `CREATE TABLE IF NOT EXISTS mind_map_generation_parts (
 run_id TEXT NOT NULL, batch_id TEXT NOT NULL, prompt_digest TEXT NOT NULL,
 status TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '',result_digest TEXT NOT NULL DEFAULT '',
 corrections INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(run_id,batch_id));`

// Splits only a typed model size failure, never an arbitrary network/auth error.
// The split marker and validated children survive a crash independently of the
// enclosing batch. The deterministic original batch plan remains unchanged.
func (s *Store) generateAdaptiveClassicMindMapAIBatch(ctx context.Context, provider AnswerProvider, cfg AnswerConfig, request ClassicMindMapAIPreviewRequest, base ClassicMindMapDocument, target ClassicMindMapNode, runID string, batch classicMindMapAIBatch) ([]ClassicMindMapAIProposal, bool, int, error) {
	if request.Action != ClassicMindMapAIExpand && request.Action != ClassicMindMapAINewMap {
		return generateClassicMindMapAIBatch(ctx, provider, cfg, request, batch, len(batch.Evidence) > 0)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, 0, err
	}
	if _, err := s.db.ExecContext(ctx, classicMindMapAIPartsSchema); err != nil {
		return nil, false, 0, err
	}
	key, err := classicMindMapAIDigest(struct{ System, User string }{batch.System, batch.User})
	if err != nil {
		return nil, false, 0, err
	}
	var status, storedKey, raw, digest string
	var corrections int
	err = s.db.QueryRowContext(ctx, `SELECT status,prompt_digest,result_json,result_digest,corrections FROM mind_map_generation_parts WHERE run_id=? AND batch_id=?`, runID, batch.ID).Scan(&status, &storedKey, &raw, &digest, &corrections)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, 0, err
	}
	if err == nil {
		if storedKey != key {
			return nil, false, 0, ErrClassicMindMapAIChanged
		}
		if status == "completed" || status == "insufficient" {
			var p []ClassicMindMapAIProposal
			if json.Unmarshal([]byte(raw), &p) != nil {
				return nil, false, 0, ErrClassicMindMapAIChanged
			}
			actual, _ := classicMindMapAIDigest(p)
			if actual != digest || (status == "completed" && len(p) == 0) || (status == "insufficient" && len(p) != 0) || validateStoredClassicMindMapAIPreview(ClassicMindMapAIPreview{Evidence: batch.Evidence, Proposals: p}) != nil {
				return nil, false, 0, ErrClassicMindMapAIChanged
			}
			return p, status == "insufficient", corrections, nil
		}
		if status != "split" && status != "split_text_v1" {
			return nil, false, 0, ErrClassicMindMapAIChanged
		}
	}
	if status != "split" && status != "split_text_v1" {
		p, insufficient, n, e := generateClassicMindMapAIBatch(ctx, provider, cfg, request, batch, len(batch.Evidence) > 0)
		if e == nil {
			state := "completed"
			if insufficient {
				state = "insufficient"
			}
			data, _ := json.Marshal(p)
			hash, _ := classicMindMapAIDigest(p)
			_, e = s.db.ExecContext(ctx, `INSERT INTO mind_map_generation_parts(run_id,batch_id,prompt_digest,status,result_json,result_digest,corrections) VALUES(?,?,?,?,?,?,?)`, runID, batch.ID, key, state, string(data), hash, n)
			return p, insufficient, n, e
		}
		_, limited := AnswerTokenLimit(e)
		if (!limited && !errors.Is(e, ErrAnswerContextLimit)) || len(batch.Evidence) == 0 || ctx.Err() != nil {
			return nil, false, n, e
		}
		status = "split"
		if len(batch.Evidence) == 1 {
			if _, ok := splitClassicMindMapAIEvidence(batch.Evidence[0]); !ok {
				return nil, false, n, e
			}
			status = "split_text_v1"
		}
		if _, e = s.db.ExecContext(ctx, `INSERT INTO mind_map_generation_parts(run_id,batch_id,prompt_digest,status,corrections) VALUES(?,?,?,?,?)`, runID, batch.ID, key, status, n); e != nil {
			return nil, false, n, e
		}
		corrections = n
	}
	var parts [][]GroundedEvidence
	if status == "split_text_v1" {
		if len(batch.Evidence) != 1 {
			return nil, false, corrections, ErrClassicMindMapAIChanged
		}
		children, ok := splitClassicMindMapAIEvidence(batch.Evidence[0])
		if !ok {
			return nil, false, corrections, ErrClassicMindMapAIChanged
		}
		parts = [][]GroundedEvidence{{children[0]}, {children[1]}}
	} else {
		mid := len(batch.Evidence) / 2
		if mid == 0 {
			return nil, false, corrections, ErrClassicMindMapAIChanged
		}
		parts = [][]GroundedEvidence{batch.Evidence[:mid], batch.Evidence[mid:]}
	}
	var results [][]ClassicMindMapAIProposal
	allInsufficient := true
	for index, part := range parts {
		children, e := buildClassicMindMapAIBatches(request, base, target, part, cfg.ContextChars)
		if e != nil || len(children) != 1 {
			return nil, false, corrections, fmt.Errorf("adaptive batch plan: %w", ErrClassicMindMapAIChanged)
		}
		child := children[0]
		if status == "split_text_v1" {
			// Identical left/right text still represents two distinct positions.
			// Namespace by the parent and side, not only the excerpt hash.
			digest, e := classicMindMapAIDigest([]any{"split_text_v1", batch.ID, index, child.ID})
			if e != nil {
				return nil, false, corrections, e
			}
			child.ID = "mmgb-" + strings.TrimPrefix(digest, "sha256:")[:32]
		}
		child.Ordinal = batch.Ordinal
		p, insufficient, n, e := s.generateAdaptiveClassicMindMapAIBatch(ctx, provider, cfg, request, base, target, runID, child)
		corrections += n
		if e != nil {
			return nil, false, corrections, e
		}
		allInsufficient = allInsufficient && insufficient
		if !insufficient {
			for i := range p {
				if p[i].GenerationPartID == "" {
					p[i].GenerationPartID = child.ID
				}
			}
			results = append(results, p)
		}
	}
	if allInsufficient {
		return nil, true, corrections, nil
	}
	// Preserve separate subtree roots until the global synthesis. Promoting the
	// first fragment to the root would hide the remaining adaptive parts there.
	p, err := reduceClassicMindMapAIProposals(ClassicMindMapAIExpand, results)
	return p, false, corrections, err
}
