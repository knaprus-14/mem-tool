package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type resumableMapProvider struct {
	calls   map[string]int
	split   bool
	failRef string
	stop    context.CancelFunc
}

func (p *resumableMapProvider) Generate(ctx context.Context, r AnswerRequest) (string, error) {
	if _, raw, ok := strings.Cut(r.Prompt, "SYNTHESIS_INPUT_JSON_BEGIN\n"); ok {
		p.calls["group"]++
		raw, _, _ = strings.Cut(raw, "\nSYNTHESIS_INPUT_JSON_END")
		var in struct {
			Roots []string `json:"roots"`
		}
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return "", err
		}
		data, err := json.Marshal(classicMindMapAISynthesisPlan{Groups: []classicMindMapAISynthesisGroup{{Title: "Темы", Members: in.Roots}}})
		return string(data), err
	}
	_, raw, ok := strings.Cut(r.Prompt, "EVIDENCE_JSON_BEGIN\n")
	if !ok {
		return "", errors.New("missing evidence")
	}
	raw, _, _ = strings.Cut(raw, "\nEVIDENCE_JSON_END")
	var evidence []GroundedEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		return "", err
	}
	refs := []string{}
	for _, e := range evidence {
		refs = append(refs, e.EvidenceRef)
	}
	key := strings.Join(refs, ",")
	p.calls[key]++
	if p.split && len(evidence) > 1 {
		return "", &AnswerTokenLimitError{MaxTokens: r.MaxTokens}
	}
	if key == p.failRef {
		if p.stop != nil {
			p.stop()
			return "", context.Canceled
		}
		return "", errors.New("temporary provider failure")
	}
	return fmt.Sprintf(`{"nodes":[{"ref":"a","parent_ref":"","label":%q,"summary":"Факт","body_markdown":"","kind":"fact","citations":[%q]}]}`, key, refs[0]), nil
}

func TestSourceMapAdaptivePartsSurviveFailureAndResume(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.MaxTokens = 4096
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	p := &resumableMapProvider{calls: map[string]int{}, split: true, failRef: "E2"}
	if _, err = processWebMapAIIn(ctx, dir, "alice", p); err != nil {
		t.Fatal(err)
	}
	tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || tasks[0].Status != "failed" || !tasks[0].CanResume {
		t.Fatal(tasks, err)
	}
	run := tasks[0].RunID
	if p.calls["E1"] != 1 || p.calls["E2"] != 1 {
		t.Fatal(p.calls)
	}
	if _, err = ResumeWebMapAIIn(ctx, dir, "bob", task.MapID, task.ID); !errors.Is(err, ErrClassicMindMapAIPreviewNotFound) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	p.failRef = ""
	for i := 0; i < 2; i++ {
		if worked, e := processWebMapAIIn(ctx, dir, "alice", p); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	tasks, err = ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || tasks[0].Status != "applied" || tasks[0].RunID != run {
		t.Fatal(tasks, err)
	}
	if p.calls["E1"] != 1 || p.calls["E2"] != 2 || p.calls["E1,E2"] != 1 {
		t.Fatal("replayed completed work", p.calls)
	}
	doc, err := s.LoadClassicMindMap(task.MapID)
	if err != nil || doc.Map.Revision != 2 {
		t.Fatal(doc, err)
	}
	var history int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_resumes WHERE run_id=? AND previous_status='failed'`, run).Scan(&history); err != nil || history != 1 {
		t.Fatal(history, err)
	}
	if _, err = ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || worked {
		t.Fatal(worked, err)
	}
}

func TestSourceMapResumeAfterShutdownAndExplicitCancel(t *testing.T) {
	for _, mode := range []string{"shutdown", "crash", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, task := failedSourceSynthesisFixture(t)
			// One committed batch and one interrupted batch, using the real saved plan.
			if _, err := s.db.Exec(`UPDATE mind_map_generation_batches SET status='cancelled',result_json='',result_digest='' WHERE run_id=? AND ordinal=1`, task.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM mind_map_generation_parts WHERE run_id=? AND batch_id IN (SELECT batch_id FROM mind_map_generation_batches WHERE run_id=? AND ordinal=1)`, task.RunID, task.RunID); err != nil {
				t.Fatal(err)
			}
			state := "running"
			runState := "cancelled"
			if mode == "cancel" {
				state = "cancelled"
			}
			if mode == "crash" {
				runState = "running"
			}
			if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET status=?,phase='generate' WHERE id=?`, state, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE mind_map_generation_runs SET status=? WHERE id=?`, runState, task.RunID); err != nil {
				t.Fatal(err)
			}
			p := &resumableMapProvider{calls: map[string]int{}}
			ctx := context.Background()
			if mode == "cancel" {
				if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || worked {
					t.Fatal("cancel auto-resumed", err)
				}
				if _, err := ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
					t.Fatal(worked, err)
				}
			}
			tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || tasks[0].Status != "applied" || tasks[0].RunID != task.RunID {
				t.Fatal(tasks, err)
			}
			if p.calls["E1"] != 0 || p.calls["E2"] != 1 {
				t.Fatal("lost checkpoint", p.calls)
			}
		})
	}
}

