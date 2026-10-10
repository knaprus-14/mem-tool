package mem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestKnowledgeApprovalPinsNodeTextAndEdgeStructure(t *testing.T) {
	for _, change := range []string{"node-text", "node-kind", "edge-label", "edge-kind", "edge-endpoint"} {
		t.Run(change, func(t *testing.T) {
			s, anchor := graphStoreAndAnchor(t)
			defer s.Close()
			graph := KnowledgeGraph{Nodes: []KnowledgeNode{
				{ID: "a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
				{ID: "b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
				{ID: "c", Kind: KnowledgeNodeClaim, Label: "C", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			}, Edges: []KnowledgeEdge{{ID: "edge", From: "a", To: "b", Kind: KnowledgeRelationSupports, Label: "supports", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}
			if err := s.UpsertKnowledgeGraph(graph); err != nil {
				t.Fatal(err)
			}
			typ, id := KnowledgeObjectNode, "a"
			if strings.HasPrefix(change, "edge") {
				typ, id = KnowledgeObjectEdge, "edge"
				for _, node := range []string{"a", "b", "c"} {
					if _, err := s.ApproveKnowledgeObject(KnowledgeObjectNode, node); err != nil {
						t.Fatal(err)
					}
				}
			}
			seen := knowledgeReviewItemByID(t, s, typ, id)
			switch change {
			case "node-text":
				if _, err := s.EditKnowledgeObject(KnowledgeEditRequest{ObjectType: typ, ID: id, Editor: "second-tab", Label: "Different", Body: "Unreviewed", ExpectedStatus: seen.Status, ExpectedContentDigest: seen.ContentDigest, ExpectedEvidenceDigest: seen.EvidenceDigest}); err != nil {
					t.Fatal(err)
				}
			case "node-kind":
				if _, err := s.db.Exec(`UPDATE knowledge_nodes SET kind=? WHERE id=?`, KnowledgeNodeDefinition, id); err != nil {
					t.Fatal(err)
				}
			case "edge-label":
				if _, err := s.EditKnowledgeObject(KnowledgeEditRequest{ObjectType: typ, ID: id, Editor: "second-tab", Label: "Changed relation", ExpectedStatus: seen.Status, ExpectedContentDigest: seen.ContentDigest, ExpectedEvidenceDigest: seen.EvidenceDigest}); err != nil {
					t.Fatal(err)
				}
			case "edge-kind":
				if _, err := s.db.Exec(`UPDATE knowledge_edges SET kind=? WHERE id=?`, KnowledgeRelationContradicts, id); err != nil {
					t.Fatal(err)
				}
			case "edge-endpoint":
				if _, err := s.db.Exec(`UPDATE knowledge_edges SET to_node='c' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			request := KnowledgeApprovalRequest{ObjectType: typ, ID: id, Reviewer: "first-tab", ExpectedEvidenceDigest: seen.EvidenceDigest, ExpectedReviewDigest: seen.ReviewDigest}
			if _, err := s.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{request}); !errors.Is(err, ErrKnowledgeContentChanged) {
				t.Fatalf("stale batch accepted: %v", err)
			}
			if _, err := s.ApproveKnowledgeObjectWithReview(request); !errors.Is(err, ErrKnowledgeContentChanged) {
				t.Fatalf("stale single approval accepted: %v", err)
			}
			current := knowledgeReviewItemByID(t, s, typ, id)
			if current.Status != KnowledgeStatusDraft || current.ReviewDigest == seen.ReviewDigest || current.EvidenceDigest != seen.EvidenceDigest {
				t.Fatalf("unexpected changed object: %+v", current)
			}
			request.ExpectedReviewDigest = current.ReviewDigest
			approved, err := s.ApproveKnowledgeObjectWithReview(request)
			if err != nil || approved.Review.ReviewDigest != current.ReviewDigest {
				t.Fatalf("fresh pinned approval/audit: %+v %v", approved, err)
			}
		})
	}
}

func TestGeneratedUpsertResetsReviewedCaseSensitiveBody(t *testing.T) {
	s, anchor := graphStoreAndAnchor(t)
	defer s.Close()
	entry := s.GetBySourceFile(anchor.SourcePath)[0]
	evidence := []GroundedEvidence{groundedEvidenceForEntry(entry, entry.Text, 0)}
	decode := func(body string) KnowledgeGraph {
		t.Helper()
		encoded, _ := json.Marshal(body)
		raw := fmt.Sprintf(`{"nodes":[{"ref":"f","kind":"formula","label":"Мощность","body":%s,"confidence":0.8,"citations":[%q]}]}`, encoded, anchor.CitationID)
		result, err := DecodeKnowledgeExtraction(raw, evidence)
		if err != nil {
			t.Fatal(err)
		}
		return result.Graph
	}
	original, replacement := decode("P = U * I"), decode("p = u * i")
	id := original.Nodes[0].ID
	if replacement.Nodes[0].ID != id {
		t.Fatal("fixture did not retain decoder's case-insensitive stable ID")
	}
	original.Nodes = append(original.Nodes, KnowledgeNode{ID: "other", Kind: KnowledgeNodeClaim, Label: "Other", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}})
	original.Edges = append(original.Edges, KnowledgeEdge{ID: "reviewed-relation", From: id, To: "other", Kind: KnowledgeRelationSupports, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}})
	if err := s.UpsertCurrentKnowledgeGraph(original); err != nil {
		t.Fatal(err)
	}
	for _, node := range original.Nodes {
		if _, err := s.ApproveKnowledgeObject(KnowledgeObjectNode, node.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApproveKnowledgeObject(KnowledgeObjectEdge, "reviewed-relation"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCurrentKnowledgeGraph(original); err != nil {
		t.Fatal(err)
	}
	if current := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, id); current.Status != KnowledgeStatusActive {
		t.Fatal("identical generation lost its review", current)
	}
	if err := s.UpsertCurrentKnowledgeGraph(replacement); err != nil {
		t.Fatal(err)
	}
	current := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, id)
	edge := knowledgeReviewItemByID(t, s, KnowledgeObjectEdge, "reviewed-relation")
	if current.Status != KnowledgeStatusDraft || edge.Status != KnowledgeStatusDraft {
		t.Fatalf("changed formula retained approval: node=%s edge=%s", current.Status, edge.Status)
	}
	var body string
	if err := s.db.QueryRow(`SELECT body FROM knowledge_nodes WHERE id=?`, id).Scan(&body); err != nil || body != "p = u * i" {
		t.Fatalf("replacement was not preserved for review: %q %v", body, err)
	}
}

func TestGeneratedUpsertPreservesReviewedManualOverride(t *testing.T) {
	s, anchor := graphStoreAndAnchor(t)
	defer s.Close()
	node := KnowledgeNode{ID: "manual-review", Kind: KnowledgeNodeClaim, Label: "Generated", Body: "Generated text", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
	if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	seen := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID)
	if _, err := s.EditKnowledgeObject(KnowledgeEditRequest{ObjectType: KnowledgeObjectNode, ID: node.ID, Editor: "author", Label: "Manual", Body: "Reviewed override", ExpectedStatus: seen.Status, ExpectedContentDigest: seen.ContentDigest, ExpectedEvidenceDigest: seen.EvidenceDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveKnowledgeObject(KnowledgeObjectNode, node.ID); err != nil {
		t.Fatal(err)
	}
	seen = knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID)
	node.Body = "Different generated text"
	if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	current := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID)
	if current.Status != KnowledgeStatusActive || current.ReviewDigest != seen.ReviewDigest {
		t.Fatalf("effective manual content lost approval: %+v", current)
	}
	// A text override must not carry an approval across a structural change.
	node.Kind = KnowledgeNodeDefinition
	if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	current = knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID)
	if current.Status != KnowledgeStatusDraft || current.ContentDigest != seen.ContentDigest {
		t.Fatalf("kind change reused approval or lost manual text: %+v", current)
	}
}

func TestGeneratedUpsertComparesLegacyStoredReviewState(t *testing.T) {
	for _, status := range []KnowledgeStatus{KnowledgeStatusActive, KnowledgeStatusRejected, KnowledgeStatusResolved} {
		t.Run(string(status), func(t *testing.T) {
			s, anchor := graphStoreAndAnchor(t)
			defer s.Close()
			node := KnowledgeNode{ID: "old-reviewed", Kind: KnowledgeNodeClaim, Label: "Original", Body: "Original text", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
				t.Fatal(err)
			}
			// Existing databases can contain old decisions without any review pin.
			if _, err := s.db.Exec(`UPDATE knowledge_nodes SET status=? WHERE id=?`, status, node.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
				t.Fatal(err)
			}
			if got := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID); got.Status != status {
				t.Fatalf("unchanged legacy status lost: %+v", got)
			}
			node.Body = "Changed text"
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
				t.Fatal(err)
			}
			if got := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, node.ID); got.Status != KnowledgeStatusDraft {
				t.Fatalf("changed legacy object retained decision: %+v", got)
			}
			var invented int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='knowledge_review_pins'`).Scan(&invented); err != nil || invented != 0 {
				t.Fatalf("upsert invented historical review pins: %d %v", invented, err)
			}
		})
	}
}

func TestGeneratedUpsertResetsReviewedEdgeStructure(t *testing.T) {
	for _, change := range []string{"kind", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			s, anchor := graphStoreAndAnchor(t)
			defer s.Close()
			graph := KnowledgeGraph{}
			for _, id := range []string{"a", "b", "c"} {
				graph.Nodes = append(graph.Nodes, KnowledgeNode{ID: id, Kind: KnowledgeNodeClaim, Label: id, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}})
			}
			edge := KnowledgeEdge{ID: "edge", From: "a", To: "b", Kind: KnowledgeRelationSupports, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
			graph.Edges = append(graph.Edges, edge)
			if err := s.UpsertKnowledgeGraph(graph); err != nil {
				t.Fatal(err)
			}
			for _, node := range graph.Nodes {
				if _, err := s.ApproveKnowledgeObject(KnowledgeObjectNode, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ApproveKnowledgeObject(KnowledgeObjectEdge, edge.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Edges: []KnowledgeEdge{edge}}); err != nil {
				t.Fatal(err)
			}
			if got := knowledgeReviewItemByID(t, s, KnowledgeObjectEdge, edge.ID); got.Status != KnowledgeStatusActive {
				t.Fatal("identical edge lost approval", got)
			}
			if change == "kind" {
				edge.Kind = KnowledgeRelationContradicts
			} else {
				edge.To = "c"
			}
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Edges: []KnowledgeEdge{edge}}); err != nil {
				t.Fatal(err)
			}
			if got := knowledgeReviewItemByID(t, s, KnowledgeObjectEdge, edge.ID); got.Status != KnowledgeStatusDraft {
				t.Fatal("changed edge structure retained approval", got)
			}
		})
	}
}

func TestKnowledgeApprovalMissingPinHTTPAndLegacyAudit(t *testing.T) {
	s, anchor := graphStoreAndAnchor(t)
	defer s.Close()
	const id = "pin-http"
	if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: id, Kind: KnowledgeNodeClaim, Label: "Original", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-upgrade audit record without changing production schemas.
	if _, err := s.db.Exec(`INSERT INTO knowledge_reviews(object_type,object_id,action,previous_status,new_status,reviewer,comment,evidence_digest,created) VALUES('node',?,'reopen','rejected','draft','old','','old','old')`, id); err != nil {
		t.Fatal(err)
	}
	old, err := s.ListKnowledgeReviews(10)
	if err != nil || len(old) != 1 || old[0].ReviewDigest != "" {
		t.Fatalf("legacy read: %+v %v", old, err)
	}
	seen := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, id)
	request := KnowledgeApprovalRequest{ObjectType: KnowledgeObjectNode, ID: id, Reviewer: "reviewer", ExpectedEvidenceDigest: seen.EvidenceDigest}
	if _, err := s.ApproveKnowledgeObjectWithReview(request); !errors.Is(err, ErrKnowledgeReviewPinRequired) {
		t.Fatalf("single API inferred missing pin: %v", err)
	}
	if _, err := s.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{request}); !errors.Is(err, ErrKnowledgeReviewPinRequired) {
		t.Fatalf("legacy unpinned batch accepted: %v", err)
	}
	const host, token = "127.0.0.1:8765", "review-pin-test-session-capability"
	handler := NewKnowledgeMapWorkspaceHandler(s, "", token, DefaultKnowledgeMapView)
	raw, _ := json.Marshal(request)
	response := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", raw)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "expected_review_digest") {
		t.Fatalf("missing HTTP pin: %d %s", response.Code, response.Body.String())
	}
	request.ExpectedReviewDigest = seen.ReviewDigest
	if _, err = s.EditKnowledgeObject(KnowledgeEditRequest{ObjectType: KnowledgeObjectNode, ID: id, Editor: "other", Label: "Changed", ExpectedStatus: seen.Status, ExpectedContentDigest: seen.ContentDigest, ExpectedEvidenceDigest: seen.EvidenceDigest}); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(request)
	response = requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", raw)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale HTTP content: %d %s", response.Code, response.Body.String())
	}
	request.ExpectedReviewDigest = knowledgeReviewItemByID(t, s, KnowledgeObjectNode, id).ReviewDigest
	result, err := s.ApproveKnowledgeObjectWithReview(request)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := s.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 2 || reviews[0].ReviewDigest != request.ExpectedReviewDigest || reviews[1].ReviewDigest != "" {
		t.Fatalf("audit compatibility: %+v %v", reviews, err)
	}
	if _, err := s.db.Exec(`UPDATE knowledge_review_pins SET review_digest='modified' WHERE review_id=?`, result.Review.ID); err == nil {
		t.Fatal("audit pin was mutable")
	}
	if _, err := s.db.Exec(`DELETE FROM knowledge_review_pins WHERE review_id=?`, result.Review.ID); err == nil {
		t.Fatal("audit pin was deletable")
	}
}

