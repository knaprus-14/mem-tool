package mem

import (
	"errors"
	"strings"
	"testing"
)

func TestKnowledgeReviewAndApprovalRequireCurrentEvidenceAndActiveEndpoints(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "review-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "review-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "review-edge", From: "review-a", To: "review-b", Kind: KnowledgeRelationSupports,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReviewKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total != 3 || report.Summary.Draft != 3 || report.Summary.Ready != 2 || report.Summary.CurrentEvidence != 3 {
		t.Fatalf("unexpected initial review report: %#v", report)
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectEdge, "review-edge"); !errors.Is(err, ErrKnowledgeEndpointsNotActive) {
		t.Fatalf("edge with draft endpoints was approved: %v", err)
	}
	for _, id := range []string{"review-a", "review-b"} {
		approved, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, id)
		if err != nil || approved.PreviousStatus != KnowledgeStatusDraft || approved.Status != KnowledgeStatusActive {
			t.Fatalf("node %s approval failed: result=%#v err=%v", id, approved, err)
		}
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectEdge, "review-edge"); err != nil {
		t.Fatal(err)
	}
	report, err = store.ReviewKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Active != 3 || report.Summary.Draft != 0 || report.Summary.Ready != 0 {
		t.Fatalf("approved graph has wrong review state: %#v", report)
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, "review-a"); err == nil {
		t.Fatal("already active node was approved again")
	}
}

func TestKnowledgeApprovalBlocksStaleAndMissingEvidenceWithoutWriting(t *testing.T) {
	for _, state := range []EvidenceState{EvidenceStale, EvidenceMissing} {
		t.Run(string(state), func(t *testing.T) {
			store, anchor := graphStoreAndAnchor(t)
			defer store.Close()
			if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
				ID: "review-blocked", Kind: KnowledgeNodeClaim, Label: "Blocked",
				Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
			}}}); err != nil {
				t.Fatal(err)
			}
			entries := store.GetBySourceFile(validStructuredChunks()[0].Provenance.SourcePath)
			if state == EvidenceMissing {
				if err := store.DeleteById(entries[0].ID); err != nil {
					t.Fatal(err)
				}
			} else {
				chunks := validStructuredChunks()
				newRevision := ChunkContentHash("changed document revision")
				for i := range chunks {
					chunks[i].Provenance.DocumentRevision = newRevision
				}
				chunks[0].Text = "changed chunk-0"
				chunks[0].Provenance.ChunkHash = ChunkContentHash(chunks[0].Text)
				if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, "review-blocked"); !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
				t.Fatalf("%s evidence did not block approval: %v", state, err)
			}
			graph, err := store.LoadKnowledgeGraph()
			if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].Status != KnowledgeStatusDraft {
				t.Fatalf("blocked approval changed graph: graph=%#v err=%v", graph, err)
			}
			report, err := store.ReviewKnowledgeGraph()
			if err != nil || len(report.Items) != 1 || report.Items[0].EvidenceState != state || report.Items[0].ReadyForApproval {
				t.Fatalf("review did not expose %s state: report=%#v err=%v", state, report, err)
			}
		})
	}
}

func TestRepeatedGeneratedUpsertPreservesReviewOnlyForExactEvidence(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	fragment := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "review-stable", Kind: KnowledgeNodeClaim, Label: "Stable review",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, "review-stable"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || loaded.Nodes[0].Status != KnowledgeStatusActive {
		t.Fatalf("exact repeated extraction lost approval: graph=%#v err=%v", loaded, err)
	}

	chunks := validStructuredChunks()
	newRevision := ChunkContentHash("review reset revision")
	for i := range chunks {
		chunks[i].Provenance.DocumentRevision = newRevision
	}
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	current := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	newAnchor, err := EvidenceAnchorForEntry(current, current.Text)
	if err != nil {
		t.Fatal(err)
	}
	fragment.Nodes[0].Evidence = []EvidenceAnchor{newAnchor}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadKnowledgeGraph()
	if err != nil || loaded.Nodes[0].Status != KnowledgeStatusDraft {
		t.Fatalf("changed evidence did not reset review: graph=%#v err=%v", loaded, err)
	}
}

