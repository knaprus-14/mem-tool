package mem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	KnowledgeSelectionManifestVersion  = 1
	MaxKnowledgeSelectionNodes         = 200
	MaxKnowledgeSelectionEdges         = 400
	MaxKnowledgeSelectionQuestionRunes = 8000

	KnowledgeSelectionModeQuestion = "question"
	KnowledgeSelectionModeSummary  = "summary"

	KnowledgeSelectionExploreNeighbours = "neighbours"
	KnowledgeSelectionExplorePath       = "path"
	KnowledgeSelectionDirectionBoth     = "both"
	KnowledgeSelectionDirectionIncoming = "incoming"
	KnowledgeSelectionDirectionOutgoing = "outgoing"
	MaxKnowledgeSelectionExploreDepth   = 3
	MaxKnowledgeSelectionPathDepth      = 24
)

var (
	ErrKnowledgeSelectionEmpty       = errors.New("knowledge selection is empty")
	ErrKnowledgeSelectionChanged     = errors.New("knowledge selection manifest changed")
	ErrKnowledgeSelectionNotCurrent  = errors.New("knowledge selection evidence is not current")
	ErrKnowledgeSelectionUnavailable = errors.New("knowledge selection answer service is unavailable")
)

// KnowledgeSelectionRequest identifies the exact graph objects selected in the
// live map. IDs are sorted by the host before digesting, but duplicates are
// rejected so the client cannot accidentally present a different selection.
type KnowledgeSelectionRequest struct {
	NodeIDs []string `json:"node_ids,omitempty"`
	EdgeIDs []string `json:"edge_ids,omitempty"`
}

type KnowledgeSelectionObject struct {
	ObjectType     KnowledgeObjectType `json:"object_type"`
	ID             string              `json:"id"`
	Kind           string              `json:"kind"`
	Label          string              `json:"label,omitempty"`
	Status         KnowledgeStatus     `json:"status"`
	Origin         KnowledgeOrigin     `json:"origin"`
	EvidenceState  EvidenceState       `json:"evidence_state"`
	EvidenceDigest string              `json:"evidence_digest"`
	ContentDigest  string              `json:"content_digest"`
}

type KnowledgeSelectionBlocker struct {
	Code       string              `json:"code"`
	ObjectType KnowledgeObjectType `json:"object_type,omitempty"`
	ID         string              `json:"id,omitempty"`
	Message    string              `json:"message"`
}

type KnowledgeSelectionSummary struct {
	Nodes           int `json:"nodes"`
	Edges           int `json:"edges"`
	Documents       int `json:"documents"`
	Pages           int `json:"pages"`
	Evidence        int `json:"evidence"`
	CurrentEvidence int `json:"current_evidence"`
	StaleEvidence   int `json:"stale_evidence"`
	MissingEvidence int `json:"missing_evidence"`
}

// KnowledgeSelectionManifest is a deterministic, host-derived pin of the
// selected subgraph and every exact evidence anchor visible at that moment.
// The digest must be echoed by model operations and is rebuilt immediately
// before generation, preventing stale browser state from changing the scope.
type KnowledgeSelectionManifest struct {
	Version  int                         `json:"version"`
	Digest   string                      `json:"digest"`
	Ready    bool                        `json:"ready"`
	Summary  KnowledgeSelectionSummary   `json:"summary"`
	Nodes    []KnowledgeSelectionObject  `json:"nodes"`
	Edges    []KnowledgeSelectionObject  `json:"edges"`
	Evidence []GroundedEvidence          `json:"evidence"`
	Blockers []KnowledgeSelectionBlocker `json:"blockers,omitempty"`
}

type KnowledgeSelectionAnswerRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	Mode                   string                    `json:"mode"`
	Question               string                    `json:"question,omitempty"`
}

type KnowledgeSelectionAnswerResult struct {
	Mode           string                    `json:"mode"`
	Answer         string                    `json:"answer"`
	Insufficient   bool                      `json:"insufficient,omitempty"`
	ManifestDigest string                    `json:"manifest_digest"`
	Sources        []GroundedEvidence        `json:"sources,omitempty"`
	Summary        KnowledgeSelectionSummary `json:"selection_summary"`
}

