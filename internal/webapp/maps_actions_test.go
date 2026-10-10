package webapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapActionHTTPIsolationAndWorkflow(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Actions", "")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", prefix, mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Body)
	}
	path := prefix + "/" + created.ID
	w = alice.call("GET", path, nil)
	var view mem.WebMapView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal(w.Body)
	}
	root := path + "/nodes/" + view.Map.RootNodeID
	endpoints := []string{path + "/duplicate", root + "/branch", root + "/ask", path + "/compare", root + "/move", root + "/delete", root + "/properties", root + "/source-add", root + "/source-remove", root + "/source-move", path + "/metadata", path + "/export", path + "/snapshots"}
	for _, url := range endpoints {
		if w = bob.call("POST", url, map[string]any{}); w.Code != 404 {
			t.Fatal("foreign", url, w.Code)
		}
		if w = (&client{app: a}).call("POST", url, map[string]any{}); w.Code != 401 {
			t.Fatal("anonymous", url, w.Code)
		}
		csrf := alice.csrf
		alice.csrf = "invalid"
		w = alice.call("POST", url, map[string]any{})
		alice.csrf = csrf
		if w.Code != 403 {
			t.Fatal("csrf", url, w.Code)
		}
		if w = alice.call("POST", url, map[string]any{"path": "C:/private", "actor": "bob"}); w.Code != 400 {
			t.Fatal("unknown fields", url, w.Code)
		}
	}
	for _, url := range []string{root + "/sources", prefix + "/candidates", path + "/snapshots"} {
		if w = bob.call("GET", url, nil); w.Code != 404 {
			t.Fatal("read isolation", url, w.Code)
		}
	}
	post := func(url string, body any) {
		t.Helper()
		w = alice.call("POST", url, body)
		if w.Code != 200 {
			t.Fatal(url, w.Code, w.Body)
		}
	}
	post(root+"/children", mem.WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("b", 32), Label: "Child"})
	var child mem.WebMapNodeEdited
	_ = json.Unmarshal(w.Body.Bytes(), &child)
	childPath := path + "/nodes/" + child.NodeID
	post(childPath+"/move", mem.WebMapAction{Revision: 2, RequestID: strings.Repeat("c", 32), ParentID: view.Map.RootNodeID, Position: 0})
	post(childPath+"/source-add", mem.WebMapSourceInput{Revision: 3, RequestID: strings.Repeat("d", 32), URL: "https://example.org/"})
	w = alice.call("GET", childPath+"/sources", nil)
	var sources mem.WebMapSourcePage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sources) != nil || len(sources.Items) != 1 {
		t.Fatal(w.Body)
	}
	post(root+"/branch", mem.WebMapBranchInput{Revision: 4})
	post(path+"/compare", mem.WebMapCompareInput{Revision: 4, OtherID: created.ID, OtherRevision: 4})
	post(path+"/metadata", mem.WebMapMetadata{Revision: 4, RequestID: strings.Repeat("e", 32), Title: "Renamed", Status: mem.ClassicMindMapStatusReady})
	post(path+"/snapshots", mem.WebMapSnapshotInput{Revision: 5, RequestID: strings.Repeat("1", 32), Reason: "Before removal"})
	w = alice.call("GET", path+"/snapshots", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Before removal") {
		t.Fatal("snapshot list", w.Code, w.Body)
	}
	post(path+"/duplicate", mem.WebMapDuplicate{Revision: 5, RequestID: strings.Repeat("2", 32), Title: "Copy"})
	post(path+"/export", map[string]any{"revision": 5, "format": "json"})
	if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Body.String(), "Renamed") {
		t.Fatal("export", w.Header(), w.Body)
	}
	if w = alice.call("POST", path+"/export", map[string]any{"revision": 4, "format": "json"}); w.Code != 409 {
		t.Fatal("export stale", w.Code)
	}
	post(childPath+"/delete", mem.WebMapAction{Revision: 5, RequestID: strings.Repeat("f", 32), Mode: mem.ClassicMindMapDeleteBranch})
	w = alice.call("GET", path, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if len(view.Nodes) != 1 || view.Map.Revision != 6 {
		t.Fatal("deleted", w.Body)
	}
}
