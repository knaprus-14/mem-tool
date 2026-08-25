package mem

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProfileKnowledgeMapMeasuresRealReadOnlyStages(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := coverageTestChunks("profile revision", []string{"Первое утверждение", "Второе утверждение"})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	first, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EvidenceAnchorForEntry(entries[1], entries[1].Text)
	if err != nil {
		t.Fatal(err)
	}
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "profile-one", Kind: KnowledgeNodeClaim, Label: "Первое утверждение", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{first}},
		{ID: "profile-two", Kind: KnowledgeNodeClaim, Label: "Второе утверждение", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{second}},
	}, Edges: []KnowledgeEdge{{
		ID: "profile-related", From: "profile-one", To: "profile-two", Kind: KnowledgeRelationRelated,
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{first, second},
	}}}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	before, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != KnowledgeMapProfileVersion || report.Options.Iterations != 2 || report.Options.ViewName != DefaultKnowledgeMapView {
		t.Fatalf("unexpected profile options: %#v", report)
	}
	if report.Database.Path != store.Path() || report.Database.DataVersion <= 0 || report.Database.MainBytes <= 0 || report.Database.PageCount <= 0 ||
		report.Database.PageSize <= 0 || report.Database.Entries != 2 || report.Database.Documents != 1 {
		t.Fatalf("unexpected database profile: %#v", report.Database)
	}
	if report.Snapshot.Nodes != 2 || report.Snapshot.Edges != 1 || report.Snapshot.Evidence != 4 ||
		report.Snapshot.HTMLBytes <= 0 || !strings.HasPrefix(report.Snapshot.GraphDigest, "sha256:") ||
		!strings.HasPrefix(report.Snapshot.EvidenceStateDigest, "sha256:") ||
		!strings.HasPrefix(report.Snapshot.ViewDigest, "sha256:") || !strings.HasPrefix(report.Snapshot.HTMLDigest, "sha256:") {
		t.Fatalf("unexpected profile snapshot: %#v", report.Snapshot)
	}
	wantStages := []string{"sqlite_graph_load", "pinned_evidence_snapshot", "live_view_assembly", "html_serialization"}
	if len(report.Stages) != len(wantStages) {
		t.Fatalf("unexpected stages: %#v", report.Stages)
	}
	for i, stage := range report.Stages {
		if stage.Name != wantStages[i] || len(stage.SamplesNS) != 2 || stage.MinNS < 0 || stage.MaxNS < stage.MinNS ||
			stage.MedianNS < stage.MinNS || stage.P95NS > stage.MaxNS || stage.MeanNS < stage.MinNS || stage.MeanNS > stage.MaxNS {
			t.Fatalf("invalid stage %d: %#v", i, stage)
		}
	}
	if len(report.Limitations) != 5 || !strings.Contains(report.Limitations[1], "Браузерный JavaScript") {
		t.Fatalf("browser limitation is missing: %#v", report.Limitations)
	}
	after, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("profile mutated knowledge graph")
	}
}

func TestSummarizeKnowledgeMapProfileStageUsesStandardMedianAndP95(t *testing.T) {
	stage := summarizeKnowledgeMapProfileStage("test", []int64{300, 0, 200, 100})
	if stage.MinNS != 0 || stage.MedianNS != 150 || stage.P95NS != 300 || stage.MaxNS != 300 || stage.MeanNS != 150 {
		t.Fatalf("unexpected profile summary: %#v", stage)
	}
}

func TestProfileKnowledgeMapValidatesIterationsAndIsStableForEmptyStore(t *testing.T) {
	var unavailable *Store
	if _, err := unavailable.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: 1}); err == nil {
		t.Fatal("unavailable store accepted")
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: -1}); err == nil {
		t.Fatal("negative iterations accepted")
	}
	if _, err := store.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: MaxKnowledgeMapProfileIterations + 1}); err == nil {
		t.Fatal("excessive iterations accepted")
	}
	report, err := store.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Snapshot.Nodes != 0 || report.Snapshot.Edges != 0 || report.Snapshot.HTMLBytes == 0 {
		t.Fatalf("empty profile is invalid: %#v", report)
	}
}
