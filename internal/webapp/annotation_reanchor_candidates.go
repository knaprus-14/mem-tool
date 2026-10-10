package webapp

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

// Called only after annotationsRoute resolves workspace ownership. GET remains
// available at the storage quota and shares the bounded query admission gate.
func (a *App) annotationReanchorCandidatesRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 7 || parts[6] != "reanchor-candidates" {
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "Метод не поддерживается")
		return true
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.annotationError(w, mem.ErrDocumentAnnotationReanchorInvalid)
		return true
	}
	for key, values := range params {
		if (key != "revision" && key != "limit") || len(values) != 1 || values[0] == "" {
			a.annotationError(w, mem.ErrDocumentAnnotationReanchorInvalid)
			return true
		}
	}
	revision, err := strconv.ParseInt(params.Get("revision"), 10, 64)
	if err != nil || revision < 1 {
		a.annotationError(w, mem.ErrDocumentAnnotationReanchorInvalid)
		return true
	}
	limit := 20
	if value, exists := params["limit"]; exists {
		limit, err = strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > mem.MaxDocumentAnnotationCandidates {
			a.annotationError(w, mem.ErrDocumentAnnotationReanchorInvalid)
			return true
		}
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	result, err := mem.FindDocumentAnnotationReanchorCandidatesIn(r.Context(), dir, parts[5], mem.DocumentAnnotationCandidateQuery{Revision: revision, Limit: limit})
	if err != nil {
		a.annotationError(w, err)
		return true
	}
	writeJSON(w, http.StatusOK, result)
	return true
}
