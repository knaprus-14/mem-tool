package webapp

import (
	"errors"
	"net/http"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) annotationKnowledgeRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 7 || (parts[6] != "knowledge" && parts[5] != "knowledge-node") {
		return false
	}
	reverse := parts[5] == "knowledge-node"
	if r.Method != "GET" && (reverse || r.Method != "POST") {
		w.Header().Set("Allow", "GET")
		if !reverse {
			w.Header().Set("Allow", "GET, POST")
		}
		writeError(w, 405, "Метод не поддерживается")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	if r.Method == "POST" {
		var input mem.DocumentAnnotationKnowledgeCreate
		if !decodeLimit(w, r, &input, 256<<10) {
			return true
		}
		result, err := mem.CreateDocumentAnnotationKnowledgeNodeIn(r.Context(), dir, actor.ID, parts[5], input)
		if err != nil {
			a.annotationKnowledgeError(w, err)
		} else {
			writeJSON(w, 200, result)
		}
		return true
	}
	offset, err := integerParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		a.annotationKnowledgeError(w, mem.ErrDocumentAnnotationInvalid)
		return true
	}
	limit, err := integerParam(r, "limit", 50, 1, 50)
	if err != nil {
		a.annotationKnowledgeError(w, mem.ErrDocumentAnnotationInvalid)
		return true
	}
	var result mem.DocumentAnnotationKnowledgePage
	if reverse {
		result, err = mem.ListKnowledgeNodeDocumentAnnotationsIn(r.Context(), dir, parts[6], offset, limit)
	} else {
		result, err = mem.ListDocumentAnnotationKnowledgeLinksIn(r.Context(), dir, parts[5], offset, limit)
	}
	if err != nil {
		a.annotationKnowledgeError(w, err)
	} else {
		writeJSON(w, 200, result)
	}
	return true
}

func (a *App) annotationKnowledgeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrDocumentAnnotationKnowledgeInvalid):
		writeError(w, 400, mem.ErrDocumentAnnotationKnowledgeInvalid.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationKnowledgeLimit):
		writeError(w, 413, mem.ErrDocumentAnnotationKnowledgeLimit.Error())
	case errors.Is(err, mem.ErrDocumentAnnotationKnowledgeUnavailable):
		writeError(w, 404, mem.ErrDocumentAnnotationKnowledgeUnavailable.Error())
	default:
		a.annotationError(w, err)
	}
}
