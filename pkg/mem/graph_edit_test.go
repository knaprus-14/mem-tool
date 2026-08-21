package mem

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeEditLifecycleIsPinnedAndAppendOnly(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "edit-lifecycle", Kind: KnowledgeNodeClaim, Label: "Исходное название", Body: "Исходное описание",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	item := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, "edit-lifecycle")
	request := KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-lifecycle", Editor: "Руслан", Comment: "Уточнил формулировку",
		Label: "Уточнённое название", Body: "Подробное и проверяемое описание",
		ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest,
		ExpectedEvidenceDigest: item.EvidenceDigest,
	}
	edited, err := store.EditKnowledgeObject(request)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Label != request.Label || edited.Body != request.Body || edited.Status != KnowledgeStatusDraft ||
		edited.Edit.Action != KnowledgeEditActionEdit || edited.Edit.Editor != "Руслан" {
		t.Fatalf("edit result is incomplete: %#v", edited)
	}
	if _, err := store.EditKnowledgeObject(request); !errors.Is(err, ErrKnowledgeContentChanged) && !errors.Is(err, ErrKnowledgeEditChanged) {
		t.Fatalf("stale edit was not rejected: %v", err)
	}
	undone, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-lifecycle", Editor: "Руслан", Comment: "Возвращаю исходную формулировку",
		ExpectedStatus: edited.Status, ExpectedContentDigest: edited.ContentDigest,
		ExpectedEvidenceDigest: edited.EvidenceDigest, ExpectedEditID: edited.Edit.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if undone.Label != "Исходное название" || undone.Body != "Исходное описание" ||
		undone.Edit.Action != KnowledgeEditActionUndo || undone.Edit.RevertsEditID != edited.Edit.ID {
		t.Fatalf("edit undo did not restore the previous content: %#v", undone)
	}
	if _, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-lifecycle", Editor: "Руслан",
		ExpectedStatus: undone.Status, ExpectedContentDigest: undone.ContentDigest,
		ExpectedEvidenceDigest: undone.EvidenceDigest, ExpectedEditID: edited.Edit.ID,
	}); !errors.Is(err, ErrKnowledgeEditChanged) {
		t.Fatalf("repeated edit undo was not rejected: %v", err)
	}
	records, err := store.ListKnowledgeEdits(10)
	if err != nil || len(records) != 2 || records[0].Action != KnowledgeEditActionUndo || records[1].Action != KnowledgeEditActionEdit {
		t.Fatalf("edit history is incomplete: records=%#v err=%v", records, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_edits SET editor = 'changed' WHERE id = ?`, records[0].ID); err == nil {
		t.Fatal("append-only edit record was updated")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_edits WHERE id = ?`, records[0].ID); err == nil {
		t.Fatal("append-only edit record was deleted")
	}
}

func TestKnowledgeEditResetsDecisionAndProtectsActiveRelations(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "edit-active-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now, Evidence: []EvidenceAnchor{anchor}},
			{ID: "edit-active-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "edit-active-edge", From: "edit-active-a", To: "edit-active-b", Kind: KnowledgeRelationSupports,
			Label: "поддерживает", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
			Created: now, Updated: now, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"edit-active-a", "edit-active-b"} {
		if _, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectEdge, "edit-active-edge"); err != nil {
		t.Fatal(err)
	}
	nodeItem := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, "edit-active-a")
	if _, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-active-a", Editor: "Руслан", Label: "A уточнённое",
		ExpectedStatus: nodeItem.Status, ExpectedContentDigest: nodeItem.ContentDigest,
		ExpectedEvidenceDigest: nodeItem.EvidenceDigest,
	}); !errors.Is(err, ErrKnowledgeActiveRelations) {
		t.Fatalf("active node with active relations was edited: %v", err)
	}
	edgeItem := knowledgeReviewItemByID(t, store, KnowledgeObjectEdge, "edit-active-edge")
	edited, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectEdge, ID: "edit-active-edge", Editor: "Руслан", Label: "усиливает",
		ExpectedStatus: edgeItem.Status, ExpectedContentDigest: edgeItem.ContentDigest,
		ExpectedEvidenceDigest: edgeItem.EvidenceDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Status != KnowledgeStatusDraft {
		t.Fatalf("editing an approved edge did not return it to review: %#v", edited)
	}
	undone, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectEdge, ID: "edit-active-edge", Editor: "Руслан",
		ExpectedStatus: edited.Status, ExpectedContentDigest: edited.ContentDigest,
		ExpectedEvidenceDigest: edited.EvidenceDigest, ExpectedEditID: edited.Edit.ID,
	})
	if err != nil || undone.Status != KnowledgeStatusActive || undone.Label != "поддерживает" {
		t.Fatalf("approved edge edit was not safely undone: result=%#v err=%v", undone, err)
	}
}

