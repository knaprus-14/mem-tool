package webapp

import (
	"errors"
	"mime"
	"net/http"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapImportRoute(w http.ResponseWriter, r *http.Request, dir string, actor workspace.Actor, parts []string) bool {
	if len(parts) == 7 && parts[6] == "import-source" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "Ожидается GET")
			return true
		}
		if r.URL.RawQuery != "" {
			writeError(w, 400, "Параметры запроса не поддерживаются")
			return true
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return true
		}
		defer release()
		source, err := mem.ReadWebMapImportSourceIn(r.Context(), dir, parts[5])
		if err != nil {
			a.outlineImportError(w, err)
			return true
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": source.Filename}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(source.Data)
		return true
	}
	if len(parts) < 6 || parts[5] != "import" {
		return false
	}
	if len(parts) != 7 || (parts[6] != "preview" && parts[6] != "apply" && parts[6] != "outline-preview" && parts[6] != "outline-apply") {
		http.NotFound(w, r)
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "Ожидается POST")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	if r.URL.RawQuery != "" {
		writeError(w, 400, "Параметры запроса не поддерживаются")
		return true
	}
	var result any
	var err error
	if parts[6] == "outline-preview" {
		var input mem.OutlineImportInput
		if !decodeLimit(w, r, &input, 2*mem.MaxOutlineImportBytes+4096) {
			return true
		}
		result, err = mem.PreviewMindMapOutline(input)
	} else if parts[6] == "outline-apply" {
		var input mem.OutlineImportApplyInput
		if !decodeLimit(w, r, &input, 2*mem.MaxOutlineImportBytes+4096) {
			return true
		}
		result, err = mem.ImportWebMapOutlineIn(r.Context(), dir, actor.ID, input, func() error {
			base, e := a.manager.Resolve(r.Context(), actor, parts[3])
			if e != nil {
				return e
			}
			return a.checkStorage(r.Context(), actor, base)
		})
	} else if parts[6] == "preview" {
		var input mem.WebMapImportPreviewInput
		if !decodeLimit(w, r, &input, 2*mem.MaxClassicMindMapImportBytes+4096) {
			return true
		}
		result, err = mem.PreviewWebMapImportIn(r.Context(), dir, input)
	} else {
		var input mem.WebMapImportInput
		if !decodeLimit(w, r, &input, 2*mem.MaxClassicMindMapImportBytes+4096) {
			return true
		}
		result, err = mem.ImportWebMapIn(r.Context(), dir, actor.ID, input)
	}
	if err != nil {
		if parts[6] == "outline-preview" || parts[6] == "outline-apply" {
			a.outlineImportError(w, err)
			return true
		}
		switch {
		case errors.Is(err, mem.ErrClassicMindMapImportInvalid), errors.Is(err, mem.ErrClassicMindMapImportFile):
			writeError(w, 400, err.Error())
		case errors.Is(err, mem.ErrClassicMindMapImportPreview):
			writeError(w, 409, err.Error())
		default:
			a.mapError(w, err)
		}
		return true
	}
	writeJSON(w, 200, result)
	return true
}

func (a *App) outlineImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrStorageLimit):
		a.fail(w, err)
	case errors.Is(err, mem.ErrOutlineImportInvalid):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrOutlineImportLimit):
		writeError(w, 413, err.Error())
	case errors.Is(err, mem.ErrOutlineImportSourceNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, mem.ErrClassicMindMapImportPreview):
		writeError(w, 409, err.Error())
	default:
		a.mapError(w, err)
	}
}
