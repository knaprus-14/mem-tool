package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebSourceMapHTTPUploadToAutomaticMap(t *testing.T) {
	a := testApp(t, true)
	c := &client{app: a}
	c.session(t)
	ctx := context.Background()
	base, err := a.manager.Create(ctx, workspace.Actor{ID: "local"}, "Source map", "")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/embeddings" {
			w.Write([]byte(`{"embedding":[1,0,0]}`))
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Error(r.URL.Path)
		}

		var request struct {
			Options struct {
				MaxTokens int `json:"num_predict"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		n := calls.Add(1)
		if n == 1 {
			if request.Options.MaxTokens != 4096 {
				t.Errorf("initial budget: %d", request.Options.MaxTokens)
			}
			json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "length", "message": map[string]string{"role": "assistant", "content": "{partial"}})
			return
		}
		if n != 2 || request.Options.MaxTokens != 8128 {
			t.Errorf("retry %d budget: %d", n, request.Options.MaxTokens)
		}
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": `{"nodes":[{"ref":"p","parent_ref":"","label":"Обзор источника","summary":"По документу","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`}})
	}))
	defer server.Close()
	dir := filepath.Join(base.Root, ".mem")
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ollama.BaseURL = server.URL
	cfg.Answer.BaseURL = server.URL
	cfg.Answer.Model = "fixture-model"
	cfg.Answer.MaxTokens = 8128
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if w := c.upload(t, base.ID, "source.md", "# Устройство\n\nОсновные части и принцип работы устройства."); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	if worked, e := a.jobs.ProcessNext(ctx); e != nil || !worked {
		t.Fatal(worked, e)
	}
	library, err := workspace.ReadLibrary(ctx, base, workspace.Query{Kind: "documents", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps/from-sources"
	in := mem.WebMapFromSources{RequestID: strings.Repeat("e", 32), Documents: []string{library.Documents[0].ID}, Detail: "brief"}
	csrf := c.csrf
	c.csrf = "bad"
	if w := c.call("POST", path, in); w.Code != 403 {
		t.Fatal(w.Code)
	}
	c.csrf = csrf
	if w := c.call("POST", path, map[string]any{"path": "C:/private", "documents": in.Documents}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := c.call("POST", path, in)
	var task mem.WebMapAITask
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &task) != nil || !task.SourceOverview {
		t.Fatal(w.Code, w.Body)
	}
	secondInput := in
	secondInput.RequestID = strings.Repeat("f", 32)
	secondResponse := c.call("POST", path, secondInput)
	var second mem.WebMapAITask
	if secondResponse.Code != 202 || json.Unmarshal(secondResponse.Body.Bytes(), &second) != nil {
		t.Fatal(secondResponse.Code, secondResponse.Body)
	}
	checkQueue := func(position int, headStatus string) {
		t.Helper()
		response := c.call("GET", "/api/v1/workspaces/"+base.ID+"/maps/"+second.MapID+"/ai", nil)
		var tasks []mem.WebMapAITask
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &tasks) != nil || len(tasks) != 1 || tasks[0].Queue == nil {
			t.Fatal(response.Code, response.Body)
		}
		q := tasks[0].Queue
		if q.Position != position || q.Ahead != position-1 || q.Pending != position {
			t.Fatalf("queue: %+v", q)
		}
		if position > 1 && (q.Head == nil || q.Head.MapID != task.MapID || q.Head.Status != headStatus) {
			t.Fatalf("queue head: %+v", q.Head)
		}
		if strings.Contains(response.Body.String(), base.Root) || strings.Contains(response.Body.String(), "entries") {
			t.Fatal("private source data in queue")
		}
	}
	checkQueue(2, "queued")
	if calls.Load() != 0 {
		t.Fatal("reading queue started model work")
	}
	for i := 0; i < 2; i++ {
		if worked, e := a.jobs.ProcessNext(ctx); e != nil || !worked {
			t.Fatal(i, worked, e)
		}
		if i == 0 {
			checkQueue(2, "ready")
		}
	}
	checkQueue(1, "")
	if calls.Load() != 2 {
		t.Fatalf("model calls: %d", calls.Load())
	}
	view, err := mem.ReadWebMapIn(ctx, dir, task.MapID)
	if err != nil || len(view.Nodes) != 2 {
		t.Fatal(view, err)
	}
	w = c.call("POST", path, in)
	var replay mem.WebMapAITask
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || replay.MapID != task.MapID || replay.Status != "applied" {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), base.Root) || strings.Contains(w.Body.String(), "source_path") {
		t.Fatal("private path in API")
	}
}
