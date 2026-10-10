package mem

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type synthesisTestProvider struct {
	t        *testing.T
	budget   int
	requests []AnswerRequest
	seen     map[string]int
	invalid  bool
	cancel   context.CancelFunc
	delegate AnswerProvider
}

func (p *synthesisTestProvider) Generate(ctx context.Context, req AnswerRequest) (string, error) {
	p.requests = append(p.requests, req)
	if utf8.RuneCountInString(req.System)+utf8.RuneCountInString(req.Prompt) > p.budget {
		p.t.Fatal("synthesis exceeded configured context")
	}
	if strings.Contains(req.Prompt, "EVIDENCE_JSON_BEGIN") {
		p.t.Fatal("reassembly regenerated document evidence")
	}
	var payload struct {
		Roots []string                        `json:"roots"`
		Nodes []classicMindMapAISynthesisNode `json:"nodes"`
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(req.Prompt, "SYNTHESIS_INPUT_JSON_BEGIN\n"), "\nSYNTHESIS_INPUT_JSON_END")
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", err
	}
	if p.seen == nil {
		p.seen = map[string]int{}
	}
	for _, n := range payload.Nodes {
		p.seen[n.Label]++
	}
	if p.cancel != nil {
		p.cancel()
	}
	if p.delegate != nil {
		return p.delegate.Generate(ctx, req)
	}
	if p.invalid {
		return `{"groups":[{"title":"Invalid","members":["unknown"]}]}`, nil
	}
	answer, _ := json.Marshal(classicMindMapAISynthesisPlan{Groups: []classicMindMapAISynthesisGroup{{Title: "Общая тема", Members: payload.Roots}}})
	return string(answer), nil
}

func TestClassicMindMapAISynthesisHierarchyPreservesLargeDocument(t *testing.T) {
	for _, action := range []ClassicMindMapAIAction{ClassicMindMapAIExpand, ClassicMindMapAINewMap} {
		t.Run(string(action), func(t *testing.T) {
			batches := [][]ClassicMindMapAIProposal{}
			original := map[string]ClassicMindMapAIProposal{}
			for b := 0; b < 24; b++ {
				batch := []ClassicMindMapAIProposal{}
				for j := 0; j < 15; j++ {
					id := fmt.Sprintf("original-%d-%d", b, j)
					n := ClassicMindMapAIProposal{ID: id, Label: id, Summary: strings.Repeat("Условие. ", 30), BodyMarkdown: "I = U/R; только при T > 10", Kind: ClassicMindMapNodeFact}
					if j == 0 {
						n.Kind = ClassicMindMapNodeTopic
					} else {
						n.ParentProposalID = fmt.Sprintf("original-%d-0", b)
					}
					batch = append(batch, n)
					original[id] = n
				}
				batches = append(batches, batch)
			}
			p := &synthesisTestProvider{t: t, budget: 12000}
			result, err := combineClassicMindMapAIProposals(context.Background(), p, AnswerConfig{Model: "fake", ContextChars: p.budget, MaxTokens: 4096}, ClassicMindMapAIPreviewRequest{Action: action, Prompt: "Group", Title: "Документ"}, batches, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.requests) < 3 {
				t.Fatal("hierarchy was not exercised")
			}
			info := classicMindMapAISynthesisInfo(result)
			if info == nil || info.Method != "hierarchical_whole_subtrees" || info.InputProposals != 360 {
				t.Fatal(info)
			}
			for _, n := range result {
				before, ok := original[n.ID]
				if !ok {
					continue
				}
				if p.seen[n.Label] != 1 {
					t.Fatalf("descriptor not inspected exactly once: %s %d", n.Label, p.seen[n.Label])
				}
				if before.ParentProposalID == "" {
					before.ParentProposalID = n.ParentProposalID
				}
				if !reflect.DeepEqual(before, n) {
					t.Fatalf("original node changed: %s", n.ID)
				}
				delete(original, n.ID)
			}
			if len(original) != 0 {
				t.Fatal("nodes lost")
			}
		})
	}
}

func TestClassicMindMapAISynthesisHierarchyRejectsInvalidOrCancelledPass(t *testing.T) {
	input := [][]ClassicMindMapAIProposal{}
	for i := 0; i < 40; i++ {
		input = append(input, []ClassicMindMapAIProposal{{ID: fmt.Sprint(i), Label: fmt.Sprint(i), Summary: strings.Repeat("X", 240)}})
	}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		p := &synthesisTestProvider{t: t, budget: 4000, invalid: !cancelled}
		if cancelled {
			p.cancel = cancel
		}
		result, err := combineClassicMindMapAIProposals(ctx, p, AnswerConfig{Model: "fake", ContextChars: 4000}, ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAIExpand}, input, nil)
		cancel()
		if err == nil || len(result) != 0 || len(p.requests) != 1 {
			t.Fatalf("partial plan escaped: %v calls=%d", err, len(p.requests))
		}
	}
}