func TestChangedNodeEvidenceDemotesGeneratedActiveEdge(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "edge-reset-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "edge-reset-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "edge-reset", From: "edge-reset-a", To: "edge-reset-b", Kind: KnowledgeRelationRelated,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"edge-reset-a", "edge-reset-b"} {
		if _, err := store.ApproveKnowledgeObject(KnowledgeObjectNode, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ApproveKnowledgeObject(KnowledgeObjectEdge, "edge-reset"); err != nil {
		t.Fatal(err)
	}

	chunks := validStructuredChunks()
	newRevision := ChunkContentHash("edge reset revision")
	for i := range chunks {
		chunks[i].Provenance.DocumentRevision = newRevision
	}
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	current := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	newAnchor, err := EvidenceAnchorForEntry(current, current.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "edge-reset-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{newAnchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]KnowledgeStatus)
	for _, node := range loaded.Nodes {
		statuses[node.ID] = node.Status
	}
	if statuses["edge-reset-a"] != KnowledgeStatusDraft || statuses["edge-reset-b"] != KnowledgeStatusActive || loaded.Edges[0].Status != KnowledgeStatusDraft {
		t.Fatalf("node/edge review invariant was not reconciled: %#v", loaded)
	}
}

func TestKnowledgeEvidenceDigestIsStableAcrossAnchorOrder(t *testing.T) {
	store, first := graphStoreAndAnchor(t)
	defer store.Close()
	entries := store.GetBySourceFile(validStructuredChunks()[0].Provenance.SourcePath)
	if len(entries) < 2 {
		t.Fatalf("test store has only %d entries", len(entries))
	}
	second, err := EvidenceAnchorForEntry(entries[1], entries[1].Text)
	if err != nil {
		t.Fatal(err)
	}
	left, err := KnowledgeEvidenceDigest([]EvidenceAnchor{first, second})
	if err != nil {
		t.Fatal(err)
	}
	right, err := KnowledgeEvidenceDigest([]EvidenceAnchor{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if left != right || !strings.HasPrefix(left, "sha256:") {
		t.Fatalf("digest is not canonical: left=%q right=%q", left, right)
	}
}

func TestKnowledgeApprovalRecordsAuditAndFailedApprovalDoesNot(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "audit-ok", Kind: KnowledgeNodeClaim, Label: "OK", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "audit-fail", Kind: KnowledgeNodeClaim, Label: "Fail", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}}); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectNode, ID: "audit-ok", Reviewer: "Руслан",
		Comment: "Проверено", ExpectedEvidenceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Review.ID == 0 || approved.Review.Reviewer != "Руслан" || approved.Review.EvidenceDigest != digest {
		t.Fatalf("approval result lost audit data: %#v", approved)
	}
	if _, err := store.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectNode, ID: "audit-fail", Reviewer: "Руслан",
		ExpectedEvidenceDigest: "sha256:" + strings.Repeat("0", 64),
	}); !errors.Is(err, ErrKnowledgeEvidenceChanged) {
		t.Fatalf("wrong digest did not fail closed: %v", err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 1 || reviews[0].ObjectID != "audit-ok" {
		t.Fatalf("failed approval wrote an audit record: reviews=%#v err=%v", reviews, err)
	}
}

func TestKnowledgeBatchApprovalIsAtomicAndAllowsEndpointNodesInSameBatch(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "batch-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "batch-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "batch-edge", From: "batch-a", To: "batch-b", Kind: KnowledgeRelationSupports,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{
		{ObjectType: KnowledgeObjectEdge, ID: "batch-edge", Reviewer: "reviewer", ExpectedEvidenceDigest: digest},
		{ObjectType: KnowledgeObjectNode, ID: "batch-b", Reviewer: "reviewer", ExpectedEvidenceDigest: digest},
		{ObjectType: KnowledgeObjectNode, ID: "batch-a", Reviewer: "reviewer", ExpectedEvidenceDigest: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Approved) != 3 || result.Approved[0].ID != "batch-edge" {
		t.Fatalf("batch result lost input order: %#v", result)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range loaded.Nodes {
		if node.Status != KnowledgeStatusActive {
			t.Fatalf("batch node %q is %s", node.ID, node.Status)
		}
	}
	if loaded.Edges[0].Status != KnowledgeStatusActive {
		t.Fatalf("batch edge is %s", loaded.Edges[0].Status)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 3 {
		t.Fatalf("batch audit is incomplete: reviews=%#v err=%v", reviews, err)
	}
}

func TestKnowledgeBatchApprovalRollsBackEveryStatusAndReview(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "rollback-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "rollback-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}}); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{
		{ObjectType: KnowledgeObjectNode, ID: "rollback-a", Reviewer: "reviewer", ExpectedEvidenceDigest: digest},
		{ObjectType: KnowledgeObjectNode, ID: "rollback-b", Reviewer: "reviewer", ExpectedEvidenceDigest: "sha256:" + strings.Repeat("f", 64)},
	})
	if !errors.Is(err, ErrKnowledgeEvidenceChanged) {
		t.Fatalf("batch with changed evidence was accepted: %v", err)
	}
	loaded, loadErr := store.LoadKnowledgeGraph()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, node := range loaded.Nodes {
		if node.Status != KnowledgeStatusDraft {
			t.Fatalf("partial batch approval changed %q to %s", node.ID, node.Status)
		}
	}
	reviews, listErr := store.ListKnowledgeReviews(10)
	if listErr != nil || len(reviews) != 0 {
		t.Fatalf("rolled back batch left audit records: reviews=%#v err=%v", reviews, listErr)
	}
}

