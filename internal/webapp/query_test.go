package webapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestQueryHTTPIsolationSourcesErrorsAndBusyLimit(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, user := range []struct {
		c         *client
		name, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if res := user.c.call("POST", "/api/v1/login", map[string]string{"user": user.name, "token": user.key}); res.Code != 200 {
			t.Fatal(res.Body)
		}
		user.c.session(t)
	}
	w, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Query tests", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.Root, ".mem")
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry, err := s.Add("needle useful evidence", "Private note", []string{"test"}, "legacy", []float32{1, 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/workspaces/" + w.ID + "/"
	q := mem.QueryRequest{Query: "needle", Mode: "text"}
	res := alice.call("POST", base+"search", q)
	var result mem.QueryResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || len(result.Hits) != 1 || result.Hits[0].ID != entry.ID {
		t.Fatalf("search: %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), w.Root) || strings.Contains(res.Body.String(), "embedding_space") || strings.Contains(res.Body.String(), "source_path") {
		t.Fatal("private implementation data leaked")
	}
	hit := result.Hits[0]
	evidence := map[string]any{"id": hit.ID, "pin": hit.Pin}
	if res = alice.call("POST", base+"evidence", evidence); res.Code != 200 || !strings.Contains(res.Body.String(), entry.Text) {
		t.Fatalf("evidence: %d %s", res.Code, res.Body)
	}
	for _, route := range []string{"search", "ask", "evidence"} {
		if res = bob.call("POST", base+route, q); res.Code != 404 {
			t.Fatalf("cross-owner %s: %d", route, res.Code)
		}
		if res = (&client{app: a}).call("POST", base+route, q); res.Code != 401 {
			t.Fatalf("anonymous %s: %d", route, res.Code)
		}
		csrf := alice.csrf
		alice.csrf = "bad"
		res = alice.call("POST", base+route, q)
		alice.csrf = csrf
		if res.Code != 403 {
			t.Fatalf("CSRF %s: %d", route, res.Code)
		}
	}
	if res = alice.call("GET", base+"search", nil); res.Code != 405 {
		t.Fatal("GET accepted a query")
	}
	if res = alice.call("POST", base+"search", map[string]any{"query": "needle", "mode": "text", "base_url": "http://evil.invalid"}); res.Code != 400 {
		t.Fatal("client model endpoint override accepted")
	}
	a.mu.Lock()
	a.queryBusy["alice"] = true
	a.mu.Unlock()
	if res = alice.call("POST", base+"search", q); res.Code != 429 {
		t.Fatalf("per-user limit: %d", res.Code)
	}
	a.mu.Lock()
	delete(a.queryBusy, "alice")
	a.mu.Unlock()
	a.queries <- struct{}{}
	a.queries <- struct{}{}
	if res = alice.call("POST", base+"search", q); res.Code != 429 {
		t.Fatalf("global limit: %d", res.Code)
	}
	<-a.queries
	<-a.queries
	// Retrieval remains visible when the answer model is unavailable.
	if res = alice.call("POST", base+"ask", q); res.Code != 200 || !strings.Contains(res.Body.String(), `"status":"unavailable"`) || !strings.Contains(res.Body.String(), entry.Text) {
		t.Fatalf("missing answer model: %d %s", res.Code, res.Body)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embeddings" {
			w.WriteHeader(500)
			io.WriteString(w, "SECRET provider path")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"claims":[{"text":"Useful evidence","citations":["E1"]}]}`}, "done": true})
	}))
	defer model.Close()
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.Model = "test-chat"
	cfg.Answer.BaseURL = model.URL
	cfg.Ollama.BaseURL = model.URL
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if res = alice.call("POST", base+"ask", q); res.Code != 200 || !strings.Contains(res.Body.String(), `"status":"answered"`) || !strings.Contains(res.Body.String(), "[1]") {
		t.Fatalf("answer: %d %s", res.Code, res.Body)
	}
	q.Mode = "hybrid"
	if res = alice.call("POST", base+"search", q); res.Code != 502 || strings.Contains(res.Body.String(), "SECRET") {
		t.Fatalf("provider failure: %d %s", res.Code, res.Body)
	}
}
