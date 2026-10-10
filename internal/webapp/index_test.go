package webapp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestIndexHTTPIsolationAndCancel(t *testing.T) {
	a := testApp(t, false)
	c, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c         *client
		name, key string
	}{{c, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.name, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Index", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, ".mem")
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Add("fixture", "title", nil, "old", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/index"
	for _, action := range []string{"start", "apply", "rollback", "cancel"} {
		if w := bob.call("POST", prefix+"/"+action, map[string]any{}); w.Code != 404 {
			t.Fatal("owner", w.Code)
		}
		csrf := c.csrf
		c.csrf = "bad"
		w := c.call("POST", prefix+"/"+action, map[string]any{})
		c.csrf = csrf
		if w.Code != 403 {
			t.Fatal("csrf", w.Code)
		}
		if w = c.call("POST", prefix+"/"+action, map[string]any{"path": "C:/private", "actor": "bob"}); w.Code != 400 {
			t.Fatal("fields", w.Code)
		}
	}
	if w := bob.call("GET", prefix, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	in := mem.WebIndexInput{RequestID: strings.Repeat("c", 32), ConfigRevision: cfg.Revision()}
	w := c.call("POST", prefix+"/start", in)
	var state mem.WebIndexState
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Status != "preparing" {
		t.Fatal(w.Code, w.Body)
	}
	w = c.call("POST", prefix+"/apply", mem.WebIndexInput{ID: state.ID})
	if w.Code != 409 {
		t.Fatal("partial apply", w.Code)
	}
	w = c.call("POST", prefix+"/cancel", mem.WebIndexInput{ID: state.ID})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatal(w.Code, w.Body)
	}
}
