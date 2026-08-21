package mem

import (
	"strings"
	"testing"
)

func TestKnowledgeDuplicateDetectionMergeAuditAndInvalidation(t *testing.T) {
	store, nodes, edge := knowledgeDuplicateFixture(t)
	defer store.Close()
	vectors := make([]KnowledgeNodeVector, 0, len(nodes))
	byID := make(map[string]KnowledgeNode)
	for _, node := range nodes {
		digest, err := KnowledgeNodeContentDigest(node)
		if err != nil {
			t.Fatal(err)
		}
		vector := []float32{0, 1}
		if node.ID == "duplicate-target" {
			vector = []float32{1, 0}
		} else if node.ID == "duplicate-source" {
			vector = []float32{0.99, 0.01}
		}
		vectors = append(vectors, KnowledgeNodeVector{NodeID: node.ID, NodeDigest: digest, Embedding: vector})
		byID[node.ID] = node
	}
	report, err := store.DetectKnowledgeNodeDuplicates(vectors, "sha256:test-space", 0.95, 10, KnowledgeNodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if report.EligibleNodes != 3 || len(report.Candidates) != 1 {
		t.Fatalf("unexpected duplicate report: %#v", report)
	}
	candidate := report.Candidates[0]
	if candidate.SuggestedSource != "duplicate-source" || candidate.SuggestedTarget != "duplicate-target" || candidate.Similarity < 0.99 {
		t.Fatalf("unsafe merge direction: %#v", candidate)
	}
	sourceSummary, targetSummary := candidate.Left, candidate.Right
	if sourceSummary.ID != candidate.SuggestedSource {
		sourceSummary, targetSummary = candidate.Right, candidate.Left
	}

	result, err := store.MergeKnowledgeDuplicate(KnowledgeNodeMergeRequest{
		SourceID: candidate.SuggestedSource, TargetID: candidate.SuggestedTarget,
		Reviewer: "reviewer", Comment: "same pressure requirement",
		ExpectedSourceNodeDigest: sourceSummary.NodeDigest, ExpectedTargetNodeDigest: targetSummary.NodeDigest,
		ExpectedSourceEvidenceDigest: sourceSummary.EvidenceDigest, ExpectedTargetEvidenceDigest: targetSummary.EvidenceDigest,
		Similarity: candidate.Similarity, EmbeddingSpace: candidate.EmbeddingSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Merge.ID == 0 || !result.Merge.Current || result.ResolvedEdges != 1 || result.Review.Action != "merge" {
		t.Fatalf("incomplete merge result: %#v", result)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]KnowledgeStatus)
	for _, node := range graph.Nodes {
		statuses[node.ID] = node.Status
	}
	if statuses["duplicate-source"] != KnowledgeStatusResolved || statuses["duplicate-target"] != KnowledgeStatusActive {
		t.Fatalf("merge changed wrong nodes: %#v", statuses)
	}
	for _, stored := range graph.Edges {
		if stored.ID == edge.ID && stored.Status != KnowledgeStatusResolved {
			t.Fatalf("incident generated edge remained in review queue: %#v", stored)
		}
	}
	merges, err := store.ListKnowledgeNodeMerges(10)
	if err != nil || len(merges) != 1 || !merges[0].Current || merges[0].TargetID != "duplicate-target" {
		t.Fatalf("merge audit is incomplete: merges=%#v err=%v", merges, err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 1 || reviews[0].Action != "merge" || reviews[0].NewStatus != KnowledgeStatusResolved {
		t.Fatalf("review audit is incomplete: reviews=%#v err=%v", reviews, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_node_merges SET reviewer = 'changed' WHERE id = ?`, merges[0].ID); err == nil {
		t.Fatal("merge audit update was not blocked")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_node_merges WHERE id = ?`, merges[0].ID); err == nil {
		t.Fatal("merge audit delete was not blocked")
	}

	// Exact re-extraction preserves the reviewed merge.
	source := byID["duplicate-source"]
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{source}, Edges: []KnowledgeEdge{edge}}); err != nil {
		t.Fatal(err)
	}
	merges, err = store.ListKnowledgeNodeMerges(10)
	if err != nil || !merges[0].Current {
		t.Fatalf("exact re-extraction invalidated merge: merges=%#v err=%v", merges, err)
	}

	// A semantic change under the same ID must reopen the source and its edges.
	source.Label = "Changed pressure requirement"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{source}}); err != nil {
		t.Fatal(err)
	}
	graph, err = store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == source.ID && node.Status != KnowledgeStatusDraft {
			t.Fatalf("changed merged node stayed resolved: %#v", node)
		}
	}
	for _, stored := range graph.Edges {
		if stored.ID == edge.ID && stored.Status != KnowledgeStatusDraft {
			t.Fatalf("changed merged node edge stayed resolved: %#v", stored)
		}
	}
	merges, err = store.ListKnowledgeNodeMerges(10)
	if err != nil || merges[0].Current || !strings.Contains(merges[0].StateReason, "source") {
		t.Fatalf("changed merge was not marked stale: merges=%#v err=%v", merges, err)
	}
}

