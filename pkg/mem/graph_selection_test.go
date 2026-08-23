package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type selectionAnswerProvider struct {
	answers  []string
	requests []AnswerRequest
}

func (p *selectionAnswerProvider) Generate(_ context.Context, request AnswerRequest) (string, error) {
	p.requests = append(p.requests, request)
	if len(p.answers) < len(p.requests) {
		return "", errors.New("unexpected selection answer request")
	}
	return p.answers[len(p.requests)-1], nil
}

func TestBuildKnowledgeSelectionManifestIsDeterministicAndPinsCurrentEvidence(t *testing.T) {
	store, firstAnchor := graphStoreAndAnchor(t)
	defer store.Close()
	entries := store.GetBySourceFile(validStructuredChunks()[0].Provenance.SourcePath)
	secondAnchor, err := EvidenceAnchorForEntry(entries[1], entries[1].Text)
	if err != nil {
		t.Fatal(err)
	}
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "selection-a", Kind: KnowledgeNodeClaim, Label: "Alpha", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{firstAnchor}},
		{ID: "selection-b", Kind: KnowledgeNodeDefinition, Label: "Beta", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{secondAnchor}},
	}, Edges: []KnowledgeEdge{{
		ID: "selection-edge", From: "selection-a", To: "selection-b", Kind: KnowledgeRelationSupports,
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{firstAnchor, secondAnchor},
	}}}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	first, err := store.BuildKnowledgeSelectionManifest(KnowledgeSelectionRequest{
		NodeIDs: []string{"selection-b", "selection-a"}, EdgeIDs: []string{"selection-edge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BuildKnowledgeSelectionManifest(KnowledgeSelectionRequest{
		NodeIDs: []string{"selection-a", "selection-b"}, EdgeIDs: []string{"selection-edge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Ready || first.Digest == "" || first.Digest != second.Digest || len(first.Evidence) != 2 ||
		first.Summary.Nodes != 2 || first.Summary.Edges != 1 || first.Summary.Documents != 1 || first.Summary.Pages != 1 ||
		first.Nodes[0].ID != "selection-a" || first.Evidence[0].CitationID > first.Evidence[1].CitationID {
		t.Fatalf("unexpected deterministic selection manifest: %#v", first)
	}
	if _, err := store.BuildKnowledgeSelectionManifest(KnowledgeSelectionRequest{NodeIDs: []string{"selection-a", "selection-a"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate selection was accepted: %v", err)
	}
}

func TestBuildKnowledgeSelectionManifestBlocksStaleOrClosedObjects(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	anchor.DocumentRevision = ChunkContentHash("different revision")
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "selection-stale", Kind: KnowledgeNodeClaim, Label: "Stale", Status: KnowledgeStatusRejected,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.BuildKnowledgeSelectionManifest(KnowledgeSelectionRequest{NodeIDs: []string{"selection-stale"}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Ready || len(manifest.Blockers) < 2 || manifest.Summary.StaleEvidence != 1 {
		t.Fatalf("unsafe selection was not blocked: %#v", manifest)
	}
}

func TestAnswerKnowledgeSelectionUsesOnlyPinnedSelectedEvidence(t *testing.T) {
	store, firstAnchor := graphStoreAndAnchor(t)
	defer store.Close()
	entries := store.GetBySourceFile(validStructuredChunks()[0].Provenance.SourcePath)
	secondAnchor, err := EvidenceAnchorForEntry(entries[1], entries[1].Text)
	if err != nil {
		t.Fatal(err)
	}
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "selection-only", Kind: KnowledgeNodeClaim, Label: "Selected", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{firstAnchor}},
		{ID: "selection-outside", Kind: KnowledgeNodeClaim, Label: "Outside", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{secondAnchor}},
	}}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"selection-only"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	provider := &selectionAnswerProvider{answers: []string{
		`{"claims":[{"text":"Selected answer","citations":["E1"]}]}`,
		`{"claims":[{"text":"Selected summary","citations":["E1"]}]}`,
	}}
	service := &KnowledgeSelectionAnswerService{
		Provider: provider,
		Config:   AnswerConfig{Model: "test-chat", BaseURL: "http://127.0.0.1:11434", TimeoutSeconds: 1, MaxTokens: 512, ContextChars: 12000, Temperature: 0.1},
	}
	result, err := store.AnswerKnowledgeSelection(context.Background(), service, KnowledgeSelectionAnswerRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
		Mode: KnowledgeSelectionModeQuestion, Question: "What is selected?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Selected answer [1, стр. 4]" || len(result.Sources) != 1 || len(provider.requests) != 1 ||
		!strings.Contains(provider.requests[0].Prompt, firstAnchor.Excerpt) || strings.Contains(provider.requests[0].Prompt, secondAnchor.Excerpt) {
		t.Fatalf("selection answer leaked outside evidence: result=%#v request=%#v", result, provider.requests)
	}
	summary, err := store.AnswerKnowledgeSelection(context.Background(), service, KnowledgeSelectionAnswerRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest, Mode: KnowledgeSelectionModeSummary,
	})
	if err != nil || summary.Answer != "Selected summary [1, стр. 4]" || len(provider.requests) != 2 ||
		!strings.Contains(provider.requests[1].Prompt, "Составь подробную") {
		t.Fatalf("selected summary failed: result=%#v requests=%#v err=%v", summary, provider.requests, err)
	}
	if _, err := store.AnswerKnowledgeSelection(context.Background(), &KnowledgeSelectionAnswerService{Provider: provider}, KnowledgeSelectionAnswerRequest{
		Selection: selection, ExpectedManifestDigest: "sha256:old", Mode: KnowledgeSelectionModeQuestion, Question: "test",
	}); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("changed manifest was accepted: %v", err)
	}
	graph.Nodes[0].Label = "Selected changed"
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AnswerKnowledgeSelection(context.Background(), service, KnowledgeSelectionAnswerRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest, Mode: KnowledgeSelectionModeQuestion, Question: "test",
	}); !errors.Is(err, ErrKnowledgeSelectionChanged) || len(provider.requests) != 2 {
		t.Fatalf("mutated selected object reached the model: err=%v calls=%d", err, len(provider.requests))
	}
}

func TestExploreKnowledgeSelectionExpandsDeterministicallyAndPinsResult(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "explore-a", Kind: KnowledgeNodeTopic, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "explore-b", Kind: KnowledgeNodeClaim, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "explore-c", Kind: KnowledgeNodeClaim, Label: "C", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "explore-in", Kind: KnowledgeNodeNote, Label: "Incoming", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "explore-e-ac", From: "explore-a", To: "explore-c", Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "explore-e-ab", From: "explore-a", To: "explore-b", Kind: KnowledgeRelationSupports, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "explore-e-in", From: "explore-in", To: "explore-a", Kind: KnowledgeRelationSupports, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"explore-a"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ExploreKnowledgeSelection(KnowledgeSelectionExploreRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
		Mode: KnowledgeSelectionExploreNeighbours, Direction: KnowledgeSelectionDirectionOutgoing, Depth: 1,
		RelationKinds: []KnowledgeRelationKind{KnowledgeRelationSupports},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Selection.NodeIDs, ",") != "explore-a,explore-b" ||
		strings.Join(result.Selection.EdgeIDs, ",") != "explore-e-ab" ||
		strings.Join(result.AddedNodeIDs, ",") != "explore-b" ||
		strings.Join(result.AddedEdgeIDs, ",") != "explore-e-ab" ||
		result.SourceManifestDigest != manifest.Digest || result.Manifest.Digest == "" || !result.Manifest.Ready {
		t.Fatalf("unexpected deterministic expansion: %#v", result)
	}
	incoming, err := store.ExploreKnowledgeSelection(KnowledgeSelectionExploreRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
		Mode: KnowledgeSelectionExploreNeighbours, Direction: KnowledgeSelectionDirectionIncoming, Depth: 1,
	})
	if err != nil || strings.Join(incoming.AddedNodeIDs, ",") != "explore-in" || strings.Join(incoming.AddedEdgeIDs, ",") != "explore-e-in" {
		t.Fatalf("incoming expansion failed: result=%#v err=%v", incoming, err)
	}
	if _, err := store.ExploreKnowledgeSelection(KnowledgeSelectionExploreRequest{
		Selection: selection, ExpectedManifestDigest: "sha256:stale", Mode: KnowledgeSelectionExploreNeighbours,
	}); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("stale expansion pin was accepted: %v", err)
	}
}

