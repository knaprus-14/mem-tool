package mem

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

func canReassembleWebMapAI(ctx context.Context, tx *sql.Tx, task WebMapAITask) (bool, error) {
	if !task.SourceOverview || task.Status != "failed" || task.RunID == "" || task.Total < 1 || (task.Phase != "merging" && task.Phase != "synthesizing" && task.Phase != "validating") {
		return false, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_batches b JOIN mind_map_generation_runs r ON r.id=b.run_id WHERE b.run_id=? AND r.map_id=? AND r.status='failed' AND b.status IN ('completed','insufficient') AND NOT EXISTS(SELECT 1 FROM mind_map_generation_previews WHERE run_id=b.run_id)`, task.RunID, task.MapID).Scan(&count)
	return count == task.Total, err
}

func ReassembleWebMapAIIn(ctx context.Context, dir, owner, mapID, id string) (WebMapAITask, error) {
	return restartWebMapAIIn(ctx, dir, owner, mapID, id, false)
}

func canResumeWebMapAI(ctx context.Context, tx *sql.Tx, task WebMapAITask) (bool, int, error) {
	if !task.SourceOverview || task.RunID == "" {
		return false, 0, nil
	}
	var count, ready int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(b.status IN ('completed','insufficient')),0)
 FROM mind_map_generation_batches b JOIN mind_map_generation_runs r ON r.id=b.run_id
 WHERE b.run_id=? AND r.map_id=? AND r.status IN ('running','failed','cancelled')`, task.RunID, task.MapID).Scan(&count, &ready)
	return count > 0 && (task.Status == "failed" || task.Status == "cancelled"), ready, err
}

func ResumeWebMapAIIn(ctx context.Context, dir, owner, mapID, id string) (WebMapAITask, error) {
	return restartWebMapAIIn(ctx, dir, owner, mapID, id, true)
}

func restartWebMapAIIn(ctx context.Context, dir, owner, mapID, id string, resume bool) (WebMapAITask, error) {
	if !webMapExactID(mapID) || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return WebMapAITask{}, ErrWebMapAIInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapAITask{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapAITask{}, err
	}
	defer tx.Rollback()
	task, err := scanWebAITask(tx.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=? AND owner=? AND map_id=?`, id, owner, mapID))
	if err != nil {
		return task, err
	}
	// Repeated delivery never creates another task or resets active work.
	if task.SourceOverview && task.RunID != "" && (task.Status == "queued" || task.Status == "running" || task.Status == "ready" || task.Status == "applied") {
		return task, nil
	}
	// Let duplicate accepted requests return above even while the worker owns
	// the lock. A cancelled worker must finish before it can be requeued.
	if resume {
		lock, err := filelock.Try(filepath.Join(dir, "web-map-ai.lock"))
		if err != nil {
			return WebMapAITask{}, ErrWebMapAIChanged
		}
		defer lock.Close()
	}
	allowed, err := canReassembleWebMapAI(ctx, tx, task)
	if resume {
		allowed, _, err = canResumeWebMapAI(ctx, tx, task)
	}
	if err != nil {
		return task, err
	}
	if !allowed {
		return task, ErrWebMapAIChanged
	}
	_, _, _, generation, digest, err := webAIInputsTx(ctx, tx, mapID, task.NodeID, task.Input)
	if err != nil {
		return task, err
	}
	if generation != task.Generation || digest != task.InputDigest {
		return task, ErrWebMapAIChanged
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return task, err
	}
	if cfg.Revision() != task.ConfigRevision {
		return task, ErrWebMapAIChanged
	}
	if resume && task.Status == "cancelled" {
		var saved int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_previews WHERE run_id=?`, task.RunID).Scan(&saved); err != nil {
			return task, err
		}
		if saved == 1 {
			if _, err = tx.ExecContext(ctx, `UPDATE mind_map_generation_runs SET status='preview' WHERE id=? AND status='cancelled'`, task.RunID); err != nil {
				return task, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='ready',phase='preview',error='' WHERE id=? AND status='cancelled'`, task.ID); err != nil {
				return task, err
			}
			task.Status, task.Phase, task.Error = "ready", "preview", ""
			return task, tx.Commit()
		}
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_ai_tasks WHERE owner=? AND status IN ('queued','running','ready')`, owner).Scan(&pending); err != nil {
		return task, err
	}
	if pending >= 20 {
		return task, ErrWebMapAILimit
	}
	phase := "reassembling"
	if resume {
		phase = "resuming"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='queued',phase=?,error='' WHERE id=? AND status IN ('failed','cancelled')`, phase, id); err != nil {
		return task, err
	}
	task.Status, task.Phase, task.Error = "queued", phase, ""
	return task, tx.Commit()
}
