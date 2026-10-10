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

func (a *App) annotationsRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	if a.annotationKnowledgeRoute(w, r, actor, dir, parts) {
		return
	}
	if a.annotationReanchorCandidatesRoute(w, r, actor, dir, parts) {
		return
	}
	if a.annotationReanchorRoute(w, r, actor, dir, parts) {
		return
	}
	if a.annotationTransferRoute(w, r, actor, dir, parts) {
		return
	}
	if a.annotationRegionsRoute(w, r, actor, dir, parts) {
		return
	}
	if a.annotationSourceRoute(w, r, actor, dir, parts) {
		return
	}
	collection := len(parts) == 5
	item := len(parts) == 6
	edit := len(parts) == 7 && parts[6] == "edit"
	history := len(parts) == 7 && parts[6] == "history"
	if !collection && !item && !edit && !history {
		writeError(w, http.StatusNotFound, "Маршрут аннотации не найден")
		return
	}
	if !(collection && (r.Method == "GET" || r.Method == "POST") || (item || history) && r.Method == "GET" || edit && r.Method == "POST") {
		writeError(w, http.StatusMethodNotAllowed, "Метод не поддерживается")
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if collection && r.Method == "POST" {
		var input mem.DocumentAnnotationCreate
		if !decodeLimit(w, r, &input, 256<<10) {
			return
		}
		result, err := mem.CreateDocumentAnnotationIn(ctx, dir, actor.ID, input)
		if err != nil {
			a.annotationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if edit {
		var input mem.DocumentAnnotationEdit
		if !decodeLimit(w, r, &input, 256<<10) {
			return
		}
		result, err := mem.UpdateDocumentAnnotationIn(ctx, dir, actor.ID, parts[5], input)
		if err != nil {
			a.annotationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if item {
		result, err := mem.ReadDocumentAnnotationIn(ctx, dir, parts[5])
		if err != nil {
			a.annotationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	offset, err := integerParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
		return
	}
	limit, err := integerParam(r, "limit", 50, 1, 50)
	if err != nil {
		a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
		return
	}
	if history {
		result, err := mem.ReadDocumentAnnotationHistoryIn(ctx, dir, parts[5], offset, limit)
		if err != nil {
			a.annotationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	archived := r.URL.Query().Get("archived")
	if archived != "" && archived != "true" && archived != "false" {
		a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
		return
	}
	result, err := mem.ListDocumentAnnotationsIn(ctx, dir, mem.DocumentAnnotationQuery{
		DocumentID: r.URL.Query().Get("document_id"), Archived: archived == "true", Offset: offset, Limit: limit,
	})
	if err != nil {
		a.annotationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) annotationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrDocumentAnnotationReanchorInvalid):
		writeError(w, http.StatusBadRequest, mem.ErrDocumentAnnotationReanchorInvalid.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationReanchorPreview):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationReanchorPreview.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationTransferInvalid):
		writeError(w, http.StatusBadRequest, mem.ErrDocumentAnnotationTransferInvalid.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationTransferPreview):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationTransferPreview.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationAlreadyAttached):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationAlreadyAttached.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationInvalid):
		writeError(w, http.StatusBadRequest, mem.ErrDocumentAnnotationInvalid.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationNotFound):
		writeError(w, http.StatusNotFound, mem.ErrDocumentAnnotationNotFound.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationConflict):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationConflict.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationRequest):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationRequest.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationStale):
		writeError(w, http.StatusConflict, mem.ErrDocumentAnnotationStale.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationLimit):
		writeError(w, http.StatusRequestEntityTooLarge, mem.ErrDocumentAnnotationLimit.Error())
	case errors.Is(err, mem.ErrQueryInvalid), errors.Is(err, mem.ErrQueryStale), errors.Is(err, mem.ErrQueryLimit), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.queryError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, "Не удалось выполнить операцию с аннотацией. Проверьте доступность базы и обновите список перед новой попыткой.")
	}
}
