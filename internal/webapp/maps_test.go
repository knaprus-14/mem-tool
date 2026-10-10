package webapp

import (
	"context"
	"encoding/json"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"strings"
	"testing"
)

func TestWebMapsHTTP(t *testing.T) {
	a := testApp(t, false)
	alice := &client{app: a}
	bob := &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Maps", "")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps"
	body := mem.WebMapCreate{Title: "Fixture", TemplateID: "study", RequestID: strings.Repeat("a", 32)}
	for _, tail := range []string{"", "/templates", "/any-map"} {
		if w := bob.call("GET", path+tail, nil); w.Code != 404 {
			t.Fatal("foreign map read")
		}
		if w := (&client{app: a}).call("GET", path+tail, nil); w.Code != 401 {
			t.Fatal("anonymous map read")
		}
	}
	if w := bob.call("POST", path, body); w.Code != 404 {
		t.Fatal("foreign create")
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w := alice.call("POST", path, body); w.Code != 403 {
		t.Fatal("CSRF create")
	}
	alice.csrf = csrf
	if w := alice.call("POST", path, map[string]string{"path": "C:/private"}); w.Code != 400 {
		t.Fatal("unknown field")
	}
	a.queryBusy["alice"] = true
	if w := alice.call("POST", path, body); w.Code != 429 {
		t.Fatal("busy create")
	}
	delete(a.queryBusy, "alice")
	w := alice.call("POST", path, body)
	var result mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = alice.call("POST", path, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("retry", w.Body)
	}
	for _, tail := range []string{"", "/templates", "/" + result.ID} {
		w = alice.call("GET", path+tail, nil)
		if w.Code != 200 {
			t.Fatalf("read %s: %d %s", tail, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), base.Root) {
			t.Fatal("path leak")
		}
		if w = bob.call("GET", path+tail, nil); w.Code != 404 {
			t.Fatal("foreign read")
		}
	}
	if w = alice.call("DELETE", path+"/"+result.ID, nil); w.Code != 405 {
		t.Fatal("unimplemented deletion allowed")
	}
	if w = alice.call("GET", path+"?offset=-1", nil); w.Code != 400 {
		t.Fatal("invalid offset")
	}
	if w = alice.call("GET", path+"?archived=anything", nil); w.Code != 400 {
		t.Fatal("invalid filter")
	}
}

func TestWebMapNodeEditHTTP(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Edit maps", "")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", path, mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Body)
	}
	w = alice.call("GET", path+"/"+created.ID, nil)
	var view mem.WebMapView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal(w.Body)
	}
	endpoint := path + "/" + created.ID + "/nodes/" + view.Map.RootNodeID
	input := mem.WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("b", 32), Label: "Changed", Body: strings.Repeat("текст ", 4000)}
	if w = bob.call("POST", endpoint, input); w.Code != 404 {
		t.Fatal("ownership", w.Code)
	}
	if w = (&client{app: a}).call("POST", endpoint, input); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w = alice.call("POST", endpoint, input); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	alice.csrf = csrf
	if w = alice.call("GET", endpoint, nil); w.Code != 405 {
		t.Fatal("method", w.Code)
	}
	if w = alice.call("POST", endpoint, map[string]string{"locked": "false"}); w.Code != 400 {
		t.Fatal("unknown field", w.Code)
	}
	a.queryBusy["alice"] = true
	if w = alice.call("POST", endpoint, input); w.Code != 429 {
		t.Fatal("busy", w.Code)
	}
	delete(a.queryBusy, "alice")
	if w = alice.call("POST", endpoint, input); w.Code != 200 {
		t.Fatal("edit", w.Code, w.Body)
	}
	if w = alice.call("POST", endpoint, input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("retry", w.Body)
	}
	input.RequestID = strings.Repeat("c", 32)
	if w = alice.call("POST", endpoint, input); w.Code != 409 {
		t.Fatal("conflict", w.Code, w.Body)
	}
	w = alice.call("GET", path+"/"+created.ID, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Map.Revision != 2 || view.Nodes[0].Body != input.Body {
		t.Fatal("saved", w.Body)
	}
	if strings.Contains(w.Body.String(), base.Root) {
		t.Fatal("path leak")
	}
	input.Revision = 2
	input.Body = strings.Repeat("x", 1<<20)
	if w = alice.call("POST", endpoint, input); w.Code != 400 {
		t.Fatal("oversized payload", w.Code)
	}
}

func TestWebMapNodeAddHTTP(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Branches", "")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", path, mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Body)
	}
	w = alice.call("GET", path+"/"+created.ID, nil)
	var view mem.WebMapView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal(w.Body)
	}
	endpoint := path + "/" + created.ID + "/nodes/" + view.Map.RootNodeID + "/children"
	input := mem.WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("b", 32), Label: "New branch", Body: "Literal <script>"}
	if w = bob.call("POST", endpoint, input); w.Code != 404 {
		t.Fatal("owner", w.Code)
	}
	if w = (&client{app: a}).call("POST", endpoint, input); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w = alice.call("POST", endpoint, input); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	alice.csrf = csrf
	if w = alice.call("GET", endpoint, nil); w.Code != 405 {
		t.Fatal("method", w.Code)
	}
	if w = alice.call("POST", endpoint, map[string]string{"parent": "bad"}); w.Code != 400 {
		t.Fatal("unknown field", w.Code)
	}
	a.queryBusy["alice"] = true
	if w = alice.call("POST", endpoint, input); w.Code != 429 {
		t.Fatal("busy", w.Code)
	}
	delete(a.queryBusy, "alice")
	w = alice.call("POST", endpoint, input)
	var result mem.WebMapNodeEdited
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.NodeID == "" || result.Revision != 2 {
		t.Fatal("create", w.Code, w.Body)
	}
	if w = alice.call("POST", endpoint, input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("retry", w.Body)
	}
	input.RequestID = strings.Repeat("c", 32)
	if w = alice.call("POST", endpoint, input); w.Code != 409 {
		t.Fatal("conflict", w.Code)
	}
	w = alice.call("GET", path+"/"+created.ID, nil)
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Nodes) != 2 {
		t.Fatal("saved node", w.Body)
	}
	for _, n := range view.Nodes {
		if n.ID == result.NodeID && (n.ParentID != view.Map.RootNodeID || n.Kind != "subtopic" || n.Body != input.Body) {
			t.Fatal("child mismatch")
		}
	}
}

