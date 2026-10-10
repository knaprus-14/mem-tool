package mem

import (
	"context"
	"errors"
	"fmt"
)

type classicMindMapAISynthesisContextError struct{ Proposals, Needed, Budget int }

func (e *classicMindMapAISynthesisContextError) Error() string {
	return fmt.Sprintf("%v: группировка всех %d предложений требует %d символов, answer.context_chars=%d", ErrClassicMindMapAIResourceLimit, e.Proposals, e.Needed, e.Budget)
}
func (e *classicMindMapAISynthesisContextError) Unwrap() error {
	return ErrClassicMindMapAIResourceLimit
}

// Each original subtree is inspected in full (using the same bounded summaries
// as one-pass synthesis). Later passes organize category titles, not claims.
// Membership is tracked by the host; no original content or evidence is rewritten.
func hierarchicalClassicMindMapAISynthesis(ctx context.Context, provider AnswerProvider, cfg AnswerConfig, request ClassicMindMapAIPreviewRequest, input []ClassicMindMapAIProposal, initial error) (classicMindMapAISynthesisPlan, map[string]string, error) {
	originalRoots := map[string]string{}
	members := map[string][]string{}
	for i, p := range input {
		if p.ParentProposalID == "" {
			ref := fmt.Sprintf("N%d", i+1)
			originalRoots[ref] = p.ID
			members[p.ID] = []string{ref}
		}
	}
	limit := min(maxClassicMindMapAISynthesisGroups, MaxClassicMindMapAIProposals-len(input))
	if request.Action == ClassicMindMapAINewMap {
		limit = min(limit, MaxClassicMindMapAIProposals-len(input)-1)
	}
	if limit < 1 {
		return classicMindMapAISynthesisPlan{}, nil, initial
	}
	level := input
	for pass := 0; pass < 12; pass++ {
		if err := ctx.Err(); err != nil {
			return classicMindMapAISynthesisPlan{}, nil, err
		}
		answer, roots, groupLimit, err := buildClassicMindMapAISynthesisRequestLimit(cfg, request, level, limit)
		if err == nil {
			plan, err := generateClassicMindMapAISynthesisPlan(ctx, provider, cfg, answer, roots, groupLimit)
			if err != nil {
				return plan, nil, err
			}
			for i := range plan.Groups {
				expanded := []string{}
				for _, ref := range plan.Groups[i].Members {
					expanded = append(expanded, members[roots[ref]]...)
				}
				plan.Groups[i].Members = expanded
			}
			plan.Hierarchical = true
			return plan, originalRoots, nil
		}
		var contextLimit *classicMindMapAISynthesisContextError
		if !errors.As(err, &contextLimit) {
			return classicMindMapAISynthesisPlan{}, nil, err
		}
		chunks, err := partitionClassicMindMapAISynthesis(cfg, request, level)
		if err != nil {
			return classicMindMapAISynthesisPlan{}, nil, initial
		}
		next := []ClassicMindMapAIProposal{}
		nextMembers := map[string][]string{}
		for _, chunk := range chunks {
			rootCount := 0
			for _, p := range chunk {
				if p.ParentProposalID == "" {
					rootCount++
				}
			}
			answer, roots, groupLimit, err := buildClassicMindMapAISynthesisRequestLimit(cfg, request, chunk, min(limit, max(1, rootCount/2)))
			if err != nil {
				return classicMindMapAISynthesisPlan{}, nil, err
			}
			plan, err := generateClassicMindMapAISynthesisPlan(ctx, provider, cfg, answer, roots, groupLimit)
			if err != nil {
				return plan, nil, err
			}
			for _, group := range plan.Groups {
				id := fmt.Sprintf("level-%d-group-%d", pass, len(next))
				next = append(next, ClassicMindMapAIProposal{ID: id, Label: group.Title, Kind: ClassicMindMapNodeTopic})
				for _, ref := range group.Members {
					nextMembers[id] = append(nextMembers[id], members[roots[ref]]...)
				}
			}
		}
		// After the first descriptor compaction every further pass must reduce
		// the number of categories. Never loop indefinitely on tiny contexts.
		if pass > 0 && len(next) >= len(level) {
			return classicMindMapAISynthesisPlan{}, nil, initial
		}
		level, members = next, nextMembers
	}
	return classicMindMapAISynthesisPlan{}, nil, initial
}

func partitionClassicMindMapAISynthesis(cfg AnswerConfig, request ClassicMindMapAIPreviewRequest, input []ClassicMindMapAIProposal) ([][]ClassicMindMapAIProposal, error) {
	byID := map[string]ClassicMindMapAIProposal{}
	for _, p := range input {
		byID[p.ID] = p
	}
	trees := map[string][]ClassicMindMapAIProposal{}
	order := []string{}
	for _, p := range input {
		root := p
		seen := map[string]bool{}
		for root.ParentProposalID != "" {
			if seen[root.ID] {
				return nil, errors.New("synthesis input cycle")
			}
			seen[root.ID] = true
			var ok bool
			root, ok = byID[root.ParentProposalID]
			if !ok {
				return nil, errors.New("synthesis input missing parent")
			}
		}
		if _, exists := trees[root.ID]; !exists {
			order = append(order, root.ID)
		}
		trees[root.ID] = append(trees[root.ID], p)
	}
	chunks := [][]ClassicMindMapAIProposal{}
	var current []ClassicMindMapAIProposal
	for _, root := range order {
		trial := append(append([]ClassicMindMapAIProposal(nil), current...), trees[root]...)
		if _, _, _, err := buildClassicMindMapAISynthesisRequest(cfg, request, trial); err == nil {
			current = trial
			continue
		}
		if len(current) > 0 {
			chunks = append(chunks, current)
		}
		current = trees[root]
		if _, _, _, err := buildClassicMindMapAISynthesisRequest(cfg, request, current); err != nil {
			return nil, err
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks, nil
}

func generateClassicMindMapAISynthesisPlan(ctx context.Context, provider AnswerProvider, cfg AnswerConfig, answer AnswerRequest, roots map[string]string, limit int) (classicMindMapAISynthesisPlan, error) {
	if err := ctx.Err(); err != nil {
		return classicMindMapAISynthesisPlan{}, err
	}
	var raw string
	var err error
	var cached bool
	cache, hasCache := provider.(classicMindMapAISynthesisCache)
	if hasCache {
		raw, cached, err = cache.loadSynthesis(ctx, answer)
	}
	if err == nil && !cached {
		raw, err = generateClassicMindMapAIStrictJSON(ctx, provider, cfg, answer)
	}
	if err != nil {
		return classicMindMapAISynthesisPlan{}, fmt.Errorf("группировка карты: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return classicMindMapAISynthesisPlan{}, err
	}
	plan, err := decodeClassicMindMapAISynthesisPlan(raw, roots, limit)
	if err != nil {
		return plan, fmt.Errorf("группировка карты отклонена: %w", err)
	}
	if hasCache && !cached {
		if err := cache.saveSynthesis(ctx, answer, raw); err != nil {
			return plan, err
		}
	}
	return plan, nil
}
