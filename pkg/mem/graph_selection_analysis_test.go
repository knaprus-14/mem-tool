package mem

import (
	"errors"
	"strings"
	"testing"
)

func TestAnalyzeKnowledgeSelectionReportsOnlyExplicitTypedGraphFacts(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "analysis-a", Kind: KnowledgeNodeClaim, Label: "Claim A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-b", Kind: KnowledgeNodeComparison, Label: "Comparison B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-gap", Kind: KnowledgeNodeGap, Label: "Gap", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-outside", Kind: KnowledgeNodeDefinition, Label: "Outside", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "analysis-compare", From: "analysis-a", To: "analysis-b", Kind: KnowledgeRelationCompares, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-depends", From: "analysis-b", To: "analysis-a", Kind: KnowledgeRelationDependsOn, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-conflict", From: "analysis-a", To: "analysis-b", Kind: KnowledgeRelationContradicts, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-gap-edge", From: "analysis-a", To: "analysis-gap", Kind: KnowledgeRelationRevealsGap, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "analysis-boundary", From: "analysis-a", To: "analysis-outside", Kind: KnowledgeRelationDefines, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"analysis-gap", "analysis-b", "analysis-a"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{Selection: KnowledgeSelectionRequest{NodeIDs: []string{"analysis-a", "analysis-b", "analysis-gap"}}, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == "" || first.Digest != second.Digest || !first.Ready || first.ManifestDigest != manifest.Digest {
		t.Fatalf("analysis is not deterministic or pinned: first=%#v second=%#v", first, second)
	}
	want := KnowledgeSelectionAnalysisSummary{Objects: 3, Documents: 1, InternalRelations: 4, BoundaryRelations: 1, Comparisons: 1, Dependencies: 1, Contradictions: 1, Gaps: 1, AnalyticalObjects: 2}
	if first.Summary != want || len(first.Boundary) != 1 || first.Boundary[0].To.ID != "analysis-outside" || first.Boundary[0].To.Selected {
		t.Fatalf("unexpected analysis summary/boundary: %#v", first)
	}
	if _, err := store.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{Selection: selection, ExpectedManifestDigest: "sha256:stale"}); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("stale analysis pin was accepted: %v", err)
	}
}

func TestSaveKnowledgeSelectionAnalysisIsAtomicAuditedAndPinned(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "report-a", Kind: KnowledgeNodeClaim, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "report-b", Kind: KnowledgeNodeDefinition, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"report-b", "report-a"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := store.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveKnowledgeSelectionAnalysis(KnowledgeSelectionAnalysisSaveRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedAnalysisDigest: "sha256:stale", Label: "Report", Author: "Руслан"}); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("stale analysis digest was accepted: %v", err)
	}
	result, err := store.SaveKnowledgeSelectionAnalysis(KnowledgeSelectionAnalysisSaveRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedAnalysisDigest: analysis.Digest, Label: "  Проверенный анализ  ", Author: " Руслан "})
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.Kind != KnowledgeNodeNote || result.Node.Origin != KnowledgeOriginGenerated || result.Node.Status != KnowledgeStatusDraft || result.Node.Label != "Проверенный анализ" || len(result.Edges) != 2 || !strings.Contains(result.Node.Body, "Зафиксированный анализ") || strings.Contains(result.Node.Body, manifest.Digest) {
		t.Fatalf("unexpected saved analysis: %#v", result)
	}
	for _, edge := range result.Edges {
		if edge.From != result.Node.ID || edge.Kind != KnowledgeRelationDerivedFrom || edge.Origin != KnowledgeOriginGenerated {
			t.Fatalf("invalid saved provenance edge: %#v", edge)
		}
	}
	reports, err := store.ListKnowledgeSelectionReports(10)
	if err != nil || len(reports) != 1 || reports[0].NodeID != result.Node.ID || reports[0].ManifestDigest != manifest.Digest || reports[0].AnalysisDigest != analysis.Digest || strings.Join(reports[0].Selection.NodeIDs, ",") != "report-a,report-b" {
		t.Fatalf("selection report audit is invalid: reports=%#v err=%v", reports, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_selection_reports SET author = 'changed' WHERE id = ?`, reports[0].ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("selection report audit could be changed: %v", err)
	}
}
