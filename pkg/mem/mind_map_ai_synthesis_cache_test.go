package mem

import (
	"context"
	"errors"
	"testing"
)

func TestValidatedGroupingCheckpointSurvivesInterruption(t *testing.T) {
	s, _ := newClassicMindMapAITestStore(t)
	calls := 0
	p := webBranchProviderFunc(func(context.Context, AnswerRequest) (string, error) {
		calls++
		return `{"groups":[{"title":"Тема","members":["N1"]}]}`, nil
	})
	cached := &classicMindMapAICachedProvider{AnswerProvider: p, store: s, runID: "saved-run"}
	ctx := context.Background()
	answer := AnswerRequest{Model: "fake", Prompt: "first grouping", MaxTokens: 4096}
	cfg := AnswerConfig{Model: "fake"}
	roots := map[string]string{"N1": "original"}
	for i := 0; i < 2; i++ {
		if _, err := generateClassicMindMapAISynthesisPlan(ctx, cached, cfg, answer, roots, 1); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("grouping repeated", calls)
	}
	if _, err := s.db.Exec(`UPDATE mind_map_generation_groups SET result_digest='corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := generateClassicMindMapAISynthesisPlan(ctx, cached, cfg, answer, roots, 1); !errors.Is(err, ErrClassicMindMapAIChanged) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("corruption silently regenerated")
	}
}

func TestRejectedGroupingIsNotCached(t *testing.T) {
	s, _ := newClassicMindMapAITestStore(t)
	calls := 0
	p := webBranchProviderFunc(func(context.Context, AnswerRequest) (string, error) {
		calls++
		if calls == 1 {
			return `{"groups":[]}`, nil
		}
		return `{"groups":[{"title":"Тема","members":["N1"]}]}`, nil
	})
	cached := &classicMindMapAICachedProvider{AnswerProvider: p, store: s, runID: "saved-run"}
	ctx := context.Background()
	answer := AnswerRequest{Model: "fake", MaxTokens: 4096}
	cfg := AnswerConfig{Model: "fake"}
	roots := map[string]string{"N1": "original"}
	if _, err := generateClassicMindMapAISynthesisPlan(ctx, cached, cfg, answer, roots, 1); err == nil {
		t.Fatal("accepted invalid grouping")
	}
	if _, err := generateClassicMindMapAISynthesisPlan(ctx, cached, cfg, answer, roots, 1); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
