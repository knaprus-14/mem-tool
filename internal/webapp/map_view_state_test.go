package webapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type mapViewHTTPFixture struct {
	app        *App
	alice, bob *client
	dir, path  string
	input      mem.WebMapViewStateInput
}

func newMapViewHTTPFixture(t *testing.T) mapViewHTTPFixture {
	t.Helper()
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, user := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := user.c.call("POST", "/api/v1/login", map[string]string{"user": user.id, "token": user.key}); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		user.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Saved views", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	store, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.ImportClassicMindMap(mem.ClassicMindMapDraft{Title: "View 😀", Mode: mem.ClassicMindMapModeManual, Status: mem.ClassicMindMapStatusDraft, Nodes: []mem.ClassicMindMapNodeDraft{{Ref: "root", Label: "Root"}, {Ref: "leaf", ParentRef: "root", Label: "Leaf", BodyMarkdown: "Оригинал"}}}, "alice", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	leaf := ""
	for _, node := range doc.Nodes {
		if node.Label == "Leaf" {
			leaf = node.ID
		}
	}
	return mapViewHTTPFixture{app: a, alice: alice, bob: bob, dir: dir, path: "/api/v1/workspaces/" + base.ID + "/maps/" + doc.Map.ID + "/view-state", input: mem.WebMapViewStateInput{MapRevision: 1, RequestID: strings.Repeat("a", 32), State: mem.WebMapViewState{DisplayMode: "table", Layout: "down", SelectedNodeID: leaf, Collapsed: []string{doc.Map.RootNodeID}, Scale: 0.25, ScrollX: 0.4, ScrollY: 0.6, TableSort: "sources", TableDescending: true, TableQuery: "источник 😀", TablePage: 4}}}
}

func TestMapViewStateHTTPAccessQuotaReplayAndReadOnly(t *testing.T) {
	f := newMapViewHTTPFixture(t)
	read := func() mem.WebMapViewStateResult {
		t.Helper()
		w := f.alice.call("GET", f.path, nil)
		var result mem.WebMapViewStateResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), f.dir) || strings.Contains(w.Body.String(), "actor_key") {
			t.Fatal("private data leaked", w.Body)
		}
		return result
	}
	initial := read()
	if initial.Revision != 0 || initial.MapRevision != 1 || initial.State.DisplayMode != "diagram" || initial.State.TableSort != "tree" || initial.State.Collapsed == nil {
		t.Fatal(initial)
	}
	for _, method := range []string{"GET", "POST"} {
		if w := f.bob.call(method, f.path, f.input); w.Code != 404 {
			t.Fatal("foreign ownership", method, w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call(method, f.path, f.input); w.Code != 401 {
			t.Fatal("anonymous", method, w.Code)
		}
		f.app.mu.Lock()
		f.app.queryBusy["alice"] = true
		f.app.mu.Unlock()
		w := f.alice.call(method, f.path, f.input)
		f.app.mu.Lock()
		delete(f.app.queryBusy, "alice")
		f.app.mu.Unlock()
		if w.Code != 429 {
			t.Fatal("busy", method, w.Code, w.Body)
		}
		if w := f.alice.call(method, f.path+"?actor=bob", f.input); w.Code != 400 {
			t.Fatal("query injection", w.Code, w.Body)
		}
	}
	csrf := f.alice.csrf
	f.alice.csrf = "bad"
	w := f.alice.call("POST", f.path, f.input)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("CSRF", w.Code, w.Body)
	}
	if w = f.alice.call("PUT", f.path, f.input); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" {
		t.Fatal("method", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", f.app.cfg.PublicOrigin+f.path, nil)
	r.AddCookie(f.alice.cookie)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross site", w.Code)
	}
	w = f.alice.call("POST", f.path, f.input)
	var saved mem.WebMapViewStateResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Revision != 1 || saved.State.TableQuery != f.input.State.TableQuery || saved.Replayed {
		t.Fatal("save", w.Code, w.Body)
	}
	if current := read(); current.Revision != 1 || current.State.Scale != 0.25 || current.State.TablePage != 4 {
		t.Fatal(current)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	w = f.alice.call("POST", f.path, f.input)
	var replay mem.WebMapViewStateResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Replayed || replay.Revision != saved.Revision {
		t.Fatal("exact retry at quota", w.Code, w.Body)
	}
	if read().Revision != 1 {
		t.Fatal("GET blocked or changed at quota")
	}
	in := f.input
	in.RequestID = strings.Repeat("b", 32)
	if w = f.alice.call("POST", f.path, in); w.Code != 409 {
		t.Fatal("CAS precedes quota", w.Code, w.Body)
	}
	in.Revision = 1
	if w = f.alice.call("POST", f.path, in); w.Code != 507 {
		t.Fatal("new save at quota", w.Code, w.Body)
	}
	if read().Revision != 1 {
		t.Fatal("denied save changed view")
	}
	f.app.cfg.MaxWorkspaceBytes = 0
	f.app.cfg.MaxUserBytes = 0
	in.MapRevision = 2
	if w = f.alice.call("POST", f.path, in); w.Code != 409 {
		t.Fatal("map pin", w.Code, w.Body)
	}
	mapPath := strings.TrimSuffix(f.path, "/view-state")
	w = f.alice.call("GET", mapPath, nil)
	var mapView mem.WebMapView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &mapView) != nil || mapView.Map.Revision != 1 || len(mapView.Nodes) != 2 {
		t.Fatal("view changed map", w.Code, w.Body)
	}
}

