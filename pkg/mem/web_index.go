package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var ErrWebIndexInvalid = errors.New("перечитайте настройки и состояние пересчёта")
var ErrWebIndexConflict = errors.New("база, настройки или состояние пересчёта изменились; обновите страницу")
var ErrWebIndexLimit = errors.New("пересчёт ограничен 20 000 записями, 64 МиБ исходных данных и 64 МиБ новых векторов")

type WebIndexState struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Total             int    `json:"total"`
	Completed         int    `json:"completed"`
	Model             string `json:"model"`
	Error             string `json:"error,omitempty"`
	CanRollback       bool   `json:"can_rollback"`
	Generation        int64  `json:"-"`
	AppliedGeneration int64  `json:"-"`
	ConfigRevision    string `json:"-"`
	Owner             string `json:"-"`
	Backend           string `json:"-"`
	Space             string `json:"-"`
}
type WebIndexInput struct {
	ID             string `json:"id,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	ConfigRevision string `json:"config_revision,omitempty"`
}

const webIndexSchema = `CREATE TABLE IF NOT EXISTS web_index_generation (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),id TEXT NOT NULL,owner TEXT NOT NULL,status TEXT NOT NULL,
 total INTEGER NOT NULL,completed INTEGER NOT NULL DEFAULT 0,model TEXT NOT NULL,backend TEXT NOT NULL,space TEXT NOT NULL,
 base_generation INTEGER NOT NULL,applied_generation INTEGER NOT NULL DEFAULT 0,config_revision TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS web_index_vectors(entry_id INTEGER PRIMARY KEY,old_embedding BLOB,old_dims INTEGER NOT NULL,old_backend TEXT NOT NULL,old_model TEXT NOT NULL,old_space TEXT NOT NULL,new_embedding BLOB,new_dims INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS web_index_requests(id TEXT PRIMARY KEY,owner TEXT NOT NULL,config_revision TEXT NOT NULL);`

func readWebIndex(ctx context.Context, tx *sql.Tx, owner string) (WebIndexState, error) {
	s := WebIndexState{Status: "none"}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_index_generation'`).Scan(&count); err != nil {
		return s, err
	}
	if count == 0 {
		return s, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT id,owner,status,total,completed,model,backend,space,base_generation,applied_generation,config_revision,error FROM web_index_generation WHERE singleton=1 AND owner=?`, owner).Scan(&s.ID, &s.Owner, &s.Status, &s.Total, &s.Completed, &s.Model, &s.Backend, &s.Space, &s.Generation, &s.AppliedGeneration, &s.ConfigRevision, &s.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return WebIndexState{Status: "none"}, nil
	}
	if err != nil {
		return s, err
	}
	if s.Status == "applied" {
		g, e := loadEntryCacheGeneration(tx)
		if e != nil {
			return s, e
		}
		s.CanRollback = g == s.AppliedGeneration
	}
	return s, nil
}

func ReadWebIndexIn(ctx context.Context, dir, owner string) (applogResult0 WebIndexState, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebIndexIn")(&applogResult1)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return WebIndexState{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WebIndexState{}, err
	}
	defer tx.Rollback()
	s, err := readWebIndex(ctx, tx, owner)
	if err != nil {
		return s, err
	}
	return s, tx.Commit()
}

// Start saves only a vector generation. Text, document revisions, note history,
// evidence anchors, FTS and the active vectors remain untouched during preparation.
func StartWebIndexIn(ctx context.Context, dir, owner string, in WebIndexInput) (applogResult0 WebIndexState, applogResult1 error) {
	defer applog.Track("pkg.mem.StartWebIndexIn")(&applogResult1)

	if in.ID != "" || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || in.ConfigRevision == "" {
		return WebIndexState{}, ErrWebIndexInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lock, err := filelock.Try(filepath.Join(dir, "config.lock"))
	if err != nil {
		return WebIndexState{}, err
	}
	defer lock.Close()
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return WebIndexState{}, err
	}
	identity, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return WebIndexState{}, ErrWebIndexInvalid
	}
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebIndexState{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebIndexState{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, webIndexSchema); err != nil {
		return WebIndexState{}, err
	}
	var savedOwner, savedConfig string
	err = tx.QueryRowContext(ctx, `SELECT owner,config_revision FROM web_index_requests WHERE id=?`, in.RequestID).Scan(&savedOwner, &savedConfig)
	if err == nil {
		if savedOwner != owner || savedConfig != in.ConfigRevision {
			return WebIndexState{}, ErrWebIndexConflict
		}
		s, e := readWebIndex(ctx, tx, owner)
		if e != nil {
			return s, e
		}
		if s.ID != in.RequestID {
			return s, ErrWebIndexConflict
		}
		return s, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WebIndexState{}, err
	}
	if cfg.Revision() != in.ConfigRevision {
		return WebIndexState{}, ErrWebIndexConflict
	}
	var active, requests, total int
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_index_generation WHERE status IN ('preparing','ready')`).Scan(&active); err != nil {
		return WebIndexState{}, err
	}
	if active != 0 {
		return WebIndexState{}, ErrWebIndexConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_index_requests`).Scan(&requests); err != nil {
		return WebIndexState{}, err
	}
	if requests >= 1000 {
		return WebIndexState{}, ErrWebIndexLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)),0) FROM entries`).Scan(&total, &size); err != nil {
		return WebIndexState{}, err
	}
	if total == 0 {
		return WebIndexState{}, ErrWebIndexInvalid
	}
	if total > 20000 || size > 64<<20 {
		return WebIndexState{}, ErrWebIndexLimit
	}
	g, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return WebIndexState{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM web_index_vectors; DELETE FROM web_index_generation;`); err != nil {
		return WebIndexState{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_index_vectors(entry_id,old_embedding,old_dims,old_backend,old_model,old_space) SELECT id,embedding,dims,backend,embedding_model,embedding_space FROM entries`); err != nil {
		return WebIndexState{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_index_generation(singleton,id,owner,status,total,model,backend,space,base_generation,config_revision) VALUES(1,?,?,'preparing',?,?,?,?,?,?)`, in.RequestID, owner, total, identity.Model, identity.Backend, identity.SpaceID, g, in.ConfigRevision); err != nil {
		return WebIndexState{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_index_requests(id,owner,config_revision) VALUES(?,?,?)`, in.RequestID, owner, in.ConfigRevision); err != nil {
		return WebIndexState{}, err
	}
	s, err := readWebIndex(ctx, tx, owner)
	if err != nil {
		return s, err
	}
	return s, tx.Commit()
}

// Process prepares one record, then releases the worker to other databases.
// Completed records survive server restart; cancellation is checked again before
// saving a model response. No network request is made under a SQLite write lock.
func ProcessWebIndexIn(ctx context.Context, dir, owner string) (bool, error) {
	return processWebIndexIn(ctx, dir, owner, GetEmbeddingContext)
}
func processWebIndexIn(ctx context.Context, dir, owner string, embed func(context.Context, *Config, string) ([]float32, error)) (bool, error) {
	s, err := ReadWebIndexIn(ctx, dir, owner)
	if err != nil || s.Status != "preparing" {
		return false, err
	}
	lock, err := filelock.Try(filepath.Join(dir, "web-index.lock"))
	if errors.Is(err, filelock.ErrBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return false, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	s, err = readWebIndex(ctx, tx, owner)
	if err != nil || s.Status != "preparing" {
		tx.Rollback()
		return false, err
	}
	g, err := loadEntryCacheGeneration(tx)
	if err != nil {
		tx.Rollback()
		return false, err
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		tx.Rollback()
		return false, err
	}
	if g != s.Generation || cfg.Revision() != s.ConfigRevision {
		tx.Rollback()
		return true, failWebIndex(ctx, db, s.ID, "База или настройки изменились. Начните пересчёт заново.")
	}
	var id int64
	var content string
	err = tx.QueryRowContext(ctx, `SELECT v.entry_id,e.text FROM web_index_vectors v JOIN entries e ON e.id=v.entry_id WHERE v.new_embedding IS NULL ORDER BY v.entry_id LIMIT 1`).Scan(&id, &content)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `UPDATE web_index_generation SET status='ready' WHERE id=? AND status='preparing'`, s.ID)
		if err != nil {
			tx.Rollback()
			return true, err
		}
		return true, tx.Commit()
	}
	if err != nil {
		tx.Rollback()
		return true, err
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	if err = validateEmbeddingText(content); err != nil {
		return true, failWebIndex(ctx, db, s.ID, "Есть пустой или слишком длинный фрагмент. Повторно обработайте соответствующий документ с меньшим размером фрагмента.")
	}
	vector, err := embed(ctx, cfg, content)
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return true, parent.Err()
		}
		return true, failWebIndex(parent, db, s.ID, "Модель не ответила за 100 секунд. Проверьте подключение и начните заново.")
	}
	if err != nil {
		return true, failWebIndex(ctx, db, s.ID, "Модель не вернула вектор. Проверьте подключение и начните заново.")
	}
	valid := len(vector) > 0 && len(vector) <= maxEmbeddingDimensions
	var norm float64
	for _, v := range vector {
		norm += float64(v) * float64(v)
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			valid = false
		}
	}
	if !valid || norm == 0 {
		return true, failWebIndex(ctx, db, s.ID, "Модель вернула недопустимый вектор.")
	}
	data, err := floatsToBytes(vector)
	if err != nil {
		return true, err
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE web_index_generation SET completed=completed WHERE singleton=1`); err != nil {
		return true, err
	}
	current, err := readWebIndex(ctx, tx, owner)
	if err != nil {
		return true, err
	}
	if current.ID != s.ID || current.Status != "preparing" {
		return true, nil
	}
	g, err = loadEntryCacheGeneration(tx)
	if err != nil {
		return true, err
	}
	if g != s.Generation {
		return true, ErrWebIndexConflict
	}
	var size int64
	var dims int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(new_embedding)),0),COALESCE(MAX(new_dims),0) FROM web_index_vectors`).Scan(&size, &dims); err != nil {
		return true, err
	}
	if size+int64(len(data)) > 64<<20 || (dims != 0 && dims != len(vector)) {
		if _, err = tx.ExecContext(ctx, `UPDATE web_index_generation SET status='failed',error='Размерность векторов изменилась или превышен лимит памяти.' WHERE id=?`, s.ID); err != nil {
			return true, err
		}
		return true, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE web_index_vectors SET new_embedding=?,new_dims=? WHERE entry_id=? AND new_embedding IS NULL`, data, len(vector), id); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE web_index_generation SET completed=completed+1,status=CASE WHEN completed+1=total THEN 'ready' ELSE 'preparing' END WHERE id=?`, s.ID); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func failWebIndex(ctx context.Context, db *sql.DB, id, message string) error {
	applog.Event("ERROR", "index.job_failed", applog.Fields{"job_id": id})
	_, err := db.ExecContext(ctx, `UPDATE web_index_generation SET status='failed',error=? WHERE id=? AND status='preparing'`, message, id)
	return err
}