func TestKnowledgeReviewHistorySurvivesReExtractionAndIsAppendOnly(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	fragment := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "history-node", Kind: KnowledgeNodeClaim, Label: "History",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectNode, ID: "history-node", Reviewer: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("re-extraction changed review history: reviews=%#v err=%v", reviews, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_reviews SET reviewer = 'changed' WHERE id = ?`, reviews[0].ID); err == nil {
		t.Fatal("review history update was not blocked")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_reviews WHERE id = ?`, reviews[0].ID); err == nil {
		t.Fatal("review history delete was not blocked")
	}
}

func TestKnowledgeApprovalValidatesReviewerCommentAndBatchDigest(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "validation-node", Kind: KnowledgeNodeClaim, Label: "Validation",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]KnowledgeApprovalRequest{
		"empty reviewer": {ObjectType: KnowledgeObjectNode, ID: "validation-node"},
		"long reviewer":  {ObjectType: KnowledgeObjectNode, ID: "validation-node", Reviewer: strings.Repeat("я", MaxKnowledgeReviewerRunes+1)},
		"long comment":   {ObjectType: KnowledgeObjectNode, ID: "validation-node", Reviewer: "r", Comment: strings.Repeat("я", MaxKnowledgeCommentRunes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ApproveKnowledgeObjectWithReview(request); err == nil {
				t.Fatal("invalid approval was accepted")
			}
		})
	}
	if _, err := store.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{{
		ObjectType: KnowledgeObjectNode, ID: "validation-node", Reviewer: "r",
	}}); err == nil || !strings.Contains(err.Error(), "expected_evidence_digest") {
		t.Fatalf("unpinned batch was accepted: %v", err)
	}
}

