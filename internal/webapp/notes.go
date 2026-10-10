package webapp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) notesRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	if len(parts) == 6 && parts[5] == "actions" && r.Method == "POST" {
		var input mem.NoteAction
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, err := mem.ApplyNoteActionIn(r.Context(), dir, input)
		if err != nil {
			a.noteError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if r.Method == "GET" && (len(parts) == 6 && parts[5] == "trash" || len(parts) == 7 && parts[6] == "history" || len(parts) == 8 && parts[6] == "history") {
		var id int64
		if parts[5] != "trash" {
			id, err = strconv.ParseInt(parts[5], 10, 64)
			if err != nil || id < 1 {
				a.noteError(w, mem.ErrNoteInvalid)
				return
			}
		}
		if len(parts) == 8 {
			version, e := strconv.ParseInt(parts[7], 10, 64)
			if e != nil || version < 1 {
				a.noteError(w, mem.ErrNoteInvalid)
				return
			}
			view, e := mem.ReadNoteVersionIn(r.Context(), dir, id, version)
			if e != nil {
				a.noteError(w, e)
				return
			}
			writeJSON(w, 200, view)
			return
		}
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
			if err != nil {
				a.noteError(w, mem.ErrNoteInvalid)
				return
			}
		}
		page, e := mem.ListNoteHistoryIn(r.Context(), dir, id, offset)
		if e != nil {
			a.noteError(w, e)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if len(parts) == 6 && r.Method == "GET" {
		id, err := strconv.ParseInt(parts[5], 10, 64)
		if err != nil || id < 1 {
			a.noteError(w, mem.ErrNoteInvalid)
			return
		}
		view, err := mem.ReadNoteIn(r.Context(), dir, id)
		if err != nil {
			a.noteError(w, err)
			return
		}
		writeJSON(w, 200, view)
		return
	}
	if len(parts) != 5 || r.Method != "POST" {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	var input mem.NoteInput
	if !decode(w, r, &input) {
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	result, err := mem.SaveNoteIn(r.Context(), dir, input)
	if err != nil {
		a.noteError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) noteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrNoteInvalid):
		writeError(w, 400, mem.ErrNoteInvalid.Error())
	case errors.Is(err, mem.ErrNoteNotFound):
		writeError(w, 404, mem.ErrNoteNotFound.Error())
	case errors.Is(err, mem.ErrNoteConflict):
		writeError(w, 409, mem.ErrNoteConflict.Error())
	case errors.Is(err, mem.ErrNoteRequest):
		writeError(w, 409, mem.ErrNoteRequest.Error())
	case errors.Is(err, mem.ErrNoteLimit):
		writeError(w, 413, mem.ErrNoteLimit.Error())
	case errors.Is(err, mem.ErrNoteHistoryLimit):
		writeError(w, 413, mem.ErrNoteHistoryLimit.Error())
	case errors.Is(err, mem.ErrNoteSnapshot):
		writeError(w, 409, mem.ErrNoteSnapshot.Error())
	case errors.Is(err, mem.ErrNoteReadLimit):
		writeError(w, 413, mem.ErrNoteReadLimit.Error())
	case errors.Is(err, mem.ErrQueryEmbedding):
		writeError(w, 502, "Модель поиска недоступна. Заметка не сохранена; проверьте модель и повторите попытку.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.queryError(w, err)
	default:
		writeError(w, 500, "Не удалось выполнить операцию с заметкой. Проверьте доступность базы; перед новой попыткой обновите список.")
	}
}
