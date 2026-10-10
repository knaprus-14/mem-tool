package mem

import (
	"context"
	"time"
)

// Called only after the caller owns the workspace worker lock and has validated
// every checkpoint against the current immutable input plan. Completed rows are
// never reset. Preserve the previous failure before starting another attempt.
func (s *Store) resumeClassicMindMapAIRun(ctx context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mind_map_generation_resumes (
 id INTEGER PRIMARY KEY, run_id TEXT NOT NULL, previous_status TEXT NOT NULL,
 error_text TEXT NOT NULL, resumed TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mind_map_generation_resumes(run_id,previous_status,error_text,resumed)
 SELECT id,status,error_text,? FROM mind_map_generation_runs WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), runID); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE mind_map_generation_runs SET status='running',error_text='',completed=''
 WHERE id=? AND status IN ('running','failed','cancelled') AND NOT EXISTS(SELECT 1 FROM mind_map_generation_previews WHERE run_id=?)`, runID, runID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrClassicMindMapAIChanged
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mind_map_generation_batches SET status='pending',error_text=''
 WHERE run_id=? AND status IN ('failed','cancelled')`, runID); err != nil {
		return err
	}
	return tx.Commit()
}
