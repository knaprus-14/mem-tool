// Package webjobs persists uploads and processing state next to the selected
// database. HTTP requests only enqueue work; workers call the shared Go importer.
package webjobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/knaprus-14/mem-tool/internal/applog"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

const (
	MaxFileBytes   int64 = 64 << 20
	MaxStoredBytes int64 = 1 << 30
	MaxSources           = 1000
	MaxPending           = 20
	MaxAttempts          = 20
	MaxChunks            = 20000
)

var (
	ErrInvalid  = errors.New("некорректные параметры загрузки")
	ErrLimit    = errors.New("достигнут лимит загрузки или очереди")
	ErrConflict = errors.New("состояние задания изменилось; обновите список")
)

type Metadata struct {
	Title        string   `json:"title"`
	Tags         []string `json:"tags"`
	Important    bool     `json:"important"`
	RelativePath string   `json:"relative_path,omitempty"`
}

type Job struct {
	ID          string                   `json:"id"`
	SourceID    string                   `json:"source_id"`
	DocumentKey string                   `json:"document_key"`
	SourceLabel string                   `json:"source_label,omitempty"`
	IsRevision  bool                     `json:"is_revision"`
	Replacement *mem.DocumentReplacement `json:"-"`
	Filename    string                   `json:"filename"`
	Size        int64                    `json:"size"`
	Status      string                   `json:"status"`
	Attempt     int                      `json:"attempt"`
	Stage       string                   `json:"stage"`
	Current     int                      `json:"current"`
	Total       int                      `json:"total"`
	Page        int                      `json:"page"`
	Chunks      int                      `json:"chunks"`
	FailedPages int                      `json:"failed_pages"`
	Error       string                   `json:"error,omitempty"`
	CreatedAt   string                   `json:"created_at"`
	UpdatedAt   string                   `json:"updated_at"`
	RunID       int64                    `json:"-"`
	Path        string                   `json:"-"`
	Hash        string                   `json:"-"`
	Owner       string                   `json:"-"`
	Metadata    Metadata                 `json:"-"`
}

type Queue struct {
	db      *sql.DB
	dir     string
	version int
}

const schema = `CREATE TABLE IF NOT EXISTS sources (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, filename TEXT NOT NULL, relative_path TEXT NOT NULL,
 sha256 TEXT NOT NULL, format TEXT NOT NULL, size INTEGER NOT NULL, metadata TEXT NOT NULL,
 created_at TEXT NOT NULL, UNIQUE(owner,sha256,format));
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, source_id TEXT NOT NULL UNIQUE, status TEXT NOT NULL,
 attempt INTEGER NOT NULL DEFAULT 1, stage TEXT NOT NULL DEFAULT '',
 current INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, page INTEGER NOT NULL DEFAULT 0,
 chunks INTEGER NOT NULL DEFAULT 0, failed_pages INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '', run_id INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_status ON jobs(status,created_at);
CREATE TABLE IF NOT EXISTS attempts (
 job_id TEXT NOT NULL, attempt INTEGER NOT NULL, status TEXT NOT NULL,
 run_id INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(job_id,attempt));
PRAGMA user_version=1;`

// Files remain immutable uploads. Several uploads may belong to one stable
// document; deduplication is now scoped explicitly to the requested operation.
const migration2 = `ALTER TABLE sources RENAME TO sources_v1;
CREATE TABLE sources (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, filename TEXT NOT NULL, relative_path TEXT NOT NULL,
 sha256 TEXT NOT NULL, format TEXT NOT NULL, size INTEGER NOT NULL, metadata TEXT NOT NULL,
 created_at TEXT NOT NULL, document_key TEXT NOT NULL);
INSERT INTO sources SELECT *,id FROM sources_v1;
DROP TABLE sources_v1;
CREATE INDEX sources_fingerprint ON sources(owner,sha256,format);
CREATE INDEX sources_document ON sources(owner,document_key);
ALTER TABLE jobs ADD COLUMN replacement TEXT NOT NULL DEFAULT '';
PRAGMA user_version=2;`

