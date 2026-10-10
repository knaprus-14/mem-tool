package webapp

import (
	"net/http"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapStudyRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, dir string, parts []string) bool {
	if len(parts) != 9 || parts[6] != "nodes" || (parts[8] != "study" && parts[8] != "study-review" && parts[8] != "study-history") {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "Используйте POST")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	if parts[8] == "study" {
		var input struct {
			Revision int64 `json:"revision"`
		}
		if !decode(w, r, &input) {
			return true
		}
		result, err := mem.ReadWebMapStudyIn(r.Context(), dir, actor.ID, parts[5], parts[7], input.Revision)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	} else if parts[8] == "study-history" {
		var input struct {
			Revision int64  `json:"revision"`
			CardID   string `json:"card_id"`
		}
		if !decode(w, r, &input) {
			return true
		}
		result, err := mem.ReadWebMapStudyHistoryIn(r.Context(), dir, actor.ID, parts[5], parts[7], input.Revision, input.CardID)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	} else {
		var input mem.WebMapStudyReview
		if !decode(w, r, &input) {
			return true
		}
		result, err := mem.ReviewWebMapStudyIn(r.Context(), dir, actor.ID, parts[5], parts[7], input)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, result)
	}
	return true
}
