package webapp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

// Reading context belongs to the authenticated actor, never an actor supplied
// in a query or JSON body. Removing a tab stays available at storage capacity.
func (a *App) readingWorkspaceRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id string) {
	base, err := a.manager.Resolve(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "Параметры запроса не поддерживаются")
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	dir := filepath.Join(base.Root, mem.MemDirName)
	if r.Method == http.MethodGet {
		result, err := mem.ReadReadingWorkspaceIn(ctx, dir, actor.ID)
		if err != nil {
			a.readingWorkspaceError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var input mem.ReadingWorkspaceMutation
	if !decodeLimit(w, r, &input, 16<<10) {
		return
	}
	result, err := mem.MutateReadingWorkspaceWithAdmissionIn(ctx, dir, actor.ID, input, func() error {
		return a.checkStorage(ctx, actor, base)
	})
	if err != nil {
		if errors.Is(err, workspace.ErrStorageLimit) {
			a.fail(w, err)
			return
		}
		a.readingWorkspaceError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) readingWorkspaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrReadingWorkspaceInvalid):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrReadingWorkspaceNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, mem.ErrReadingWorkspaceConflict), errors.Is(err, mem.ErrReadingWorkspaceRequest), errors.Is(err, mem.ErrReadingWorkspaceDuplicate), errors.Is(err, mem.ErrReadingWorkspaceStale):
		writeError(w, 409, err.Error())
	case errors.Is(err, mem.ErrReadingWorkspaceLimit):
		writeError(w, 413, err.Error())
	case errors.Is(err, mem.ErrQueryInvalid), errors.Is(err, mem.ErrQueryStale), errors.Is(err, mem.ErrQueryLimit), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.queryError(w, err)
	default:
		writeError(w, 500, "Не удалось открыть или сохранить рабочее место чтения. Обновите его перед новой попыткой.")
	}
}