func TestExploreKnowledgeSelectionFindsDeterministicShortestPath(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := make([]KnowledgeNode, 0, 4)
	for _, id := range []string{"path-a", "path-b", "path-c", "path-d"} {
		nodes = append(nodes, KnowledgeNode{ID: id, Kind: KnowledgeNodeTopic, Label: id, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}})
	}
	edges := []KnowledgeEdge{
		{ID: "path-e-ac", From: "path-a", To: "path-c", Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "path-e-cd", From: "path-c", To: "path-d", Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "path-e-ab", From: "path-a", To: "path-b", Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "path-e-bd", From: "path-b", To: "path-d", Kind: KnowledgeRelationRelated, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"path-d", "path-a"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ExploreKnowledgeSelection(KnowledgeSelectionExploreRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
		Mode: KnowledgeSelectionExplorePath, Direction: KnowledgeSelectionDirectionBoth, Depth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PathFound || strings.Join(result.AddedNodeIDs, ",") != "path-b" || strings.Join(result.AddedEdgeIDs, ",") != "path-e-ab,path-e-bd" {
		t.Fatalf("unexpected shortest path: %#v", result)
	}
	notFound, err := store.ExploreKnowledgeSelection(KnowledgeSelectionExploreRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
		Mode: KnowledgeSelectionExplorePath, Direction: KnowledgeSelectionDirectionBoth, Depth: 1,
	})
	if err != nil || notFound.PathFound || len(notFound.AddedNodeIDs) != 0 || len(notFound.AddedEdgeIDs) != 0 {
		t.Fatalf("depth-limited path result is invalid: result=%#v err=%v", notFound, err)
	}
}
