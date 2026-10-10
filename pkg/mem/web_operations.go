package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

var ErrWebOperationInvalid = errors.New("проверьте параметры операции или перечитайте настройки")

// A safe, actionable message: never expose provider URLs or raw remote errors.
var ErrWebGraphAnswerModel = errors.New("Для построения и анализа графа выберите модель ответов в разделе «Настройки базы». bge-m3 подходит только для поиска. Сохраните настройки и создайте новое задание.")

func validateWebGraphAnswerModel(kind string, cfg *Config) error {
	if kind != "graph-extract" && kind != "graph-analyze" {
		return nil
	}
	model := strings.TrimSpace(cfg.Answer.Model)
	if model == "" || isEmbeddingModel(model) {
		return ErrWebGraphAnswerModel
	}
	return nil
}

type retainedDocumentIdentity struct{ ID, Path, Hash string }
type operationDocument struct {
	Identity     retainedDocumentIdentity
	State, Title string
	Tags         []string
	Important    bool
	Revision     string
}
type WebOperationInput struct {
	RequestID      string `json:"request_id"`
	Kind           string `json:"kind"`
	ConfigRevision string `json:"config_revision"`
	Focus          string `json:"focus"`
	Document       string `json:"document"`
	Batches        int    `json:"batches"`
	ContextChars   int    `json:"context_chars"`
	AllChunks      bool   `json:"all_chunks,omitempty"`
}
type WebOperation struct {
	ID        string              `json:"id"`
	Kind      string              `json:"kind"`
	Status    string              `json:"status"`
	Current   int                 `json:"current"`
	Total     int                 `json:"total"`
	Stage     string              `json:"stage"`
	Error     string              `json:"error,omitempty"`
	RunID     string              `json:"run_id,omitempty"`
	Updated   string              `json:"updated"`
	Document  string              `json:"document,omitempty"`
	AllChunks bool                `json:"all_chunks,omitempty"`
	Input     WebOperationInput   `json:"-"`
	Owner     string              `json:"-"`
	Documents []operationDocument `json:"-"`
	ImportRun int64               `json:"-"`
	Local     bool                `json:"-"`
}

const webOperationSchema = `CREATE TABLE IF NOT EXISTS web_operations(id TEXT PRIMARY KEY,owner TEXT NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,input TEXT NOT NULL,documents TEXT NOT NULL,local INTEGER NOT NULL,current INTEGER NOT NULL DEFAULT 0,total INTEGER NOT NULL DEFAULT 0,stage TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',import_run INTEGER NOT NULL DEFAULT 0,updated TEXT NOT NULL);`
const operationColumns = `id,owner,kind,status,input,documents,local,current,total,stage,error,run_id,import_run,updated`

