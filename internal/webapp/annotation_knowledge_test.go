package webapp

import (
	"encoding/json"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestAnnotationKnowledgeHTTPAccessAndWorkflow(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Начало: цитата.", "цитата", len([]rune("Начало: ")))
	a := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	path := f.path + "/" + a.ID + "/knowledge"
	if w := f.alice.call("GET", path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal(w.Code, w.Body)
	}
	in := mem.DocumentAnnotationKnowledgeCreate{Revision: 1, Kind: mem.KnowledgeNodeQuestion, Label: "Почему?", Body: "Мой вопрос", RequestID: strings.Repeat("b", 32)}
	var created mem.DocumentAnnotationKnowledgeResult
	w := f.alice.call("POST", path, in)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Node.ID == "" || created.Replayed || created.Node.Status != mem.KnowledgeStatusDraft {
		t.Fatal(w.Code, w.Body)
	}
	reverse := f.path + "/knowledge-node/" + created.Node.ID
	for _, route := range []struct {
		method, path string
		body         any
	}{{"GET", path, nil}, {"GET", reverse, nil}, {"POST", path, in}} {
		if w = f.bob.call(route.method, route.path, route.body); w.Code != 404 {
			t.Fatal("foreign", w.Code, w.Body)
		}
		if w = (&client{app: f.app}).call(route.method, route.path, route.body); w.Code != 401 {
			t.Fatal("anonymous", w.Code, w.Body)
		}
		f.app.mu.Lock()
		f.app.queryBusy["alice"] = true
		f.app.mu.Unlock()
		w = f.alice.call(route.method, route.path, route.body)
		f.app.mu.Lock()
		delete(f.app.queryBusy, "alice")
		f.app.mu.Unlock()
		if w.Code != 429 {
			t.Fatal("busy", w.Code, w.Body)
		}
	}
	csrf := f.alice.csrf
	f.alice.csrf = "wrong"
	w = f.alice.call("POST", path, in)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("csrf", w.Code, w.Body)
	}
	if w = f.alice.call("POST", path, map[string]any{"revision": 1, "kind": "note", "label": "x", "actor": "bob"}); w.Code != 400 {
		t.Fatal("unknown field", w.Code, w.Body)
	}
	for _, endpoint := range []string{path, reverse} {
		if w = f.alice.call("DELETE", endpoint, nil); w.Code != 405 {
			t.Fatal(w.Code, w.Body)
		}
		for _, query := range []string{"?offset=-1", "?offset=1000001", "?limit=0", "?limit=51"} {
			if w = f.alice.call("GET", endpoint+query, nil); w.Code != 400 {
				t.Fatal(w.Code, w.Body)
			}
		}
	}
	if w = f.alice.call("POST", reverse, in); w.Code != 405 {
		t.Fatal(w.Code, w.Body)
	}
	readAnnotationHTTPResult(t, f.alice.call("POST", f.path+"/"+a.ID+"/edit", mem.DocumentAnnotationEdit{Revision: 1, Note: "Новый комментарий", Color: "blue", RequestID: strings.Repeat("c", 32)}))
	w = f.alice.call("POST", path, in)
	var retry mem.DocumentAnnotationKnowledgeResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &retry) != nil || !retry.Replayed || retry.Node != created.Node || retry.Link != created.Link {
		t.Fatal("late replay", w.Code, w.Body)
	}
	changed := in
	changed.RequestID = strings.Repeat("d", 32)
	if w = f.alice.call("POST", path, changed); w.Code != 409 {
		t.Fatal("stale revision", w.Code, w.Body)
	}
	archive := true
	readAnnotationHTTPResult(t, f.alice.call("POST", f.path+"/"+a.ID+"/edit", mem.DocumentAnnotationEdit{Revision: 2, Archived: &archive, RequestID: strings.Repeat("e", 32)}))
	changed.Revision = 3
	if w = f.alice.call("POST", path, changed); w.Code != 409 {
		t.Fatal("archived", w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	for _, endpoint := range []string{path, reverse} {
		w = f.alice.call("GET", endpoint, nil)
		var page mem.DocumentAnnotationKnowledgePage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].Snapshot.Revision != 1 || page.Items[0].Annotation.Revision != 3 {
			t.Fatal("GET at quota", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "source_path") || strings.Contains(w.Body.String(), strings.ReplaceAll(f.dir, `\`, `\\`)) {
			t.Fatal("private path", w.Body)
		}
	}
	if w = f.alice.call("POST", path, in); w.Code != 507 {
		t.Fatal("POST storage gate", w.Code, w.Body)
	}
}