// Open only creates queue storage after an explicit upload. read mode neither
// migrates nor creates any files. ErrNotExist means this base has no web jobs.
func Open(ctx context.Context, w workspace.Workspace, create bool) (*Queue, error) {
	dir := filepath.Join(w.Root, ".mem")
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if actual != dir {
		return nil, ErrInvalid
	}
	path := filepath.Join(dir, "web-jobs.db")
	info, err := os.Lstat(path)
	if err != nil && (!create || !os.IsNotExist(err)) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	var db *sql.DB
	if create {
		db, err = sqliteutil.Open(path)
	} else {
		db, err = sqliteutil.OpenReadOnly(ctx, path)
	}
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	q := &Queue{db: db, dir: dir}
	var version int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 2 {
		db.Close()
		return nil, fmt.Errorf("формат очереди создан более новой версией mem-tool")
	}
	if create && version < 2 {
		migrationLock, e := filelock.Try(filepath.Join(dir, "web-schema.lock"))
		if e != nil {
			db.Close()
			return nil, e
		}
		defer migrationLock.Close()
		tx, e := db.BeginTx(ctx, nil)
		if e == nil {
			e = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version)
			if e == nil && version > 2 {
				e = fmt.Errorf("формат очереди создан более новой версией mem-tool")
			}
			if e == nil && version == 0 {
				_, e = tx.ExecContext(ctx, schema)
			}
			if e == nil && version < 2 {
				_, e = tx.ExecContext(ctx, migration2)
			}
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
		}
		if e != nil {
			db.Close()
			return nil, e
		}
		version = 2
	}
	q.version = version
	return q, nil
}

func (q *Queue) Close() error { return q.db.Close() }

func validate(name string, m Metadata) (string, error) {
	if m.RelativePath != "" {
		if len(m.RelativePath) > 1024 || !utf8.ValidString(m.RelativePath) || strings.ContainsAny(m.RelativePath, `\:`) {
			return "", ErrInvalid
		}
		parts := strings.Split(m.RelativePath, "/")
		if parts[len(parts)-1] != name {
			return "", ErrInvalid
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return "", ErrInvalid
			}
			for _, r := range part {
				if r < 32 || r == 127 {
					return "", ErrInvalid
				}
			}
		}
	}
	if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 180 || strings.ContainsAny(name, `/\<>:"|?*`) || strings.HasSuffix(name, ".") {
		return "", ErrInvalid
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return "", ErrInvalid
		}
	}
	base := strings.ToUpper(strings.Split(name, ".")[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
		return "", ErrInvalid
	}
	format := strings.ToLower(filepath.Ext(name))
	switch format {
	case ".md", ".markdown":
		format = "markdown"
	case ".pdf":
		format = "pdf"
	case ".djvu", ".djv":
		format = "djvu"
	case ".docx":
		format = "docx"
	case ".txt", ".csv", ".tsv", ".json", ".xml":
		format = "text"
	default:
		return "", fmt.Errorf("%w: поддерживаются Markdown, PDF, DjVu, DOCX, TXT, CSV, TSV, JSON и XML", ErrInvalid)
	}
	if utf8.RuneCountInString(m.Title) > 200 || len(m.Tags) > 20 {
		return "", ErrInvalid
	}
	for _, tag := range m.Tags {
		if strings.TrimSpace(tag) == "" || utf8.RuneCountInString(tag) > 80 {
			return "", ErrInvalid
		}
	}
	return format, nil
}

func (q *Queue) Enqueue(ctx context.Context, actor workspace.Actor, name string, body io.Reader, metadata Metadata) (job Job, duplicate bool, logErr error) {
	defer applog.Track("jobs.Enqueue")(&logErr)
	defer func() {
		if logErr == nil {
			applog.Event("INFO", "jobs.queued", applog.Fields{"job_id": job.ID, "duplicate": duplicate})
		}
	}()

	return q.enqueue(ctx, actor, name, body, metadata, "", nil)
}

