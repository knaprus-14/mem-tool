package webapp

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapSnapshotRoute(w http.ResponseWriter, r *http.Request, dir, actor string, parts []string) bool {
	if len(parts) < 8 || parts[6] != "snapshots" {
		return false
	}
	id, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil || id <= 0 || id > 9007199254740991 {
		a.mapError(w, mem.ErrWebMapHistoryInvalid)
		return true
	}
	preview := len(parts) == 8 && r.Method == http.MethodGet
	restore := len(parts) == 9 && parts[8] == "restore" && r.Method == http.MethodPost
	if !preview && !restore {
		writeError(w, 405, "Метод не поддерживается")
		return true
	}
	release, ok := a.reserveQuery(w, workspace.Actor{ID: actor})
	if !ok {
		return true
	}
	defer release()
	var result any
	if preview {
		revision, e := integerParam(r, "revision", 0, 1, 9007199254740991)
		if e != nil {
			a.mapError(w, mem.ErrWebMapHistoryInvalid)
			return true
		}
		result, err = mem.ReadWebMapSnapshotIn(r.Context(), dir, parts[5], id, int64(revision))
	} else {
		var input mem.WebMapSnapshotRestore
		if !decode(w, r, &input) {
			return true
		}
		result, err = mem.RestoreWebMapSnapshotIn(r.Context(), dir, actor, parts[5], id, input)
	}
	switch {
	case errors.Is(err, mem.ErrWebMapSnapshotNotFound):
		writeError(w, 404, mem.ErrWebMapSnapshotNotFound.Error())
	case errors.Is(err, mem.ErrWebMapSnapshotInvalid):
		writeError(w, 409, mem.ErrWebMapSnapshotInvalid.Error())
	case errors.Is(err, mem.ErrWebMapSnapshotChanged):
		writeError(w, 409, mem.ErrWebMapSnapshotChanged.Error())
	case err != nil:
		a.mapError(w, err)
	default:
		writeJSON(w, 200, result)
	}
	return true
}
