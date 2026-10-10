package mem

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func queryFixture(t *testing.T) (string, *Store, *Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultLocalConfig()
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return dir, s, cfg
}

func TestReadOnlyQueryWordsFiltersAndEvidencePin(t *testing.T) {
	dir, s, cfg := queryFixture(t)
	// Text search must work across stored backends with no model endpoint.
	cfg.Ollama.BaseURL = "http://127.0.0.1:1"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	one, err := s.Add("needle pressure valve", "Manual", []string{"keep"}, "old-backend", []float32{1, 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Add("needle unrelated", "Other", []string{"other"}, "another", []float32{0, 1}, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private-directory", "manual.pdf")
	if _, err = s.db.Exec(`UPDATE entries SET source_file=?,source_path=?,document_id=?,page=7,created='2026-09-09T12:00:00Z' WHERE id=?`, path, path, "web:"+strings.Repeat("a", 32), one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO document_import_runs(requested_path,status,owner_pid,started_at) VALUES('other-process','running',0,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err = s.db.QueryRow(`SELECT generation FROM entry_fts_state`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	q := QueryRequest{Query: "needle", Mode: "text", Tags: []string{"keep"}, Important: true, From: "2026-09-09", To: "2026-09-09", PageFrom: 7, PageTo: 7, Document: "web:" + strings.Repeat("a", 32)}
	got, err := QueryIn(context.Background(), dir, q, false)
	if err != nil || len(got.Hits) != 1 || got.Hits[0].ID != one.ID || got.Hits[0].Source != "manual.pdf" {
		t.Fatalf("query: %+v %v", got, err)
	}
	after, err := os.ReadFile(StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("query wrote store.db")
	}
	var afterGeneration int64
	var status string
	if err = s.db.QueryRow(`SELECT generation FROM entry_fts_state`).Scan(&afterGeneration); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT status FROM document_import_runs WHERE requested_path='other-process'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if generation != afterGeneration || status != "running" {
		t.Fatal("query rebuilt FTS or recovered another process")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private-directory") || strings.Contains(string(encoded), "source_path") {
		t.Fatalf("query leaked paths: %s", encoded)
	}
	hit := got.Hits[0]
	if evidence, err := ReadQueryEvidenceIn(context.Background(), dir, hit.ID, hit.Pin); err != nil || evidence.Text != one.Text {
		t.Fatalf("evidence: %+v %v", evidence, err)
	}
	if _, err = s.db.Exec(`UPDATE entries SET text='changed' WHERE id=?`, one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadQueryEvidenceIn(context.Background(), dir, hit.ID, hit.Pin); !errors.Is(err, ErrQueryStale) {
		t.Fatalf("stale citation: %v", err)
	}
}

func TestQueryVectorIdentityAndSharedRanking(t *testing.T) {
	dir, s, cfg := queryFixture(t)
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"embedding":[1,0]}`) }))
	defer model.Close()
	cfg.Ollama.BaseURL = model.URL
	cfg.Ollama.Model = "test-vector"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	identity, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.AddWithEmbeddingIdentity("valve manual", "", []string{"pump"}, identity, []float32{1, 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	old := identity
	old.SpaceID = "sha256:" + strings.Repeat("b", 64)
	if _, err = s.AddWithEmbeddingIdentity("valve old model", "", nil, old, []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	q := QueryRequest{Query: "valve", Mode: "vector"}
	got, err := QueryIn(context.Background(), dir, q, false)
	if err != nil || len(got.Hits) != 1 || got.Hits[0].ID != current.ID || got.Incompatible != 1 || calls.Load() != 1 {
		t.Fatalf("vector space: %+v %v", got, err)
	}
	cli, err := s.SearchWithOptions(SearchOptions{Query: "valve", QueryVector: []float32{1, 0}, Backend: identity.Backend, EmbeddingSpace: identity.SpaceID})
	if err != nil {
		t.Fatal(err)
	}
	ReRankResults(cli, "valve")
	SortSearchResults(cli)
	q.Mode = "hybrid"
	got, err = QueryIn(context.Background(), dir, q, false)
	if err != nil || got.LexicalMode != lexicalFTS5 || len(got.Hits) != len(cli) {
		t.Fatalf("shared FTS: %+v %v", got, err)
	}
	for i, hit := range got.Hits {
		if hit.ID != cli[i].ID || hit.Score != cli[i].Score {
			t.Fatalf("ranking differs: %+v %+v", hit, cli[i])
		}
	}
}

func TestQueryAnswersValidateCitationsAndDetectChangedSources(t *testing.T) {
	for _, scenario := range []string{"valid", "uncited", "unknown", "insufficient", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			dir, s, cfg := queryFixture(t)
			entry, err := s.Add("needle valve pressure is 5 bar", "Manual", nil, "legacy", []float32{1, 0}, false)
			if err != nil {
				t.Fatal(err)
			}
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					t.Errorf("unexpected provider endpoint %s", r.URL.Path)
				}
				payload, _ := io.ReadAll(r.Body)
				if strings.Contains(string(payload), dir) {
					t.Error("server path sent to model")
				}
				content := `{"claims":[{"text":"Pressure is 5 bar","citations":["E1"]}]}`
				switch scenario {
				case "uncited":
					content = `{"claims":[{"text":"UNSUPPORTED SECRET OUTPUT","citations":[]}]}`
				case "unknown":
					content = `{"claims":[{"text":"UNKNOWN OUTPUT","citations":["entry-999"]}]}`
				case "insufficient":
					content = `{"insufficient_evidence":"Not enough evidence"}`
				case "stale":
					if _, e := s.db.Exec(`UPDATE entries SET tags='["edited"]' WHERE id=?`, entry.ID); e != nil {
						t.Error(e)
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}, "done": true})
			}))
			defer model.Close()
			cfg.Answer.BaseURL = model.URL
			cfg.Answer.Model = "test-chat"
			if err = SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			got, err := QueryIn(context.Background(), dir, QueryRequest{Query: "needle", Mode: "text"}, true)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "valid":
				if got.Status != "answered" || len(got.Sources) != 1 || !strings.Contains(got.Answer, "[1]") || got.Sources[0].ID != entry.ID {
					t.Fatal(got)
				}
			case "insufficient":
				if got.Status != "insufficient" || len(got.Sources) != 0 {
					t.Fatal(got)
				}
			case "stale":
				if got.Status != "stale" || strings.Contains(got.Answer, "Pressure is 5") {
					t.Fatal(got)
				}
			default:
				if got.Status != "rejected" || strings.Contains(got.Answer, "OUTPUT") {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestQueryCancellationInvalidInputsAndBounds(t *testing.T) {
	dir, s, cfg := queryFixture(t)
	if _, err := s.Add("needle", "", nil, "ollama", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	for _, q := range []QueryRequest{{Query: ""}, {Query: "x", Mode: "bad"}, {Query: "x", Limit: 51}, {Query: "x", From: "2026-13-01"}, {Query: "x", From: "2026-09-09", To: "2026-09-08"}, {Query: "x", PageFrom: 8, PageTo: 7}, {Query: strings.Repeat("x", 2001)}} {
		if _, err := QueryIn(context.Background(), dir, q, false); !errors.Is(err, ErrQueryInvalid) {
			t.Fatalf("invalid query accepted: %+v %v", q, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := QueryIn(ctx, dir, QueryRequest{Query: "needle", Mode: "text"}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer model.Close()
	defer close(release)
	cfg.Ollama.BaseURL = model.URL
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := QueryIn(ctx, dir, QueryRequest{Query: "needle"}, false); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("model not called")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model request ignored cancellation")
	}
	// The bound is checked before materializing the corpus or calling a model.
	if _, err := s.db.Exec(`UPDATE entries SET text=zeroblob(67108865)`); err != nil {
		t.Fatal(err)
	}
	if _, err := QueryIn(context.Background(), dir, QueryRequest{Query: "needle", Mode: "text"}, false); !errors.Is(err, ErrQueryLimit) {
		t.Fatalf("large corpus: %v", err)
	}
}