func TestSourceMapActualShutdownReusesAdaptiveCheckpoint(t *testing.T) {
	dir, _, in := sourceMapFixture(t)
	cfg, _ := LoadConfigIn(dir)
	cfg.Answer.MaxTokens = 4096
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	p := &resumableMapProvider{calls: map[string]int{}, split: true, failRef: "E2", stop: cancel}
	if _, err = processWebMapAIIn(ctx, dir, "alice", p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tasks, err := ListWebMapAIIn(context.Background(), dir, "alice", task.MapID)
	if err != nil || tasks[0].Status != "running" {
		t.Fatal(tasks, err)
	}
	p.failRef = ""
	p.stop = nil
	for i := 0; i < 2; i++ {
		if worked, e := processWebMapAIIn(context.Background(), dir, "alice", p); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	if p.calls["E1"] != 1 || p.calls["E2"] != 2 || p.calls["E1,E2"] != 1 {
		t.Fatal(p.calls)
	}
}

func TestSourceMapResumeRejectsChangedAndCorruptState(t *testing.T) {
	for _, mode := range []string{"source", "map", "config", "checkpoint", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, task := failedSourceSynthesisFixture(t)
			ctx := context.Background()
			switch mode {
			case "source":
				s.db.Exec(`UPDATE entries SET text=text||' changed'`)
			case "map":
				s.db.Exec(`UPDATE mind_maps SET revision=revision+1 WHERE id=?`, task.MapID)
			case "config":
				cfg, _ := LoadConfigIn(dir)
				cfg.Answer.Model = "different"
				if err := SaveConfigIn(dir, cfg); err != nil {
					t.Fatal(err)
				}
			case "checkpoint":
				s.db.Exec(`UPDATE mind_map_generation_batches SET result_digest='bad' WHERE run_id=? AND ordinal=0`, task.RunID)
			case "prompt":
				s.db.Exec(`UPDATE mind_map_generation_batches SET prompt_digest='bad' WHERE run_id=? AND ordinal=0`, task.RunID)
			}
			_, err := ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID)
			if mode == "source" || mode == "map" || mode == "config" {
				if err == nil {
					t.Fatal("accepted changed inputs")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := &resumableMapProvider{calls: map[string]int{}}
			if _, err = processWebMapAIIn(ctx, dir, "alice", p); err != nil {
				t.Fatal(err)
			}
			if len(p.calls) != 0 {
				t.Fatal("model called for corrupt checkpoint")
			}
			tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || tasks[0].Status != "failed" {
				t.Fatal(tasks, err)
			}
		})
	}
}

func TestSourceMapWorkerHasNoWholeJobDeadline(t *testing.T) {
	dir, _, in := sourceMapFixture(t)
	ctx := context.Background()
	if _, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in); err != nil {
		t.Fatal(err)
	}
	p := webBranchProviderFunc(func(call context.Context, r AnswerRequest) (string, error) {
		deadline, ok := call.Deadline()
		if !ok || time.Until(deadline) > 90*time.Second {
			t.Fatal("individual request must still be bounded")
		}
		return sourceMapResponse, nil
	})
	if _, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMapResumeCancelledReadyPreviewWithoutModel(t *testing.T) {
	dir, _, in := sourceMapFixture(t)
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	p := &resumableMapProvider{calls: map[string]int{}}
	if _, err = processWebMapAIIn(ctx, dir, "alice", p); err != nil {
		t.Fatal(err)
	}
	if err = CancelWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || !tasks[0].CanResume {
		t.Fatal(tasks, err)
	}
	if _, err = ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if p.calls["E1,E2"] != 1 {
		t.Fatal("regenerated preview", p.calls)
	}
	tasks, err = ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || tasks[0].Status != "applied" {
		t.Fatal(tasks, err)
	}
}
