package mem

import (
	"strings"
	"testing"
)

func TestKnowledgeLearningRouteUsesOnlyReviewedGroundedItemsAndExplicitOrder(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "route-source", Kind: KnowledgeNodeDefinition, Label: "Закон Ома", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-card", Kind: KnowledgeNodeCard, Label: "Что связывает закон Ома?", Body: "Ток, напряжение и сопротивление.", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-question", Kind: KnowledgeNodeQuestion, Label: "Как найти ток?", Body: "Ожидаемый ответ для проверки:\nРазделить напряжение на сопротивление.", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-draft", Kind: KnowledgeNodeCard, Label: "Непроверенная карточка", Body: "Черновик", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "route-card-source", From: "route-card", To: "route-source", Kind: KnowledgeRelationDerivedFrom, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-question-source", From: "route-question", To: "route-source", Kind: KnowledgeRelationAsks, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-draft-source", From: "route-draft", To: "route-source", Kind: KnowledgeRelationDerivedFrom, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-order", From: "route-card", To: "route-question", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"route-source"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if !route.Ready || !route.ExplicitOrder || route.Digest == "" || len(route.Items) != 2 || len(route.Relations) != 1 {
		t.Fatalf("unexpected route: %#v", route)
	}
	if route.Items[0].ID != "route-card" || route.Items[0].Level != 0 || route.Items[1].ID != "route-question" || route.Items[1].Level != 1 {
		t.Fatalf("explicit prerequisite order was not preserved: %#v", route.Items)
	}
	if route.Items[1].Answer != "Разделить напряжение на сопротивление." {
		t.Fatalf("question answer prefix leaked into route: %q", route.Items[1].Answer)
	}
	if len(route.Excluded) != 1 || route.Excluded[0].ID != "route-draft" || !strings.Contains(route.Excluded[0].Reason, "не подтверждён") {
		t.Fatalf("unreviewed card was not explained: %#v", route.Excluded)
	}
}

func TestKnowledgeLearningRouteReversesDependsOnAndIgnoresDraftOrder(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "route-basic", Kind: KnowledgeNodeCard, Label: "Базовое", Body: "A", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-advanced", Kind: KnowledgeNodeQuestion, Label: "Продвинутое", Body: "B", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "route-depends", From: "route-advanced", To: "route-basic", Kind: KnowledgeRelationDependsOn, Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-draft-order", From: "route-advanced", To: "route-basic", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"route-advanced"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Items) != 2 || route.Items[0].ID != "route-basic" || route.Items[1].ID != "route-advanced" {
		t.Fatalf("depends_on direction is wrong: %#v", route.Items)
	}
	if len(route.Relations) != 1 || route.Relations[0].Before != "route-basic" || route.Relations[0].After != "route-advanced" || route.Summary.UnreviewedRelations != 1 {
		t.Fatalf("route relation admission is wrong: %#v", route)
	}
}

func TestKnowledgeLearningRouteNoOrderWarningIncludesQuestions(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	node := KnowledgeNode{ID: "route-only-question", Kind: KnowledgeNodeQuestion, Label: "Почему?", Body: "Ожидаемый ответ для проверки:\nПотому что.", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{node.ID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if !route.Ready || len(route.Items) != 1 || route.Items[0].Kind != KnowledgeNodeQuestion {
		t.Fatalf("question-only route is invalid: %#v", route)
	}
	found := false
	for _, warning := range route.Warnings {
		if warning.Code == "no_explicit_order" {
			found = strings.Contains(warning.Message, "учебные объекты")
		}
	}
	if !found {
		t.Fatalf("question-only route has a card-specific warning: %#v", route.Warnings)
	}
}

func TestKnowledgeLearningRouteReportsCycleAndRejectsStaleManifest(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "route-cycle-a", Kind: KnowledgeNodeCard, Label: "A", Body: "A", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-cycle-b", Kind: KnowledgeNodeCard, Label: "B", Body: "B", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "route-cycle-ab", From: "route-cycle-a", To: "route-cycle-b", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "route-cycle-ba", From: "route-cycle-b", To: "route-cycle-a", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"route-cycle-a"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: "sha256:stale"}); !strings.Contains(err.Error(), ErrKnowledgeSelectionChanged.Error()) {
		t.Fatalf("stale route manifest was accepted: %v", err)
	}
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if route.Ready || route.Summary.CycleItems != 2 || len(route.Items) != 2 || !route.Items[0].Cyclic || !route.Items[1].Cyclic {
		t.Fatalf("dependency cycle is hidden: %#v", route)
	}
	found := false
	for _, warning := range route.Warnings {
		found = found || warning.Code == "dependency_cycle"
	}
	if !found {
		t.Fatalf("cycle warning is missing: %#v", route.Warnings)
	}
}
