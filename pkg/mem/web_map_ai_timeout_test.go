package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type slowSourceMapProvider struct {
	calls, roots int
	delay        time.Duration
}

func (p *slowSourceMapProvider) Generate(ctx context.Context, in AnswerRequest) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(p.delay):
	}
	p.calls++
	if _, raw, ok := strings.Cut(in.Prompt, "SYNTHESIS_INPUT_JSON_BEGIN\n"); ok {
		raw, _, _ = strings.Cut(raw, "\nSYNTHESIS_INPUT_JSON_END")
		var input struct {
			Roots []string `json:"roots"`
		}
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			return "", err
		}
		if len(input.Roots) != p.roots {
			return "", fmt.Errorf("global grouping omitted original branches: got %d", len(input.Roots))
		}
		plan, err := json.Marshal(classicMindMapAISynthesisPlan{Groups: []classicMindMapAISynthesisGroup{{Title: "Разделы документа", Members: input.Roots}}})
		return string(plan), err
	}
	_, raw, ok := strings.Cut(in.Prompt, "EVIDENCE_JSON_BEGIN\n")
	if !ok {
		return "", errors.New("missing evidence")
	}
	raw, _, _ = strings.Cut(raw, "\nEVIDENCE_JSON_END")
	var evidence []GroundedEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		return "", err
	}
	if len(evidence) != 1 {
		return "", fmt.Errorf("expected one fragment per batch, got %d", len(evidence))
	}
	return fmt.Sprintf(`{"nodes":[{"ref":"n","parent_ref":"","label":"Тема %d","summary":"Проверено","body_markdown":"","kind":"subtopic","citations":[%q]}]}`, p.calls, evidence[0].EvidenceRef), nil
}

func TestWebSourceMapContinuesBeyondFifteenMinutes(t *testing.T) {
	testWebSourceMapLongJob(t, 64, 16*time.Second, 15*time.Minute)
}

func TestWebSourceMapContinuesBeyondTwoHours(t *testing.T) {
	testWebSourceMapLongJob(t, 92, 80*time.Second, 2*time.Hour)
}

func testWebSourceMapLongJob(t *testing.T, count int, delay, minimum time.Duration) {
	// Go's virtual clock exercises the real worker, per-call contexts, progress
	// polling, durable batches and publication without a 17-minute wall wait.
	synctest.Test(t, func(t *testing.T) {
		dir, s, in := sourceMapFixture(t)
		seed := validStructuredChunks()[0]
		if err := s.db.QueryRow(`SELECT source_path FROM entries LIMIT 1`).Scan(&seed.Provenance.SourcePath); err != nil {
			t.Fatal(err)
		}
		chunks := make([]DocumentChunk, count)
		for i := range chunks {
			chunks[i] = seed
			chunks[i].Text = fmt.Sprintf("Раздел %d. ", i) + strings.Repeat("Материал документа. ", 300)
			chunks[i].ChunkIndex = i
			chunks[i].TotalChunks = len(chunks)
			chunks[i].Provenance.BlockChunkIndex = i
			chunks[i].Provenance.BlockTotalChunks = len(chunks)
			chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
		}
		if err := s.ReplaceDocumentChunks(seed.Provenance.SourcePath, chunks); err != nil {
			t.Fatal(err)
		}
		task, err := CreateWebMapFromSourcesIn(context.Background(), dir, "alice", in)
		if err != nil {
			t.Fatal(err)
		}
		provider := &slowSourceMapProvider{roots: count, delay: delay}
		started := time.Now()
		if worked, err := processWebMapAIIn(context.Background(), dir, "alice", provider); err != nil || !worked {
			t.Fatal(worked, err)
		}
		if provider.calls != count+1 || time.Since(started) <= minimum {
			t.Fatal("long generation not exercised", provider.calls, time.Since(started))
		}
		var status string
		if err = s.db.QueryRow(`SELECT status FROM web_map_ai_tasks WHERE id=?`, task.ID).Scan(&status); err != nil || status != "ready" {
			t.Fatal(status, err)
		}
		if worked, err := processWebMapAIIn(context.Background(), dir, "alice", provider); err != nil || !worked {
			t.Fatal(worked, err)
		}
		view, err := ReadWebMapIn(context.Background(), dir, task.MapID)
		if err != nil || view.Map.Revision != 2 || len(view.Nodes) != count+2 {
			t.Fatal(view.Map, len(view.Nodes), err)
		}
	})
}

func TestWebMapAITimeBudgetsAndFailureClassification(t *testing.T) {
	job := webMapAIFailureMessage(&webMapAIJobTimeout{Budget: 2 * time.Hour}, 58, 64)
	model := webMapAIFailureMessage(fmt.Errorf("private provider detail: %w", context.DeadlineExceeded), 58, 64)
	if !strings.Contains(job, "общий лимит задания (120 мин.)") || !strings.Contains(model, "одного ответа") || strings.Contains(model, "private") {
		t.Fatal(job, model)
	}
}

func TestWebSourceMapCancellationStillInterruptsProvider(t *testing.T) {
	for _, mode := range []string{"user", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, in := sourceMapFixture(t)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			task, err := CreateWebMapFromSourcesIn(parent, dir, "alice", in)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			provider := webBranchProviderFunc(func(call context.Context, _ AnswerRequest) (string, error) {
				called = true
				if mode == "shutdown" {
					cancel()
				} else if err := CancelWebMapAIIn(parent, dir, "alice", task.MapID, task.ID); err != nil {
					t.Fatal(err)
				}
				select {
				case <-call.Done():
					return "", call.Err()
				case <-time.After(3 * time.Second):
					t.Fatal("source task cancellation did not reach provider")
					return "", nil
				}
			})
			_, err = processWebMapAIIn(parent, dir, "alice", provider)
			if !called || (mode == "shutdown" && !errors.Is(err, context.Canceled)) || (mode == "user" && err != nil) {
				t.Fatal(called, err)
			}
			view, err := ReadWebMapIn(context.Background(), dir, task.MapID)
			if err != nil || view.Map.Revision != 1 {
				t.Fatal(view.Map, err)
			}
			if mode == "user" {
				tasks, err := ListWebMapAIIn(context.Background(), dir, "alice", task.MapID)
				if err != nil || len(tasks) != 1 || tasks[0].Status != "cancelled" {
					t.Fatal(tasks, err)
				}
			}
		})
	}
}
