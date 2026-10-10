package mem

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebGraphRejectsMissingAnswerModelBeforeQueue(t *testing.T) {
	for _, model := range []string{"", " bge-m3:latest "} {
		t.Run(model, func(t *testing.T) {
			dir, _, cfg := queryFixture(t)
			cfg.Answer.Model = model
			if err := SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"graph-extract", "graph-analyze"} {
				in := WebOperationInput{RequestID: strings.Repeat("a", 32), Kind: kind, ConfigRevision: cfg.Revision(), Focus: "topics", Batches: 1, ContextChars: 12000}
				if _, err := StartWebOperationIn(context.Background(), dir, "alice", in, true); !errors.Is(err, ErrWebGraphAnswerModel) {
					t.Fatal(kind, err)
				}
			}
			jobs, err := ListWebOperationsIn(context.Background(), dir, "alice")
			if err != nil || len(jobs) != 0 {
				t.Fatal("invalid job queued", jobs, err)
			}
		})
	}
}

func TestWebGraphLegacyJobExplainsMissingModelAndRejectsRetry(t *testing.T) {
	ctx := context.Background()
	dir, s, cfg := queryFixture(t)
	cfg.Answer.Model = "test-chat"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	in := WebOperationInput{RequestID: strings.Repeat("b", 32), Kind: "graph-extract", ConfigRevision: cfg.Revision(), Focus: "topics", Batches: 1, ContextChars: 12000}
	op, err := StartWebOperationIn(ctx, dir, "alice", in, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.Model = ""
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	// Reproduce an alpha.30 queued job whose pinned configuration had no model.
	in.ConfigRevision = cfg.Revision()
	encoded, _ := json.Marshal(in)
	if _, err = s.db.Exec(`UPDATE web_operations SET input=? WHERE id=?`, string(encoded), op.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := ProcessWebOperationIn(ctx, dir, "alice"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	jobs, err := ListWebOperationsIn(ctx, dir, "alice")
	if err != nil || len(jobs) != 1 || jobs[0].Status != "failed" || jobs[0].Error != ErrWebGraphAnswerModel.Error() {
		t.Fatal(jobs, err)
	}
	if err = ActWebOperationIn(ctx, dir, "alice", op.ID, "retry"); !errors.Is(err, ErrWebGraphAnswerModel) {
		t.Fatal(err)
	}
	if err = ActWebOperationIn(ctx, dir, "bob", op.ID, "retry"); !errors.Is(err, ErrWebOperationInvalid) {
		t.Fatal("foreign job", err)
	}
}

func TestWebGraphRejectedBatchShowsCheckpointRecoveryMessage(t *testing.T) {
	s, _ := newKnowledgeExtractionJobStore(t, []string{"A requirement."})
	defer s.Close()
	dir := filepath.Dir(s.Path())
	cfg := DefaultLocalConfig()
	cfg.Answer.Model = "test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": `{"requirements":[]}`}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	cfg.Answer.BaseURL = server.URL
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	in := WebOperationInput{RequestID: strings.Repeat("c", 32), Kind: "graph-extract", ConfigRevision: cfg.Revision(), Focus: "topics", Batches: 1, ContextChars: 12000}
	if _, err := StartWebOperationIn(context.Background(), dir, "local", in, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessWebOperationIn(context.Background(), dir, "local"); err != nil {
		t.Fatal(err)
	}
	jobs, err := ListWebOperationsIn(context.Background(), dir, "local")
	if err != nil || len(jobs) != 1 || jobs[0].Status != "failed" || !strings.Contains(jobs[0].Error, "пакете 1") || !strings.Contains(jobs[0].Error, "Продолжить") || strings.Contains(jobs[0].Error, server.URL) {
		t.Fatal(jobs, err)
	}
}
