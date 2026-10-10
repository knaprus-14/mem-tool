package webapp

import (
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"net/http"
	"path/filepath"
	"strconv"
)

func (a *App) entryMetadataRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	id, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil || id < 1 {
		a.noteError(w, mem.ErrNoteInvalid)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	switch r.Method {
	case "GET":
		out, e := mem.ReadEntryMetadataIn(r.Context(), dir, id)
		if e != nil {
			a.noteError(w, e)
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var in mem.NoteInput
		if !decode(w, r, &in) {
			return
		}
		if in.ID != id {
			a.noteError(w, mem.ErrNoteInvalid)
			return
		}
		out, e := mem.SaveEntryMetadataIn(r.Context(), dir, actor.ID, in)
		if e != nil {
			a.noteError(w, e)
			return
		}
		writeJSON(w, 200, out)
	default:
		writeError(w, 405, "Метод не поддерживается")
	}
}
