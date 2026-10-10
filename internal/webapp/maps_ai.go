package webapp

import (
	"net/http"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapAIRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) {
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	if len(parts) == 9 && parts[6] == "nodes" && parts[8] == "ai" && r.Method == "POST" {
		var input mem.WebMapAIInput
		if !decodeLimit(w, r, &input, 64<<10) {
			return
		}
		result, err := mem.StartWebMapAIIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
		if err != nil {
			a.mapError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 7 && r.Method == "GET" {
		result, err := mem.ListWebMapAIIn(r.Context(), dir, actor.ID, parts[5])
		if err != nil {
			a.mapError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 8 && r.Method == "GET" {
		result, err := mem.ReadWebMapAIReviewIn(r.Context(), dir, actor.ID, parts[5], parts[7])
		if err != nil {
			a.mapError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 9 && r.Method == "POST" {
		switch parts[8] {
		case "reassemble", "resume":
			var input struct{}
			if !decode(w, r, &input) {
				return
			}
			restart := mem.ReassembleWebMapAIIn
			if parts[8] == "resume" {
				restart = mem.ResumeWebMapAIIn
			}
			result, err := restart(r.Context(), dir, actor.ID, parts[5], parts[7])
			if err != nil {
				a.mapError(w, err)
				return
			}
			writeJSON(w, 202, result)
			return
		case "cancel":
			var input struct{}
			if !decode(w, r, &input) {
				return
			}
			if err := mem.CancelWebMapAIIn(r.Context(), dir, actor.ID, parts[5], parts[7]); err != nil {
				a.mapError(w, err)
				return
			}
			writeJSON(w, 200, map[string]bool{"cancelled": true})
			return
		case "apply":
			var input mem.WebMapAIApply
			if !decode(w, r, &input) {
				return
			}
			result, err := mem.ApplyWebMapAIIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
			if err != nil {
				a.mapError(w, err)
				return
			}
			writeJSON(w, 200, result)
			return
		}
	}
	writeError(w, 405, "Метод не поддерживается")
}
