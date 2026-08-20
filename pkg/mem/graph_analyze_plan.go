package mem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// BuildCorpusAnalysisPlan partitions current active claims into deterministic,
// context-bounded batches. Every runnable batch contains at least two claims
// whose evidence covers at least two documents. A previously covered claim may
// be reused as a bridge so that a final claim from one document is not stranded.
func (s *Store) BuildCorpusAnalysisPlan(focus string, contextBudget, maxBatches int) (CorpusAnalysisPlan, error) {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return CorpusAnalysisPlan{}, errors.New("corpus analysis focus is empty")
	}
	if contextBudget <= 0 {
		contextBudget = DefaultAnswerContextChars
	}
	if contextBudget > MaxAnswerContextChars {
		return CorpusAnalysisPlan{}, fmt.Errorf("corpus analysis context budget must not exceed %d", MaxAnswerContextChars)
	}
	if maxBatches < 1 || maxBatches > MaxCorpusAnalysisBatches {
		return CorpusAnalysisPlan{}, fmt.Errorf("corpus analysis batches must be between 1 and %d", MaxCorpusAnalysisBatches)
	}

	candidates, skippedNonCurrent, eligibleDocuments, err := s.loadCorpusAnalysisCandidates(focus)
	if err != nil {
		return CorpusAnalysisPlan{}, err
	}
	plan := CorpusAnalysisPlan{
		EligibleClaims: len(candidates), SkippedNonCurrent: skippedNonCurrent,
		EligibleDocuments: eligibleDocuments,
	}
	if _, baseSize, err := buildCorpusAnalysisPromptPayload(focus, nil); err != nil {
		return CorpusAnalysisPlan{}, err
	} else if baseSize > contextBudget {
		return CorpusAnalysisPlan{}, fmt.Errorf("corpus analysis context budget %d is too small for instructions and focus (%d)", contextBudget, baseSize)
	}

	remaining := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		remaining[candidate.claim.nodeID] = true
	}
	covered := make(map[string]bool, len(candidates))
	coveredDocuments := make(map[string]bool)

	for len(remaining) > 0 && len(plan.Batches) < maxBatches {
		seedIndex := firstRemainingCorpusCandidate(candidates, remaining)
		if seedIndex < 0 {
			break
		}
		seed := candidates[seedIndex]
		if _, size, err := buildCorpusAnalysisPromptPayload(focus, []CorpusAnalysisClaim{seed.claim}); err != nil {
			return CorpusAnalysisPlan{}, err
		} else if size > contextBudget {
			delete(remaining, seed.claim.nodeID)
			continue
		}

		selected := []corpusAnalysisCandidate{seed}
		partnerIndex := -1
		for pass := 0; pass < 2 && partnerIndex < 0; pass++ {
			for i, candidate := range candidates {
				isRemaining := remaining[candidate.claim.nodeID]
				if candidate.claim.nodeID == seed.claim.nodeID || (pass == 0 && !isRemaining) || (pass == 1 && isRemaining) {
					continue
				}
				trial := []CorpusAnalysisClaim{seed.claim, candidate.claim}
				if corpusClaimsDocumentCount(trial) < 2 {
					continue
				}
				_, size, err := buildCorpusAnalysisPromptPayload(focus, trial)
				if err != nil {
					return CorpusAnalysisPlan{}, err
				}
				if size <= contextBudget {
					partnerIndex = i
					break
				}
			}
		}
		if partnerIndex < 0 {
			delete(remaining, seed.claim.nodeID)
			continue
		}
		selected = append(selected, candidates[partnerIndex])

		for _, candidate := range candidates {
			if len(selected) >= MaxCorpusAnalysisClaims || !remaining[candidate.claim.nodeID] || corpusCandidateSelected(selected, candidate.claim.nodeID) {
				continue
			}
			trial := append(corpusClaims(selected), candidate.claim)
			_, size, err := buildCorpusAnalysisPromptPayload(focus, trial)
			if err != nil {
				return CorpusAnalysisPlan{}, err
			}
			if size <= contextBudget {
				selected = append(selected, candidate)
			}
		}

		prompt, size, err := buildCorpusAnalysisPromptPayload(focus, corpusClaims(selected))
		if err != nil {
			return CorpusAnalysisPlan{}, err
		}
		if size > contextBudget || len(prompt.Claims) < 2 {
			return CorpusAnalysisPlan{}, errors.New("corpus analysis planner produced an invalid batch")
		}
		prompt.EligibleClaims = len(candidates)
		prompt.SkippedNonCurrent = skippedNonCurrent
		prompt.DocumentCount = corpusClaimsDocumentCount(prompt.Claims)
		prompt.BatchID = stableCorpusAnalysisBatchID(focus, prompt.Claims)
		if prompt.DocumentCount < 2 {
			return CorpusAnalysisPlan{}, errors.New("corpus analysis planner produced a single-document batch")
		}
		plan.Batches = append(plan.Batches, prompt)

		for _, candidate := range selected {
			if remaining[candidate.claim.nodeID] {
				delete(remaining, candidate.claim.nodeID)
			}
			covered[candidate.claim.nodeID] = true
			for documentID := range candidate.docs {
				coveredDocuments[documentID] = true
			}
		}
	}

	plan.CoveredClaims = len(covered)
	plan.UncoveredClaims = plan.EligibleClaims - plan.CoveredClaims
	plan.CoveredDocuments = len(coveredDocuments)
	return plan, nil
}

