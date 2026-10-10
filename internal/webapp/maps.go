package webapp

import (
	"errors"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"mime"
	"net/http"
	"path/filepath"
)

func (a *App) mapsRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	if a.mapAnnotationRoute(w, r, actor, dir, parts) {
		return
	}
	if a.mapViewPresetsRoute(w, r, dir, actor, parts) {
		return
	}
	if a.mapViewStateRoute(w, r, dir, actor, parts) {
		return
	}
	if a.mapImportRoute(w, r, dir, actor, parts) {
		return
	}
	if a.mapAttachmentRoute(w, r, dir, actor.ID, parts) {
		return
	}
	if len(parts) == 6 && parts[5] == "from-sources" && r.Method == "POST" {
		var input mem.WebMapFromSources
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.CreateWebMapFromSourcesIn(r.Context(), dir, actor.ID, input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 202, result)
		return
	}

	if len(parts) == 7 && (parts[6] == "delete" || parts[6] == "restore") && r.Method == "POST" {
		var input mem.WebMapDelete
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		action := mem.DeleteWebMapIn
		if parts[6] == "restore" {
			action = mem.RestoreWebMapIn
		}
		result, e := action(r.Context(), dir, actor.ID, parts[5], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}

	if (len(parts) >= 7 && parts[6] == "ai") || (len(parts) == 9 && parts[6] == "nodes" && parts[8] == "ai") {
		a.mapAIRoute(w, r, actor, dir, parts)
		return
	}
	if len(parts) == 7 && parts[6] == "duplicate" && r.Method == "POST" {
		var input mem.WebMapDuplicate
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.DuplicateWebMapIn(r.Context(), dir, actor.ID, parts[5], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 7 && parts[6] == "compare" && r.Method == "POST" {
		var input mem.WebMapCompareInput
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.CompareWebMapsIn(r.Context(), dir, parts[5], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if a.mapStudyRoute(w, r, actor, dir, parts) {
		return
	}
	if len(parts) == 9 && parts[6] == "nodes" && (parts[8] == "branch" || parts[8] == "ask") && r.Method == "POST" {
		var input mem.WebMapBranchInput
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		if parts[8] == "branch" {
			if input.Question != "" || input.Manifest != "" {
				a.mapError(w, mem.ErrWebMapBranchInvalid)
				return
			}
			result, e := mem.ReadWebMapBranchIn(r.Context(), dir, parts[5], parts[7], input.Revision)
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, result)
		} else {
			result, e := mem.AnswerWebMapBranchIn(r.Context(), dir, parts[5], parts[7], input)
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, result)
		}
		return
	}
	if len(parts) == 11 && parts[6] == "nodes" && parts[8] == "sources" && (parts[10] == "document" || parts[10] == "file") && (r.Method == "GET" || r.Method == "HEAD") {
		revision, e := integerParam(r, "revision", 0, 1, 9007199254740991)
		if e != nil {
			a.mapError(w, mem.ErrWebMapSourceInvalid)
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		if parts[10] == "document" {
			result, e := mem.ReadWebMapDocumentIn(r.Context(), dir, parts[5], parts[7], parts[9], int64(revision))
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, result)
			return
		}
		file, close, e := mem.OpenWebMapDocumentPDFIn(r.Context(), dir, parts[5], parts[7], parts[9], int64(revision), a.cfg.Local)
		if e != nil {
			writeError(w, 409, "Оригинал PDF недоступен или изменён. Повторите импорт документа и обновите привязку источника.")
			return
		}
		defer close()
		info, e := file.Stat()
		if e != nil {
			a.mapError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `inline; filename="source.pdf"`)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'")
		http.ServeContent(w, r, "source.pdf", info.ModTime(), file)
		return
	}
	if len(parts) == 9 && parts[6] == "nodes" && parts[8] == "sources" && r.Method == "GET" {
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		page, e := mem.ListWebMapSourcesIn(r.Context(), dir, parts[5], parts[7])
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if len(parts) == 9 && parts[6] == "nodes" && (parts[8] == "source-add" || parts[8] == "source-remove" || parts[8] == "source-move") && r.Method == "POST" {
		var input mem.WebMapSourceInput
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.ActWebMapSourceIn(r.Context(), dir, actor.ID, parts[5], parts[7], parts[8][7:], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 6 && parts[5] == "candidates" && r.Method == "GET" {
		offset, e := integerParam(r, "offset", 0, 0, 1000000)
		if e != nil {
			a.mapError(w, mem.ErrWebMapSourceInvalid)
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		page, e := mem.ListWebMapCandidatesIn(r.Context(), dir, offset)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if len(parts) == 7 && parts[6] == "metadata" && r.Method == "POST" {
		var input mem.WebMapMetadata
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.EditWebMapMetadataIn(r.Context(), dir, actor.ID, parts[5], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 7 && parts[6] == "export" && r.Method == "POST" {
		var input struct {
			Format   mem.ClassicMindMapExportFormat `json:"format"`
			Revision int64                          `json:"revision"`
		}
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.ExportWebMapIn(r.Context(), dir, parts[5], input.Format, input.Revision, a.cfg.Local)
		if e != nil {
			a.mapError(w, e)
			return
		}
		w.Header().Set("Content-Type", result.MediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Filename}))
		w.WriteHeader(200)
		_, _ = w.Write(result.Data)
		return
	}
	if a.mapSnapshotRoute(w, r, dir, actor.ID, parts) {
		return
	}
	if len(parts) == 7 && parts[6] == "snapshots" {
		if r.Method == "GET" {
			before, e := integerParam(r, "before", 0, 0, 9007199254740991)
			if e != nil {
				a.mapError(w, mem.ErrWebMapActionInvalid)
				return
			}
			page, e := mem.ListWebMapSnapshotsIn(r.Context(), dir, parts[5], int64(before))
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, page)
			return
		}
		if r.Method == "POST" {
			var input mem.WebMapSnapshotInput
			if !decode(w, r, &input) {
				return
			}
			release, ok := a.reserveQuery(w, actor)
			if !ok {
				return
			}
			defer release()
			result, e := mem.CreateWebMapSnapshotIn(r.Context(), dir, actor.ID, parts[5], input)
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, result)
			return
		}
	}
	if len(parts) == 7 {
		if parts[6] == "history" && r.Method == "GET" {
			before, e := integerParam(r, "before", 0, 0, 9007199254740991)
			if e != nil {
				a.mapError(w, mem.ErrWebMapHistoryInvalid)
				return
			}
			page, e := mem.ListWebMapHistoryIn(r.Context(), dir, parts[5], int64(before))
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, page)
			return
		}
		if (parts[6] == "undo" || parts[6] == "redo") && r.Method == "POST" {
			var input mem.WebMapUndo
			if !decode(w, r, &input) {
				return
			}
			release, ok := a.reserveQuery(w, actor)
			if !ok {
				return
			}
			defer release()
			var result mem.WebMapNodeEdited
			var e error
			if parts[6] == "redo" {
				result, e = mem.RedoWebMapIn(r.Context(), dir, actor.ID, parts[5], input)
			} else {
				result, e = mem.UndoWebMapIn(r.Context(), dir, actor.ID, parts[5], input)
			}
			if errors.Is(e, mem.ErrWebMapRedoDiverged) {
				a.mapError(w, e)
				return
			}
			if errors.Is(e, mem.ErrClassicMindMapRevisionConflict) {
				writeError(w, 409, "Карта изменилась. Обновите историю перед действием.")
				return
			}
			if e != nil {
				a.mapError(w, e)
				return
			}
			writeJSON(w, 200, result)
			return
		}
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	if len(parts) == 9 && parts[6] == "nodes" && (parts[8] == "move" || parts[8] == "delete" || parts[8] == "properties") && r.Method == "POST" {
		var input mem.WebMapAction
		if !decode(w, r, &input) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		result, e := mem.ActWebMapNodeIn(r.Context(), dir, actor.ID, parts[5], parts[7], parts[8], input)
		if e != nil {
			a.mapError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 8 || len(parts) == 9 {
		if parts[6] != "nodes" || r.Method != "POST" || (len(parts) == 9 && parts[8] != "children") {
			writeError(w, 405, "Метод не поддерживается")
			return
		}
		var input mem.WebMapNodeEdit
		if !decodeLimit(w, r, &input, 1<<20) {
			return
		}
		release, ok := a.reserveQuery(w, actor)
		if !ok {
			return
		}
		defer release()
		var result mem.WebMapNodeEdited
		if len(parts) == 9 {
			result, err = mem.AddWebMapNodeIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
		} else {
			result, err = mem.EditWebMapNodeIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
		}
		if err != nil {
			a.mapError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if r.Method == "GET" {
		if len(parts) == 6 && parts[5] == "templates" {
			writeJSON(w, 200, mem.ListClassicMindMapTemplates())
			return
		}
		if len(parts) == 6 {
			view, err := mem.ReadWebMapIn(r.Context(), dir, parts[5])
			if err != nil {
				a.mapError(w, err)
				return
			}
			writeJSON(w, 200, view)
			return
		}
		offset, err := integerParam(r, "offset", 0, 0, 1000000)
		if err != nil {
			a.mapError(w, mem.ErrWebMapInvalid)
			return
		}
		archived := r.URL.Query().Get("archived")
		if archived != "" && archived != "true" && archived != "false" {
			a.mapError(w, mem.ErrWebMapInvalid)
			return
		}
		deleted := r.URL.Query().Get("deleted")
		if deleted != "" && deleted != "true" && deleted != "false" {
			a.mapError(w, mem.ErrWebMapInvalid)
			return
		}
		page, err := mem.ListWebMapsFilteredIn(r.Context(), dir, offset, archived == "true", deleted == "true")
		if err != nil {
			a.mapError(w, err)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if len(parts) != 5 || r.Method != "POST" {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	var input mem.WebMapCreate
	if !decode(w, r, &input) {
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	result, err := mem.CreateWebMapIn(r.Context(), dir, actor.ID, input)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *App) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrWebMapSourceOverviewInvalid), errors.Is(err, mem.ErrWebMapSourceOverviewProvenance):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrWebMapSourceOverviewLimit):
		writeError(w, 413, err.Error())
	case errors.Is(err, mem.ErrWebMapAIInvalid):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrWebMapAIChanged), errors.Is(err, mem.ErrClassicMindMapAIChanged), errors.Is(err, mem.ErrClassicMindMapAIAlreadyApplied):
		writeError(w, 409, mem.ErrWebMapAIChanged.Error())
	case errors.Is(err, mem.ErrClassicMindMapAIPreviewNotFound):
		writeError(w, 404, "Предложение не найдено")
	case errors.Is(err, mem.ErrWebMapAILimit), errors.Is(err, mem.ErrClassicMindMapAIResourceLimit):
		writeError(w, 413, mem.ErrWebMapAILimit.Error())
	case errors.Is(err, mem.ErrClassicMindMapAINoChanges):
		writeError(w, 409, "Выбранные текст или источники уже есть в узле. Карта не изменена.")
	case errors.Is(err, mem.ErrWebMapBranchInvalid), errors.Is(err, mem.ErrWebMapBranchEmpty):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrWebMapBranchChanged):
		writeError(w, 409, err.Error())
	case errors.Is(err, mem.ErrQueryAnswer):
		writeError(w, 502, "Модель не вернула подтверждённый ответ. Проверьте настройки модели и наличие ссылок на фрагменты в её ответе.")
	case errors.Is(err, mem.ErrWebMapSourceInvalid):
		writeError(w, 400, mem.ErrWebMapSourceInvalid.Error())
	case errors.Is(err, mem.ErrWebMapSourceConflict), errors.Is(err, mem.ErrQueryStale):
		writeError(w, 409, mem.ErrWebMapSourceConflict.Error())
	case errors.Is(err, mem.ErrQueryLimit):
		writeError(w, 413, "Фрагменты слишком велики для веб-просмотра.")
	case errors.Is(err, mem.ErrClassicMindMapPNGTooLarge):
		writeError(w, 413, "Карта слишком велика для PNG. Выберите SVG или HTML — они сохраняют всю карту.")
	case errors.Is(err, mem.ErrWebMapActionInvalid), errors.Is(err, mem.ErrWebMapStructure):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrWebMapRedoDiverged):
		writeError(w, 409, "После отмены появились другие изменения. Сначала отмените более новые действия или продолжите работу с текущей картой.")
	case errors.Is(err, mem.ErrWebMapHistoryInvalid):
		writeError(w, 400, mem.ErrWebMapHistoryInvalid.Error())
	case errors.Is(err, mem.ErrWebMapRedoUnavailable):
		writeError(w, 409, mem.ErrWebMapRedoUnavailable.Error())
	case errors.Is(err, mem.ErrWebMapUndoUnavailable):
		writeError(w, 409, mem.ErrWebMapUndoUnavailable.Error())
	case errors.Is(err, mem.ErrWebMapNodeInvalid):
		writeError(w, 400, mem.ErrWebMapNodeInvalid.Error())
	case errors.Is(err, mem.ErrClassicMindMapRevisionConflict):
		writeError(w, 409, "Карта изменилась. Ваш текст остался в форме. Закройте её и обновите карту перед новой правкой.")
	case errors.Is(err, mem.ErrClassicMindMapLocked):
		writeError(w, 409, "Узел заблокирован. Разблокируйте его в свойствах узла и повторите действие.")
	case errors.Is(err, mem.ErrClassicMindMapNodeNotFound):
		writeError(w, 404, "Узел не найден. Обновите карту.")
	case errors.Is(err, mem.ErrWebMapEditLimit):
		writeError(w, 413, mem.ErrWebMapEditLimit.Error())
	case errors.Is(err, mem.ErrWebMapEditRequest):
		writeError(w, 409, mem.ErrWebMapEditRequest.Error())
	case errors.Is(err, mem.ErrWebMapInvalid):
		writeError(w, 400, mem.ErrWebMapInvalid.Error())
	case errors.Is(err, mem.ErrClassicMindMapNotFound):
		writeError(w, 404, "Карта не найдена. Обновите библиотеку.")
	case errors.Is(err, mem.ErrWebMapLimit):
		writeError(w, 413, mem.ErrWebMapLimit.Error())
	case errors.Is(err, mem.ErrClassicMindMapRenderLimit):
		writeError(w, 409, "Структура карты не подходит для просмотра. Данные не изменены; проверьте карту в прежнем редакторе.")
	case errors.Is(err, mem.ErrWebMapRequest):
		writeError(w, 409, mem.ErrWebMapRequest.Error())
	default:
		writeError(w, 500, "Не удалось выполнить действие с картой. Проверьте доступность базы и обновите библиотеку перед новой попыткой.")
	}
}
