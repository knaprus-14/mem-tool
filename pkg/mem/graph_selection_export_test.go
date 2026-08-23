package mem

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExportKnowledgeSelectionProducesPortableGroundedArtifacts(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "export-first", Kind: KnowledgeNodeTask, Label: "Сначала", Body: "Подготовить данные", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "export-second", Kind: KnowledgeNodeTask, Label: "Затем", Body: "=ОПАСНАЯ_ФОРМУЛА", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{{
		ID: "export-order", From: "export-first", To: "export-second", Kind: KnowledgeRelationPrerequisite,
		Label: "порядок", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"export-second", "export-first"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		format   string
		filename string
		markers  []string
	}{
		{KnowledgeSelectionExportReport, "mem-selection-report.md", []string{"# Проверяемый экспорт", "book.pdf", "стр. 4", "Сначала", "Затем", "prerequisite"}},
		{KnowledgeSelectionExportPlan, "mem-selection-plan.md", []string{"1. **Сначала**", "2. **Затем**", "стр. 4", "Manifest:"}},
		{KnowledgeSelectionExportChecklist, "mem-selection-checklist.md", []string{"- [ ] **Сначала**", "- [ ] **Затем**"}},
		{KnowledgeSelectionExportTable, "mem-selection-table.csv", []string{"Тип объекта;Название", "'=ОПАСНАЯ_ФОРМУЛА", "book.pdf", "стр. 4"}},
		{KnowledgeSelectionExportBranch, "mem-selection-branch.json", []string{`"version": 1`, `"title": "Проверяемый экспорт"`, `"manifest"`, `"analysis"`, `"graph"`}},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			result, err := store.ExportKnowledgeSelection(KnowledgeSelectionExportRequest{
				Selection: selection, ExpectedManifestDigest: manifest.Digest, Format: test.format, Title: "Проверяемый экспорт",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Filename != test.filename || result.ContentType == "" || len(result.Content) == 0 {
				t.Fatalf("invalid export metadata: %#v", result)
			}
			content := string(result.Content)
			for _, marker := range test.markers {
				if !strings.Contains(content, marker) {
					t.Fatalf("%s export is missing %q:\n%s", test.format, marker, content)
				}
			}
		})
	}

	branch, err := store.ExportKnowledgeSelection(KnowledgeSelectionExportRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Format: KnowledgeSelectionExportBranch})
	if err != nil {
		t.Fatal(err)
	}
	var decoded knowledgeSelectionBranchExport
	if err := json.Unmarshal(branch.Content, &decoded); err != nil || len(decoded.Graph.Nodes) != 2 || len(decoded.Graph.Edges) != 1 || decoded.Manifest.Digest != manifest.Digest {
		t.Fatalf("branch JSON is not self-contained: decoded=%#v err=%v", decoded, err)
	}
	if _, err := store.ExportKnowledgeSelection(KnowledgeSelectionExportRequest{Selection: selection, ExpectedManifestDigest: "sha256:stale", Format: KnowledgeSelectionExportReport}); !strings.Contains(err.Error(), ErrKnowledgeSelectionChanged.Error()) {
		t.Fatalf("stale export pin was accepted: %v", err)
	}
}

func TestKnowledgeSelectionPlanMarksDependencyCycles(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	nodes := []KnowledgeNode{
		{ID: "cycle-a", Kind: KnowledgeNodeTask, Label: "A", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "cycle-b", Kind: KnowledgeNodeTask, Label: "B", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{
		{ID: "cycle-ab", From: "cycle-a", To: "cycle-b", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "cycle-ba", From: "cycle-b", To: "cycle-a", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"cycle-a", "cycle-b"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ExportKnowledgeSelection(KnowledgeSelectionExportRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Format: KnowledgeSelectionExportPlan})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Content), "найден цикл") {
		t.Fatalf("dependency cycle is hidden from plan:\n%s", result.Content)
	}
}
