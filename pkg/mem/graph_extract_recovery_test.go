package mem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeCorrectionDistinguishesRequirementFailures(t *testing.T) {
	for _, tc := range []struct{ err, want string }{
		{`json: unknown field "requirement"`, "invalid JSON location"},
		{`json: unknown field "requirements"`, "plural spelling"},
		{`knowledge node proposal "n6": every requirement citation must supply a quoted part`, "distinct citation_id"},
		{`knowledge node proposal "n4": requirement Требование must be an exact nonempty quote from an attached citation`, "line-break hyphens"},
		{`json: cannot unmarshal array into Go struct field knowledgeRequirementProposal.nodes.requirement.statement`, "statement must be an object"},
	} {
		if got := correctedKnowledgeExtractionPrompt("original", errors.New(tc.err), 4); !strings.Contains(got, tc.want) {
			t.Fatal(tc.err, got)
		}
	}
}

func TestKnowledgeCorrectionCandidateIsBoundedAndNodeScoped(t *testing.T) {
	raw := `{"nodes":[{"ref":"n4","body":"bad quotation"},{"ref":"n5","body":"unrelated node"}],"edges":[]}`
	got := knowledgeExtractionRejectedCandidate(raw, errors.New(`knowledge node proposal "n4": requirement quote invalid`))
	if !strings.Contains(got, "bad quotation") || strings.Contains(got, "unrelated node") || !strings.Contains(got, "untrusted output") {
		t.Fatal(got)
	}
	if got := knowledgeExtractionRejectedCandidate(strings.Repeat("x", 7000), errors.New("shape")); got != "" {
		t.Fatal("unbounded candidate")
	}
	if got := knowledgeExtractionRejectedCandidate(`{"nodes":`, errors.New("shape")); got != "" {
		t.Fatal("partial JSON candidate")
	}
}

