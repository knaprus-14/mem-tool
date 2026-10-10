package webapp

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) annotationRegionsRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 7 || (parts[6] != "regions" && parts[6] != "page") {
		return false
	}
	if r.Method != "GET" && (parts[6] != "regions" || r.Method != "POST") {
		writeError(w, 405, "Метод не поддерживается")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	if parts[6] == "page" {
		revision, err := integerParam(r, "revision", 0, 1, 1000000000)
		if err != nil {
			a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
			return true
		}
		page, err := mem.RenderDocumentAnnotationPageIn(r.Context(), dir, parts[5], int64(revision), a.cfg.Local)
		if err != nil {
			if errors.Is(err, mem.ErrDocumentAnnotationPreviewBusy) {
				writeError(w, 429, err.Error())
				return true
			}
			if errors.Is(err, mem.ErrDocumentAnnotationNotFound) || errors.Is(err, mem.ErrDocumentAnnotationConflict) || errors.Is(err, mem.ErrDocumentAnnotationInvalid) || errors.Is(err, mem.ErrDocumentAnnotationStale) {
				a.annotationError(w, err)
				return true
			}
			if errors.Is(err, mem.ErrSourceFileChanged) || errors.Is(err, mem.ErrSourceFileUnverified) {
				writeError(w, 409, "Оригинал PDF недоступен или изменён. Сохранённая цитата и координаты остаются в базе.")
				return true
			}
			writeError(w, 422, "Страница PDF недоступна. Проверьте исходный файл и настройки pdftoppm, mutool или Python с PyMuPDF. Сохранённая цитата остаётся доступной.")
			return true
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Mem-Source-Hash", page.FileHash)
		w.Header().Set("X-Mem-Physical-Page", strconv.Itoa(page.Page))
		w.Write(page.PNG)
		return true
	}
	if r.Method == "GET" {
		result, err := mem.ListDocumentAnnotationRegionsIn(r.Context(), dir, parts[5])
		if err != nil {
			a.annotationError(w, err)
		} else {
			writeJSON(w, 200, result)
		}
		return true
	}
	var input mem.DocumentAnnotationRegionCreate
	if !decodeLimit(w, r, &input, 16<<10) {
		return true
	}
	result, err := mem.CreateDocumentAnnotationRegionIn(r.Context(), dir, actor.ID, parts[5], input, a.cfg.Local)
	if err != nil {
		if errors.Is(err, mem.ErrSourceFileChanged) || errors.Is(err, mem.ErrSourceFileUnverified) {
			writeError(w, 409, "Оригинал PDF недоступен или изменён. Область не сохранена.")
			return true
		}
		a.annotationError(w, err)
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
