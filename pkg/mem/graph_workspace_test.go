package mem

import (
	"errors"
	"testing"
)

func TestCreateKnowledgeWorkspaceNodeCopiesPinnedCurrentProvenance(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const parentID = "workspace-parent"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: parentID, Kind: KnowledgeNodeClaim, Label: "Проверяемое утверждение", Body: "Точный исходный текст",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	parent := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, parentID)
	result, err := store.CreateKnowledgeWorkspaceNode(KnowledgeWorkspaceCreateRequest{
		ParentNodeID: parentID, Kind: KnowledgeNodeNote, Label: "Моя заметка", Body: "Что важно запомнить",
		Author: "Руслан", Comment: "Создано при изучении источника",
		ExpectedParentStatus: parent.Status, ExpectedParentContent: parent.ContentDigest,
		ExpectedEvidence: parent.EvidenceDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.ID == "" || result.Edge.ID == "" || result.Node.Status != KnowledgeStatusDraft ||
		result.Node.Origin != KnowledgeOriginManual || result.Node.Kind != KnowledgeNodeNote ||
		result.Edge.From != result.Node.ID || result.Edge.To != parentID ||
		result.Edge.Kind != KnowledgeRelationDerivedFrom || result.Edge.Origin != KnowledgeOriginManual ||
		len(result.Evidence) != 1 || result.Evidence[0].State != EvidenceCurrent ||
		len(result.Node.Evidence) != 1 || result.Node.Evidence[0] != anchor ||
		result.Creation.Author != "Руслан" || result.Creation.Comment == "" {
		t.Fatalf("workspace creation lost semantics or provenance: %#v", result)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("workspace graph was not stored atomically: %#v", graph)
	}
	records, err := store.ListKnowledgeWorkspaceCreations(10)
	if err != nil || len(records) != 1 || records[0].NodeID != result.Node.ID ||
		records[0].ParentContentDigest != parent.ContentDigest || records[0].EvidenceDigest != parent.EvidenceDigest {
		t.Fatalf("workspace creation audit is incomplete: records=%#v err=%v", records, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_workspace_creations SET author = 'attacker' WHERE id = ?`, records[0].ID); err == nil {
		t.Fatal("append-only workspace creation record was updated")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_workspace_creations WHERE id = ?`, records[0].ID); err == nil {
		t.Fatal("append-only workspace creation record was deleted")
	}
}

func TestCreateKnowledgeWorkspaceNodeUsesKindSpecificRelation(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const parentID = "workspace-relation-parent"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: parentID, Kind: KnowledgeNodeTopic, Label: "Тема", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	parent := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, parentID)
	for kind, wantRelation := range map[KnowledgeNodeKind]KnowledgeRelationKind{
		KnowledgeNodeNote:       KnowledgeRelationDerivedFrom,
		KnowledgeNodeQuestion:   KnowledgeRelationAsks,
		KnowledgeNodeCard:       KnowledgeRelationDerivedFrom,
		KnowledgeNodeHypothesis: KnowledgeRelationHypothesizes,
		KnowledgeNodeDecision:   KnowledgeRelationBasedOn,
		KnowledgeNodeTask:       KnowledgeRelationActsOn,
	} {
		t.Run(string(kind), func(t *testing.T) {
			result, err := store.CreateKnowledgeWorkspaceNode(KnowledgeWorkspaceCreateRequest{
				ParentNodeID: parentID, Kind: kind, Label: "Ручной объект: " + string(kind), Author: "Руслан",
				ExpectedParentStatus: parent.Status, ExpectedParentContent: parent.ContentDigest,
				ExpectedEvidence: parent.EvidenceDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Edge.Kind != wantRelation || result.Creation.RelationKind != wantRelation ||
				result.Edge.From != result.Node.ID || result.Edge.To != parentID {
				t.Fatalf("workspace kind %q has the wrong semantic relation: %#v", kind, result)
			}
		})
	}
}

func TestCreateKnowledgeWorkspaceNodeFailsClosedOnChangedParent(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const parentID = "changed-parent"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: parentID, Kind: KnowledgeNodeClaim, Label: "До изменения", Status: KnowledgeStatusActive,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	parent := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, parentID)
	request := KnowledgeWorkspaceCreateRequest{
		ParentNodeID: parentID, Kind: KnowledgeNodeNote, Label: "Не должна сохраниться", Author: "Руслан",
		ExpectedParentStatus: parent.Status, ExpectedParentContent: parent.ContentDigest,
		ExpectedEvidence: parent.EvidenceDigest,
	}
	if _, err := store.db.Exec(`UPDATE knowledge_nodes SET label = 'После изменения' WHERE id = ?`, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateKnowledgeWorkspaceNode(request); !errors.Is(err, ErrKnowledgeContentChanged) {
		t.Fatalf("changed parent content was not rejected: %v", err)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 {
		t.Fatalf("failed creation left partial graph objects: %#v", graph)
	}
	if records, err := store.ListKnowledgeWorkspaceCreations(10); err != nil || len(records) != 0 {
		t.Fatalf("failed creation left an audit record: %#v err=%v", records, err)
	}
}

func TestCreateKnowledgeWorkspaceNodeRejectsUnsupportedInput(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	validDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for name, request := range map[string]KnowledgeWorkspaceCreateRequest{
		"unsupported kind": {ParentNodeID: "parent", Kind: KnowledgeNodeClaim, Label: "Claim", Author: "Руслан", ExpectedParentStatus: KnowledgeStatusDraft, ExpectedParentContent: validDigest, ExpectedEvidence: validDigest},
		"empty author":     {ParentNodeID: "parent", Kind: KnowledgeNodeNote, Label: "Note", ExpectedParentStatus: KnowledgeStatusDraft, ExpectedParentContent: validDigest, ExpectedEvidence: validDigest},
		"empty label":      {ParentNodeID: "parent", Kind: KnowledgeNodeQuestion, Author: "Руслан", ExpectedParentStatus: KnowledgeStatusDraft, ExpectedParentContent: validDigest, ExpectedEvidence: validDigest},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateKnowledgeWorkspaceNode(request); err == nil {
				t.Fatal("invalid workspace creation was accepted")
			}
		})
	}
	if _, err := store.ListKnowledgeWorkspaceCreations(0); err == nil {
		t.Fatal("invalid creation history limit was accepted")
	}
}
