// Package webapp is the shared HTTP application for mem gui and mem serve.
package webapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/knaprus-14/mem-tool/internal/applog"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/knaprus-14/mem-tool/internal/buildinfo"
	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/webjobs"
	"github.com/knaprus-14/mem-tool/internal/workspace"
)

//go:embed assets/map-navigation.js assets/map-view-presets.js assets/map-structured-view.js assets/map-structured-view.css assets/map-view-controls.js assets/map-view-export.js assets/graph-documents.js assets/operations.js assets/maintenance.js assets/index.html assets/reading-workspace.js assets/reading-workspace.css assets/safe-markdown.js assets/safe-markdown.css assets/app.css assets/app.js assets/map-layout.js assets/map-actions.js assets/map-library.js assets/map-import.js assets/map-document.js assets/document-annotations.js assets/document-annotations.css assets/annotation-regions.js assets/annotation-regions.css assets/annotation-transfer.js assets/annotation-reanchor.js assets/annotation-knowledge.js assets/map-resize.js assets/map-workbench.js assets/index-tools.js assets/maps-ai.js assets/job-history.js assets/map-from-sources.js
var assets embed.FS

type session struct {
	ID         string
	Actor      workspace.Actor
	Name, CSRF string
	Expires    time.Time
}
type loginWindow struct {
	Since time.Time
	Count int
}
type App struct {
	graphs     graphSessions
	jobs       *webjobs.Service
	uploads    chan struct{}
	queries    chan struct{}
	queryBusy  map[string]bool
	cfg        Config
	manager    *workspace.Manager
	cookieName string
	mu         sync.Mutex
	sessions   map[string]session
	logins     map[string]loginWindow
	clientLogs map[string]time.Time
}

// New requires the final origin, including an OS-assigned port in local mode.
func New(c Config, m *workspace.Manager) (*App, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.Path != "" {
		return nil, fmt.Errorf("не задан конечный origin приложения")
	}
	if c.Local && (u.Scheme != "http" || !isLoopback(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.Fragment != "") {
		return nil, fmt.Errorf("локальный origin должен указывать на loopback HTTP")
	}
	if m == nil {
		return nil, fmt.Errorf("каталог баз не открыт")
	}
	actors := []workspace.Actor{}
	if c.Local {
		actors = append(actors, workspace.Actor{ID: "local"})
	} else {
		for _, u := range c.Users {
			actors = append(actors, workspace.Actor{ID: u.ID})
		}
	}
	app := &App{cfg: c, manager: m, jobs: webjobs.New(m, actors), uploads: make(chan struct{}, 2), queries: make(chan struct{}, 2), queryBusy: map[string]bool{}, cookieName: "mem_web_" + randomToken()[:12], sessions: map[string]session{}, logins: map[string]loginWindow{}}
	app.jobs.BeforeWork = app.checkStorage
	return app, nil
}

func (a *App) Start(ctx context.Context) { a.jobs.Start(ctx) }
func (a *App) Close()                    { a.jobs.Close(); a.closeGraphs() }

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	applog.HTTP("web", http.HandlerFunc(a.serveHTTP)).ServeHTTP(w, r)
}

