package webapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestOfflineWorkspaceLocalRecoveryAndHostedQuotaFailClosed(t *testing.T) {
	for _, local := range []bool{true, false} {
		name := "hosted"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			a := testApp(t, local)
			actor := workspace.Actor{ID: "alice"}
			if local {
				actor.ID = "local"
			}
			c := &client{app: a}
			if !local {
				if w := c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": strings.Repeat("a", 40)}); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			}
			c.session(t)
			missing, err := a.manager.Create(context.Background(), actor, "external", "")
			if err != nil {
				t.Fatal(err)
			}
			healthy, err := a.manager.Create(context.Background(), actor, "healthy", "")
			if err != nil {
				t.Fatal(err)
			}
			// Both paths are inside testApp's private temporary directory.
			if err = os.Rename(missing.Root, missing.Root+".offline"); err != nil {
				t.Fatal(err)
			}
			list := c.call("GET", "/api/v1/workspaces", nil)
			if list.Code != 200 {
				t.Fatal(list.Code, list.Body)
			}
			var views []struct {
				ID      string `json:"id"`
				Offline bool   `json:"offline"`
			}
			if err = json.Unmarshal(list.Body.Bytes(), &views); err != nil {
				t.Fatal(err)
			}
			for _, v := range views {
				if v.ID == missing.ID && v.Offline != local {
					t.Fatalf("offline indicator=%v local=%v", v.Offline, local)
				}
			}
			path := "/api/v1/workspaces/" + healthy.ID
			if w := c.call("GET", path+"/library", nil); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			want := 500
			if local {
				want = 200
			}
			if w := c.call("GET", path+"/storage", nil); w.Code != want {
				t.Fatal("quota", w.Code, w.Body)
			}
			want = 500
			if local {
				want = 201
			}
			if w := c.call("POST", "/api/v1/workspaces", map[string]string{"name": "still usable"}); w.Code != want {
				t.Fatal("create", w.Code, w.Body)
			}
			if err = os.Rename(missing.Root+".offline", missing.Root); err != nil {
				t.Fatal(err)
			}
			if w := c.call("GET", path+"/storage", nil); w.Code != 200 {
				t.Fatal("reconnected", w.Code, w.Body)
			}
			list = c.call("GET", "/api/v1/workspaces", nil)
			if list.Code != 200 || strings.Contains(list.Body.String(), `"offline":true`) {
				t.Fatal("still offline after return", list.Code, list.Body)
			}
		})
	}
}

func TestEvidenceRemainsReadableAtStorageLimit(t *testing.T) {
	a := testApp(t, true)
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "local"}, "evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := mem.NewStore(filepath.Join(base.Root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Add("needle useful evidence", "note", nil, "legacy", []float32{1, 0}, true); err != nil {
		t.Fatal(err)
	}
	c := &client{app: a}
	c.session(t)
	path := "/api/v1/workspaces/" + base.ID + "/"
	w := c.call("POST", path+"search", mem.QueryRequest{Query: "needle", Mode: "text"})
	var result mem.QueryResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Hits) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	evidence := map[string]any{"id": result.Hits[0].ID, "pin": result.Hits[0].Pin}
	a.cfg.MaxWorkspaceBytes = 1
	a.cfg.MaxUserBytes = 1
	if w = c.call("POST", path+"search", mem.QueryRequest{Query: "needle", Mode: "text"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = c.call("POST", path+"evidence", evidence); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = c.call("POST", path+"notes", map[string]any{}); w.Code != 507 {
		t.Fatal("write bypassed quota", w.Code, w.Body)
	}
}
