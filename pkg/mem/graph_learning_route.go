package mem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const KnowledgeLearningRouteVersion = 1

// KnowledgeLearningRouteRequest pins the user's current selection before a
// read-only route is derived from the reviewed graph. No model is involved.
type KnowledgeLearningRouteRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
}

type KnowledgeLearningRouteItem struct {
	ID            string                        `json:"id"`
	Kind          KnowledgeNodeKind             `json:"kind"`
	Prompt        string                        `json:"prompt"`
	Answer        string                        `json:"answer"`
	Level         int                           `json:"level"`
	Selected      bool                          `json:"selected,omitempty"`
	EvidenceState EvidenceState                 `json:"evidence_state"`
	Sources       []KnowledgeSelectionSourceRef `json:"sources"`
	Cyclic        bool                          `json:"cyclic,omitempty"`
}

type KnowledgeLearningRouteRelation struct {
	ID            string                        `json:"id"`
	Kind          KnowledgeRelationKind         `json:"kind"`
	From          string                        `json:"from"`
	To            string                        `json:"to"`
	Before        string                        `json:"before"`
	After         string                        `json:"after"`
	EvidenceState EvidenceState                 `json:"evidence_state"`
	Sources       []KnowledgeSelectionSourceRef `json:"sources"`
}

type KnowledgeLearningRouteExclusion struct {
	ID            string            `json:"id"`
	Kind          KnowledgeNodeKind `json:"kind"`
	Label         string            `json:"label"`
	Status        KnowledgeStatus   `json:"status"`
	EvidenceState EvidenceState     `json:"evidence_state"`
	Reason        string            `json:"reason"`
}

type KnowledgeLearningRouteWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type KnowledgeLearningRouteSummary struct {
	Items               int `json:"items"`
	Levels              int `json:"levels"`
	OrderRelations      int `json:"order_relations"`
	Excluded            int `json:"excluded"`
	UnreviewedRelations int `json:"unreviewed_relations"`
	CycleItems          int `json:"cycle_items"`
}

// KnowledgeLearningRoute contains only reviewed cards/questions. Explicit,
// active and current prerequisite/depends_on edges determine their order.
// Warnings make omissions and uncertainty visible instead of inventing order.
type KnowledgeLearningRoute struct {
	Version        int                               `json:"version"`
	Digest         string                            `json:"digest"`
	ManifestDigest string                            `json:"manifest_digest"`
	Ready          bool                              `json:"ready"`
	ExplicitOrder  bool                              `json:"explicit_order"`
	Summary        KnowledgeLearningRouteSummary     `json:"summary"`
	Items          []KnowledgeLearningRouteItem      `json:"items"`
	Relations      []KnowledgeLearningRouteRelation  `json:"relations,omitempty"`
	Excluded       []KnowledgeLearningRouteExclusion `json:"excluded,omitempty"`
	Warnings       []KnowledgeLearningRouteWarning   `json:"warnings,omitempty"`
}