func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	u, _ := url.Parse(a.cfg.PublicOrigin)
	if !strings.EqualFold(r.Host, u.Host) {
		writeError(w, 403, "Адрес запроса не разрешён")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeError(w, 403, "Запрос с другого сайта отклонён")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != a.cfg.PublicOrigin {
		writeError(w, 403, "Origin не разрешён")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != a.cfg.PublicOrigin {
		writeError(w, 403, "Для изменения данных требуется same-origin запрос")
		return
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/readyz" && r.Method == "GET" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.manager.Ready(ctx) != nil {
			writeError(w, 503, "Сервис не готов")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return
	}
	if r.Method == "GET" {
		path := map[string]string{"/assets/map-navigation.js": "assets/map-navigation.js", "/assets/map-view-presets.js": "assets/map-view-presets.js", "/assets/map-structured-view.js": "assets/map-structured-view.js", "/assets/map-structured-view.css": "assets/map-structured-view.css", "/assets/map-view-controls.js": "assets/map-view-controls.js", "/assets/map-view-export.js": "assets/map-view-export.js", "/": "assets/index.html", "/assets/graph-documents.js": "assets/graph-documents.js", "/assets/maintenance.js": "assets/maintenance.js", "/assets/operations.js": "assets/operations.js", "/assets/reading-workspace.js": "assets/reading-workspace.js", "/assets/reading-workspace.css": "assets/reading-workspace.css", "/assets/safe-markdown.js": "assets/safe-markdown.js", "/assets/safe-markdown.css": "assets/safe-markdown.css", "/assets/app.js": "assets/app.js", "/assets/map-layout.js": "assets/map-layout.js", "/assets/map-actions.js": "assets/map-actions.js", "/assets/map-library.js": "assets/map-library.js", "/assets/map-import.js": "assets/map-import.js", "/assets/map-document.js": "assets/map-document.js", "/assets/document-annotations.js": "assets/document-annotations.js", "/assets/document-annotations.css": "assets/document-annotations.css", "/assets/annotation-regions.js": "assets/annotation-regions.js", "/assets/annotation-regions.css": "assets/annotation-regions.css", "/assets/annotation-transfer.js": "assets/annotation-transfer.js", "/assets/annotation-reanchor.js": "assets/annotation-reanchor.js", "/assets/annotation-knowledge.js": "assets/annotation-knowledge.js", "/assets/map-resize.js": "assets/map-resize.js", "/assets/map-workbench.js": "assets/map-workbench.js", "/assets/index-tools.js": "assets/index-tools.js", "/assets/maps-ai.js": "assets/maps-ai.js", "/assets/job-history.js": "assets/job-history.js", "/assets/map-from-sources.js": "assets/map-from-sources.js", "/assets/app.css": "assets/app.css"}[r.URL.Path]
		if path != "" {
			data, err := assets.ReadFile(path)
			if err != nil {
				writeError(w, 500, "Ресурс недоступен")
				return
			}
			mime := "text/html; charset=utf-8"
			if strings.HasSuffix(path, ".js") {
				mime = "text/javascript; charset=utf-8"
			}
			if strings.HasSuffix(path, ".css") {
				mime = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", mime)
			_, _ = w.Write(data)
			return
		}
	}
	if r.URL.Path == "/api/v1/login" && r.Method == "POST" {
		a.login(w, r)
		return
	}
	s, ok := a.getSession(r)
	if r.URL.Path == "/api/v1/session" && r.Method == "GET" {
		if !ok && a.cfg.Local {
			s, ok = a.startSession(w, workspace.Actor{ID: "local"}, "Локальный пользователь")
			if !ok {
				return
			}
		}
		if !ok {
			writeJSON(w, 200, map[string]any{"authenticated": false, "local": a.cfg.Local, "version": buildinfo.Version})
			return
		}
		writeJSON(w, 200, map[string]any{"authenticated": true, "local": a.cfg.Local, "version": buildinfo.Version, "user": s.Name, "csrf": s.CSRF})
		return
	}
	if !ok {
		writeError(w, 401, "Войдите в приложение")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(r.Header.Get("X-Mem-CSRF"))) != 1 {
		writeError(w, 403, "Проверка сессии не пройдена; обновите страницу")
		return
	}
	if r.URL.Path == "/api/v1/logout" && r.Method == "POST" {
		if c, e := r.Cookie(a.cookieName); e == nil {
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.retireSessionGraphs(s.CSRF)
			a.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: a.cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: u.Scheme == "https", SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/graph/") {
		a.graphRoute(w, r, s)
		return
	}
	if r.URL.Path == "/api/v1/client-events" && r.Method == "POST" {
		a.clientEvent(w, r, s.Actor.ID)
		return
	}
	if r.URL.Path == "/api/v1/workspaces" {
		switch r.Method {
		case "GET":
			list, err := a.manager.List(r.Context(), s.Actor)
			if err != nil {
				a.fail(w, err)
				return
			}
			views := []any{}
			for _, item := range list {
				views = append(views, a.workspaceView(item))
			}
			writeJSON(w, 200, views)
		case "POST":
			var body struct {
				Name string `json:"name"`
				Path string `json:"path"`
			}
			if !decode(w, r, &body) {
				return
			}
			if !a.cfg.Local && body.Path != "" {
				writeError(w, 400, "Сервер сам выбирает каталог новой базы")
				return
			}
			_, used, err := a.storageUsage(r.Context(), s.Actor, "")
			if err != nil {
				a.fail(w, err)
				return
			}
			if used >= a.cfg.MaxUserBytes {
				a.fail(w, workspace.ErrStorageLimit)
				return
			}
			item, err := a.manager.Create(r.Context(), s.Actor, body.Name, body.Path)
			if err != nil {
				a.fail(w, err)
				return
			}
			writeJSON(w, 201, a.workspaceView(item))
		default:
			writeError(w, 405, "Метод не поддерживается")
		}
		return
	}
	if r.URL.Path == "/api/v1/workspaces/open" && r.Method == "POST" {
		if !a.cfg.Local {
			writeError(w, 403, "Открытие по пути доступно только в локальном режиме")
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		if !decode(w, r, &body) {
			return
		}
		item, err := a.manager.Register(r.Context(), s.Actor, body.Path)
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, 200, a.workspaceView(item))
		return
	}
	if r.URL.Path == "/api/v1/workspaces/import" && r.Method == "POST" {
		a.importPortable(w, r, s.Actor)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" {
		if parts[4] == "storage" && len(parts) == 5 && r.Method == "GET" {
			a.storageRoute(w, r, s.Actor, parts[3])
			return
		}
		if storageWriteRequest(r) {
			base, e := a.manager.Resolve(r.Context(), s.Actor, parts[3])
			if e != nil {
				a.fail(w, e)
				return
			}
			if e = a.checkStorage(r.Context(), s.Actor, base); e != nil {
				a.fail(w, e)
				return
			}
		}
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "export" && r.Method == "POST" {
		a.exportPortable(w, r, s.Actor, parts[3])
		return
	}
	if len(parts) >= 5 && len(parts) <= 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "maintenance" {
		a.maintenanceRoute(w, r, s.Actor, parts)
		return
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "reading-workspace" {
		a.readingWorkspaceRoute(w, r, s.Actor, parts[3])
		return
	}
	if len(parts) >= 5 && len(parts) <= 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "annotations" {
		a.annotationsRoute(w, r, s.Actor, parts)
		return
	}
	if len(parts) == 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "entries" && parts[6] == "metadata" {
		a.entryMetadataRoute(w, r, s.Actor, parts)
		return
	}
	if (len(parts) == 5 || len(parts) == 6 || len(parts) == 7 || len(parts) == 8 || len(parts) == 9 || len(parts) == 11) && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "maps" {
		a.mapsRoute(w, r, s.Actor, parts)
		return
	}
	if len(parts) >= 5 && len(parts) <= 8 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "notes" {
		a.notesRoute(w, r, s.Actor, parts)
		return
	}
	if (len(parts) == 5 || len(parts) == 6) && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "index" {
		action := ""
		if len(parts) == 6 {
			action = parts[5]
		}
		a.indexRoute(w, r, s.Actor, parts[3], action)
		return
	}
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "settings" {
		a.settingsToolRoute(w, r, s.Actor, parts[3], parts[5])
		return
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[4] == "settings" {
		a.settingsRoute(w, r, s.Actor, parts[3])
		return
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && (parts[4] == "search" || parts[4] == "ask" || parts[4] == "evidence") {
		a.queryRoute(w, r, s.Actor, parts)
		return
	}
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && (parts[4] == "uploads" || parts[4] == "jobs" || parts[4] == "revisions") {
		a.jobRoutes(w, r, s.Actor, parts)
		return
	}
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && r.Method == "GET" {
		item, err := a.manager.Resolve(r.Context(), s.Actor, parts[3])
		if err != nil {
			a.fail(w, err)
			return
		}
		switch {
		case len(parts) == 5 && parts[4] == "library":
			limit, err := integerParam(r, "limit", 30, 1, 100)
			if err != nil {
				a.fail(w, err)
				return
			}
			offset, err := integerParam(r, "offset", 0, 0, 100000000)
			if err != nil {
				a.fail(w, err)
				return
			}
			result, err := workspace.ReadLibrary(r.Context(), item, workspace.Query{Kind: r.URL.Query().Get("kind"), Document: r.URL.Query().Get("document"), Limit: limit, Offset: offset})
			if err != nil {
				a.fail(w, err)
				return
			}
			writeJSON(w, 200, result)
			return
		case len(parts) == 6 && parts[4] == "entries":
			id, err := strconv.ParseInt(parts[5], 10, 64)
			if err != nil || id < 1 {
				writeError(w, 400, "Некорректный номер записи")
				return
			}
			result, err := workspace.ReadEntry(r.Context(), item, id)
			if err != nil {
				a.fail(w, err)
				return
			}
			writeJSON(w, 200, result)
			return
		}
	}
	writeError(w, 404, "Страница не найдена")
}

func (a *App) workspaceView(w workspace.Workspace) any {
	return struct {
		workspace.Workspace
		Path    string `json:"path,omitempty"`
		Offline bool   `json:"offline,omitempty"`
	}{Workspace: w, Offline: a.cfg.Local && localWorkspaceUnavailable(w), Path: func() string {
		if a.cfg.Local {
			return w.Root
		}
		return ""
	}()}
}
func (a *App) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrStorageLimit):
		writeError(w, 507, err.Error())
	case errors.Is(err, filelock.ErrBusy):
		writeError(w, 409, "В эту базу уже загружается файл. Повторите через несколько секунд.")
	case errors.Is(err, webjobs.ErrLimit):
		writeError(w, 413, "Лимит: файл до 64 МиБ, до 20 активных заданий, 1000 оригиналов и 1 ГиБ оригиналов на базу.")
	case errors.Is(err, webjobs.ErrInvalid):
		writeError(w, 400, "Проверьте файл и подпись. Поддерживаются Markdown, PDF и DjVu; имя файла не должно содержать специальные символы пути.")
	case errors.Is(err, webjobs.ErrConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, workspace.ErrNotFound):
		writeError(w, 404, workspace.ErrNotFound.Error())
	case errors.Is(err, workspace.ErrInvalid):
		if a.cfg.Local {
			writeError(w, 400, err.Error())
		} else {
			writeError(w, 400, "Проверьте параметры базы")
		}
	default:
		applog.Error("web.operation_failed", err)
		log.Printf("web operation failed: %v", err)
		writeError(w, 500, "Не удалось прочитать или сохранить базу. Подробности в журнале сервера.")
	}
}
func (a *App) getSession(r *http.Request) (session, bool) {
	c, err := r.Cookie(a.cookieName)
	if err != nil {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireSessionsLocked(time.Now())
	s, ok := a.sessions[c.Value]
	return s, ok
}

func (a *App) expireSessionsLocked(now time.Time) {
	for id, s := range a.sessions {
		if !s.Expires.After(now) {
			delete(a.sessions, id)
			a.retireSessionGraphs(s.CSRF)
		}
	}
}

func (a *App) startSession(w http.ResponseWriter, actor workspace.Actor, name string) (session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.expireSessionsLocked(now)
	if len(a.sessions) >= 128 {
		writeError(w, 429, "Достигнут лимит открытых сессий")
		return session{}, false
	}
	id := randomToken()
	s := session{ID: id, Actor: actor, Name: name, CSRF: randomToken(), Expires: now.Add(8 * time.Hour)}
	a.sessions[id] = s
	http.SetCookie(w, &http.Cookie{Name: a.cookieName, Value: id, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	return s, true
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Local {
		writeError(w, 404, "Локальному режиму не требуется вход")
		return
	}
	if !a.allowLogin(r) {
		writeError(w, 429, "Слишком много попыток входа. Повторите через минуту.")
		return
	}
	var body struct {
		User  string `json:"user"`
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	hash := sha256.Sum256([]byte(body.Token))
	for _, user := range a.cfg.Users {
		matches := subtle.ConstantTimeCompare(hash[:], user.tokenHash[:])
		if user.ID == body.User && matches == 1 {
			if _, ok := a.startSession(w, workspace.Actor{ID: user.ID}, user.Name); !ok {
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	writeError(w, 401, "Неверный пользователь или ключ доступа")
}
func (a *App) allowLogin(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Forwarded addresses are intentionally not trusted. Behind a proxy this
	// conservative limit is shared; configurable trusted proxies come later.
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for ip, v := range a.logins {
		if now.Sub(v.Since) >= time.Minute {
			delete(a.logins, ip)
		}
	}
	v, ok := a.logins[host]
	if !ok {
		if len(a.logins) >= 4096 {
			return false
		}
		v = loginWindow{Since: now}
	}
	v.Count++
	a.logins[host] = v
	return v.Count <= 10
}
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, 16<<10)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		writeError(w, 415, "Ожидается JSON")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "Некорректное тело запроса")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeError(w, 400, "Лишние данные в запросе")
		return false
	}
	return true
}
func integerParam(r *http.Request, key string, def, minValue, maxValue int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minValue || n > maxValue {
		return 0, workspace.ErrInvalid
	}
	return n, nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
