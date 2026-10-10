package webapp

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) annotationSourceRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 7 || (parts[5] != "entry" && parts[6] != "source" && parts[6] != "file" && parts[6] != "links") {
		return false
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "Ожидается GET")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	if parts[5] == "entry" {
		id, err := strconv.ParseInt(parts[6], 10, 64)
		if err != nil || id <= 0 {
			a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
			return true
		}
		item, err := mem.ReadDocumentAnnotationEntryIn(r.Context(), dir, id, r.URL.Query().Get("pin"))
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, item)
		return true
	}
	if parts[6] == "source" {
		item, err := mem.ReadDocumentAnnotationSourceIn(r.Context(), dir, parts[5])
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, item)
		return true
	}
	if parts[6] == "links" {
		item, err := mem.ListDocumentAnnotationMapLinksIn(r.Context(), dir, parts[5])
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, item)
		return true
	}
	revision, err := integerParam(r, "revision", 0, 1, 1000000000)
	if err != nil {
		a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
		return true
	}
	file, closeFile, err := mem.OpenDocumentAnnotationPDFIn(r.Context(), dir, parts[5], int64(revision), a.cfg.Local)
	if err != nil {
		writeError(w, 409, "Оригинал PDF недоступен или изменён. Сохранённая цитата доступна в аннотации.")
		return true
	}
	defer closeFile()
	info, err := file.Stat()
	if err != nil {
		writeError(w, 409, "Оригинал PDF недоступен")
		return true
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="source.pdf"`)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'")
	http.ServeContent(w, r, "source.pdf", info.ModTime(), file)
	return true
}

func (a *App) mapAnnotationRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 9 || parts[6] != "nodes" || parts[8] != "annotations" {
		return false
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "Ожидается POST")
		return true
	}
	var input mem.WebMapAnnotationAttach
	if !decode(w, r, &input) {
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	result, err := mem.AttachDocumentAnnotationToWebMapIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
	if err != nil {
		if errors.Is(err, mem.ErrDocumentAnnotationAlreadyAttached) || errors.Is(err, mem.ErrDocumentAnnotationInvalid) || errors.Is(err, mem.ErrDocumentAnnotationNotFound) || errors.Is(err, mem.ErrDocumentAnnotationConflict) || errors.Is(err, mem.ErrDocumentAnnotationStale) || errors.Is(err, mem.ErrDocumentAnnotationLimit) {
			a.annotationError(w, err)
		} else {
			a.mapError(w, err)
		}
		return true
	}
	writeJSON(w, 200, result)
	return true
}
