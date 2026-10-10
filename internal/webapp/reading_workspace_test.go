package webapp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestReadingWorkspaceHTTPAccessFreshnessAndRecovery(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "😀 Сохранённый текст чтения", "текст", 14)
	path := strings.TrimSuffix(f.path, "/annotations") + "/reading-workspace"
	read := func() mem.ReadingWorkspace {
		t.Helper()
		w := f.alice.call("GET", path, nil)
		var out mem.ReadingWorkspace
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), f.dir) || strings.Contains(w.Body.String(), `"source_path"`) {
			t.Fatal("private filesystem path exposed", w.Body)
		}
		return out
	}
	empty := read()
	in := mem.ReadingWorkspaceMutation{Revision: empty.Revision, RequestID: strings.Repeat("a", 32), Action: "add", EntryID: f.hit.ID, Pin: f.hit.Pin, Start: 0, End: len([]rune(f.hit.Text))}
	for _, route := range []struct {
		method string
		body   any
	}{{"GET", nil}, {"POST", in}} {
		if w := f.bob.call(route.method, path, route.body); w.Code != 404 {
			t.Fatal("foreign workspace", w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call(route.method, path, route.body); w.Code != 401 {
			t.Fatal("anonymous", w.Code, w.Body)
		}
		f.app.mu.Lock()
		f.app.queryBusy["alice"] = true
		f.app.mu.Unlock()
		w := f.alice.call(route.method, path, route.body)
		f.app.mu.Lock()
		delete(f.app.queryBusy, "alice")
		f.app.mu.Unlock()
		if w.Code != 429 {
			t.Fatal("query admission", w.Code, w.Body)
		}
	}
	csrf := f.alice.csrf
	f.alice.csrf = "invalid"
	w := f.alice.call("POST", path, in)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("csrf", w.Code, w.Body)
	}
	if w = f.alice.call("GET", path+"?actor=bob", nil); w.Code != 400 {
		t.Fatal("query parameters", w.Code, w.Body)
	}
	if w = f.alice.call("PUT", path, in); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" {
		t.Fatal("method", w.Code, w.Body)
	}
	if w = f.alice.call("POST", path, map[string]any{"actor": "bob"}); w.Code != 400 {
		t.Fatal("actor in body", w.Code, w.Body)
	}
	request := httptest.NewRequest("GET", f.app.cfg.PublicOrigin+path, nil)
	request.AddCookie(f.alice.cookie)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	f.app.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("cross site", w.Code, w.Body)
	}
	w = f.alice.call("POST", path, in)
	var created mem.ReadingWorkspaceResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || len(created.Workspace.Items) != 1 {
		t.Fatal("capture", w.Code, w.Body)
	}
	w = f.alice.call("POST", path, in)
	var replay mem.ReadingWorkspaceResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Replayed || replay.Workspace.Items[0].ID != created.Workspace.Items[0].ID {
		t.Fatal("retry", w.Code, w.Body)
	}
	in.RequestID = strings.Repeat("b", 32)
	if w = f.alice.call("POST", path, in); w.Code != 409 {
		t.Fatal("old revision", w.Code, w.Body)
	}
	state := read()
	item := state.Items[0]
	if item.Text != f.hit.Text || string(item.SourceState) != "current" || state.ActiveID != item.ID {
		t.Fatal("saved content", state)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	// A response lost before capacity was reached must still be recoverable.
	in.RequestID = strings.Repeat("a", 32)
	if w = f.alice.call("POST", path, in); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Replayed {
		t.Fatal("receipt recovery at quota", w.Code, w.Body)
	}
	in.RequestID = strings.Repeat("b", 32)
	in.Revision = state.Revision
	if w = f.alice.call("POST", path, in); w.Code != 507 {
		t.Fatal("add at quota", w.Code, w.Body)
	}
	if read().Revision != state.Revision {
		t.Fatal("GET changes state")
	}
	position := mem.ReadingWorkspaceMutation{Revision: state.Revision, RequestID: strings.Repeat("c", 32), Action: "position", ID: item.ID, Scroll: 0.75}
	if w = f.alice.call("POST", path, position); w.Code != 200 {
		t.Fatal("position at quota", w.Code, w.Body)
	}
	state = read()
	if state.Items[0].Scroll != 0.75 {
		t.Fatal(state)
	}
	remove := mem.ReadingWorkspaceMutation{Revision: state.Revision, RequestID: strings.Repeat("d", 32), Action: "remove", ID: item.ID}
	if w = f.alice.call("POST", path, remove); w.Code != 200 {
		t.Fatal("remove at quota", w.Code, w.Body)
	}
	state = read()
	if len(state.Items) != 0 || state.ActiveID != "" {
		t.Fatal("removed active", state)
	}
}

func TestReadingWorkspaceAssetsAndEntryPoints(t *testing.T) {
	a := testApp(t, true)
	for _, path := range []string{"/assets/reading-workspace.js", "/assets/reading-workspace.css", "/assets/safe-markdown.js", "/assets/safe-markdown.css"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", a.cfg.PublicOrigin+path, nil))
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal(path, w.Code)
		}
	}
	html, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`id="reading-workspace-open"`, `id="entry-reading-save"`, `<div id="map-node-body">`, `<div id="map-document-node-body">`} {
		if !strings.Contains(string(html), marker) {
			t.Fatal("missing UI", marker)
		}
	}
}