func ChangeWebIndexIn(ctx context.Context, dir, owner, action string, in WebIndexInput) (applogResult0 WebIndexState, applogResult1 error) {
	defer applog.Track("pkg.mem.ChangeWebIndexIn")(&applogResult1)

	if len(in.ID) != 32 || strings.Trim(in.ID, "0123456789abcdef") != "" || in.RequestID != "" || in.ConfigRevision != "" || (action != "apply" && action != "rollback" && action != "cancel") {
		return WebIndexState{}, ErrWebIndexInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lock, err := filelock.Try(filepath.Join(dir, "config.lock"))
	if err != nil {
		return WebIndexState{}, err
	}
	defer lock.Close()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebIndexState{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebIndexState{}, err
	}
	defer tx.Rollback()
	// Reserve the writer before checking the shared generation counter.
	if _, err = tx.ExecContext(ctx, `UPDATE entry_cache_state SET generation=generation WHERE singleton=1`); err != nil {
		return WebIndexState{}, err
	}
	s, err := readWebIndex(ctx, tx, owner)
	if err != nil {
		return s, err
	}
	if s.ID != in.ID {
		return s, ErrWebIndexConflict
	}
	target := map[string]string{"apply": "applied", "rollback": "rolled_back", "cancel": "cancelled"}[action]
	if s.Status == target {
		return s, nil
	}
	g, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return s, err
	}
	switch action {
	case "cancel":
		if s.Status != "preparing" && s.Status != "ready" {
			return s, ErrWebIndexConflict
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM web_index_vectors`); err != nil {
			return s, err
		}
	case "apply":
		cfg, e := LoadConfigIn(dir)
		if e != nil {
			return s, e
		}
		if s.Status != "ready" || s.Completed != s.Total || g != s.Generation || cfg.Revision() != s.ConfigRevision {
			return s, ErrWebIndexConflict
		}
		var total, missing int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(new_embedding IS NULL),0) FROM web_index_vectors`).Scan(&total, &missing); err != nil {
			return s, err
		}
		if total != s.Total || missing != 0 {
			return s, ErrWebIndexConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE entries SET embedding=(SELECT new_embedding FROM web_index_vectors WHERE entry_id=entries.id),dims=(SELECT new_dims FROM web_index_vectors WHERE entry_id=entries.id),backend=?,embedding_model=?,embedding_space=? WHERE id IN (SELECT entry_id FROM web_index_vectors)`, s.Backend, s.Model, s.Space)
	case "rollback":
		if s.Status != "applied" || g != s.AppliedGeneration {
			return s, ErrWebIndexConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE entries SET embedding=(SELECT old_embedding FROM web_index_vectors WHERE entry_id=entries.id),dims=(SELECT old_dims FROM web_index_vectors WHERE entry_id=entries.id),backend=(SELECT old_backend FROM web_index_vectors WHERE entry_id=entries.id),embedding_model=(SELECT old_model FROM web_index_vectors WHERE entry_id=entries.id),embedding_space=(SELECT old_space FROM web_index_vectors WHERE entry_id=entries.id) WHERE id IN (SELECT entry_id FROM web_index_vectors)`)
	}
	if err != nil {
		return s, err
	}
	g, err = loadEntryCacheGeneration(tx)
	if err != nil {
		return s, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE web_index_generation SET status=?,applied_generation=? WHERE id=?`, target, g, s.ID); err != nil {
		return s, err
	}
	s, err = readWebIndex(ctx, tx, owner)
	if err != nil {
		return s, err
	}
	return s, tx.Commit()
}
