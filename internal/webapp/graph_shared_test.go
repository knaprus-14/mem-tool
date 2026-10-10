package webapp

import (
	"context"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"strings"
	"testing"
)

func TestSharedGraphOwnershipAndMount(t *testing.T) {
	a := testApp(t, false)
	t.Cleanup(a.closeGraphs)
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
	base, e := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Graph", "")
	if e != nil {
		t.Fatal(e)
	}
	path := "/graph/" + base.ID + "/"
	if w := bob.call("GET", path, nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	if w := (&client{app: a}).call("GET", path, nil); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	w := alice.call("GET", path, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"api_base":"`+path+`api"`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = alice.call("POST", path+"api/edit", map[string]string{}); w.Code != 403 {
		t.Fatal("missing graph capability", w.Code, w.Body)
	}
}
