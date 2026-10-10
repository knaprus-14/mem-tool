package webapp

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type graphSession struct {
	store   *mem.Store
	handler http.Handler
	cancel  context.CancelFunc
	csrf    string
	used    time.Time
	active  int
	retired bool
	closing bool
}
type graphSessions struct {
	sync.Mutex
	items   map[string]*graphSession
	closed  bool
	drained sync.WaitGroup
}

func (a *App) closeGraphs() {
	a.graphs.Lock()
	a.graphs.closed = true
	for key, g := range a.graphs.items {
		a.retireGraphLocked(key, g)
	}
	a.graphs.Unlock()
	a.graphs.drained.Wait()
}

// a.graphs must be locked. Removed handlers remain alive until their last HTTP
// request and all background runners have drained; logout itself never waits.
func (a *App) retireGraphLocked(key string, g *graphSession) {
	delete(a.graphs.items, key)
	g.retired = true
	g.cancel()
	a.closeRetiredGraphLocked(g)
}

func (a *App) closeRetiredGraphLocked(g *graphSession) {
	if !g.retired || g.active != 0 || g.closing {
		return
	}
	g.closing = true
	go func() {
		defer a.graphs.drained.Done()
		if closer, ok := g.handler.(io.Closer); ok {
			_ = closer.Close()
		}
		_ = g.store.Close()
	}()
}

func (a *App) retireSessionGraphs(csrf string) {
	a.graphs.Lock()
	defer a.graphs.Unlock()
	for key, g := range a.graphs.items {
		if g.csrf == csrf {
			a.retireGraphLocked(key, g)
		}
	}
}

func (a *App) releaseGraph(g *graphSession) {
	a.graphs.Lock()
	defer a.graphs.Unlock()
	g.active--
	g.used = time.Now()
	a.closeRetiredGraphLocked(g)
}

func (a *App) evictIdleGraphLocked() bool {
	var oldest *graphSession
	var oldestKey string
	for key, g := range a.graphs.items {
		if g.active != 0 {
			continue
		}
		lifecycle, ok := g.handler.(interface{ CanEvict() bool })
		if !ok || !lifecycle.CanEvict() {
			continue
		}
		if oldest == nil || g.used.Before(oldest.used) {
			oldest, oldestKey = g, key
		}
	}
	if oldest == nil {
		return false
	}
	a.retireGraphLocked(oldestKey, oldest)
	return true
}

func (a *App) graphRoute(w http.ResponseWriter, r *http.Request, s session) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/graph/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, 404, "Граф не найден")
		return
	}
	base, err := a.manager.Resolve(r.Context(), s.Actor, parts[0])
	if err != nil {
		a.fail(w, err)
		return
	}
	if storageWriteRequest(r) {
		if err = a.checkStorage(r.Context(), s.Actor, base); err != nil {
			a.fail(w, err)
			return
		}
	}
	key := s.Actor.ID + ":" + base.ID + ":" + s.CSRF
	// Revalidate while taking the graph lock: a request that passed the outer
	// middleware before logout must not recreate a revoked handler afterwards.
	a.mu.Lock()
	current, authenticated := a.sessions[s.ID]
	if !authenticated || current.CSRF != s.CSRF || !current.Expires.After(time.Now()) {
		a.mu.Unlock()
		writeError(w, 401, "Сессия завершена. Войдите в приложение снова.")
		return
	}
	a.graphs.Lock()
	a.mu.Unlock()
	if a.graphs.closed {
		a.graphs.Unlock()
		writeError(w, 503, "Сервер завершается")
		return
	}
	if a.graphs.items == nil {
		a.graphs.items = map[string]*graphSession{}
	}
	g := a.graphs.items[key]
	if g == nil {
		if len(a.graphs.items) >= 8 && !a.evictIdleGraphLocked() {
			a.graphs.Unlock()
			writeError(w, 429, "Сеансы графа заняты запросами или фоновыми заданиями. Дождитесь завершения работы и повторите.")
			return
		}
		store, e := mem.OpenWebGraphIn(r.Context(), filepath.Join(base.Root, mem.MemDirName))
		if e != nil {
			a.graphs.Unlock()
			a.fail(w, e)
			return
		}
		ctx, cancel := context.WithDeadline(context.Background(), s.Expires)
		handler, e := mem.NewSharedKnowledgeMapHandler(ctx, store, base.Name, s.CSRF, a.cfg.PublicOrigin, "/graph/"+base.ID+"/", a.cfg.Local)
		if e != nil {
			cancel()
			store.Close()
			a.graphs.Unlock()
			a.fail(w, e)
			return
		}
		g = &graphSession{store: store, handler: handler, cancel: cancel, csrf: s.CSRF}
		a.graphs.items[key] = g
		a.graphs.drained.Add(1)
		expiring := g
		context.AfterFunc(ctx, func() {
			a.graphs.Lock()
			defer a.graphs.Unlock()
			if a.graphs.items[key] == expiring {
				a.retireGraphLocked(key, expiring)
			}
		})
		applog.Event("info", "web.graph.open", applog.Fields{"workspace": base.ID})
	}
	g.active++
	a.graphs.Unlock()
	defer a.releaseGraph(g)
	g.handler.ServeHTTP(w, r)
}
