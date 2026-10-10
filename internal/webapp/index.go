package webapp

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) indexRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id, action string) {
	base, err := a.manager.Resolve(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	var result mem.WebIndexState
	if r.Method == "GET" && action == "" {
		result, err = mem.ReadWebIndexIn(r.Context(), dir, actor.ID)
	} else if r.Method == "POST" && action != "" {
		var input mem.WebIndexInput
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		if action == "start" {
			result, err = mem.StartWebIndexIn(r.Context(), dir, actor.ID, input)
		} else {
			result, err = mem.ChangeWebIndexIn(r.Context(), dir, actor.ID, action, input)
		}
	} else {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, mem.ErrWebIndexInvalid):
			writeError(w, 400, err.Error())
		case errors.Is(err, mem.ErrWebIndexConflict), errors.Is(err, filelock.ErrBusy):
			writeError(w, 409, mem.ErrWebIndexConflict.Error())
		case errors.Is(err, mem.ErrWebIndexLimit):
			writeError(w, 413, err.Error())
		default:
			writeError(w, 500, "Не удалось изменить пересчёт. Обновите его состояние перед повтором.")
		}
		return
	}
	writeJSON(w, 200, result)
}
