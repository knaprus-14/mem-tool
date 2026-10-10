package webapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestSettingsEditingIsolationValidationAndPersistence(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(map[bool]string{true: "local", false: "server"}[local], func(t *testing.T) {
			a := testApp(t, local)
			a.cfg.Models = []ModelChoice{{Role: "embedding", Backend: "ollama", Model: "new-embed"}, {Role: "answer", Backend: "ollama", Model: "new-chat"}}
			c := &client{app: a}
			actor := workspace.Actor{ID: "local"}
			if !local {
				actor.ID = "alice"
				if w := c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": strings.Repeat("a", 40)}); w.Code != 200 {
					t.Fatal(w.Body)
				}
			}
			c.session(t)
			base, err := a.manager.Create(context.Background(), actor, "Settings fixture", "")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(base.Root, ".mem")
			cfg, err := mem.LoadConfigIn(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Polza.APIKey = "hidden-secret"
			cfg.Ingest.Python = "private-tool-path"
			cfg.Answer.Temperature = 0.25
			if err = mem.SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			store, err := mem.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.Add("keep evidence", "Fixture", []string{"tag"}, "old", []float32{1, 0}, true); err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			dbBefore, err := os.ReadFile(mem.StorePathIn(dir))
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/workspaces/" + base.ID + "/settings"
			read := func() settingsView {
				t.Helper()
				w := c.call("GET", path, nil)
				var view settingsView
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
					t.Fatalf("read: %d %s", w.Code, w.Body)
				}
				for _, forbidden := range []string{"hidden-secret", "private-tool-path", "api_key", "base_url", base.Root} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Fatal("settings leaked internals")
					}
				}
				return view
			}
			initial := read()
			body := settingsUpdate{Settings: initial.Settings, AcceptEmbeddingChange: true}
			body.EmbeddingModel = "new-embed"
			body.AnswerModel = "new-chat"
			body.ChunkSize = 800
			body.ChunkOverlap = 80
			body.ChunkStrategy = "sentence"
			body.AnswerMaxTokens = 1024
			assertRejected := func(want int, candidate any) {
				t.Helper()
				before, _ := os.ReadFile(mem.ConfigPathIn(dir))
				w := c.call("POST", path, candidate)
				if w.Code != want {
					t.Fatalf("want %d got %d: %s", want, w.Code, w.Body)
				}
				after, _ := os.ReadFile(mem.ConfigPathIn(dir))
				if string(before) != string(after) {
					t.Fatal("rejected update changed config")
				}
			}
			for _, change := range []func(*settingsUpdate){
				func(x *settingsUpdate) { x.AcceptEmbeddingChange = false }, func(x *settingsUpdate) { x.ChunkOverlap = x.ChunkSize }, func(x *settingsUpdate) { x.ChunkSize = 2001 }, func(x *settingsUpdate) { x.ChunkStrategy = "invalid" }, func(x *settingsUpdate) { x.AnswerTimeout = 0 }, func(x *settingsUpdate) { x.AnswerMaxTokens = 100001 }, func(x *settingsUpdate) { x.AnswerContextChars = -1 }, func(x *settingsUpdate) { x.EmbeddingModel = "bad\nname" }, func(x *settingsUpdate) { x.Backend = "arbitrary" },
			} {
				candidate := body
				change(&candidate)
				assertRejected(400, candidate)
			}
			csrf := c.csrf
			c.csrf = "bad"
			assertRejected(403, body)
			c.csrf = csrf
			assertRejected(400, map[string]any{"revision": body.Revision, "answer_base_url": "http://private", "api_key": "no"})
			if !local {
				candidate := body
				candidate.AnswerModel = "unlisted"
				assertRejected(400, candidate)
				candidate = body
				candidate.EmbeddingModel = "unlisted"
				assertRejected(400, candidate)
				bob := &client{app: a}
				bob.call("POST", "/api/v1/login", map[string]string{"user": "bob", "token": strings.Repeat("b", 40)})
				bob.session(t)
				for _, method := range []string{"GET", "POST"} {
					if w := bob.call(method, path, body); w.Code != 404 {
						t.Fatalf("cross-owner %s: %d", method, w.Code)
					}
				}
			}
			if w := (&client{app: a}).call("POST", path, body); w.Code != 401 {
				t.Fatal("anonymous edit allowed")
			}
			if w := c.call("PUT", path, body); w.Code != 405 {
				t.Fatal("PUT allowed")
			}
			lock, err := filelock.Try(filepath.Join(dir, "config.lock"))
			if err != nil {
				t.Fatal(err)
			}
			assertRejected(409, body)
			lock.Close()
			w := c.call("POST", path, body)
			if w.Code != 200 {
				t.Fatalf("save: %d %s", w.Code, w.Body)
			}
			saved := read()
			if saved.Revision == initial.Revision || saved.ChunkSize != 800 || saved.EmbeddingModel != "new-embed" || saved.AnswerModel != "new-chat" {
				t.Fatalf("not saved: %+v", saved)
			}
			assertRejected(409, body)
			loaded, err := mem.LoadConfigIn(dir)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Polza.APIKey != cfg.Polza.APIKey || loaded.Ingest != cfg.Ingest || loaded.Answer.BaseURL != cfg.Answer.BaseURL || loaded.Ollama.BaseURL != cfg.Ollama.BaseURL || loaded.Answer.Temperature != 0.25 {
				t.Fatal("hidden config changed")
			}
			loaded.Answer.MaxTokens = 777
			if err = mem.SaveConfigIn(dir, loaded); err != nil {
				t.Fatal(err)
			}
			assertRejected(409, settingsUpdate{Settings: saved.Settings})
			dbAfter, err := os.ReadFile(mem.StorePathIn(dir))
			if err != nil || string(dbBefore) != string(dbAfter) {
				t.Fatal("settings modified indexed data")
			}
			// Empty model explicitly disables answers; no model endpoint is contacted.
			disable := settingsUpdate{Settings: read().Settings}
			disable.AnswerModel = ""
			if w := c.call("POST", path, disable); w.Code != 200 {
				t.Fatalf("disable: %s", w.Body)
			}
		})
	}
}

func TestSettingsModelPolicyValidation(t *testing.T) {
	for _, models := range [][]ModelChoice{
		{{Role: "answer", Backend: "polza", Model: "m"}}, {{Role: "other", Backend: "ollama", Model: "m"}}, {{Role: "embedding", Backend: "ollama", Model: "bad name"}}, {{Role: "answer", Backend: "ollama", Model: "m"}, {Role: "answer", Backend: "ollama", Model: "m"}},
	} {
		cfg := Config{Local: true, Models: models}
		if err := cfg.validate(); err == nil {
			t.Fatalf("accepted policy: %+v", models)
		}
	}
}
