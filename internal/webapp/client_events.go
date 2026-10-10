package webapp

import (
	"net/http"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
)

func (a *App) clientEvent(w http.ResponseWriter, r *http.Request, actor string) {
	var event struct {
		Kind   string `json:"kind"`
		Asset  string `json:"asset"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	if !decodeLimit(w, r, &event, 1024) {
		return
	}
	if event.Kind != "error" && event.Kind != "rejection" && event.Kind != "action_error" {
		writeError(w, 400, "Некорректное событие")
		return
	}
	if event.Line < 0 || event.Line > 10000000 || event.Column < 0 || event.Column > 10000000 {
		writeError(w, 400, "Некорректная позиция")
		return
	}
	if event.Asset != "" {
		if strings.ContainsAny(event.Asset, "/\\") || !strings.HasSuffix(event.Asset, ".js") {
			writeError(w, 400, "Неизвестный скрипт")
			return
		}
		if _, err := assets.ReadFile("assets/" + event.Asset); err != nil {
			writeError(w, 400, "Неизвестный скрипт")
			return
		}
	}
	a.mu.Lock()
	if a.clientLogs == nil {
		a.clientLogs = map[string]time.Time{}
	}
	last := a.clientLogs[actor]
	allowed := time.Since(last) >= time.Second
	if allowed {
		a.clientLogs[actor] = time.Now()
	}
	a.mu.Unlock()
	if !allowed {
		writeError(w, 429, "Слишком много событий")
		return
	}
	applog.Event("ERROR", "web.client_error", applog.Fields{"kind": event.Kind, "asset": event.Asset, "line": event.Line, "column": event.Column, "actor_id": applog.Ref(actor)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