func (q *Queue) enqueue(ctx context.Context, actor workspace.Actor, name string, body io.Reader, metadata Metadata, documentKey string, replacement *mem.DocumentReplacement) (Job, bool, error) {
	if !actor.Valid() {
		return Job{}, false, workspace.ErrNotFound
	}
	format, err := validate(name, metadata)
	if err != nil {
		return Job{}, false, err
	}
	uploadLock, err := filelock.Try(filepath.Join(q.dir, "web-upload.lock"))
	if err != nil {
		return Job{}, false, err
	}
	defer uploadLock.Close()
	root, err := q.uploadRoot()
	if err != nil {
		return Job{}, false, err
	}
	defer root.Close()
	id := randomID()
	temp := ".incoming-" + id
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Job{}, false, err
	}
	defer root.Remove(temp)
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(&contextReader{ctx: ctx, r: body}, MaxFileBytes+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return Job{}, false, copyErr
	}
	if closeErr != nil {
		return Job{}, false, closeErr
	}
	if n > MaxFileBytes {
		return Job{}, false, ErrLimit
	}
	if n == 0 {
		return Job{}, false, fmt.Errorf("%w: файл пуст", ErrInvalid)
	}
	// Count physical files too, including originals orphaned by a power loss
	// before the queue transaction committed. They cannot bypass the disk quota.
	var physicalBytes int64
	if err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() && path != temp {
			info, e := d.Info()
			if e != nil {
				return e
			}
			physicalBytes += info.Size()
		}
		return nil
	}); err != nil {
		return Job{}, false, err
	}
	fingerprint := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	var existing string
	replacementJSON := ""
	if replacement != nil {
		b, _ := json.Marshal(replacement)
		replacementJSON = string(b)
	}
	query := `SELECT jobs.id FROM sources JOIN jobs ON sources.id=jobs.source_id WHERE owner=? AND sha256=? AND format=?`
	args := []any{actor.ID, fingerprint, format}
	if replacement != nil {
		query += ` AND document_key=? AND (jobs.replacement=? OR (relative_path=? AND jobs.status='succeeded'))`
		rel, e := filepath.Rel(filepath.Join(q.dir, "uploads"), replacement.SourcePath)
		if e != nil {
			rel = ""
		} // A transferred source may retain a locator from another OS/volume.
		args = append(args, documentKey, replacementJSON, filepath.ToSlash(rel))
	}
	err = tx.QueryRowContext(ctx, query+` ORDER BY jobs.created_at DESC,jobs.id DESC LIMIT 1`, args...).Scan(&existing)
	if err == nil {
		tx.Rollback()
		j, e := q.Get(ctx, actor, existing)
		return j, true, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	var count, pending int
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0) FROM sources`).Scan(&count, &size); err != nil {
		return Job{}, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running','cancelling')`).Scan(&pending); err != nil {
		return Job{}, false, err
	}
	if count >= MaxSources || pending >= MaxPending || size+n > MaxStoredBytes || physicalBytes+n > MaxStoredBytes {
		return Job{}, false, ErrLimit
	}
	if err = root.Mkdir(id, 0700); err != nil {
		return Job{}, false, err
	}
	rel := filepath.Join(id, name)
	committed := false
	defer func() {
		if !committed {
			root.Remove(rel)
			root.Remove(id)
		}
	}()
	if err = root.Rename(temp, rel); err != nil {
		return Job{}, false, err
	}
	meta, _ := json.Marshal(metadata)
	now := timestamp()
	if documentKey == "" {
		documentKey = id
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sources(id,owner,filename,relative_path,sha256,format,size,metadata,created_at,document_key) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, actor.ID, name, filepath.ToSlash(rel), fingerprint, format, n, string(meta), now, documentKey); err != nil {
		return Job{}, false, err
	}
	jobID := randomID()
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,source_id,status,created_at,updated_at,replacement) VALUES(?,?,'queued',?,?,?)`, jobID, id, now, now, replacementJSON); err != nil {
		return Job{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Job{}, false, err
	}
	committed = true
	j, err := q.Get(ctx, actor, jobID)
	return j, false, err
}

func (q *Queue) uploadRoot() (*os.Root, error) {
	path := filepath.Join(q.dir, "uploads")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if actual != path {
		return nil, ErrInvalid
	}
	return os.OpenRoot(path)
}

const jobColumns = `j.id,j.source_id,s.filename,s.size,j.status,j.attempt,j.stage,j.current,j.total,j.page,j.chunks,j.failed_pages,j.error,j.created_at,j.updated_at,j.run_id,s.relative_path,s.sha256,s.owner,s.metadata`

func (q *Queue) columns() string {
	if q.version < 2 {
		return jobColumns + `,s.id,''`
	}
	return jobColumns + `,s.document_key,j.replacement`
}

func scan(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var meta, replacement string
	err := row.Scan(&j.ID, &j.SourceID, &j.Filename, &j.Size, &j.Status, &j.Attempt, &j.Stage, &j.Current, &j.Total, &j.Page, &j.Chunks, &j.FailedPages, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.RunID, &j.Path, &j.Hash, &j.Owner, &meta, &j.DocumentKey, &replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return j, workspace.ErrNotFound
	}
	if err != nil {
		return j, err
	}
	err = json.Unmarshal([]byte(meta), &j.Metadata)
	if err == nil && replacement != "" {
		err = json.Unmarshal([]byte(replacement), &j.Replacement)
	}
	j.SourceLabel = j.Metadata.RelativePath
	j.IsRevision = j.Replacement != nil
	return j, err
}
func (q *Queue) Get(ctx context.Context, actor workspace.Actor, id string) (Job, error) {
	return scan(q.db.QueryRowContext(ctx, `SELECT `+q.columns()+` FROM jobs j JOIN sources s ON j.source_id=s.id WHERE j.id=? AND s.owner=?`, id, actor.ID))
}
func (q *Queue) List(ctx context.Context, actor workspace.Actor) ([]Job, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+q.columns()+` FROM jobs j JOIN sources s ON j.source_id=s.id WHERE s.owner=? ORDER BY CASE WHEN j.status IN ('queued','running','cancelling') THEN 0 ELSE 1 END,j.updated_at DESC,j.id DESC LIMIT 100`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, e := scan(rows)
		if e != nil {
			return nil, e
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (q *Queue) Change(ctx context.Context, actor workspace.Actor, id, action string) (Job, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scan(tx.QueryRowContext(ctx, `SELECT `+q.columns()+` FROM jobs j JOIN sources s ON j.source_id=s.id WHERE j.id=? AND s.owner=?`, id, actor.ID))
	if err != nil {
		return j, err
	}
	switch action {
	case "cancel":
		switch j.Status {
		case "queued":
			j.Status = "cancelled"
		case "running", "cancelling":
			j.Status = "cancelling"
		default:
			return j, ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET status=?,updated_at=? WHERE id=?`, j.Status, timestamp(), id)
	case "retry":
		// A partial revision has already replaced the pinned prior state. Require
		// a new preview so a retry cannot silently overwrite intervening edits.
		if j.Status == "partial" && j.IsRevision {
			return j, ErrConflict
		}
		if j.Status != "failed" && j.Status != "cancelled" && j.Status != "interrupted" && j.Status != "partial" {
			return j, ErrConflict
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running','cancelling')`).Scan(&pending); err != nil {
			return j, err
		}
		if j.Attempt >= MaxAttempts || pending >= MaxPending {
			return j, ErrLimit
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET status='queued',attempt=attempt+1,stage='',current=0,total=0,page=0,chunks=0,failed_pages=0,error='',run_id=0,updated_at=? WHERE id=?`, timestamp(), id)
	default:
		return j, ErrInvalid
	}
	if err != nil {
		return j, err
	}
	if err = tx.Commit(); err != nil {
		return j, err
	}
	return q.Get(ctx, actor, id)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func randomID() string  { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
