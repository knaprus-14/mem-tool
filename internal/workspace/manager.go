// Package workspace owns explicit, user-scoped database references. No method
// changes the process working directory or reads an implicit global database.
package workspace

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

var (
	ErrNotFound  = errors.New("база не найдена или недоступна")
	ErrInvalid   = errors.New("некорректные параметры базы")
	ownerPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

type Actor struct{ ID string }

func (a Actor) Valid() bool { return ownerPattern.MatchString(a.ID) }

type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Root      string `json:"-"`
}

type Manager struct {
	db          *sql.DB
	managedRoot string
}

// Open creates only the web catalog. Existing .mem databases stay untouched.
func Open(stateDir string) (applogResult0 *Manager, applogResult1 error) {
	defer applog.Track("internal.workspace.Open")(&applogResult1)

	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	db, err := sqliteutil.Open(filepath.Join(abs, "catalog.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, err
	}
	if version > 1 {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, fmt.Errorf("каталог веб-баз создан более новой версией mem-tool")
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS web_workspaces (
		id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, name TEXT NOT NULL,
		root TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS web_workspaces_owner ON web_workspaces(owner_id);
		PRAGMA user_version=1;`); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Manager{db: db, managedRoot: filepath.Join(abs, "workspaces")}, nil
}

func (m *Manager) Close() (applogResult0 error) {
	defer applog.Track("internal.workspace.Manager.Close")(&applogResult0)
	return m.db.Close()
}
func (m *Manager) Ready(ctx context.Context) (applogResult0 error) {
	defer applog.Track("internal.workspace.Manager.Ready")(&applogResult0)
	return m.db.PingContext(ctx)
}

func (m *Manager) List(ctx context.Context, actor Actor) (applogResult0 []Workspace, applogResult1 error) {
	defer applog.Track("internal.workspace.Manager.List")(&applogResult1)

	if !actor.Valid() {
		return nil, ErrNotFound
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id,name,root,created_at FROM web_workspaces WHERE owner_id=? ORDER BY created_at,id`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Root, &w.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

func (m *Manager) Resolve(ctx context.Context, actor Actor, id string) (applogResult0 Workspace, applogResult1 error) {
	defer applog.Track("internal.workspace.Manager.Resolve")(&applogResult1)

	if !actor.Valid() {
		return Workspace{}, ErrNotFound
	}
	var w Workspace
	err := m.db.QueryRowContext(ctx, `SELECT id,name,root,created_at FROM web_workspaces WHERE id=? AND owner_id=?`, id, actor.ID).Scan(&w.ID, &w.Name, &w.Root, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	actual, err := filepath.EvalSymlinks(w.Root)
	if err != nil {
		return w, fmt.Errorf("каталог базы недоступен: %w", err)
	}
	if actual != w.Root {
		return Workspace{}, fmt.Errorf("%w: расположение базы изменилось", ErrInvalid)
	}
	// Recheck .mem itself, so replacing it with a symlink cannot redirect a session.
	memDir := filepath.Join(w.Root, mem.MemDirName)
	actual, err = filepath.EvalSymlinks(memDir)
	if err != nil {
		return w, err
	}
	if actual != memDir {
		return Workspace{}, fmt.Errorf("%w: ссылка вместо каталога базы", ErrInvalid)
	}
	return w, nil
}

// Register is only exposed by the local HTTP mode. Server users never supply paths.
func (m *Manager) Register(ctx context.Context, actor Actor, root string) (applogResult0 Workspace, applogResult1 error) {
	defer applog.Track("internal.workspace.Manager.Register")(&applogResult1)

	if !actor.Valid() || strings.TrimSpace(root) == "" {
		return Workspace{}, ErrInvalid
	}
	resolved, err := mem.ResolveDatabaseRoot(root)
	if err != nil {
		return Workspace{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return Workspace{}, err
	}
	memDir := filepath.Join(resolved, mem.MemDirName)
	actual, err := filepath.EvalSymlinks(memDir)
	if err != nil {
		return Workspace{}, err
	}
	if actual != memDir {
		return Workspace{}, fmt.Errorf("%w: ссылка вместо каталога базы", ErrInvalid)
	}
	var existing Workspace
	var owner string
	err = m.db.QueryRowContext(ctx, `SELECT id,name,root,created_at,owner_id FROM web_workspaces WHERE root=?`, resolved).Scan(&existing.ID, &existing.Name, &existing.Root, &existing.CreatedAt, &owner)
	if err == nil {
		if owner != actor.ID {
			return Workspace{}, ErrNotFound
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, err
	}
	name := filepath.Base(resolved)
	if data, err := os.ReadFile(mem.MetaPathIn(memDir)); err == nil {
		var meta mem.MemMeta
		if json.Unmarshal(data, &meta) == nil && strings.TrimSpace(meta.Name) != "" {
			name = meta.Name
		}
	}
	w := Workspace{ID: randomID(), Name: name, Root: resolved, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	// Validate readability before registering; this does not migrate the source.
	if _, err := ReadLibrary(ctx, w, Query{Limit: 1}); err != nil {
		return Workspace{}, err
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO web_workspaces(id,owner_id,name,root,created_at) VALUES(?,?,?,?,?) ON CONFLICT(root) DO NOTHING`, w.ID, actor.ID, w.Name, w.Root, w.CreatedAt)
	if err != nil {
		return Workspace{}, err
	}
	// Resolve the winning row when two requests attach the same root concurrently.
	err = m.db.QueryRowContext(ctx, `SELECT id,name,root,created_at FROM web_workspaces WHERE root=? AND owner_id=?`, resolved, actor.ID).Scan(&w.ID, &w.Name, &w.Root, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	return w, err
}

// Create allocates a fresh child directory in managed storage when root is empty.
// Explicit local roots must already exist; InitMemIn refuses to replace .mem.
func (m *Manager) Create(ctx context.Context, actor Actor, name, root string) (applogResult0 Workspace, applogResult1 error) {
	defer applog.Track("internal.workspace.Manager.Create")(&applogResult1)

	name = strings.TrimSpace(name)
	if !actor.Valid() || name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return Workspace{}, ErrInvalid
	}
	if root == "" {
		root = filepath.Join(m.managedRoot, actor.ID, randomID())
		if err := os.MkdirAll(root, 0700); err != nil {
			return Workspace{}, err
		}
	} else {
		var err error
		root, err = filepath.Abs(root)
		if err != nil {
			return Workspace{}, err
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return Workspace{}, fmt.Errorf("%w: выберите существующую папку", ErrInvalid)
		}
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Workspace{}, err
	}
	memDir := filepath.Join(root, mem.MemDirName)
	if err := mem.InitMemIn(memDir, name); err != nil {
		return Workspace{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	store, err := mem.NewStore(memDir)
	if err != nil {
		return Workspace{}, err
	}
	if err = store.Close(); err != nil {
		return Workspace{}, err
	}
	// On a catalog failure the created database is deliberately retained for recovery.
	return m.Register(ctx, actor, root)
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
