package webapp

import (
	"context"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) storageUsage(ctx context.Context, actor workspace.Actor, id string) (current workspace.StorageUsage, total int64, err error) {
	bases, err := a.manager.List(ctx, actor)
	if err != nil {
		return
	}
	for _, base := range bases {
		// Local registrations may point to removable disks. Their unavailable
		// directories are outside the current local storage total; the selected
		// base still fails normally. Hosted quotas always fail closed instead.
		if a.cfg.Local && base.ID != id && localWorkspaceUnavailable(base) {
			continue
		}
		usage, e := workspace.MeasureStorage(ctx, base.Root)
		if e != nil {
			if a.cfg.Local && base.ID != id && localWorkspaceUnavailable(base) {
				continue
			}
			err = e
			return
		}
		total += usage.Bytes
		if base.ID == id {
			current = usage
		}
	}
	return
}

func localWorkspaceUnavailable(base workspace.Workspace) bool {
	info, err := os.Stat(filepath.Join(base.Root, ".mem"))
	return os.IsNotExist(err) || os.IsPermission(err) || (err == nil && !info.IsDir())
}
func (a *App) checkStorage(ctx context.Context, actor workspace.Actor, base workspace.Workspace) error {
	current, total, err := a.storageUsage(ctx, actor, base.ID)
	if err != nil {
		return err
	}
	if current.Bytes >= a.cfg.MaxWorkspaceBytes || total >= a.cfg.MaxUserBytes {
		return workspace.ErrStorageLimit
	}
	return nil
}
func (a *App) storageRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id string) {
	if _, err := a.manager.Resolve(r.Context(), actor, id); err != nil {
		a.fail(w, err)
		return
	}
	current, total, err := a.storageUsage(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workspace": current, "user_bytes": total, "workspace_limit": a.cfg.MaxWorkspaceBytes, "user_limit": a.cfg.MaxUserBytes})
}
func storageWriteRequest(r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" {
		return false
	}
	// Saved-view admission happens after receipt recovery.
	if r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/view-state") || strings.HasSuffix(r.URL.Path, "/view-presets")) {
		return false
	}
	// External import checks capacity after its receipt lookup, so a lost
	// successful response remains recoverable even when the workspace is full.
	if r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/maps/import/outline-preview") || strings.HasSuffix(r.URL.Path, "/maps/import/outline-apply")) {
		return false
	}
	if r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/maps/import/preview") || strings.HasSuffix(r.URL.Path, "/annotations/import-preview") || strings.HasSuffix(r.URL.Path, "/reanchor-preview")) {
		return false
	}
	// Reading mutations check capacity after decoding: removal and bounded position updates remain available.
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/reading-workspace") {
		return false
	}
	// Maintenance reports have distinct routes from map snapshot creation.
	if strings.Contains(r.URL.Path, "/maintenance/") {
		for _, report := range []string{"snapshots", "coverage", "status", "diff", "corpus-diff", "extract-runs", "extract-run", "analysis-runs", "analysis-run", "restore-runs", "reviews", "edits", "merges", "profile", "eval", "eval-labeled", "prune-runs"} {
			if strings.HasSuffix(r.URL.Path, "/"+report) {
				return false
			}
		}
	}
	// Cleanup/cancellation remain accessible when capacity is used.
	for _, suffix := range []string{"/cancel", "/delete", "/source-remove", "/export", "/search", "/ask", "/evidence", "/client-events", "/study", "/study-history"} {
		if strings.HasSuffix(r.URL.Path, suffix) {
			return false
		}
	}
	return true
}