// KnowledgeSelectionExploreRequest describes a host-side graph navigation
// operation over a pinned selection. It never invokes a model and never
// mutates the graph. RelationKinds empty means all supported relation kinds.
type KnowledgeSelectionExploreRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	Mode                   string                    `json:"mode"`
	Direction              string                    `json:"direction,omitempty"`
	Depth                  int                       `json:"depth,omitempty"`
	RelationKinds          []KnowledgeRelationKind   `json:"relation_kinds,omitempty"`
}

type KnowledgeSelectionExploreResult struct {
	Mode                 string                     `json:"mode"`
	Direction            string                     `json:"direction"`
	Depth                int                        `json:"depth"`
	SourceManifestDigest string                     `json:"source_manifest_digest"`
	Selection            KnowledgeSelectionRequest  `json:"selection"`
	Manifest             KnowledgeSelectionManifest `json:"manifest"`
	AddedNodeIDs         []string                   `json:"added_node_ids,omitempty"`
	AddedEdgeIDs         []string                   `json:"added_edge_ids,omitempty"`
	PathFound            bool                       `json:"path_found,omitempty"`
	Truncated            bool                       `json:"truncated,omitempty"`
}

// KnowledgeSelectionAnswerService contains only the local answer dependency
// needed by live selected-subgraph operations. It does not perform retrieval:
// the exact current anchors in the pinned selection are the complete context.
type KnowledgeSelectionAnswerService struct {
	Provider AnswerProvider
	Config   AnswerConfig
}