func TestKnowledgeReviewDecisionsRejectChangedContent(t *testing.T) {
	for _, action := range []string{"reject", "reopen", "undo"} {
		t.Run(action, func(t *testing.T) {
			s, anchor := graphStoreAndAnchor(t)
			defer s.Close()
			const id = "decision-pin"
			if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: id, Kind: KnowledgeNodeClaim, Label: "Original", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}); err != nil {
				t.Fatal(err)
			}
			seen := knowledgeReviewItemByID(t, s, KnowledgeObjectNode, id)
			request := KnowledgeReviewMutationRequest{ObjectType: KnowledgeObjectNode, ID: id, Reviewer: "first-tab", Comment: "Reviewed", ExpectedEvidenceDigest: seen.EvidenceDigest, ExpectedReviewDigest: seen.ReviewDigest}
			if action != "reject" {
				if _, err := s.RejectKnowledgeObject(request); err != nil {
					t.Fatal(err)
				}
				if action == "undo" {
					reopened, err := s.ReopenKnowledgeObject(request)
					if err != nil {
						t.Fatal(err)
					}
					request.ExpectedReviewID = reopened.Review.ID
				}
			}
			// A generated replacement can change text without changing evidence or
			// a rejected status. The existing user also can edit an unchanged draft.
			if _, err := s.db.Exec(`UPDATE knowledge_nodes SET body='Unreviewed replacement' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			var err error
			switch action {
			case "reject":
				_, err = s.RejectKnowledgeObject(request)
			case "reopen":
				_, err = s.ReopenKnowledgeObject(request)
			case "undo":
				_, err = s.UndoKnowledgeReview(request)
			}
			if !errors.Is(err, ErrKnowledgeContentChanged) {
				t.Fatalf("stale %s decision accepted: %v", action, err)
			}
		})
	}
}
