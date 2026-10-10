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

func TestWebSourceMapHTTPReassemblyOfSavedParts(t *testing.T) {
	testWebSourceMapHTTPContinue(t, "reassemble")
}

func TestWebSourceMapHTTPResumeOfSavedParts(t *testing.T) {
	testWebSourceMapHTTPContinue(t, "resume")
}

func testWebSourceMapHTTPContinue(t *testing.T, action string) {
	a := testApp(t, true)
	c := &client{app: a}
	c.session(t)
	ctx := context.Background()
	base, err := a.manager.Create(ctx, workspace.Actor{ID: "local"}, "Reassembly", "")
	if err != nil {
		t.Fatal(err)
	}
	var allowGrouping atomic.Bool
	var extractionCalls, groupingCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/embeddings" {
			w.Write([]byte(`{"embedding":[1,0,0]}`))
			return
		}
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		prompt := ""
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		content := ""
		if strings.Contains(prompt, "SYNTHESIS_INPUT_JSON_BEGIN\n") {
			groupingCalls.Add(1)
			if !allowGrouping.Load() {
				content = `{"groups":[]}`
			} else {
				raw := strings.Split(strings.Split(prompt, "SYNTHESIS_INPUT_JSON_BEGIN\n")[1], "\nSYNTHESIS_INPUT_JSON_END")[0]
				var payload struct {
					Roots []string `json:"roots"`
				}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Error(err)
					return
				}
				encoded, _ := json.Marshal(map[string]any{"groups": []any{map[string]any{"title": "Общая тема", "members": payload.Roots}}})
				content = string(encoded)
			}
		} else {
			extractionCalls.Add(1)
			parts := strings.Split(prompt, "EVIDENCE_JSON_BEGIN\n")
			if len(parts) != 2 {
				t.Error("missing evidence")
				return
			}
			raw := strings.Split(parts[1], "\nEVIDENCE_JSON_END")[0]
			var evidence []struct {
				Ref string `json:"evidence_ref"`
			}
			if err := json.Unmarshal([]byte(raw), &evidence); err != nil || len(evidence) == 0 {
				t.Error("invalid evidence")
				return
			}
			encoded, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"ref": "root", "parent_ref": "", "label": "Часть документа", "summary": "Условие", "body_markdown": "", "kind": "topic", "citations": []string{evidence[0].Ref}}}})
			content = string(encoded)
		}
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": content}})
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
	cfg.Answer.ContextChars = 6500
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.md", "second.md"} {
		if w := c.upload(t, base.ID, name, "# "+name+"\n\n"+strings.Repeat("Проверяем условия документа. ", 110)); w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
		if worked, e := a.jobs.ProcessNext(ctx); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	library, err := workspace.ReadLibrary(ctx, base, workspace.Query{Kind: "documents", Limit: 30})
	if err != nil || len(library.Documents) != 2 {
		t.Fatal(library, err)
	}
	in := mem.WebMapFromSources{RequestID: strings.Repeat("d", 32), Documents: []string{library.Documents[0].ID, library.Documents[1].ID}}
	basePath := "/api/v1/workspaces/" + base.ID + "/maps/"
	w := c.call("POST", basePath+"from-sources", in)
	var task mem.WebMapAITask
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if worked, e := mem.ProcessWebMapAIIn(ctx, dir, "local"); e != nil || !worked {
		t.Fatal(worked, e)
	}
	get := func() mem.WebMapAITask {
		t.Helper()
		w := c.call("GET", basePath+task.MapID+"/ai", nil)
		var tasks []mem.WebMapAITask
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tasks) != nil || len(tasks) != 1 {
			t.Fatal(w.Code, w.Body)
		}
		return tasks[0]
	}
	failed := get()
	if !failed.CanReassemble || failed.Total < 2 || failed.Status != "failed" {
		t.Fatal(failed)
	}
	calls := extractionCalls.Load()
	route := basePath + task.MapID + "/ai/" + task.ID + "/" + action
	csrf := c.csrf
	c.csrf = "bad"
	if w := c.call("POST", route, map[string]any{}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	c.csrf = csrf
	if w := c.call("POST", route, map[string]any{"run_id": "foreign"}); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	for i := 0; i < 2; i++ {
		if w := c.call("POST", route, map[string]any{}); w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	allowGrouping.Store(true)
	for i := 0; i < 2; i++ {
		if worked, e := mem.ProcessWebMapAIIn(ctx, dir, "local"); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	if got := get(); got.Status != "applied" || got.CanReassemble {
		t.Fatal(got)
	}
	if extractionCalls.Load() != calls || groupingCalls.Load() < 2 {
		t.Fatal("reassembly repeated extraction")
	}
	view, err := mem.ReadWebMapIn(ctx, dir, task.MapID)
	if err != nil || view.Map.Revision != 2 || len(view.Nodes) < 4 {
		t.Fatal(view, err)
	}
}