func (s *Store) BuildKnowledgeSelectionManifest(request KnowledgeSelectionRequest) (KnowledgeSelectionManifest, error) {
	nodeIDs, err := normalizeKnowledgeSelectionIDs(request.NodeIDs, MaxKnowledgeSelectionNodes, "node")
	if err != nil {
		return KnowledgeSelectionManifest{}, err
	}
	edgeIDs, err := normalizeKnowledgeSelectionIDs(request.EdgeIDs, MaxKnowledgeSelectionEdges, "edge")
	if err != nil {
		return KnowledgeSelectionManifest{}, err
	}
	if len(nodeIDs)+len(edgeIDs) == 0 {
		return KnowledgeSelectionManifest{}, ErrKnowledgeSelectionEmpty
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionManifest{}, err
	}
	report, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionManifest{}, err
	}
	nodes := make(map[string]KnowledgeNode, len(graph.Nodes))
	edges := make(map[string]KnowledgeEdge, len(graph.Edges))
	reviews := make(map[string]KnowledgeReviewItem, len(report.Items))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range graph.Edges {
		edges[edge.ID] = edge
	}
	for _, item := range report.Items {
		reviews[string(item.ObjectType)+":"+item.ID] = item
	}

	manifest := KnowledgeSelectionManifest{
		Version:  KnowledgeSelectionManifestVersion,
		Nodes:    make([]KnowledgeSelectionObject, 0, len(nodeIDs)),
		Edges:    make([]KnowledgeSelectionObject, 0, len(edgeIDs)),
		Evidence: make([]GroundedEvidence, 0),
		Blockers: make([]KnowledgeSelectionBlocker, 0),
	}
	evidenceByCitation := make(map[string]GroundedEvidence)
	documents, pages := make(map[string]bool), make(map[string]bool)
	appendObject := func(objectType KnowledgeObjectType, id, kind, label string, status KnowledgeStatus, origin KnowledgeOrigin) error {
		item, ok := reviews[string(objectType)+":"+id]
		if !ok {
			return fmt.Errorf("knowledge selection %s %q has no review state", objectType, id)
		}
		if item.Kind != kind || item.Label != label || item.Status != status || item.Origin != origin {
			return fmt.Errorf("%w: knowledge selection %s %q changed while its manifest was being built", ErrKnowledgeSelectionChanged, objectType, id)
		}
		selected := KnowledgeSelectionObject{
			ObjectType: objectType, ID: id, Kind: kind, Label: label, Status: status, Origin: origin,
			EvidenceState: item.EvidenceState, EvidenceDigest: item.EvidenceDigest, ContentDigest: item.ContentDigest,
		}
		if objectType == KnowledgeObjectNode {
			manifest.Nodes = append(manifest.Nodes, selected)
		} else {
			manifest.Edges = append(manifest.Edges, selected)
		}
		if status == KnowledgeStatusRejected || status == KnowledgeStatusResolved {
			manifest.Blockers = append(manifest.Blockers, KnowledgeSelectionBlocker{
				Code: "object_closed", ObjectType: objectType, ID: id,
				Message: "Отклонённый или закрытый объект нельзя использовать как контекст ответа.",
			})
		}
		if len(item.Evidence) == 0 {
			manifest.Blockers = append(manifest.Blockers, KnowledgeSelectionBlocker{
				Code: "evidence_empty", ObjectType: objectType, ID: id,
				Message: "У выбранного объекта нет проверяемых источников.",
			})
		}
		if item.EvidenceState != EvidenceCurrent {
			manifest.Blockers = append(manifest.Blockers, KnowledgeSelectionBlocker{
				Code: "evidence_" + string(item.EvidenceState), ObjectType: objectType, ID: id,
				Message: "Источник выбранного объекта устарел или отсутствует.",
			})
		}
		for _, resolution := range item.Evidence {
			grounded, buildErr := groundedEvidenceForSelectionAnchor(resolution.Anchor)
			if buildErr != nil {
				return buildErr
			}
			if existing, exists := evidenceByCitation[grounded.CitationID]; exists {
				if !reflect.DeepEqual(existing, grounded) {
					return fmt.Errorf("knowledge selection citation %q resolves to conflicting anchors", grounded.CitationID)
				}
				continue
			}
			evidenceByCitation[grounded.CitationID] = grounded
			switch resolution.State {
			case EvidenceCurrent:
				manifest.Summary.CurrentEvidence++
			case EvidenceStale:
				manifest.Summary.StaleEvidence++
			case EvidenceMissing:
				manifest.Summary.MissingEvidence++
			}
			documentKey := firstNonEmpty(grounded.DocumentID, grounded.SourcePath)
			if documentKey != "" {
				documents[documentKey] = true
				if grounded.Page > 0 {
					pages[documentKey+"\x00"+fmt.Sprint(grounded.Page)] = true
				}
			}
		}
		return nil
	}
	for _, id := range nodeIDs {
		node, ok := nodes[id]
		if !ok {
			return KnowledgeSelectionManifest{}, fmt.Errorf("knowledge selection node %q not found", id)
		}
		if err := appendObject(KnowledgeObjectNode, node.ID, string(node.Kind), node.Label, node.Status, node.Origin); err != nil {
			return KnowledgeSelectionManifest{}, err
		}
	}
	for _, id := range edgeIDs {
		edge, ok := edges[id]
		if !ok {
			return KnowledgeSelectionManifest{}, fmt.Errorf("knowledge selection edge %q not found", id)
		}
		if err := appendObject(KnowledgeObjectEdge, edge.ID, string(edge.Kind), edge.Label, edge.Status, edge.Origin); err != nil {
			return KnowledgeSelectionManifest{}, err
		}
	}
	for _, item := range evidenceByCitation {
		manifest.Evidence = append(manifest.Evidence, item)
	}
	sort.Slice(manifest.Evidence, func(i, j int) bool { return manifest.Evidence[i].CitationID < manifest.Evidence[j].CitationID })
	manifest.Summary.Nodes = len(manifest.Nodes)
	manifest.Summary.Edges = len(manifest.Edges)
	manifest.Summary.Documents = len(documents)
	manifest.Summary.Pages = len(pages)
	manifest.Summary.Evidence = len(manifest.Evidence)
	if len(manifest.Evidence) == 0 {
		manifest.Blockers = append(manifest.Blockers, KnowledgeSelectionBlocker{Code: "selection_without_evidence", Message: "В выбранной области нет evidence."})
	}
	manifest.Ready = len(manifest.Blockers) == 0
	manifest.Digest, err = knowledgeSelectionManifestDigest(manifest)
	if err != nil {
		return KnowledgeSelectionManifest{}, err
	}
	return manifest, nil
}

func normalizeKnowledgeSelectionIDs(values []string, limit int, kind string) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("knowledge selection exceeds %d %ss", limit, kind)
	}
	result := append([]string(nil), values...)
	seen := make(map[string]bool, len(result))
	for _, id := range result {
		if err := validateKnowledgeID(id); err != nil {
			return nil, fmt.Errorf("knowledge selection %s: %w", kind, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("knowledge selection contains duplicate %s %q", kind, id)
		}
		seen[id] = true
	}
	sort.Strings(result)
	return result, nil
}

