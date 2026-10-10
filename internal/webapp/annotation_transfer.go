package webapp

import (
	"context"
	"net/http"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) annotationTransferRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 6 || (parts[5] != "export" && parts[5] != "import-preview" && parts[5] != "import") {
		return false
	}
	if r.Method != http.MethodPost {
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
	switch parts[5] {
	case "export":
		var input mem.DocumentAnnotationExportRequest
		if !decodeLimit(w, r, &input, 16<<10) {
			return true
		}
		result, err := mem.ExportDocumentAnnotationsIn(ctx, dir, input)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"filename": result.Filename, "media_type": result.MediaType, "content": string(result.Data), "count": result.Count, "digest": result.Digest})
	case "import-preview":
		var input mem.DocumentAnnotationImportPreviewInput
		if !decodeLimit(w, r, &input, 20<<20) {
			return true
		}
		result, err := mem.PreviewDocumentAnnotationImportIn(ctx, dir, input)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
	case "import":
		var input mem.DocumentAnnotationImportInput
		if !decodeLimit(w, r, &input, 20<<20) {
			return true
		}
		result, err := mem.ImportDocumentAnnotationsIn(ctx, dir, actor.ID, input)
		if err != nil {
			a.annotationError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
	}
	return true
}
