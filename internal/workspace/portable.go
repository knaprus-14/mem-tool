package workspace

import (
	"context"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Import always creates a new private directory; an existing library cannot be
// overwritten by a damaged archive or by a repeated upload.
func (m *Manager) ImportPortable(ctx context.Context, actor Actor, name, archive string, maxWorkspaceBytes, remainingUserBytes int64, validate ...func(*mem.Config) error) (w Workspace, manifest mem.PortableManifest, err error) {
	name = strings.TrimSpace(name)
	if !actor.Valid() || name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return w, manifest, ErrInvalid
	}
	parent := filepath.Join(m.managedRoot, actor.ID)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return
	}
	root, err := os.MkdirTemp(parent, "import-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil && w.ID == "" {
			_ = os.RemoveAll(root)
		}
	}()
	dir := filepath.Join(root, mem.MemDirName)
	manifest, err = mem.RestorePortableDatabaseIn(ctx, archive, dir, actor.ID)
	if err != nil {
		return
	}
	for _, check := range validate {
		cfg, e := mem.LoadConfigIn(dir)
		if e != nil {
			return w, manifest, e
		}
		if e = check(cfg); e != nil {
			return w, manifest, e
		}
	}
	usage, e := MeasureStorage(ctx, root)
	if e != nil {
		return w, manifest, e
	}
	if usage.Bytes > maxWorkspaceBytes || usage.Bytes > remainingUserBytes {
		return w, manifest, ErrStorageLimit
	}
	w, err = m.Register(ctx, actor, root)
	if err != nil {
		return
	}
	_, err = m.db.ExecContext(ctx, `UPDATE web_workspaces SET name=? WHERE id=? AND owner_id=?`, name, w.ID, actor.ID)
	if err == nil {
		w.Name = name
	}
	return
}