func (s *Store) BuildKnowledgeLearningRoute(request KnowledgeLearningRouteRequest) (KnowledgeLearningRoute, error) {
	nodeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.NodeIDs, MaxKnowledgeSelectionNodes, "node")
	if err != nil {
		return KnowledgeLearningRoute{}, err
	}
	edgeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.EdgeIDs, MaxKnowledgeSelectionEdges, "edge")
	if err != nil {
		return KnowledgeLearningRoute{}, err
	}
	request.Selection = KnowledgeSelectionRequest{NodeIDs: nodeIDs, EdgeIDs: edgeIDs}
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil {
		return KnowledgeLearningRoute{}, err
	}
	if request.ExpectedManifestDigest == "" || request.ExpectedManifestDigest != manifest.Digest {
		return KnowledgeLearningRoute{}, fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeSelectionChanged, request.ExpectedManifestDigest, manifest.Digest)
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeLearningRoute{}, err
	}
	report, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeLearningRoute{}, err
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

	selected := make(map[string]bool, len(nodeIDs)+len(edgeIDs)*2)
	for _, id := range nodeIDs {
		selected[id] = true
	}
	for _, id := range edgeIDs {
		if edge, ok := edges[id]; ok {
			selected[edge.From], selected[edge.To] = true, true
		}
	}
	eligible := func(node KnowledgeNode) bool {
		if node.Kind != KnowledgeNodeCard && node.Kind != KnowledgeNodeQuestion {
			return false
		}
		review, ok := reviews[string(KnowledgeObjectNode)+":"+node.ID]
		return ok && node.Status == KnowledgeStatusActive && review.EvidenceState == EvidenceCurrent && len(review.Evidence) > 0
	}
	result := KnowledgeLearningRoute{Version: KnowledgeLearningRouteVersion, ManifestDigest: manifest.Digest}
	admitted := make(map[string]bool)
	exclusionByID := make(map[string]KnowledgeLearningRouteExclusion)
	addExclusion := func(node KnowledgeNode, reason string) {
		if _, exists := exclusionByID[node.ID]; exists {
			return
		}
		review := reviews[string(KnowledgeObjectNode)+":"+node.ID]
		exclusionByID[node.ID] = KnowledgeLearningRouteExclusion{ID: node.ID, Kind: node.Kind, Label: node.Label, Status: node.Status, EvidenceState: review.EvidenceState, Reason: reason}
	}
	consider := func(node KnowledgeNode, reason string) bool {
		if node.Kind != KnowledgeNodeCard && node.Kind != KnowledgeNodeQuestion {
			return false
		}
		if eligible(node) {
			admitted[node.ID] = true
			return true
		}
		addExclusion(node, reason)
		return false
	}
	for id := range selected {
		if node, ok := nodes[id]; ok {
			consider(node, knowledgeLearningRouteNodeReason(node, reviews[string(KnowledgeObjectNode)+":"+id]))
		}
	}
	// Provenance edges associate reviewed learning objects with a selected
	// source/analysis node. They scope the route but never establish its order.
	for _, edge := range graph.Edges {
		if edge.Kind != KnowledgeRelationDerivedFrom && edge.Kind != KnowledgeRelationAsks {
			continue
		}
		if !selected[edge.To] || edge.Status == KnowledgeStatusRejected || edge.Status == KnowledgeStatusResolved {
			continue
		}
		review := reviews[string(KnowledgeObjectEdge)+":"+edge.ID]
		if review.EvidenceState != EvidenceCurrent {
			continue
		}
		if node, ok := nodes[edge.From]; ok {
			consider(node, knowledgeLearningRouteNodeReason(node, reviews[string(KnowledgeObjectNode)+":"+node.ID]))
		}
	}

	// Follow only reviewed, current order relations. This admits connected
	// reviewed learning objects while recording non-admissible prerequisites.
	changed := true
	for changed {
		changed = false
		for _, edge := range graph.Edges {
			if edge.Kind != KnowledgeRelationPrerequisite && edge.Kind != KnowledgeRelationDependsOn {
				continue
			}
			if !admitted[edge.From] && !admitted[edge.To] {
				continue
			}
			otherID := edge.From
			if admitted[edge.From] {
				otherID = edge.To
			}
			other, ok := nodes[otherID]
			if !ok || (other.Kind != KnowledgeNodeCard && other.Kind != KnowledgeNodeQuestion) {
				continue
			}
			edgeReview := reviews[string(KnowledgeObjectEdge)+":"+edge.ID]
			if edge.Status != KnowledgeStatusActive || edgeReview.EvidenceState != EvidenceCurrent || len(edgeReview.Evidence) == 0 {
				continue
			}
			before := len(admitted)
			consider(other, knowledgeLearningRouteNodeReason(other, reviews[string(KnowledgeObjectNode)+":"+other.ID]))
			changed = changed || len(admitted) > before
		}
	}

	adjacency := make(map[string]map[string]bool, len(admitted))
	indegree := make(map[string]int, len(admitted))
	level := make(map[string]int, len(admitted))
	for id := range admitted {
		indegree[id] = 0
	}
	unreviewedRelations := 0
	for _, edge := range graph.Edges {
		if edge.Kind != KnowledgeRelationPrerequisite && edge.Kind != KnowledgeRelationDependsOn {
			continue
		}
		if !admitted[edge.From] || !admitted[edge.To] || edge.From == edge.To {
			continue
		}
		review := reviews[string(KnowledgeObjectEdge)+":"+edge.ID]
		if edge.Status != KnowledgeStatusActive || review.EvidenceState != EvidenceCurrent || len(review.Evidence) == 0 {
			unreviewedRelations++
			continue
		}
		before, after := edge.From, edge.To
		if edge.Kind == KnowledgeRelationDependsOn {
			before, after = edge.To, edge.From
		}
		if adjacency[before] == nil {
			adjacency[before] = make(map[string]bool)
		}
		if !adjacency[before][after] {
			adjacency[before][after] = true
			indegree[after]++
		}
		result.Relations = append(result.Relations, KnowledgeLearningRouteRelation{ID: edge.ID, Kind: edge.Kind, From: edge.From, To: edge.To, Before: before, After: after, EvidenceState: review.EvidenceState, Sources: knowledgeSelectionSourceRefs(edge.Evidence)})
	}
	sort.Slice(result.Relations, func(i, j int) bool {
		if result.Relations[i].Before != result.Relations[j].Before {
			return result.Relations[i].Before < result.Relations[j].Before
		}
		if result.Relations[i].After != result.Relations[j].After {
			return result.Relations[i].After < result.Relations[j].After
		}
		return result.Relations[i].ID < result.Relations[j].ID
	})
	lessID := func(left, right string) bool {
		leftNode, rightNode := nodes[left], nodes[right]
		leftLabel, rightLabel := strings.ToLower(leftNode.Label), strings.ToLower(rightNode.Label)
		if leftLabel != rightLabel {
			return leftLabel < rightLabel
		}
		return left < right
	}
	ready := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return lessID(ready[i], ready[j]) })
	ordered := make([]string, 0, len(admitted))
	seen := make(map[string]bool, len(admitted))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, id)
		nextIDs := make([]string, 0, len(adjacency[id]))
		for next := range adjacency[id] {
			nextIDs = append(nextIDs, next)
		}
		sort.Slice(nextIDs, func(i, j int) bool { return lessID(nextIDs[i], nextIDs[j]) })
		for _, next := range nextIDs {
			if level[next] < level[id]+1 {
				level[next] = level[id] + 1
			}
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
		sort.Slice(ready, func(i, j int) bool { return lessID(ready[i], ready[j]) })
	}
	cycleIDs := make(map[string]bool)
	if len(ordered) != len(admitted) {
		remaining := make([]string, 0, len(admitted)-len(ordered))
		maxLevel := 0
		for _, value := range level {
			if value > maxLevel {
				maxLevel = value
			}
		}
		for id := range admitted {
			if !seen[id] {
				remaining = append(remaining, id)
				cycleIDs[id] = true
				level[id] = maxLevel + 1
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return lessID(remaining[i], remaining[j]) })
		ordered = append(ordered, remaining...)
		result.Warnings = append(result.Warnings, KnowledgeLearningRouteWarning{Code: "dependency_cycle", Message: "В подтверждённых связях prerequisite/depends_on найден цикл; циклические элементы вынесены в отдельный уровень и требуют исправления связей."})
	}
	for _, id := range ordered {
		node := nodes[id]
		review := reviews[string(KnowledgeObjectNode)+":"+id]
		result.Items = append(result.Items, KnowledgeLearningRouteItem{ID: id, Kind: node.Kind, Prompt: node.Label, Answer: knowledgeLearningRouteAnswer(node), Level: level[id], Selected: selected[id], EvidenceState: review.EvidenceState, Sources: knowledgeSelectionSourceRefs(node.Evidence), Cyclic: cycleIDs[id]})
	}
	for _, exclusion := range exclusionByID {
		result.Excluded = append(result.Excluded, exclusion)
	}
	sort.Slice(result.Excluded, func(i, j int) bool {
		if result.Excluded[i].Label != result.Excluded[j].Label {
			return result.Excluded[i].Label < result.Excluded[j].Label
		}
		return result.Excluded[i].ID < result.Excluded[j].ID
	})
	result.ExplicitOrder = len(result.Relations) > 0
	if len(result.Items) == 0 {
		result.Warnings = append(result.Warnings, KnowledgeLearningRouteWarning{Code: "no_reviewed_learning_items", Message: "В выбранной области нет подтверждённых карточек или вопросов с актуальными источниками."})
	} else if !result.ExplicitOrder {
		result.Warnings = append(result.Warnings, KnowledgeLearningRouteWarning{Code: "no_explicit_order", Message: "Подтверждённые учебные объекты найдены, но между ними нет подтверждённых prerequisite/depends_on; алфавитный порядок не является учебной зависимостью."})
	}
	if len(result.Excluded) > 0 {
		result.Warnings = append(result.Warnings, KnowledgeLearningRouteWarning{Code: "learning_items_excluded", Message: "Черновики, закрытые объекты и объекты с неактуальными источниками исключены из маршрута."})
	}
	if unreviewedRelations > 0 {
		result.Warnings = append(result.Warnings, KnowledgeLearningRouteWarning{Code: "order_relations_excluded", Message: "Неподтверждённые или неактуальные связи порядка не использованы."})
	}
	levels := 0
	for _, item := range result.Items {
		if item.Level+1 > levels {
			levels = item.Level + 1
		}
	}
	result.Summary = KnowledgeLearningRouteSummary{Items: len(result.Items), Levels: levels, OrderRelations: len(result.Relations), Excluded: len(result.Excluded), UnreviewedRelations: unreviewedRelations, CycleItems: len(cycleIDs)}
	result.Ready = len(result.Items) > 0 && len(cycleIDs) == 0
	currentManifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil || currentManifest.Digest != manifest.Digest {
		return KnowledgeLearningRoute{}, fmt.Errorf("%w: selection changed while the learning route was built", ErrKnowledgeSelectionChanged)
	}
	result.Digest, err = knowledgeLearningRouteDigest(result)
	if err != nil {
		return KnowledgeLearningRoute{}, err
	}
	return result, nil
}

func knowledgeLearningRouteNodeReason(node KnowledgeNode, review KnowledgeReviewItem) string {
	if node.Status != KnowledgeStatusActive {
		return "Учебный объект ещё не подтверждён или уже закрыт."
	}
	if review.EvidenceState != EvidenceCurrent || len(review.Evidence) == 0 {
		return "Источник учебного объекта отсутствует или устарел."
	}
	return "Учебный объект не допущен в маршрут."
}

func knowledgeLearningRouteAnswer(node KnowledgeNode) string {
	const expected = "Ожидаемый ответ для проверки:\n"
	if node.Kind == KnowledgeNodeQuestion && strings.HasPrefix(node.Body, expected) {
		return strings.TrimSpace(strings.TrimPrefix(node.Body, expected))
	}
	return strings.TrimSpace(node.Body)
}

func knowledgeLearningRouteDigest(route KnowledgeLearningRoute) (string, error) {
	pinned := route
	pinned.Digest = ""
	encoded, err := json.Marshal(pinned)
	if err != nil {
		return "", fmt.Errorf("encode knowledge learning route: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