func (s *Store) loadCorpusAnalysisCandidates(focus string) ([]corpusAnalysisCandidate, int, int, error) {
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return nil, 0, 0, err
	}
	candidates := make([]corpusAnalysisCandidate, 0)
	skippedNonCurrent := 0
	documents := make(map[string]bool)
	for _, node := range graph.Nodes {
		if node.Status != KnowledgeStatusActive || node.Kind != KnowledgeNodeClaim {
			continue
		}
		current := true
		for _, anchor := range node.Evidence {
			if s.ResolveEvidenceAnchor(anchor).State != EvidenceCurrent {
				current = false
				break
			}
		}
		if !current {
			skippedNonCurrent++
			continue
		}
		claim := CorpusAnalysisClaim{
			Label: node.Label, Body: node.Body, nodeID: node.ID,
			anchors: append([]EvidenceAnchor(nil), node.Evidence...),
		}
		docs := make(map[string]bool)
		for _, anchor := range node.Evidence {
			docs[anchor.DocumentID] = true
			documents[anchor.DocumentID] = true
			claim.Evidence = append(claim.Evidence, groundedEvidenceForAnchor(anchor))
		}
		candidates = append(candidates, corpusAnalysisCandidate{
			claim: claim, score: corpusFocusScore(focus, node.Label+" "+node.Body), docs: docs,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].claim.nodeID < candidates[j].claim.nodeID
	})
	return candidates, skippedNonCurrent, len(documents), nil
}

func buildCorpusAnalysisPromptPayload(focus string, claims []CorpusAnalysisClaim) (CorpusAnalysisPrompt, int, error) {
	focusJSON, err := json.Marshal(focus)
	if err != nil {
		return CorpusAnalysisPrompt{}, 0, fmt.Errorf("corpus analysis: encode focus: %w", err)
	}
	serializable := append([]CorpusAnalysisClaim(nil), claims...)
	for i := range serializable {
		serializable[i].Ref = fmt.Sprintf("c%d", i+1)
	}
	encoded, err := json.MarshalIndent(serializable, "", "  ")
	if err != nil {
		return CorpusAnalysisPrompt{}, 0, fmt.Errorf("corpus analysis: encode claims: %w", err)
	}
	user := "Focus (user input): " + string(focusJSON) + "\n\nCLAIMS_JSON_BEGIN\n" + string(encoded) + "\nCLAIMS_JSON_END\n"
	prompt := CorpusAnalysisPrompt{System: corpusAnalysisSystemPrompt, User: user, Claims: serializable}
	return prompt, utf8.RuneCountInString(prompt.System) + utf8.RuneCountInString(prompt.User), nil
}

func firstRemainingCorpusCandidate(candidates []corpusAnalysisCandidate, remaining map[string]bool) int {
	for i, candidate := range candidates {
		if remaining[candidate.claim.nodeID] {
			return i
		}
	}
	return -1
}

func corpusCandidateSelected(selected []corpusAnalysisCandidate, nodeID string) bool {
	for _, candidate := range selected {
		if candidate.claim.nodeID == nodeID {
			return true
		}
	}
	return false
}