func scanOperation(row interface{ Scan(...any) error }) (op WebOperation, err error) {
	var input, documents string
	err = row.Scan(&op.ID, &op.Owner, &op.Kind, &op.Status, &input, &documents, &op.Local, &op.Current, &op.Total, &op.Stage, &op.Error, &op.RunID, &op.ImportRun, &op.Updated)
	if err == nil {
		err = json.Unmarshal([]byte(input), &op.Input)
	}
	if err == nil {
		err = json.Unmarshal([]byte(documents), &op.Documents)
	}
	op.Document, op.AllChunks = op.Input.Document, op.Input.AllChunks
	return
}
func operationExists(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_operations'`).Scan(&count)
	return count != 0, err
}
func ListWebOperationsIn(ctx context.Context, dir, owner string) ([]WebOperation, error) {
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	exists, err := operationExists(ctx, db)
	if err != nil || !exists {
		return []WebOperation{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE owner=? ORDER BY updated DESC,id LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WebOperation{}
	for rows.Next() {
		op, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, op)
	}
	return items, rows.Err()
}
func StartWebOperationIn(ctx context.Context, dir, owner string, in WebOperationInput, local bool) (op WebOperation, err error) {
	defer applog.Track("pkg.mem.StartWebOperationIn")(&err)
	if len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || len(in.Focus) > 8000 || len(in.Document) > 256 {
		return op, ErrWebOperationInvalid
	}
	if in.Kind != "rechunk" && in.Kind != "graph-extract" && in.Kind != "graph-analyze" && in.Kind != "graph-duplicates" {
		return op, ErrWebOperationInvalid
	}
	if in.AllChunks && (in.Kind != "graph-extract" || strings.TrimSpace(in.Document) == "") {
		return op, ErrWebOperationInvalid
	}
	if in.Kind != "rechunk" && (strings.TrimSpace(in.Focus) == "" || in.Batches < 1 || in.Batches > 128 || in.ContextChars < 1000 || in.ContextChars > 48000) {
		return op, ErrWebOperationInvalid
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return op, err
	}
	if cfg.Revision() != in.ConfigRevision {
		return op, ErrWebOperationInvalid
	}
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return op, err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, webOperationSchema); err != nil {
		return op, err
	}
	if in.AllChunks {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE document_id=?`, in.Document).Scan(&count); err != nil {
			return op, err
		}
		if count == 0 {
			return op, ErrWebOperationInvalid
		}
	}
	encoded, _ := json.Marshal(in)
	old, e := scanOperation(db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE id=?`, in.RequestID))
	if e == nil {
		if old.Owner != owner || old.Input != in || old.Local != local {
			return op, ErrWebOperationInvalid
		}
		return old, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return op, e
	}
	if err = validateWebGraphAnswerModel(in.Kind, cfg); err != nil && !in.AllChunks {
		return op, err
	}
	docs := []operationDocument{}
	if in.Kind == "rechunk" {
		rows, e := db.QueryContext(ctx, `SELECT DISTINCT document_id FROM entries WHERE document_id!='' AND (?='' OR document_id=?) ORDER BY document_id LIMIT 1001`, in.Document, in.Document)
		if e != nil {
			return op, e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return op, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return op, e
		}
		if len(ids) == 0 || len(ids) > 1000 {
			return op, ErrWebOperationInvalid
		}
		for _, id := range ids {
			var chunks, size int64
			if e = db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)),0) FROM entries WHERE document_id=?`, id).Scan(&chunks, &size); e != nil {
				return op, e
			}
			if chunks > 20000 || size > 64<<20 {
				return op, ErrWebIndexLimit
			}
			state, e := ReadDocumentUpdateStateIn(ctx, dir, id)
			if e != nil {
				return op, e
			}
			var rev, hash string
			e = db.QueryRowContext(ctx, `SELECT document_revision FROM entries WHERE document_id=? LIMIT 1`, id).Scan(&rev)
			if e != nil {
				return op, e
			}
			e = db.QueryRowContext(ctx, `SELECT file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, id, rev).Scan(&hash)
			if e != nil {
				return op, ErrSourceFileUnverified
			}
			docs = append(docs, operationDocument{Identity: retainedDocumentIdentity{id, state.SourcePath, hash}, State: state.State, Title: state.Title, Tags: state.Tags, Important: state.Important, Revision: rev})
		}
	}
	docJSON, _ := json.Marshal(docs)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	// Another client may have finished the same preflight while we planned.
	old, e = scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE id=?`, in.RequestID))
	if e == nil {
		if old.Owner != owner || old.Input != in || old.Local != local {
			return op, ErrWebOperationInvalid
		}
		return old, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return op, e
	}
	var active, total int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN status IN ('queued','running') THEN 1 END),COUNT(*) FROM web_operations`).Scan(&active, &total); err != nil {
		return op, err
	}
	if active >= 4 || total >= 1000 {
		return op, ErrWebMapEditLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_operations(id,owner,kind,status,input,documents,local,total,updated) VALUES(?,?,?,'queued',?,?,?,?,?)`, in.RequestID, owner, in.Kind, string(encoded), string(docJSON), local, len(docs), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return op, err
	}
	if err = tx.Commit(); err != nil {
		return op, err
	}
	return scanOperation(db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE id=?`, in.RequestID))
}
func ActWebOperationIn(ctx context.Context, dir, owner, id, action string) error {
	if action != "cancel" && action != "retry" {
		return ErrWebOperationInvalid
	}
	if action == "retry" {
		lock, err := filelock.Try(filepath.Join(dir, "web-operations.lock"))
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return err
	}
	defer db.Close()
	query := `UPDATE web_operations SET status='cancelled',updated=? WHERE id=? AND owner=? AND status IN ('queued','running')`
	if action == "retry" {
		op, e := scanOperation(db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE id=? AND owner=?`, id, owner))
		if e != nil {
			return ErrWebOperationInvalid
		}
		cfg, e := LoadConfigIn(dir)
		if e != nil {
			return e
		}
		if cfg.Revision() != op.Input.ConfigRevision {
			return ErrWebOperationInvalid
		}
		if e = validateWebGraphAnswerModel(op.Kind, cfg); e != nil && !op.Input.AllChunks {
			return e
		}
		query = `UPDATE web_operations SET status='queued',error='',updated=? WHERE id=? AND owner=? AND status IN ('failed','interrupted','cancelled')`
	}
	res, err := db.ExecContext(ctx, query, time.Now().UTC().Format(time.RFC3339Nano), id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrWebOperationInvalid
	}
	return nil
}

func ProcessWebOperationIn(parent context.Context, dir, owner string) (worked bool, err error) {
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return false, err
	}
	defer db.Close()
	exists, err := operationExists(parent, db)
	if err != nil || !exists {
		return false, err
	}
	lock, err := filelock.Try(filepath.Join(dir, "web-operations.lock"))
	if errors.Is(err, filelock.ErrBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	// A process death is explicit interruption, never an assumed success.
	if _, err = db.ExecContext(parent, `UPDATE web_operations SET status='interrupted',error='Работа прервалась. Можно продолжить с сохранённого этапа.' WHERE owner=? AND status='running'`, owner); err != nil {
		return false, err
	}
	op, err := scanOperation(db.QueryRowContext(parent, `SELECT `+operationColumns+` FROM web_operations WHERE owner=? AND status='queued' ORDER BY updated,id LIMIT 1`, owner))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer applog.Track("pkg.mem.ProcessWebOperationIn")(&err)
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	claimed, err := claimWebOperation(ctx, db, op.ID)
	if err != nil || !claimed {
		return true, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var status string
				if e := db.QueryRowContext(ctx, `SELECT status FROM web_operations WHERE id=?`, op.ID).Scan(&status); e != nil || status != "running" {
					cancel()
					return
				}
			}
		}
	}()
	progress := func(stage string, current, total int, run string, importRun int64) error {
		op.Stage, op.Current, op.Total, op.RunID, op.ImportRun = stage, current, total, run, importRun
		applog.Event("INFO", "operation.progress", applog.Fields{"operation_id": op.ID, "kind": op.Kind, "stage": stage, "current": current, "total": total})
		result, e := db.ExecContext(ctx, `UPDATE web_operations SET stage=?,current=?,total=?,run_id=?,import_run=?,updated=? WHERE id=? AND status='running'`, stage, current, total, run, importRun, time.Now().UTC().Format(time.RFC3339Nano), op.ID)
		if e == nil {
			if n, _ := result.RowsAffected(); n != 1 {
				cancel()
				return context.Canceled
			}
		}
		return e
	}
	configLock, e := filelock.Try(filepath.Join(dir, "config.lock"))
	if e == nil {
		cfg, readErr := LoadConfigIn(dir)
		e = readErr
		if e == nil && cfg.Revision() != op.Input.ConfigRevision {
			e = ErrWebOperationInvalid
		}
		if e == nil {
			if op.Kind == "rechunk" {
				e = runRechunkOperation(ctx, db, dir, cfg, &op, progress)
			} else {
				e = runGraphOperation(ctx, dir, cfg, &op, progress)
			}
		}
		configLock.Close()
	}
	cancel()
	<-done
	status, message := "completed", ""
	if e != nil {
		status = "failed"
		message = "Операция остановлена; проверьте журнал. Повтор продолжит сохранённые этапы."
		if errors.Is(e, context.Canceled) {
			status = "cancelled"
		}
		var rejected *webGraphExtractionFailure
		if errors.As(e, &rejected) {
			message = fmt.Sprintf("Модель не прошла проверку структуры или цитат в пакете %d. Сохранён прогресс: %d/%d. Нажмите «Продолжить»: готовые пакеты не будут генерироваться заново. Покрытие обновится после публикации всей порции.", rejected.Batch, op.Current, op.Total)
		}
		if errors.Is(e, ErrWebGraphAnswerModel) {
			message = ErrWebGraphAnswerModel.Error()
		}
		if errors.Is(e, ErrWebOperationInvalid) {
			message = "Настройки изменились. Создайте новое задание."
		}
		applog.Error("operation.failed", e)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	_, err = db.ExecContext(finishCtx, `UPDATE web_operations SET status=?,error=?,updated=? WHERE id=? AND status='running'`, status, message, time.Now().UTC().Format(time.RFC3339Nano), op.ID)
	return true, err
}

type operationProgress func(string, int, int, string, int64) error

func ReadWebOperationResultIn(ctx context.Context, dir, owner, id string) (json.RawMessage, error) {
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var result string
	err = db.QueryRowContext(ctx, `SELECT r.result FROM web_operation_results r JOIN web_operations o ON r.id=o.id WHERE o.id=? AND o.owner=? AND o.status='completed' AND length(CAST(r.result AS BLOB))<=4194304`, id, owner).Scan(&result)
	if err != nil {
		return nil, ErrWebOperationInvalid
	}
	return json.RawMessage(result), nil
}

func runRechunkOperation(ctx context.Context, db *sql.DB, dir string, cfg *Config, op *WebOperation, progress operationProgress) error {
	for i := op.Current; i < len(op.Documents); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		d := op.Documents[i]
		if op.ImportRun > 0 {
			run, e := ReadDocumentImportRunIn(ctx, dir, op.ImportRun)
			if e != nil {
				return e
			}
			if run.DocumentUpdated {
				if e = progress("Документ сохранён", i+1, len(op.Documents), "", 0); e != nil {
					return e
				}
				continue
			}
		}
		current, e := ReadDocumentUpdateStateIn(ctx, dir, d.Identity.ID)
		if e != nil {
			return e
		}
		if current.State != d.State {
			return ErrDocumentUpdateStateChanged
		}
		anchor := EvidenceAnchor{DocumentID: d.Identity.ID, DocumentRevision: d.Revision, SourcePath: d.Identity.Path}
		file, e := openLocatedSource(ctx, db, dir, anchor, op.Local)
		if e != nil {
			return e
		}
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
			file.Close()
			return ErrSourceFileUnverified
		}
		snapshot, e := (&Store{db: db}).verifiedSourceFile(ctx, anchor, file)
		file.Close()
		if e != nil {
			return e
		}
		tmp, e := os.MkdirTemp(dir, "rechunk-")
		if e != nil {
			closeSourceSnapshot(snapshot)
			return e
		}
		path := filepath.Join(tmp, "source"+strings.ToLower(filepath.Ext(d.Identity.Path)))
		output, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			_, e = io.Copy(output, snapshot)
			closeErr := output.Close()
			if e == nil {
				e = closeErr
			}
		}
		closeSourceSnapshot(snapshot)
		if e == nil {
			s := &Store{db: db, path: StorePathIn(dir), importOnly: true}
			_, e = ImportDocument(ctx, cfg, s, path, ImportOptions{Title: d.Title, Tags: d.Tags, Important: d.Important, retained: &d.Identity, Replacement: &DocumentReplacement{SourcePath: d.Identity.Path, State: d.State}, MaxChunks: 20000, Started: func(id int64) error { return progress("Извлечение", i, len(op.Documents), "", id) }, Progress: func(event ingest.ProgressEvent) {
				if ctx.Err() == nil {
					_ = progress(fmt.Sprintf("%s: %d/%d", event.Stage, event.Current, event.Total), i, len(op.Documents), "", op.ImportRun)
				}
			}})
		}
		os.Remove(path)
		os.Remove(tmp)
		if e != nil {
			return e
		}
		if e = progress("Документ сохранён", i+1, len(op.Documents), "", 0); e != nil {
			return e
		}
	}
	return nil
}

// The queued selection is advisory. Cancellation may commit before this claim;
// only a queued-to-running transition permits the worker to start any work.
func claimWebOperation(ctx context.Context, db *sql.DB, id string) (bool, error) {
	result, err := db.ExecContext(ctx, `UPDATE web_operations SET status='running',error='' WHERE id=? AND status='queued'`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
