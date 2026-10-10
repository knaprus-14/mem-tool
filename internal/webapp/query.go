package webapp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) queryRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	if r.Method != "POST" {
		writeError(w, 405, "Используйте POST")
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	dir := filepath.Join(base.Root, ".mem")
	if parts[4] == "evidence" {
		var body struct {
			ID  int64  `json:"id"`
			Pin string `json:"pin"`
		}
		if !decode(w, r, &body) {
			return
		}
		hit, e := mem.ReadQueryEvidenceIn(ctx, dir, body.ID, body.Pin)
		if e != nil {
			a.queryError(w, e)
			return
		}
		writeJSON(w, 200, hit)
		return
	}
	var body mem.QueryRequest
	if !decode(w, r, &body) {
		return
	}
	result, err := mem.QueryIn(ctx, dir, body, parts[4] == "ask")
	if err != nil {
		if errors.Is(err, mem.ErrQueryAnswer) && len(result.Hits) > 0 {
			result.Status = "unavailable"
			result.Answer = mem.ErrQueryAnswer.Error()
			writeJSON(w, 200, result)
			return
		}
		a.queryError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// Search, answers and note embeddings share the same per-user/process budget.
func (a *App) reserveQuery(w http.ResponseWriter, actor workspace.Actor) (func(), bool) {
	a.mu.Lock()
	busy := a.queryBusy[actor.ID]
	if !busy {
		a.queryBusy[actor.ID] = true
	}
	a.mu.Unlock()
	if busy {
		writeError(w, 429, "Предыдущий запрос ещё выполняется. Дождитесь результата или отмените его.")
		return nil, false
	}
	releaseUser := func() { a.mu.Lock(); delete(a.queryBusy, actor.ID); a.mu.Unlock() }
	select {
	case a.queries <- struct{}{}:
		return func() { <-a.queries; releaseUser() }, true
	default:
		releaseUser()
		writeError(w, 429, "Сервис занят. Повторите запрос через несколько секунд.")
		return nil, false
	}
}

func (a *App) queryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mem.ErrQueryInvalid):
		writeError(w, 400, "Проверьте запрос (до 2000 символов), режим, даты, страницы и число результатов (1–50).")
	case errors.Is(err, mem.ErrQueryLimit):
		writeError(w, 413, mem.ErrQueryLimit.Error())
	case errors.Is(err, mem.ErrQueryStale):
		writeError(w, 409, mem.ErrQueryStale.Error())
	case errors.Is(err, mem.ErrQueryEmbedding):
		writeError(w, 502, mem.ErrQueryEmbedding.Error())
	case errors.Is(err, mem.ErrQueryAnswer):
		writeError(w, 502, mem.ErrQueryAnswer.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, 408, "Запрос отменён.")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, 504, "Превышено время ожидания. Уточните запрос или проверьте доступность модели.")
	default:
		writeError(w, 500, "Не удалось прочитать базу для поиска. Проверьте её доступность и совместимость версии.")
	}
}