func groundedEvidenceForSelectionAnchor(anchor EvidenceAnchor) (GroundedEvidence, error) {
	if err := validateEvidenceAnchor(anchor); err != nil {
		return GroundedEvidence{}, fmt.Errorf("knowledge selection contains invalid evidence: %w", err)
	}
	name := filepath.Base(anchor.SourcePath)
	if name == "." || name == "" {
		name = anchor.DocumentID
	}
	location := make([]string, 0, 3)
	if anchor.Page > 0 {
		location = append(location, fmt.Sprintf("page %d", anchor.Page))
	}
	location = append(location, fmt.Sprintf("block %d", anchor.BlockIndex+1), fmt.Sprintf("chunk %d", anchor.BlockChunkIndex+1))
	return GroundedEvidence{
		CitationID: anchor.CitationID, CitationLabel: name + " | " + strings.Join(location, " | "),
		DocumentID: anchor.DocumentID, DocumentRevision: anchor.DocumentRevision,
		SourcePath: anchor.SourcePath, Page: anchor.Page, BlockIndex: anchor.BlockIndex,
		BlockChunkIndex: anchor.BlockChunkIndex, ChunkHash: anchor.ChunkHash,
		EvidenceHash: anchor.EvidenceHash, Text: anchor.Excerpt,
	}, nil
}

func knowledgeSelectionManifestDigest(manifest KnowledgeSelectionManifest) (string, error) {
	pinned := struct {
		Version  int                        `json:"version"`
		Nodes    []KnowledgeSelectionObject `json:"nodes"`
		Edges    []KnowledgeSelectionObject `json:"edges"`
		Evidence []GroundedEvidence         `json:"evidence"`
	}{manifest.Version, manifest.Nodes, manifest.Edges, manifest.Evidence}
	encoded, err := json.Marshal(pinned)
	if err != nil {
		return "", fmt.Errorf("encode knowledge selection manifest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ExploreKnowledgeSelection expands a pinned selection through exact graph
// relations or finds a deterministic shortest path between two selected
// nodes. The returned manifest pins the resulting subgraph for a subsequent
// answer operation.
func (s *Store) ExploreKnowledgeSelection(request KnowledgeSelectionExploreRequest) (KnowledgeSelectionExploreResult, error) {
	sourceManifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	if request.ExpectedManifestDigest == "" || request.ExpectedManifestDigest != sourceManifest.Digest {
		return KnowledgeSelectionExploreResult{}, fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeSelectionChanged, request.ExpectedManifestDigest, sourceManifest.Digest)
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	direction := strings.ToLower(strings.TrimSpace(request.Direction))
	if direction == "" {
		direction = KnowledgeSelectionDirectionBoth
	}
	if direction != KnowledgeSelectionDirectionBoth && direction != KnowledgeSelectionDirectionIncoming && direction != KnowledgeSelectionDirectionOutgoing {
		return KnowledgeSelectionExploreResult{}, fmt.Errorf("unsupported knowledge selection direction %q", request.Direction)
	}
	depth := request.Depth
	switch mode {
	case KnowledgeSelectionExploreNeighbours:
		if depth == 0 {
			depth = 1
		}
		if depth < 1 || depth > MaxKnowledgeSelectionExploreDepth {
			return KnowledgeSelectionExploreResult{}, fmt.Errorf("knowledge selection neighbour depth must be between 1 and %d", MaxKnowledgeSelectionExploreDepth)
		}
	case KnowledgeSelectionExplorePath:
		if depth == 0 {
			depth = MaxKnowledgeSelectionPathDepth
		}
		if depth < 1 || depth > MaxKnowledgeSelectionPathDepth {
			return KnowledgeSelectionExploreResult{}, fmt.Errorf("knowledge selection path depth must be between 1 and %d", MaxKnowledgeSelectionPathDepth)
		}
	default:
		return KnowledgeSelectionExploreResult{}, fmt.Errorf("unsupported knowledge selection explore mode %q", request.Mode)
	}
	allowedKinds, err := normalizeKnowledgeSelectionRelationKinds(request.RelationKinds)
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	nodeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.NodeIDs, MaxKnowledgeSelectionNodes, "node")
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	edgeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.EdgeIDs, MaxKnowledgeSelectionEdges, "edge")
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From != graph.Edges[j].From {
			return graph.Edges[i].From < graph.Edges[j].From
		}
		if graph.Edges[i].To != graph.Edges[j].To {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		return graph.Edges[i].ID < graph.Edges[j].ID
	})
	nodes := make(map[string]bool, len(nodeIDs))
	edges := make(map[string]bool, len(edgeIDs))
	for _, id := range nodeIDs {
		nodes[id] = true
	}
	for _, id := range edgeIDs {
		edges[id] = true
	}
	result := KnowledgeSelectionExploreResult{
		Mode: mode, Direction: direction, Depth: depth, SourceManifestDigest: sourceManifest.Digest,
	}
	if mode == KnowledgeSelectionExploreNeighbours {
		result.Truncated = exploreKnowledgeNeighbours(graph, nodes, edges, allowedKinds, direction, depth)
	} else {
		if len(nodeIDs) != 2 {
			return KnowledgeSelectionExploreResult{}, errors.New("knowledge selection path requires exactly two selected nodes")
		}
		result.PathFound = exploreKnowledgeShortestPath(graph, nodes, edges, allowedKinds, direction, nodeIDs[0], nodeIDs[1], depth)
	}
	result.Selection.NodeIDs = sortedKnowledgeSelectionSet(nodes)
	result.Selection.EdgeIDs = sortedKnowledgeSelectionSet(edges)
	result.AddedNodeIDs = knowledgeSelectionDifference(result.Selection.NodeIDs, nodeIDs)
	result.AddedEdgeIDs = knowledgeSelectionDifference(result.Selection.EdgeIDs, edgeIDs)
	result.Manifest, err = s.BuildKnowledgeSelectionManifest(result.Selection)
	if err != nil {
		return KnowledgeSelectionExploreResult{}, err
	}
	currentSource, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil || currentSource.Digest != sourceManifest.Digest {
		return KnowledgeSelectionExploreResult{}, fmt.Errorf("%w: selection changed while graph navigation was running", ErrKnowledgeSelectionChanged)
	}
	return result, nil
}

