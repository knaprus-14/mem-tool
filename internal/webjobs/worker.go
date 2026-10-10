package webjobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/knaprus-14/mem-tool/internal/applog"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sourcefile"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type Importer func(context.Context, string, string, mem.ImportOptions) (mem.ImportResult, error)
type Service struct {
	BeforeWork func(context.Context, workspace.Actor, workspace.Workspace) error
	manager    *workspace.Manager
	actors     []workspace.Actor
	Import     Importer
	next       int
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func New(manager *workspace.Manager, actors []workspace.Actor) *Service {
	return &Service{manager: manager, actors: append([]workspace.Actor(nil), actors...), Import: mem.ImportDocumentIn}
}

// Start runs one job at a time. Per-workspace OS locks coordinate separate web
// processes and release automatically on crash; no stale PID/lease guessing.
func (s *Service) Start(parent context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		for ctx.Err() == nil {
			worked, err := s.ProcessNext(ctx)
			if err != nil && ctx.Err() == nil {
				applog.Error("jobs.worker_failed", err)
				log.Printf("web import worker: %v", err)
			}
			if !worked || err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
	}()
}

func (s *Service) Close() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// ProcessNext is also a deterministic integration-test entry point. Callers
// must not run it concurrently on the same Service.
func (s *Service) ProcessNext(ctx context.Context) (bool, error) {
	type target struct {
		actor workspace.Actor
		id    string
	}
	var targets []target
	for _, actor := range s.actors {
		list, err := s.manager.List(ctx, actor)
		if err != nil {
			return false, err
		}
		for _, w := range list {
			targets = append(targets, target{actor, w.ID})
		}
	}
	if len(targets) == 0 {
		return false, nil
	}
	start := s.next % len(targets)
	var firstErr error
	for i := range targets {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		index := (start + i) % len(targets)
		target := targets[index]
		w, err := s.manager.Resolve(ctx, target.actor, target.id)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		worked, err := s.processTarget(ctx, target.actor, w)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if worked {
			s.next = index + 1
			return true, err
		}
	}
	return false, firstErr
}

func (s *Service) processTarget(parent context.Context, actor workspace.Actor, w workspace.Workspace) (bool, error) {
	if s.BeforeWork != nil {
		if err := s.BeforeWork(parent, actor, w); err != nil {
			return false, err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.BeforeWork != nil {
					if err := s.BeforeWork(ctx, actor, w); err != nil {
						applog.Error("jobs.storage_limit", err)
						cancel()
						return
					}
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	dir := filepath.Join(w.Root, ".mem")
	if worked, err := mem.ProcessWebOperationIn(ctx, dir, actor.ID); worked || err != nil {
		return worked, err
	}
	if worked, err := mem.ProcessWebIndexIn(ctx, dir, actor.ID); worked || err != nil {
		return worked, err
	}
	if worked, err := mem.ProcessWebMapAIIn(ctx, dir, actor.ID); worked || err != nil {
		return worked, err
	}
	if _, err := os.Lstat(filepath.Join(dir, "web-jobs.db")); os.IsNotExist(err) {
		return false, nil
	}
	return s.processWorkspace(ctx, actor, w)
}

func (s *Service) processWorkspace(ctx context.Context, actor workspace.Actor, w workspace.Workspace) (bool, error) {
	lock, err := filelock.Try(filepath.Join(w.Root, ".mem", "web-worker.lock"))
	if errors.Is(err, filelock.ErrBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	q, err := Open(ctx, w, true)
	if err != nil {
		return false, err
	}
	defer q.Close()
	if err = q.reconcile(ctx, actor); err != nil {
		return false, err
	}
	j, err := q.claim(ctx, actor)
	if errors.Is(err, workspace.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.execute(ctx, q, j)
}

func (q *Queue) claim(ctx context.Context, actor workspace.Actor) (Job, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scan(tx.QueryRowContext(ctx, `SELECT `+q.columns()+` FROM jobs j JOIN sources s ON s.id=j.source_id WHERE s.owner=? AND j.status='queued' ORDER BY j.created_at,j.id LIMIT 1`, actor.ID))
	if err != nil {
		return j, err
	}
	now := timestamp()
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET status='running',stage='analyze',updated_at=? WHERE id=?`, now, j.ID); err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(job_id,attempt,status,started_at) VALUES(?,?,'running',?)`, j.ID, j.Attempt, now); err != nil {
		return j, err
	}
	err = tx.Commit()
	j.Status = "running"
	return j, err
}

func (q *Queue) finish(ctx context.Context, j Job, status, message string, result mem.ImportResult) error {
	applog.Event("INFO", "jobs.finish", applog.Fields{"job_id": j.ID, "attempt": j.Attempt, "status": status, "stage": j.Stage, "chunks": result.Chunks, "failed_pages": result.FailedPages})
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := timestamp()
	stage := j.Stage
	if status == "succeeded" || status == "partial" {
		stage = "done"
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,stage=?,chunks=?,failed_pages=?,error=?,updated_at=? WHERE id=? AND attempt=? AND status IN ('running','cancelling')`, status, stage, result.Chunks, result.FailedPages, message, now, j.ID, j.Attempt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status=?,error=?,finished_at=? WHERE job_id=? AND attempt=?`, status, message, now, j.ID, j.Attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *Queue) receipt(ctx context.Context, j Job, runID int64) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET run_id=?,updated_at=? WHERE id=? AND attempt=? AND status='running'`, runID, timestamp(), j.ID, j.Attempt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return context.Canceled
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET run_id=? WHERE job_id=? AND attempt=?`, runID, j.ID, j.Attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *Queue) reconcile(ctx context.Context, actor workspace.Actor) error {
	rows, err := q.db.QueryContext(ctx, `SELECT `+q.columns()+` FROM jobs j JOIN sources s ON s.id=j.source_id WHERE s.owner=? AND j.status IN ('running','cancelling')`, actor.ID)
	if err != nil {
		return err
	}
	var jobs []Job
	for rows.Next() {
		j, e := scan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		status := "interrupted"
		message := "Обработка прервалась. Можно повторить с начала; оригинал сохранён."
		result := mem.ImportResult{}
		if j.Status == "cancelling" {
			status = "cancelled"
			message = "Обработка отменена."
		}
		if j.RunID > 0 {
			run, e := mem.ReadDocumentImportRunIn(ctx, q.dir, j.RunID)
			if e != nil {
				return fmt.Errorf("проверка результата задания %s: %w", j.ID, e)
			}
			if run.DocumentUpdated && (run.Status == mem.DocumentImportRunSucceeded || run.Status == mem.DocumentImportRunPartial) {
				status = run.Status
				message = ""
				result = mem.ImportResult{Chunks: run.Chunks, FailedPages: run.FailedPages}
			} else if run.Status == mem.DocumentImportRunFailed {
				status = "failed"
				j.Stage = run.FinalStage
				message = publicFailure(run.FinalStage)
				if j.IsRevision && strings.Contains(run.ErrorMessage, mem.ErrDocumentUpdateStateChanged.Error()) {
					status = "conflict"
					message = revisionConflictMessage
				}
			} else if run.Status == mem.DocumentImportRunCancelled {
				status = "cancelled"
				message = "Обработка отменена."
			}
		}
		if err = q.finish(ctx, j, status, message, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) execute(parent context.Context, q *Queue, j Job) (logErr error) {
	defer applog.Track("jobs.execute")(&logErr)
	applog.Event("INFO", "jobs.started", applog.Fields{"job_id": j.ID, "attempt": j.Attempt})
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	watchDone := make(chan struct{})
	stopWatch := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				var status string
				err := q.db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=?`, j.ID).Scan(&status)
				if err != nil || status != "running" {
					cancel()
					return
				}
			}
		}
	}()
	var progressErr error
	progressStage := "analyze"
	lastProgress := time.Time{}
	progress := func(e ingest.ProgressEvent) {
		if e.Stage == ingest.StageDone {
			return
		} // extraction done is not document commit
		if e.Stage == progressStage && time.Since(lastProgress) < 200*time.Millisecond && e.Current != e.Total {
			return
		}
		progressStage = e.Stage
		lastProgress = time.Now()
		_, err := q.db.ExecContext(ctx, `UPDATE jobs SET stage=?,current=?,total=?,page=?,updated_at=? WHERE id=? AND attempt=? AND status='running'`, e.Stage, e.Current, e.Total, e.Page, timestamp(), j.ID, j.Attempt)
		if err != nil {
			progressErr = err
			cancel()
		}
	}
	path, err := q.sourcePath(ctx, j)
	var result mem.ImportResult
	if err == nil {
		if j.Replacement != nil {
			current, e := mem.ReadDocumentUpdateStateIn(ctx, q.dir, "web:"+j.DocumentKey)
			if errors.Is(e, sql.ErrNoRows) || (e == nil && (current.State != j.Replacement.State || current.SourcePath != j.Replacement.SourcePath)) {
				err = mem.ErrDocumentUpdateStateChanged
			} else if e != nil {
				err = e
			}
		}
		if err == nil {
			result, err = s.Import(ctx, q.dir, path, mem.ImportOptions{Title: j.Metadata.Title, Tags: j.Metadata.Tags, Important: j.Metadata.Important, DocumentID: "web:" + j.DocumentKey, Replacement: j.Replacement, MaxChunks: MaxChunks, Progress: progress, Started: func(id int64) error { return q.receipt(ctx, j, id) }})
		}
	}
	close(stopWatch)
	<-watchDone
	// A successful receipt wins over cancellation that raced with atomic commit.
	status := "succeeded"
	message := ""
	if err == nil && result.Status == mem.DocumentImportRunPartial {
		status = "partial"
	}
	if err != nil {
		status = "failed"
		message = publicFailure(progressStage)
		if parent.Err() != nil {
			status = "interrupted"
			message = "Сервер остановлен. Можно повторить обработку с начала."
		}
		var currentStatus string
		if e := q.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, j.ID).Scan(&currentStatus); e == nil && currentStatus == "cancelling" {
			status = "cancelled"
			message = "Обработка отменена."
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "Превышено время обработки (30 минут). Можно повторить задание."
		}
		if progressErr != nil {
			message = "Не удалось сохранить прогресс. Повторите обработку."
		}
		if errors.Is(err, mem.ErrDocumentUpdateStateChanged) {
			status = "conflict"
			message = revisionConflictMessage
		}
		// Raw converter/provider errors can include server paths, credentials or
		// document text. Only a bounded, stage-specific message reaches the UI/log.
		log.Printf("web import %s attempt %d: %s (%s)", j.ID, j.Attempt, status, progressStage)
	}
	j.Stage = progressStage
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	return q.finish(finishCtx, j, status, message, result)
}

const revisionConflictMessage = "Документ изменён после выбора новой редакции. Обновите список документов и загрузите редакцию заново. Текущее содержимое сохранено."

func (q *Queue) sourcePath(ctx context.Context, j Job) (string, error) {
	rel := filepath.FromSlash(j.Path)
	if !filepath.IsLocal(rel) || filepath.Dir(rel) != j.SourceID || filepath.Base(rel) != j.Filename {
		return "", ErrInvalid
	}
	path := filepath.Join(q.dir, "uploads", rel)
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if actual != path {
		return "", ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != j.Size {
		return "", ErrInvalid
	}
	hash, err := sourcefile.Hash(ctx, path)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(hash, j.Hash) {
		return "", fmt.Errorf("сохранённый оригинал изменился")
	}
	return path, nil
}

func publicFailure(stage string) string {
	if stage == ingest.StageEmbed {
		return "Не удалось получить векторы. Проверьте доступность выбранной модели и повторите задание. Документ не обновлён."
	}
	return "Не удалось прочитать документ. Проверьте формат файла и установленные средства извлечения текста/OCR, затем повторите задание."
}
