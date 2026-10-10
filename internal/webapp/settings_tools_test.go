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

func TestSettingsToolsIsolationPreviewAndModel(t *testing.T) {
	a := testApp(t, false)
	c := &client{app: a}
	c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": strings.Repeat("a", 40)})
	c.session(t)
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Tools", "")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/settings/"
	bob := &client{app: a}
	bob.call("POST", "/api/v1/login", map[string]string{"user": "bob", "token": strings.Repeat("b", 40)})
	bob.session(t)
	for _, action := range []string{"preview", "check-model"} {
		if w := bob.call("POST", prefix+action, map[string]any{}); w.Code != 404 {
			t.Fatal("owner", action, w.Code)
		}
		csrf := c.csrf
		c.csrf = "bad"
		w := c.call("POST", prefix+action, map[string]any{})
		c.csrf = csrf
		if w.Code != 403 {
			t.Fatal("csrf", action, w.Code)
		}
		if w = c.call("POST", prefix+action, map[string]string{"base_url": "http://example.org", "api_key": "x"}); w.Code != 400 {
			t.Fatal("endpoint accepted", w.Code)
		}
	}
	in := chunkPreviewInput{Text: strings.Repeat("Пример. ", 100), Size: 100, Overlap: 10, Strategy: "sentence"}
	w := c.call("POST", prefix+"preview", in)
	var preview struct {
		Chunks []mem.Chunk `json:"chunks"`
		Total  int         `json:"total"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Total != len(mem.ChunkDocument(in.Text, in.Size, in.Overlap, in.Strategy)) {
		t.Fatal(w.Code, w.Body)
	}
	in.Overlap = in.Size
	if w = c.call("POST", prefix+"preview", in); w.Code != 400 {
		t.Fatal("overlap", w.Code)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Error("unexpected request", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[1,0,1]}`))
	}))
	defer server.Close()
	dir := filepath.Join(base.Root, ".mem")
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ollama.BaseURL = server.URL
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	w = c.call("POST", prefix+"check-model", map[string]string{"revision": cfg.Revision(), "role": "embedding"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dimensions":3`) {
		t.Fatal(w.Code, w.Body)
	}
	w = c.call("POST", prefix+"check-model", map[string]string{"revision": "old", "role": "embedding"})
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}