func normalizeKnowledgeSelectionRelationKinds(values []KnowledgeRelationKind) (map[KnowledgeRelationKind]bool, error) {
	allowed := make(map[KnowledgeRelationKind]bool, len(values))
	for _, kind := range values {
		if !validKnowledgeRelationKind(kind) {
			return nil, fmt.Errorf("unsupported knowledge selection relation kind %q", kind)
		}
		if allowed[kind] {
			return nil, fmt.Errorf("knowledge selection contains duplicate relation kind %q", kind)
		}
		allowed[kind] = true
	}
	return allowed, nil
}

func knowledgeSelectionEdgeAllowed(edge KnowledgeEdge, allowed map[KnowledgeRelationKind]bool) bool {
	return len(allowed) == 0 || allowed[edge.Kind]
}

func knowledgeSelectionStep(edge KnowledgeEdge, from, direction string) (string, bool) {
	switch direction {
	case KnowledgeSelectionDirectionOutgoing:
		return edge.To, edge.From == from
	case KnowledgeSelectionDirectionIncoming:
		return edge.From, edge.To == from
	default:
		if edge.From == from {
			return edge.To, true
		}
		return edge.From, edge.To == from
	}
}

func exploreKnowledgeNeighbours(graph KnowledgeGraph, nodes, selectedEdges map[string]bool, allowed map[KnowledgeRelationKind]bool, direction string, depth int) bool {
	edgeByID := make(map[string]KnowledgeEdge, len(graph.Edges))
	for _, edge := range graph.Edges {
		edgeByID[edge.ID] = edge
	}
	frontier := make(map[string]bool, len(nodes))
	for id := range nodes {
		frontier[id] = true
	}
	for id := range selectedEdges {
		if edge, ok := edgeByID[id]; ok {
			nodes[edge.From], nodes[edge.To] = true, true
			frontier[edge.From], frontier[edge.To] = true, true
		}
	}
	truncated := false
	for level := 0; level < depth && len(frontier) > 0; level++ {
		next := make(map[string]bool)
		for _, edge := range graph.Edges {
			if !knowledgeSelectionEdgeAllowed(edge, allowed) {
				continue
			}
			for from := range frontier {
				other, ok := knowledgeSelectionStep(edge, from, direction)
				if !ok {
					continue
				}
				needNode, needEdge := !nodes[other], !selectedEdges[edge.ID]
				if (needNode && len(nodes) >= MaxKnowledgeSelectionNodes) || (needEdge && len(selectedEdges) >= MaxKnowledgeSelectionEdges) {
					truncated = true
					continue
				}
				selectedEdges[edge.ID] = true
				if needNode {
					nodes[other] = true
					next[other] = true
				}
				break
			}
		}
		frontier = next
	}
	return truncated
}