func TestWebMapHistoryAndUndoHTTP(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "History", "")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", path, mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Body)
	}
	path += "/" + created.ID
	w = alice.call("GET", path, nil)
	var view mem.WebMapView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal(w.Body)
	}
	w = alice.call("POST", path+"/nodes/"+view.Map.RootNodeID+"/children", mem.WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("b", 32), Label: "Child"})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = alice.call("GET", path+"/history", nil)
	var page mem.WebMapHistory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.UndoID == 0 || len(page.Items) != 2 {
		t.Fatal(w.Body)
	}
	if strings.Contains(w.Body.String(), base.Root) || strings.Contains(w.Body.String(), "before_json") {
		t.Fatal("path or snapshot leak")
	}
	input := mem.WebMapUndo{Revision: page.Revision, ChangeID: page.UndoID, RequestID: strings.Repeat("c", 32)}
	for _, route := range []string{"history", "undo"} {
		method := "GET"
		var body any
		if route == "undo" {
			method = "POST"
			body = input
		}
		if w = bob.call(method, path+"/"+route, body); w.Code != 404 {
			t.Fatal("owner", route, w.Code)
		}
		if w = (&client{app: a}).call(method, path+"/"+route, body); w.Code != 401 {
			t.Fatal("anonymous", route, w.Code)
		}
	}
	if w = alice.call("GET", path+"/history?before=-1", nil); w.Code != 400 {
		t.Fatal("cursor", w.Code)
	}
	if w = alice.call("GET", path+"/undo", nil); w.Code != 405 {
		t.Fatal("method", w.Code)
	}
	if w = alice.call("POST", path+"/history", input); w.Code != 405 {
		t.Fatal("method", w.Code)
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w = alice.call("POST", path+"/undo", input); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	alice.csrf = csrf
	if w = alice.call("POST", path+"/undo", map[string]string{"extra": "field"}); w.Code != 400 {
		t.Fatal("unknown field", w.Code)
	}
	a.queryBusy["alice"] = true
	if w = alice.call("POST", path+"/undo", input); w.Code != 429 {
		t.Fatal("busy", w.Code)
	}
	delete(a.queryBusy, "alice")
	if w = alice.call("POST", path+"/undo", input); w.Code != 200 {
		t.Fatal("undo", w.Code, w.Body)
	}
	if w = alice.call("POST", path+"/undo", input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("retry", w.Body)
	}
	input.RequestID = strings.Repeat("d", 32)
	if w = alice.call("POST", path+"/undo", input); w.Code != 409 {
		t.Fatal("conflict", w.Code)
	}
	w = alice.call("GET", path, nil)
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Map.Revision != 3 || len(view.Nodes) != 1 {
		t.Fatal("restored", w.Body)
	}
	w = alice.call("GET", path+"/history", nil)
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.RedoID == 0 {
		t.Fatal(w.Body)
	}
	input = mem.WebMapUndo{Revision: 3, ChangeID: page.RedoID, RequestID: strings.Repeat("e", 32)}
	if w = bob.call("POST", path+"/redo", input); w.Code != 404 {
		t.Fatal("redo owner", w.Code)
	}
	if w = (&client{app: a}).call("POST", path+"/redo", input); w.Code != 401 {
		t.Fatal("redo anonymous", w.Code)
	}
	if w = alice.call("GET", path+"/redo", nil); w.Code != 405 {
		t.Fatal("redo method", w.Code)
	}
	alice.csrf = "bad"
	if w = alice.call("POST", path+"/redo", input); w.Code != 403 {
		t.Fatal("redo csrf", w.Code)
	}
	alice.csrf = csrf
	if w = alice.call("POST", path+"/redo", map[string]string{"extra": "field"}); w.Code != 400 {
		t.Fatal("redo fields", w.Code)
	}
	a.queryBusy["alice"] = true
	if w = alice.call("POST", path+"/redo", input); w.Code != 429 {
		t.Fatal("redo busy", w.Code)
	}
	delete(a.queryBusy, "alice")
	if w = alice.call("POST", path+"/redo", input); w.Code != 200 {
		t.Fatal("redo", w.Code, w.Body)
	}
	if w = alice.call("POST", path+"/redo", input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("redo retry", w.Body)
	}
	input.RequestID = strings.Repeat("f", 32)
	if w = alice.call("POST", path+"/redo", input); w.Code != 409 {
		t.Fatal("redo conflict", w.Code)
	}
	w = alice.call("GET", path, nil)
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Map.Revision != 4 || len(view.Nodes) != 2 {
		t.Fatal("redo restored", w.Body)
	}

}