func TestKnowledgeDuplicateMergeRejectsUnpinnedOrUnreviewableNodes(t *testing.T) {
	store, nodes, _ := knowledgeDuplicateFixture(t)
	defer store.Close()
	byID := make(map[string]KnowledgeNode)
	for _, node := range nodes {
		byID[node.ID] = node
	}
	sourceNodeDigest, _ := KnowledgeNodeContentDigest(byID["duplicate-source"])
	targetNodeDigest, _ := KnowledgeNodeContentDigest(byID["duplicate-target"])
	sourceEvidenceDigest, _ := KnowledgeEvidenceDigest(byID["duplicate-source"].Evidence)
	targetEvidenceDigest, _ := KnowledgeEvidenceDigest(byID["duplicate-target"].Evidence)
	request := KnowledgeNodeMergeRequest{
		SourceID: "duplicate-source", TargetID: "duplicate-target", Reviewer: "reviewer",
		ExpectedSourceNodeDigest: sourceNodeDigest, ExpectedTargetNodeDigest: targetNodeDigest,
		ExpectedSourceEvidenceDigest: sourceEvidenceDigest, ExpectedTargetEvidenceDigest: targetEvidenceDigest,
		Similarity: 0.99, EmbeddingSpace: "sha256:test-space",
	}
	request.ExpectedSourceNodeDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := store.MergeKnowledgeDuplicate(request); err == nil {
		t.Fatal("changed source digest was accepted")
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == "duplicate-source" && node.Status != KnowledgeStatusDraft {
			t.Fatalf("rejected merge changed source: %#v", node)
		}
	}
	merges, err := store.ListKnowledgeNodeMerges(10)
	if err != nil || len(merges) != 0 {
		t.Fatalf("rejected merge left audit: merges=%#v err=%v", merges, err)
	}
	request.ExpectedSourceNodeDigest = sourceNodeDigest
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Edges: []KnowledgeEdge{{
		ID: "manual-duplicate-edge", From: "duplicate-source", To: "different-node",
		Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual,
		Evidence: byID["duplicate-source"].Evidence,
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MergeKnowledgeDuplicate(request); err == nil || !strings.Contains(err.Error(), "incident edges") {
		t.Fatalf("merge accepted non-generated incident edge: %v", err)
	}
}

func TestKnowledgeDuplicateTargetChangeReopensSource(t *testing.T) {
	store, nodes, _ := knowledgeDuplicateFixture(t)
	defer store.Close()
	byID := make(map[string]KnowledgeNode)
	for _, node := range nodes {
		byID[node.ID] = node
	}
	source := byID["duplicate-source"]
	target := byID["duplicate-target"]
	sourceNodeDigest, _ := KnowledgeNodeContentDigest(source)
	targetNodeDigest, _ := KnowledgeNodeContentDigest(target)
	sourceEvidenceDigest, _ := KnowledgeEvidenceDigest(source.Evidence)
	targetEvidenceDigest, _ := KnowledgeEvidenceDigest(target.Evidence)
	if _, err := store.MergeKnowledgeDuplicate(KnowledgeNodeMergeRequest{
		SourceID: source.ID, TargetID: target.ID, Reviewer: "reviewer",
		ExpectedSourceNodeDigest: sourceNodeDigest, ExpectedTargetNodeDigest: targetNodeDigest,
		ExpectedSourceEvidenceDigest: sourceEvidenceDigest, ExpectedTargetEvidenceDigest: targetEvidenceDigest,
		Similarity: 0.99, EmbeddingSpace: "sha256:test-space",
	}); err != nil {
		t.Fatal(err)
	}
	target.Label = "Changed canonical pressure"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{target}}); err != nil {
		t.Fatal(err)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == source.ID && node.Status != KnowledgeStatusDraft {
			t.Fatalf("target change did not reopen source: %#v", node)
		}
	}
	merges, err := store.ListKnowledgeNodeMerges(10)
	if err != nil || len(merges) != 1 || merges[0].Current || merges[0].StateReason == "" {
		t.Fatalf("target change did not stale merge: merges=%#v err=%v", merges, err)
	}
}

func knowledgeDuplicateFixture(t *testing.T) (*Store, []KnowledgeNode, KnowledgeEdge) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"Maximum working pressure is 1.0 MPa.", "Working pressure shall not exceed 1.0 MPa.", "Temperature is 80 C."}
	nodes := make([]KnowledgeNode, 0, len(texts))
	ids := []string{"duplicate-target", "duplicate-source", "different-node"}
	labels := []string{"Maximum working pressure", "Maximum pressure", "Temperature limit"}
	statuses := []KnowledgeStatus{KnowledgeStatusActive, KnowledgeStatusDraft, KnowledgeStatusActive}
	for i, text := range texts {
		source := "C:/docs/duplicate-" + ids[i] + ".md"
		entry, err := store.AddDocumentChunkWithEmbeddingIdentity(text, labels[i], nil,
			EmbeddingIdentity{Backend: "test", Model: "chunk-model", SpaceID: ChunkContentHash("chunk-space")},
			[]float32{1, 0}, source, 0, 1, false, Provenance{
				DocumentID: "doc-" + ids[i], DocumentRevision: ChunkContentHash("revision-" + ids[i]),
				ChunkHash: ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown",
				Page: 1, BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1,
				ExtractionMethod: "text", OCRConfidence: -1,
			})
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		anchor, err := EvidenceAnchorForEntry(*entry, entry.Text)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		nodes = append(nodes, KnowledgeNode{
			ID: ids[i], Kind: KnowledgeNodeClaim, Label: labels[i], Body: text,
			Status: statuses[i], Origin: KnowledgeOriginGenerated, Confidence: 0.9,
			Evidence: []EvidenceAnchor{anchor},
		})
	}
	edge := KnowledgeEdge{
		ID: "duplicate-edge", From: "duplicate-source", To: "different-node",
		Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Confidence: 0.8,
		Evidence: []EvidenceAnchor{nodes[1].Evidence[0]},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: []KnowledgeEdge{edge}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, nodes, edge
}