func exploreKnowledgeShortestPath(graph KnowledgeGraph, nodes, selectedEdges map[string]bool, allowed map[KnowledgeRelationKind]bool, direction, start, target string, maxDepth int) bool {
	type predecessor struct{ node, edge string }
	seen := map[string]bool{start: true}
	previous := make(map[string]predecessor)
	frontier := []string{start}
	found := false
	for level := 0; level < maxDepth && len(frontier) > 0 && !found; level++ {
		next := make([]string, 0)
		for _, current := range frontier {
			for _, edge := range graph.Edges {
				if !knowledgeSelectionEdgeAllowed(edge, allowed) {
					continue
				}
				other, ok := knowledgeSelectionStep(edge, current, direction)
				if !ok || seen[other] {
					continue
				}
				seen[other] = true
				previous[other] = predecessor{node: current, edge: edge.ID}
				next = append(next, other)
				if other == target {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		frontier = next
	}
	if !found {
		return false
	}
	for current := target; current != start; {
		step := previous[current]
		nodes[current] = true
		selectedEdges[step.edge] = true
		current = step.node
	}
	nodes[start] = true
	return true
}

func sortedKnowledgeSelectionSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func knowledgeSelectionDifference(values, original []string) []string {
	seen := make(map[string]bool, len(original))
	for _, value := range original {
		seen[value] = true
	}
	result := make([]string, 0)
	for _, value := range values {
		if !seen[value] {
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) AnswerKnowledgeSelection(ctx context.Context, service *KnowledgeSelectionAnswerService, request KnowledgeSelectionAnswerRequest) (KnowledgeSelectionAnswerResult, error) {
	if service == nil || service.Provider == nil {
		return KnowledgeSelectionAnswerResult{}, ErrKnowledgeSelectionUnavailable
	}
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil {
		return KnowledgeSelectionAnswerResult{}, err
	}
	if request.ExpectedManifestDigest == "" || request.ExpectedManifestDigest != manifest.Digest {
		return KnowledgeSelectionAnswerResult{}, fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeSelectionChanged, request.ExpectedManifestDigest, manifest.Digest)
	}
	if !manifest.Ready {
		return KnowledgeSelectionAnswerResult{}, ErrKnowledgeSelectionNotCurrent
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	question := strings.TrimSpace(request.Question)
	switch mode {
	case KnowledgeSelectionModeQuestion:
		if question == "" {
			return KnowledgeSelectionAnswerResult{}, errors.New("knowledge selection question is empty")
		}
	case KnowledgeSelectionModeSummary:
		if question == "" {
			question = "Составь подробную, но компактную сводку выбранных сведений. Не добавляй информацию вне evidence."
		} else {
			question = "Составь сводку выбранных сведений с акцентом на следующую тему: " + question
		}
	default:
		return KnowledgeSelectionAnswerResult{}, fmt.Errorf("unsupported knowledge selection answer mode %q", request.Mode)
	}
	if utf8.RuneCountInString(question) > MaxKnowledgeSelectionQuestionRunes {
		return KnowledgeSelectionAnswerResult{}, fmt.Errorf("knowledge selection question exceeds %d characters", MaxKnowledgeSelectionQuestionRunes)
	}
	answerCfg := service.Config.WithDefaults()
	prompt, err := buildGroundedPromptFromEvidence(question, manifest.Evidence, answerCfg.ContextChars)
	if err != nil {
		return KnowledgeSelectionAnswerResult{}, err
	}
	answerRequest := AnswerRequest{
		Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
		MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
	}
	schemaMode := UsesGroundedAnswerSchema(answerCfg.Model)
	if schemaMode {
		answerRequest, err = GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if err != nil {
			return KnowledgeSelectionAnswerResult{}, err
		}
	}
	generationContext, cancel := AnswerContext(ctx, answerCfg)
	defer cancel()
	raw, err := service.Provider.Generate(generationContext, answerRequest)
	if err != nil {
		return KnowledgeSelectionAnswerResult{}, err
	}
	validated := ValidateGroundedAnswer(raw, prompt.Evidence)
	if schemaMode {
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	} else if ShouldRetryGroundedAnswerWithSchema(raw, validated) {
		retry, schemaErr := GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if schemaErr != nil {
			return KnowledgeSelectionAnswerResult{}, schemaErr
		}
		raw, err = service.Provider.Generate(generationContext, retry)
		if err != nil {
			return KnowledgeSelectionAnswerResult{}, err
		}
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	}
	if validated.Rejected {
		return KnowledgeSelectionAnswerResult{}, fmt.Errorf("selected grounded answer rejected: %s", validated.Reason)
	}
	return KnowledgeSelectionAnswerResult{
		Mode: mode, Answer: validated.Answer, Insufficient: validated.Insufficient,
		ManifestDigest: manifest.Digest, Sources: validated.Used, Summary: manifest.Summary,
	}, nil
}

func buildGroundedPromptFromEvidence(question string, source []GroundedEvidence, contextBudget int) (GroundedPrompt, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return GroundedPrompt{}, errors.New("question is empty")
	}
	if contextBudget <= 0 {
		contextBudget = DefaultAnswerContextChars
	}
	if contextBudget > MaxAnswerContextChars {
		return GroundedPrompt{}, fmt.Errorf("grounded prompt context budget must not exceed %d", MaxAnswerContextChars)
	}
	questionJSON, err := json.Marshal(question)
	if err != nil {
		return GroundedPrompt{}, err
	}
	build := func(evidence []GroundedEvidence) (GroundedPrompt, int, error) {
		encoded, encodeErr := json.MarshalIndent(evidence, "", "  ")
		if encodeErr != nil {
			return GroundedPrompt{}, 0, encodeErr
		}
		user := "Question (user input): " + string(questionJSON) + "\n\nEVIDENCE_JSON_BEGIN\n" + string(encoded) + "\nEVIDENCE_JSON_END\n"
		prompt := GroundedPrompt{System: groundedSystemPrompt, User: user, Evidence: evidence}
		return prompt, utf8.RuneCountInString(prompt.System) + utf8.RuneCountInString(prompt.User), nil
	}
	_, baseSize, err := build(nil)
	if err != nil {
		return GroundedPrompt{}, err
	}
	if baseSize > contextBudget {
		return GroundedPrompt{}, fmt.Errorf("grounded prompt context budget %d is too small for instructions and question (%d)", contextBudget, baseSize)
	}
	selected := make([]GroundedEvidence, 0, len(source))
	seen := make(map[string]bool, len(source))
	for _, input := range source {
		if strings.TrimSpace(input.Text) == "" || seen[input.CitationID] {
			return GroundedPrompt{}, fmt.Errorf("selected evidence %q is empty or duplicated", input.CitationID)
		}
		if _, anchorErr := evidenceAnchorFromGrounded(input); anchorErr != nil {
			return GroundedPrompt{}, fmt.Errorf("selected evidence %q is invalid: %w", input.CitationID, anchorErr)
		}
		seen[input.CitationID] = true
		full := input
		full.EvidenceRef = fmt.Sprintf("E%d", len(selected)+1)
		trial := append(append([]GroundedEvidence(nil), selected...), full)
		if _, size, buildErr := build(trial); buildErr != nil {
			return GroundedPrompt{}, buildErr
		} else if size <= contextBudget {
			selected = trial
			continue
		}
		runes := []rune(input.Text)
		low, high, best := 0, len(runes), 0
		for low <= high {
			middle := low + (high-low)/2
			candidate := full
			candidate.Text = string(runes[:middle])
			candidate.EvidenceHash = ChunkContentHash(candidate.Text)
			candidate.Truncated = true
			trial = append(append([]GroundedEvidence(nil), selected...), candidate)
			_, size, buildErr := build(trial)
			if buildErr != nil {
				return GroundedPrompt{}, buildErr
			}
			if size <= contextBudget {
				best = middle
				low = middle + 1
			} else {
				high = middle - 1
			}
		}
		if best > 0 {
			candidate := full
			candidate.Text = string(runes[:best])
			candidate.EvidenceHash = ChunkContentHash(candidate.Text)
			candidate.Truncated = true
			selected = append(selected, candidate)
		}
		break
	}
	if len(selected) == 0 {
		return GroundedPrompt{}, errors.New("selected evidence does not fit the configured context budget")
	}
	prompt, _, err := build(selected)
	return prompt, err
}
