package mem

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebGraphFullDocumentContinuesPortionsAndResumes(t *testing.T) {
	for _, stopAt := range []string{"none", "checkpoint", "committed portion"} {
		t.Run(stopAt, func(t *testing.T) {
			s, entries := newKnowledgeExtractionJobStore(t, []string{strings.Repeat("First requirement. ", 250), strings.Repeat("Second requirement. ", 250), strings.Repeat("Third requirement. ", 250)})
			defer s.Close()
			scope := KnowledgeCoverageOptions{Document: entries[0].DocumentID}
			plan, err := s.BuildKnowledgeExtractionJobPlan("full document", scope, 12000, 1)
			if err != nil || plan.SelectedChunks != 1 || plan.RemainingChunks != 2 {
				t.Fatalf("fixture plan: %+v %v", plan, err)
			}
			// A second document must never enter this operation.
			other := knowledgeExtractionJobChunks([]string{"Unrelated private document."}, "other revision")
			other[0].Provenance.DocumentID = "other"
			other[0].Provenance.SourcePath = "C:/docs/other.pdf"
			if err = s.ReplaceDocumentChunks(other[0].Provenance.SourcePath, other); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "Unrelated private document") {
					t.Error("document scope leaked")
				}
				citation := ""
				for _, entry := range entries {
					id, _ := CitationForEntry(entry)
					if strings.Contains(string(data), id) {
						citation = id
						break
					}
				}
				if citation == "" {
					t.Error("no document evidence in request")
				}
				reply := `{"nodes":[{"ref":"fact","kind":"claim","label":"Requirement","confidence":0.9,"citations":["` + citation + `"]}],"edges":[]}`
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}, "done": true, "done_reason": "stop"})
			}))
			defer model.Close()
			cfg := DefaultLocalConfig()
			cfg.Answer.Model = "test"
			cfg.Answer.BaseURL = model.URL
			dir := filepath.Dir(s.Path())
			if err = SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			input := WebOperationInput{RequestID: strings.Repeat("a", 32), Kind: "graph-extract", ConfigRevision: cfg.Revision(), Document: entries[0].DocumentID, Focus: "full document", Batches: 1, ContextChars: 12000, AllChunks: true}
			op, err := StartWebOperationIn(context.Background(), dir, "local", input, true)
			if err != nil {
				t.Fatal(err)
			}
			if !op.AllChunks || op.Document != input.Document {
				t.Fatal("missing UI scope", op)
			}
			if stopAt != "none" {
				stopped := errors.New("simulated process death after commit")
				err = runGraphOperation(context.Background(), dir, cfg, &op, func(stage string, current, total int, run string, _ int64) error {
					op.RunID = run
					op.Current = current
					op.Total = total
					if (stopAt == "committed portion" && strings.HasPrefix(stage, "Сохранено.")) || (stopAt == "checkpoint" && stage == "Извлечение тем" && current == 1) {
						return stopped
					}
					return nil
				})
				if !errors.Is(err, stopped) || calls.Load() != 1 {
					t.Fatal("crash fixture", err, calls.Load())
				}
				if _, err = s.db.Exec(`UPDATE web_operations SET run_id=?,current=?,total=? WHERE id=?`, op.RunID, op.Current, op.Total, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := ProcessWebOperationIn(context.Background(), dir, "local"); err != nil || !worked {
				t.Fatal(worked, err)
			}
			jobs, err := ListWebOperationsIn(context.Background(), dir, "local")
			if err != nil || len(jobs) != 1 || jobs[0].Status != "completed" || jobs[0].Current != 3 || jobs[0].Total != 3 {
				t.Fatal(jobs, err)
			}
			if calls.Load() != 3 {
				t.Fatal("chunks repeated or omitted", calls.Load())
			}
			report, err := s.BuildKnowledgeCoverageReport(scope)
			if err != nil || report.Summary.ProcessedChunks != 3 || report.Summary.CoveredChunks != 3 {
				t.Fatal(report.Summary, err)
			}
			report, err = s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: "other"})
			if err != nil || report.Summary.ProcessedChunks != 0 {
				t.Fatal("other document touched", report.Summary, err)
			}
			// Repeated submission is idempotent, and a new full pass skips existing work.
			if _, err = StartWebOperationIn(context.Background(), dir, "local", input, true); err != nil {
				t.Fatal(err)
			}
			input.RequestID = strings.Repeat("b", 32)
			if _, err = StartWebOperationIn(context.Background(), dir, "local", input, true); err != nil {
				t.Fatal(err)
			}
			if _, err = ProcessWebOperationIn(context.Background(), dir, "local"); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 {
				t.Fatal("already covered fragments regenerated")
			}
		})
	}
}

func TestWebGraphFullDocumentRejectsMissingScope(t *testing.T) {
	for _, in := range []WebOperationInput{
		{Kind: "graph-extract", AllChunks: true},
		{Kind: "graph-analyze", AllChunks: true, Document: "doc"},
	} {
		in.RequestID = strings.Repeat("a", 32)
		if _, err := StartWebOperationIn(context.Background(), t.TempDir(), "local", in, true); !errors.Is(err, ErrWebOperationInvalid) {
			t.Fatal(err)
		}
	}
}

func TestWebGraphFullDocumentWithoutModelPublishesSourceAndIssue(t *testing.T) {
	s, entries := newKnowledgeExtractionJobStore(t, []string{"Document without model."})
	defer s.Close()
	dir := filepath.Dir(s.Path())
	cfg := DefaultLocalConfig()
	cfg.Answer.Model = ""
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	in := WebOperationInput{RequestID: strings.Repeat("d", 32), Kind: "graph-extract", Document: entries[0].DocumentID, Focus: "all", AllChunks: true, ConfigRevision: cfg.Revision(), Batches: 1, ContextChars: 12000}
	if _, err := StartWebOperationIn(t.Context(), dir, "local", in, true); err != nil {
		t.Fatal(err)
	}
	if worked, err := ProcessWebOperationIn(t.Context(), dir, "local"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	jobs, err := ListWebOperationsIn(t.Context(), dir, "local")
	if err != nil || len(jobs) != 1 || jobs[0].Status != "completed" {
		t.Fatal(jobs, err)
	}
	report, err := s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || report.Summary.CoveredChunks != 1 || report.Summary.SemanticPendingChunks != 1 {
		t.Fatal(report.Summary, err)
	}
}