func corpusClaims(candidates []corpusAnalysisCandidate) []CorpusAnalysisClaim {
	claims := make([]CorpusAnalysisClaim, 0, len(candidates))
	for _, candidate := range candidates {
		claims = append(claims, candidate.claim)
	}
	return claims
}

func corpusClaimsDocumentCount(claims []CorpusAnalysisClaim) int {
	documents := make(map[string]bool)
	for _, claim := range claims {
		for _, evidence := range claim.Evidence {
			documents[evidence.DocumentID] = true
		}
	}
	return len(documents)
}

func stableCorpusAnalysisBatchID(focus string, claims []CorpusAnalysisClaim) string {
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-corpus-batch-v1")
	writeKnowledgeIDField(h, normalizeKnowledgeIdentityText(focus))
	for _, claim := range claims {
		writeKnowledgeIDField(h, claim.nodeID)
		for _, anchor := range claim.anchors {
			writeKnowledgeIDField(h, anchor.CitationID)
			writeKnowledgeIDField(h, anchor.DocumentRevision)
			writeKnowledgeIDField(h, anchor.ChunkHash)
			writeKnowledgeIDField(h, anchor.EvidenceHash)
		}
	}
	return "kab-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// MergeCorpusAnalysisGraphs combines independently validated batches without
// allowing conflicting objects with the same host-derived ID.
func MergeCorpusAnalysisGraphs(graphs ...KnowledgeGraph) (KnowledgeGraph, error) {
	nodes := make(map[string]KnowledgeNode)
	edges := make(map[string]KnowledgeEdge)
	for batchIndex, graph := range graphs {
		if err := validateCorpusAnalysisFragment(graph); err != nil {
			return KnowledgeGraph{}, fmt.Errorf("corpus batch %d: %w", batchIndex+1, err)
		}
		for _, node := range graph.Nodes {
			if existing, ok := nodes[node.ID]; ok {
				if !sameCorpusNode(existing, node) {
					return KnowledgeGraph{}, fmt.Errorf("corpus batches disagree on node %q", node.ID)
				}
				if node.Confidence > existing.Confidence {
					existing.Confidence = node.Confidence
					nodes[node.ID] = existing
				}
				continue
			}
			nodes[node.ID] = node
		}
		for _, edge := range graph.Edges {
			if existing, ok := edges[edge.ID]; ok {
				if !sameCorpusEdge(existing, edge) {
					return KnowledgeGraph{}, fmt.Errorf("corpus batches disagree on edge %q", edge.ID)
				}
				if edge.Confidence > existing.Confidence {
					existing.Confidence = edge.Confidence
					edges[edge.ID] = existing
				}
				continue
			}
			edges[edge.ID] = edge
		}
	}

	merged := KnowledgeGraph{
		Nodes: make([]KnowledgeNode, 0, len(nodes)),
		Edges: make([]KnowledgeEdge, 0, len(edges)),
	}
	for _, node := range nodes {
		merged.Nodes = append(merged.Nodes, node)
	}
	for _, edge := range edges {
		merged.Edges = append(merged.Edges, edge)
	}
	sort.Slice(merged.Nodes, func(i, j int) bool { return merged.Nodes[i].ID < merged.Nodes[j].ID })
	sort.Slice(merged.Edges, func(i, j int) bool { return merged.Edges[i].ID < merged.Edges[j].ID })
	if len(merged.Nodes) == 0 {
		return merged, nil
	}
	if err := validateCorpusAnalysisFragment(merged); err != nil {
		return KnowledgeGraph{}, err
	}
	if err := ValidateKnowledgeGraph(merged); err != nil {
		return KnowledgeGraph{}, err
	}
	return merged, nil
}

func sameCorpusNode(left, right KnowledgeNode) bool {
	return left.ID == right.ID && left.Kind == right.Kind && left.Label == right.Label && left.Body == right.Body &&
		left.Status == right.Status && left.Origin == right.Origin && equalEvidenceAnchors(left.Evidence, right.Evidence)
}

func sameCorpusEdge(left, right KnowledgeEdge) bool {
	return left.ID == right.ID && left.From == right.From && left.To == right.To && left.Kind == right.Kind &&
		left.Label == right.Label && left.Status == right.Status && left.Origin == right.Origin &&
		equalEvidenceAnchors(left.Evidence, right.Evidence)
}