func TestKnowledgeEditRejectedObjectReturnsToReviewAndUndoRestoresRejection(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "edit-rejected", Kind: KnowledgeNodeClaim, Label: "Неточная формулировка",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	item := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, "edit-rejected")
	if _, err := store.RejectKnowledgeObject(KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-rejected", Reviewer: "Руслан", Comment: "Нужно исправить",
		ExpectedEvidenceDigest: item.EvidenceDigest,
	}); err != nil {
		t.Fatal(err)
	}
	item = knowledgeReviewItemByID(t, store, KnowledgeObjectNode, "edit-rejected")
	edited, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-rejected", Editor: "Руслан", Label: "Исправленная формулировка",
		ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest,
		ExpectedEvidenceDigest: item.EvidenceDigest,
	})
	if err != nil || edited.Status != KnowledgeStatusDraft {
		t.Fatalf("rejected edit did not return to draft: result=%#v err=%v", edited, err)
	}
	undone, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: "edit-rejected", Editor: "Руслан",
		ExpectedStatus: edited.Status, ExpectedContentDigest: edited.ContentDigest,
		ExpectedEvidenceDigest: edited.EvidenceDigest, ExpectedEditID: edited.Edit.ID,
	})
	if err != nil || undone.Status != KnowledgeStatusRejected || undone.Label != "Неточная формулировка" {
		t.Fatalf("edit undo did not restore rejected state: result=%#v err=%v", undone, err)
	}
}

func TestGeneratedUpsertPreservesCurrentManualEditUntilUndo(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	node := KnowledgeNode{
		ID: "edit-preserved", Kind: KnowledgeNodeClaim, Label: "Исходный текст", Body: "Исходное описание",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now,
		Evidence: []EvidenceAnchor{anchor},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	item := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, node.ID)
	edited, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "Руслан",
		Label: "Ручная формулировка", Body: "Ручное уточнение",
		ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest,
		ExpectedEvidenceDigest: item.EvidenceDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	node.Label = "Новая генерация"
	node.Body = "Новая сгенерированная версия"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || loaded.Nodes[0].Label != "Ручная формулировка" || loaded.Nodes[0].Body != "Ручное уточнение" {
		t.Fatalf("generated upsert overwrote manual content: graph=%#v err=%v", loaded, err)
	}
	current := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, node.ID)
	if _, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "Руслан",
		ExpectedStatus: current.Status, ExpectedContentDigest: current.ContentDigest,
		ExpectedEvidenceDigest: current.EvidenceDigest, ExpectedEditID: edited.Edit.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadKnowledgeGraph()
	if err != nil || loaded.Nodes[0].Label != "Новая генерация" || loaded.Nodes[0].Body != "Новая сгенерированная версия" {
		t.Fatalf("undone manual override still blocked extraction: graph=%#v err=%v", loaded, err)
	}
}

func TestGeneratedUpsertPreservesEarlierEditAfterUndoingOnlyNewestEdit(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	node := KnowledgeNode{
		ID: "edit-layered", Kind: KnowledgeNodeClaim, Label: "Generated A",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now,
		Evidence: []EvidenceAnchor{anchor},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	firstPins := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, node.ID)
	first, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "Руслан", Label: "Manual B",
		ExpectedStatus: firstPins.Status, ExpectedContentDigest: firstPins.ContentDigest,
		ExpectedEvidenceDigest: firstPins.EvidenceDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondPins := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, node.ID)
	second, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "Руслан", Label: "Manual C",
		ExpectedStatus: secondPins.Status, ExpectedContentDigest: secondPins.ContentDigest,
		ExpectedEvidenceDigest: secondPins.EvidenceDigest, ExpectedEditID: first.Edit.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoKnowledgeEdit(KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "Руслан",
		ExpectedStatus: second.Status, ExpectedContentDigest: second.ContentDigest,
		ExpectedEvidenceDigest: second.EvidenceDigest, ExpectedEditID: second.Edit.ID,
	}); err != nil {
		t.Fatal(err)
	}
	node.Label = "Generated D"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || len(loaded.Nodes) != 1 || loaded.Nodes[0].Label != "Manual B" {
		t.Fatalf("undoing the newest edit lost the earlier active override: graph=%#v err=%v", loaded, err)
	}
}

func TestKnowledgeEdgeEditRejectsBody(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	_, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectEdge, ID: "missing-edge", Editor: "Руслан", Body: "unsupported",
		ExpectedStatus:         KnowledgeStatusDraft,
		ExpectedContentDigest:  "sha256:" + strings.Repeat("0", 64),
		ExpectedEvidenceDigest: "sha256:" + strings.Repeat("0", 64),
	})
	if err == nil || err.Error() != "knowledge edge has no body" {
		t.Fatalf("edge body was accepted: %v", err)
	}
}

func knowledgeReviewItemByID(t *testing.T, store *Store, objectType KnowledgeObjectType, id string) KnowledgeReviewItem {
	t.Helper()
	report, err := store.ReviewKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.Items {
		if item.ObjectType == objectType && item.ID == id {
			return item
		}
	}
	t.Fatalf("knowledge %s %q not found in review report", objectType, id)
	return KnowledgeReviewItem{}
}