// Opt-in: use only a disposable SQLite backup. Rebuild the OLD run unchanged,
// then call the already configured model for its failed batch. No graph publish.
func TestKnowledgeFailedBatchRecoveryLive(t *testing.T) {
	root := os.Getenv("MEM_GRAPH_RECOVERY_REPRO")
	if root == "" {
		t.Skip("set MEM_GRAPH_RECOVERY_REPRO to a disposable corpus copy")
	}
	dir := filepath.Join(root, ".mem")
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.ListKnowledgeExtractionRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range runs {
		if wanted := os.Getenv("MEM_GRAPH_RECOVERY_RUN"); wanted != "" && summary.ID != wanted {
			continue
		}
		run, err := s.LoadKnowledgeExtractionRun(summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i, batch := range run.Batches {
			if batch.Status != KnowledgeExtractionBatchFailed {
				continue
			}
			plan, err := s.RebuildKnowledgeExtractionRunPlan(run.ID, run.Focus)
			if err != nil {
				t.Fatal("old checkpoint no longer resumable", err)
			}
			completed := 0
			for _, b := range run.Batches {
				if b.Status == KnowledgeExtractionBatchCompleted {
					completed++
				}
			}
			cfg.Answer = cfg.Answer.WithMapGenerationDefaults()
			cfg.Answer.TimeoutSeconds = 120
			provider, err := NewOllamaAnswerProvider(cfg.Answer)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &knowledgeBenchmarkRecorder{provider: provider}
			ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
			defer cancel()
			result, rejected, extractErr := ExtractKnowledgeExtractionBatch(ctx, recorder, cfg.Answer, plan.Batches[i], i+1, len(plan.Batches))
			report := map[string]any{"run": run.ID, "batch": i + 1, "retained_completed_batches": completed, "attempts": recorder.attempts, "result": result, "rejected": rejected}
			if extractErr != nil {
				report["error"] = extractErr.Error()
			}
			data, _ := json.MarshalIndent(report, "", "  ")
			artifact := filepath.Join(root, "recovery-result.json")
			if err = os.WriteFile(artifact, data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("batch=%d evidence=%d old_checkpoints=%d calls=%d nodes=%d edges=%d artifact=%s", i+1, len(plan.Batches[i].Prompt.Evidence), completed, len(recorder.attempts), len(result.Graph.Nodes), len(result.Graph.Edges), artifact)
			if extractErr != nil {
				t.Fatal(extractErr)
			}
			if result.Insufficient || len(result.Graph.Nodes) == 0 {
				t.Fatal("no recovered graph")
			}
			return
		}
	}
	t.Fatal("no failed batch in snapshot")
}

type knowledgeRecoveryProvider struct {
	replies  []string
	requests []AnswerRequest
}

func (p *knowledgeRecoveryProvider) Generate(_ context.Context, r AnswerRequest) (string, error) {
	p.requests = append(p.requests, r)
	return p.replies[len(p.requests)-1], nil
}

func TestKnowledgeRuntimeCorrectionPreservesCheckpointIdentity(t *testing.T) {
	s, _ := newKnowledgeExtractionJobStore(t, []string{"A pump supplies water."})
	defer s.Close()
	plan, err := s.BuildKnowledgeExtractionJobPlan("pump", KnowledgeCoverageOptions{}, 12000, 1)
	if err != nil {
		t.Fatal(err)
	}
	answer := AnswerConfig{Model: "test", MaxTokens: 4096, TimeoutSeconds: 1}
	run, err := s.PrepareKnowledgeExtractionRun(plan, "pump", 12000, 1, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	original := plan.Batches[0].Prompt.System
	citation := plan.Batches[0].Prompt.Evidence[0].CitationID
	p := &knowledgeRecoveryProvider{replies: []string{`{"requirements":[]}`, `{"nodes":[{"ref":"n1","kind":"claim","label":"Pump","confidence":0.9,"citations":["` + citation + `"]}],"edges":[]}`}}
	if _, rejected, err := ExtractKnowledgeExtractionBatch(context.Background(), p, answer, plan.Batches[0], 1, 1); err != nil || rejected {
		t.Fatal(rejected, err)
	}
	if len(p.requests) != 2 || !strings.Contains(p.requests[0].System, knowledgeExtractionShapeGuidance) || !strings.Contains(p.requests[1].System, "plural spelling") || !strings.Contains(p.requests[1].Prompt, "REJECTED_CANDIDATE_DATA") {
		t.Fatal("correction did not reach provider")
	}
	if plan.Batches[0].Prompt.System != original {
		t.Fatal("mutated persisted prompt")
	}
	rebuilt, err := s.RebuildKnowledgeExtractionRunPlan(run.ID, "pump")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.PrepareKnowledgeExtractionRun(rebuilt, "pump", 12000, 1, answer, run.ID)
	if err != nil || resumed.ID != run.ID {
		t.Fatal("checkpoint identity changed", err)
	}
}

func TestKnowledgeStoredFailedRunRemainsResumable(t *testing.T) {
	root := os.Getenv("MEM_GRAPH_RECOVERY_REPRO")
	id := os.Getenv("MEM_GRAPH_RECOVERY_RUN")
	if root == "" || id == "" {
		t.Skip("set a disposable recovery snapshot and run ID")
	}
	dir := filepath.Join(root, ".mem")
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.LoadKnowledgeExtractionRun(id)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RebuildKnowledgeExtractionRunPlan(id, old.Focus)
	if err != nil {
		t.Fatal(err)
	}
	answer := cfg.Answer.WithMapGenerationDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 180)
	answer.MaxTokens = min(answer.MaxTokens, 16384)
	resumed, err := s.PrepareKnowledgeExtractionRun(plan, old.Focus, old.ContextChars, old.MaxBatches, answer, id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for i, b := range old.Batches {
		if b.Status == KnowledgeExtractionBatchCompleted {
			count++
			if resumed.Batches[i].Status != b.Status {
				t.Fatal("lost completed batch", i)
			}
		}
	}
	t.Logf("retained completed batches=%d; resumed=%s", count, resumed.ID)
}