func TestKnowledgeRejectReopenAndUndoArePinnedAndAppendOnly(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	fragment := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "review-lifecycle", Kind: KnowledgeNodeClaim, Label: "Lifecycle",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	base := KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectNode, ID: "review-lifecycle", Reviewer: "Руслан",
		ExpectedEvidenceDigest: digest,
	}
	if _, err := store.RejectKnowledgeObject(base); err == nil || !strings.Contains(err.Error(), "comment") {
		t.Fatalf("rejection without a reason was accepted: %v", err)
	}
	base.Comment = "Формулировка не подтверждается источником"
	rejected, err := store.RejectKnowledgeObject(base)
	if err != nil || rejected.Status != KnowledgeStatusRejected || rejected.Review.Action != KnowledgeReviewActionReject {
		t.Fatalf("rejection failed: result=%#v err=%v", rejected, err)
	}
	if err := store.UpsertKnowledgeGraph(fragment); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReviewKnowledgeGraph()
	if err != nil || report.Summary.Rejected != 1 || report.Items[0].Status != KnowledgeStatusRejected {
		t.Fatalf("exact re-extraction did not preserve rejection: report=%#v err=%v", report, err)
	}

	undoReject := base
	undoReject.Comment = "Отменяю ошибочное отклонение"
	undoReject.ExpectedReviewID = rejected.Review.ID
	undone, err := store.UndoKnowledgeReview(undoReject)
	if err != nil || undone.Status != KnowledgeStatusDraft || undone.Review.RevertsReviewID != rejected.Review.ID {
		t.Fatalf("rejection undo failed: result=%#v err=%v", undone, err)
	}
	undoAgain := undoReject
	undoAgain.ExpectedReviewID = undone.Review.ID
	if _, err := store.UndoKnowledgeReview(undoAgain); !errors.Is(err, ErrKnowledgeReviewNotReversible) {
		t.Fatalf("undo record was reversible: %v", err)
	}

	base.ExpectedReviewID = 0
	base.Comment = "Повторная проверка"
	rejected, err = store.RejectKnowledgeObject(base)
	if err != nil {
		t.Fatal(err)
	}
	reopen := base
	reopen.Comment = "Новые основания для проверки"
	reopened, err := store.ReopenKnowledgeObject(reopen)
	if err != nil || reopened.Status != KnowledgeStatusDraft || reopened.Review.Action != KnowledgeReviewActionReopen {
		t.Fatalf("reopen failed: result=%#v err=%v", reopened, err)
	}
	undoReopen := reopen
	undoReopen.ExpectedReviewID = reopened.Review.ID
	undone, err = store.UndoKnowledgeReview(undoReopen)
	if err != nil || undone.Status != KnowledgeStatusRejected || undone.Review.RevertsReviewID != reopened.Review.ID {
		t.Fatalf("reopen undo failed: result=%#v err=%v", undone, err)
	}
	latest, err := store.ListLatestKnowledgeReviews()
	if err != nil || len(latest) != 1 || latest[0].Action != KnowledgeReviewActionUndo || latest[0].RevertsReviewID != reopened.Review.ID {
		t.Fatalf("latest review snapshot is wrong: reviews=%#v err=%v", latest, err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 5 {
		t.Fatalf("review lifecycle audit is incomplete: reviews=%#v err=%v", reviews, err)
	}
}

func TestKnowledgeApprovalUndoRequiresIncidentRelationsFirst(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "undo-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "undo-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "undo-edge", From: "undo-a", To: "undo-b", Kind: KnowledgeRelationSupports,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	approvals := make(map[string]KnowledgeApprovalResult)
	for _, id := range []string{"undo-a", "undo-b"} {
		result, err := store.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
			ObjectType: KnowledgeObjectNode, ID: id, Reviewer: "reviewer", ExpectedEvidenceDigest: digest,
		})
		if err != nil {
			t.Fatal(err)
		}
		approvals[id] = result
	}
	edgeApproval, err := store.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectEdge, ID: "undo-edge", Reviewer: "reviewer", ExpectedEvidenceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	undoA := KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectNode, ID: "undo-a", Reviewer: "reviewer",
		ExpectedEvidenceDigest: digest, ExpectedReviewID: approvals["undo-a"].Review.ID,
	}
	if _, err := store.UndoKnowledgeReview(undoA); !errors.Is(err, ErrKnowledgeActiveRelations) {
		t.Fatalf("active incident relation did not block node undo: %v", err)
	}
	if _, err := store.UndoKnowledgeReview(KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectEdge, ID: "undo-edge", Reviewer: "reviewer",
		ExpectedEvidenceDigest: digest, ExpectedReviewID: edgeApproval.Review.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoKnowledgeReview(undoA); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]KnowledgeStatus)
	for _, node := range loaded.Nodes {
		statuses[node.ID] = node.Status
	}
	if statuses["undo-a"] != KnowledgeStatusDraft || statuses["undo-b"] != KnowledgeStatusActive || loaded.Edges[0].Status != KnowledgeStatusDraft {
		t.Fatalf("undo broke graph status invariants: %#v", loaded)
	}
}

func TestKnowledgeRejectionFailsClosedOnChangedEvidenceDigest(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "reject-pinned", Kind: KnowledgeNodeClaim, Label: "Pinned",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := store.RejectKnowledgeObject(KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectNode, ID: "reject-pinned", Reviewer: "reviewer", Comment: "reason",
		ExpectedEvidenceDigest: "sha256:" + strings.Repeat("0", 64),
	})
	if !errors.Is(err, ErrKnowledgeEvidenceChanged) {
		t.Fatalf("changed evidence digest did not block rejection: %v", err)
	}
	loaded, loadErr := store.LoadKnowledgeGraph()
	if loadErr != nil || loaded.Nodes[0].Status != KnowledgeStatusDraft {
		t.Fatalf("failed rejection changed object: graph=%#v err=%v", loaded, loadErr)
	}
	reviews, listErr := store.ListKnowledgeReviews(10)
	if listErr != nil || len(reviews) != 0 {
		t.Fatalf("failed rejection wrote audit: reviews=%#v err=%v", reviews, listErr)
	}
}
