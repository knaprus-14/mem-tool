package mem

import (
	"context"
	"database/sql"
	"errors"
)

// Only validated grouping plans enter this cache. Raw failed model responses
// must never become permanent retries of the same invalid JSON.
type classicMindMapAISynthesisCache interface {
	loadSynthesis(context.Context, AnswerRequest) (string, bool, error)
	saveSynthesis(context.Context, AnswerRequest, string) error
}

type classicMindMapAICachedProvider struct {
	AnswerProvider
	store *Store
	runID string
}

func (p *classicMindMapAICachedProvider) MaxOutputTokens() int {
	if bounded, ok := p.AnswerProvider.(interface{ MaxOutputTokens() int }); ok {
		return bounded.MaxOutputTokens()
	}
	return maxClassicMindMapAITokens
}

func (p *classicMindMapAICachedProvider) loadSynthesis(ctx context.Context, request AnswerRequest) (string, bool, error) {
	if _, err := p.store.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mind_map_generation_groups(
 run_id TEXT NOT NULL, request_digest TEXT NOT NULL, result_json TEXT NOT NULL, result_digest TEXT NOT NULL,
 PRIMARY KEY(run_id,request_digest))`); err != nil {
		return "", false, err
	}
	key, err := classicMindMapAIDigest(request)
	if err != nil {
		return "", false, err
	}
	var raw, digest string
	err = p.store.db.QueryRowContext(ctx, `SELECT result_json,result_digest FROM mind_map_generation_groups WHERE run_id=? AND request_digest=?`, p.runID, key).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if ChunkContentHash(raw) != digest {
		return "", false, ErrClassicMindMapAIChanged
	}
	return raw, true, nil
}

func (p *classicMindMapAICachedProvider) saveSynthesis(ctx context.Context, request AnswerRequest, raw string) error {
	key, err := classicMindMapAIDigest(request)
	if err != nil {
		return err
	}
	_, err = p.store.db.ExecContext(ctx, `INSERT INTO mind_map_generation_groups(run_id,request_digest,result_json,result_digest) VALUES(?,?,?,?)`, p.runID, key, raw, ChunkContentHash(raw))
	return err
}
