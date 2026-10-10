package webapp

import (
	"context"
	"encoding/json"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"strings"
	"testing"
)

func TestWebMapDeleteHTTP(t *testing.T) {
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
	base, e := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Maps", "")
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", path, mem.WebMapCreate{Title: "Delete", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	path += "/" + created.ID
	in := mem.WebMapDelete{Revision: 1, RequestID: strings.Repeat("c", 32)}
	if w = bob.call("POST", path+"/delete", in); w.Code != 404 {
		t.Fatal("foreign", w.Code)
	}
	if w = (&client{app: a}).call("POST", path+"/delete", in); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
	csrf := alice.csrf
	alice.csrf = "invalid"
	if w = alice.call("POST", path+"/delete", in); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	alice.csrf = csrf
	if w = alice.call("POST", path+"/delete", map[string]any{"revision": 1, "request_id": in.RequestID, "force": true}); w.Code != 400 {
		t.Fatal("strict decode", w.Code)
	}
	if w = alice.call("POST", path+"/delete", mem.WebMapDelete{Revision: 2, RequestID: in.RequestID}); w.Code != 409 {
		t.Fatal("stale", w.Code)
	}
	for i := 0; i < 2; i++ {
		w = alice.call("POST", path+"/delete", in)
		var result mem.WebMapNodeEdited
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Replayed != (i == 1) {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w = alice.call("GET", path, nil); w.Code != 404 {
		t.Fatal("deleted readable", w.Code)
	}
}
