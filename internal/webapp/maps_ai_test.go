package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapAIHTTPAndDurableWorker(t *testing.T) {
	a := testApp(t, false)
	c, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c         *client
		name, key string
	}{{c, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.name, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "AI", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, ".mem")
	created, err := mem.CreateWebMapIn(context.Background(), dir, "alice", mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	view, err := mem.ReadWebMapIn(context.Background(), dir, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps/" + created.ID
	start := path + "/nodes/" + view.Map.RootNodeID + "/ai"
	for _, url := range []string{"/api/v1/workspaces/" + base.ID + "/maps/from-sources", start, path + "/ai/" + strings.Repeat("c", 32) + "/cancel", path + "/ai/" + strings.Repeat("c", 32) + "/apply"} {
		if w := bob.call("POST", url, map[string]any{}); w.Code != 404 {
			t.Fatal("owner", w.Code)
		}
		csrf := c.csrf
		c.csrf = "wrong"
		w := c.call("POST", url, map[string]any{})
		c.csrf = csrf
		if w.Code != 403 {
			t.Fatal("csrf", w.Code)
		}
		if w = c.call("POST", url, map[string]string{"path": "/private", "actor": "bob"}); w.Code != 400 {
			t.Fatal("fields", w.Code)
		}
	}
	for _, url := range []string{path + "/ai", path + "/ai/" + strings.Repeat("c", 32)} {
		if w := bob.call("GET", url, nil); w.Code != 404 {
			t.Fatal("read owner", w.Code)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Error("unexpected provider request", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": `{"nodes":[{"ref":"n","parent_ref":"","label":"Идея","summary":"Без источников","body_markdown":"","kind":"subtopic","citations":[]}]}`}})
	}))
	defer server.Close()
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.Model = "fake-instruct"
	cfg.Answer.BaseURL = server.URL
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	in := mem.WebMapAIInput{RequestID: strings.Repeat("c", 32), Revision: 1, Action: mem.ClassicMindMapAIExpand, Prompt: "Предложи идею", AllowUngrounded: true}
	w := c.call("POST", start, in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if worked, e := a.jobs.ProcessNext(context.Background()); e != nil || !worked {
		t.Fatal("worker", worked, e)
	}
	w = c.call("GET", path+"/ai/"+in.RequestID, nil)
	var review mem.WebMapAIReview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil || review.Grounded || len(review.Proposals) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	w = c.call("POST", path+"/ai/"+in.RequestID+"/apply", mem.WebMapAIApply{RequestID: strings.Repeat("d", 32), Revision: 1, Digest: review.Digest, ProposalIDs: []string{review.Proposals[0].ID}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = c.call("GET", path, nil)
	if !strings.Contains(w.Body.String(), "Идея") {
		t.Fatal("applied node missing", w.Body)
	}
}