func TestMapViewStateHTTPStrictCompleteJSONAndBounds(t *testing.T) {
	f := newMapViewHTTPFixture(t)
	data, err := json.Marshal(f.input)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	post := func(raw string, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", f.app.cfg.PublicOrigin+f.path, strings.NewReader(raw))
		r.AddCookie(f.alice.cookie)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", f.app.cfg.PublicOrigin)
		r.Header.Set("X-Mem-CSRF", f.alice.csrf)
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		return w
	}
	cases := []string{
		strings.Replace(valid, `"revision":0`, `"revision":0,"revision":0`, 1), strings.Replace(valid, `"revision":0`, `"Revision":0`, 1), strings.Replace(valid, `"revision":0`, `"revision":0,"actor":"bob"`, 1),
		strings.Replace(valid, `"scale":0.25`, `"scale":0.25,"scale":1`, 1), strings.Replace(valid, `"table_descending":true`, `"table_descending":null`, 1), strings.Replace(valid, `"table_descending":true,`, "", 1),
		strings.Replace(valid, `"table_query":"источник 😀"`, `"table_query":"\ud800"`, 1), strings.Replace(valid, `"table_query":"источник 😀"`, `"table_query":"\u0000"`, 1), strings.Replace(valid, "источник 😀", string([]byte{0xff}), 1),
		strings.Replace(valid, `"scale":0.25`, `"scale":1e999`, 1), strings.Replace(valid, `"scale":0.25`, `"scale":0.049`, 1), strings.Replace(valid, `"table_page":4`, `"table_page":1000`, 1), valid + ` {}`, `null`,
	}
	for i, raw := range cases {
		if w := post(raw, "application/json"); w.Code != 400 {
			t.Fatalf("case%d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := post(valid, "text/plain"); w.Code != 415 {
		t.Fatal("content type", w.Code, w.Body)
	}
	if w := post(strings.Repeat(" ", (256<<10)+1), "application/json"); w.Code != 413 {
		t.Fatal("raw body limit", w.Code, w.Body)
	}
	// An escaped supplementary rune is valid and retains its exact text.
	escaped := strings.ReplaceAll(valid, "😀", `\ud83d\ude00`)
	if w := post(escaped, "application/json; charset=utf-8"); w.Code != 200 {
		t.Fatal("valid Unicode escapes", w.Code, w.Body)
	}
}
