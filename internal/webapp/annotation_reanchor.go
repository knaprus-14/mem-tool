package webapp

import (
	"context"
	"net/http"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) annotationReanchorRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 7 || (parts[6] != "reanchor-preview" && parts[6] != "reanchor" && parts[6] != "lineage") {
		return false
	}
	if (parts[6] == "lineage" && r.Method != http.MethodGet) || (parts[6] != "lineage" && r.Method != http.MethodPost) {
		writeError(w, http.StatusMethodNotAllowed, "Метод не поддерживается")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	switch parts[6] {
	case "lineage":
		offset, err := integerParam(r, "offset", 0, 0, 1000000)
		if err != nil {
			a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
			return true
		}
		limit, err := integerParam(r, "limit", 30, 1, 50)
		if err != nil {
			a.annotationError(w, mem.ErrDocumentAnnotationInvalid)
			return true
		}
		result, err := mem.ListDocumentAnnotationLineageIn(ctx, dir, parts[5], offset, limit)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	case "reanchor-preview":
		var input mem.DocumentAnnotationReanchorPreviewInput
		if !decodeLimit(w, r, &input, 256<<10) {
			return true
		}
		result, err := mem.PreviewDocumentAnnotationReanchorIn(ctx, dir, parts[5], input)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	case "reanchor":
		var input mem.DocumentAnnotationReanchorInput
		if !decodeLimit(w, r, &input, 256<<10) {
			return true
		}
		result, err := mem.ReanchorDocumentAnnotationIn(ctx, dir, actor.ID, parts[5], input)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	}
	return true
}
