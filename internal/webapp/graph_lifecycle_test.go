package webapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/knaprus-14/mem-tool/internal/workspace"
)

func TestGraphSessionsReleaseOnLogoutAndExpiry(t *testing.T) {
	a := testApp(t, true)
	t.Cleanup(a.closeGraphs)
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "local"}, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	c := &client{app: a}
	path := "/graph/" + base.ID + "/"
	for i := 0; i < 10; i++ {
		c.session(t)
		if w := c.call("GET", path, nil); w.Code != 200 {
			t.Fatalf("open %d: %d %s", i, w.Code, w.Body)
		}
		if i%2 == 0 {
			if w := c.call("POST", "/api/v1/logout", map[string]any{}); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
		} else {
			a.mu.Lock()
			s := a.sessions[c.cookie.Value]
			s.Expires = time.Now().Add(-time.Second)
			a.sessions[c.cookie.Value] = s
			a.mu.Unlock()
			if w := c.call("GET", path, nil); w.Code != 401 {
				t.Fatal("expired request:", w.Code, w.Body)
			}
		}
		a.graphs.Lock()
		remaining := len(a.graphs.items)
		a.graphs.Unlock()
		if remaining != 0 {
			t.Fatalf("retained %d graph sessions", remaining)
		}
	}
}

type heldGraphHandler struct {
	http.Handler
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
	pinned  bool
}

func (h *heldGraphHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	close(h.entered)
	<-h.release
	h.Handler.ServeHTTP(w, r)
}
func (h *heldGraphHandler) Close() error {
	h.once.Do(func() { close(h.closed) })
	return h.Handler.(io.Closer).Close()
}
func (h *heldGraphHandler) CanEvict() bool { return !h.pinned }

func TestGraphLogoutDrainsActiveRequestAndRejectsStaleSession(t *testing.T) {
	a := testApp(t, true)
	t.Cleanup(a.closeGraphs)
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "local"}, "held", "")
	if err != nil {
		t.Fatal(err)
	}
	c := &client{app: a}
	c.session(t)
	path := "/graph/" + base.ID + "/"
	if w := c.call("GET", path, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	a.mu.Lock()
	oldSession := a.sessions[c.cookie.Value]
	a.mu.Unlock()
	key := "local:" + base.ID + ":" + c.csrf
	a.graphs.Lock()
	g := a.graphs.items[key]
	held := &heldGraphHandler{Handler: g.handler, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	g.handler = held
	a.graphs.Unlock()
	// Register release after a.closeGraphs so cleanup also drains on test failure.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	t.Cleanup(release)
	request := httptest.NewRequest("GET", a.cfg.PublicOrigin+path, nil)
	request.AddCookie(c.cookie)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); a.ServeHTTP(response, request) }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter")
	}
	if w := c.call("POST", "/api/v1/logout", map[string]any{}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	select {
	case <-held.closed:
		t.Fatal("handler closed while HTTP request still active")
	default:
	}
	// Simulate a request delayed between middleware authentication and graphRoute.
	stale := httptest.NewRecorder()
	a.graphRoute(stale, httptest.NewRequest("GET", a.cfg.PublicOrigin+path, nil), oldSession)
	if stale.Code != 401 {
		t.Fatal("revoked session recreated graph:", stale.Code)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not drain")
	}
	if response.Code != 200 {
		t.Fatal("in-flight request lost its Store:", response.Code, response.Body)
	}
	select {
	case <-held.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("retired handler was not closed")
	}
}

func TestGraphLRUPreservesPinnedJobsAndEvictsIdleHandlers(t *testing.T) {
	a := testApp(t, true)
	t.Cleanup(a.closeGraphs)
	c := &client{app: a}
	c.session(t)
	var firstKey, secondKey string
	var pinned *heldGraphHandler
	for i := 0; i < 9; i++ {
		base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "local"}, "lru", "")
		if err != nil {
			t.Fatal(err)
		}
		if w := c.call("GET", "/graph/"+base.ID+"/", nil); w.Code != 200 {
			t.Fatal(i, w.Code, w.Body)
		}
		key := "local:" + base.ID + ":" + c.csrf
		a.graphs.Lock()
		if i == 0 {
			firstKey = key
			g := a.graphs.items[key]
			pinned = &heldGraphHandler{Handler: g.handler, pinned: true, closed: make(chan struct{})}
			g.handler = pinned
		}
		if i == 1 {
			secondKey = key
		}
		a.graphs.Unlock()
	}
	a.graphs.Lock()
	count := len(a.graphs.items)
	first, second := a.graphs.items[firstKey], a.graphs.items[secondKey]
	a.graphs.Unlock()
	if count != 8 || first == nil || second != nil {
		t.Fatalf("LRU: count=%d pinned=%v oldest_idle=%v", count, first != nil, second != nil)
	}
	select {
	case <-pinned.closed:
		t.Fatal("pinned background job/result was evicted")
	default:
	}
}
