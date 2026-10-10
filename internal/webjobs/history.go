package webjobs

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

type Attempt struct {
	Number    int    `json:"number"`
	Status    string `json:"status"`
	Started   string `json:"started"`
	Finished  string `json:"finished"`
	HasReport bool   `json:"has_report"`
	HasError  bool   `json:"has_error"`
}

func (q *Queue) History(ctx context.Context, actor workspace.Actor, id string) ([]Attempt, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := q.Get(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, `SELECT attempt,substr(status,1,32),substr(started_at,1,40),substr(finished_at,1,40),run_id>0,length(error)>0 FROM attempts WHERE job_id=? ORDER BY attempt DESC LIMIT ?`, id, MaxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Attempt{}
	for rows.Next() {
		var a Attempt
		if err = rows.Scan(&a.Number, &a.Status, &a.Started, &a.Finished, &a.HasReport, &a.HasError); err != nil {
			return nil, err
		}
		switch a.Status {
		case "running", "succeeded", "partial", "failed", "cancelled", "interrupted", "conflict":
		default:
			a.Status = "unknown"
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (q *Queue) AttemptPages(ctx context.Context, actor workspace.Actor, id string, attempt, after int) (mem.WebImportPages, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := q.Get(ctx, actor, id); err != nil {
		return mem.WebImportPages{}, err
	}
	if attempt < 1 || attempt > MaxAttempts || after < 0 {
		return mem.WebImportPages{}, ErrInvalid
	}
	var runID int64
	err := q.db.QueryRowContext(ctx, `SELECT run_id FROM attempts WHERE job_id=? AND attempt=?`, id, attempt).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && runID == 0 {
		return mem.WebImportPages{}, workspace.ErrNotFound
	}
	if err != nil {
		return mem.WebImportPages{}, err
	}
	result, err := mem.ReadWebImportPagesIn(ctx, q.dir, runID, after)
	if errors.Is(err, sql.ErrNoRows) {
		err = workspace.ErrNotFound
	}
	return result, err
}
