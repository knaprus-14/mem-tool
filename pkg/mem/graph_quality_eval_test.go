package mem

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKnowledgeQualityEvalIsDeterministicAndReportsPassingAndFailingGates(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	chunks := coverageTestChunks("quality revision", []string{"alpha", "beta", "gamma"})
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
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "quality-draft", Kind: KnowledgeNodeClaim, Label: "Draft", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.8, Evidence: []EvidenceAnchor{first}},
		{ID: "quality-active", Kind: KnowledgeNodeClaim, Label: "Active", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Confidence: 0.9, Evidence: []EvidenceAnchor{second}},
	}}); err != nil {
		t.Fatal(err)
	}

	minProcessing, minCoverage := 66.0, 66.0
	maxUnprocessed, maxUncovered, maxLowOCR, maxWarnings := 1, 1, 1, 1
	minNodes, maxDraft, maxStale, maxMissing := 2, 1, 0, 0
	manifest := KnowledgeQualityManifest{
		SchemaVersion: KnowledgeQualityManifestSchemaVersion,
		Name:          "RF baseline",
		Scope:         KnowledgeCoverageOptions{Document: chunks[0].Provenance.SourcePath},
		Requirements: KnowledgeQualityRequirements{
			MinProcessingPercent: &minProcessing, MinKnowledgeCoveragePercent: &minCoverage,
			MaxUnprocessedChunks: &maxUnprocessed, MaxUncoveredChunks: &maxUncovered,
			MaxLowConfidenceOCRChunks: &maxLowOCR, MaxWarningChunks: &maxWarnings,
			MinExtractedNodes: &minNodes, MaxDraftObjects: &maxDraft,
			MaxStaleEvidenceObjects: &maxStale, MaxMissingEvidenceObjects: &maxMissing,
		},
	}
	report, err := store.EvaluateKnowledgeQuality(manifest, 65)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || len(report.Checks) != 10 || !strings.HasPrefix(report.ManifestDigest, "sha256:") || !strings.HasPrefix(report.SnapshotDigest, "sha256:") {
		t.Fatalf("unexpected passing report: %#v", report)
	}
	repeated, err := store.EvaluateKnowledgeQuality(manifest, 65)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, repeated) {
		t.Fatalf("quality report is not deterministic:\nfirst=%#v\nsecond=%#v", report, repeated)
	}

	minCoverage = 90
	maxWarnings = 0
	failed, err := store.EvaluateKnowledgeQuality(manifest, 65)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Passed {
		t.Fatalf("failing gates were accepted: %#v", failed.Checks)
	}
	failures := 0
	for _, check := range failed.Checks {
		if !check.Passed {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("expected two failing checks, got %d: %#v", failures, failed.Checks)
	}
}

func TestParseKnowledgeQualityManifestIsStrictAndPreservesExplicitZero(t *testing.T) {
	data := []byte(`{
  "schema_version": 1,
  "name": "No warnings",
  "scope": {"document": " C:/docs/coverage.pdf "},
  "requirements": {"max_warning_chunks": 0, "min_extracted_relations": 0}
}`)
	manifest, err := ParseKnowledgeQualityManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Scope.Document != "C:/docs/coverage.pdf" || manifest.Requirements.MaxWarningChunks == nil || *manifest.Requirements.MaxWarningChunks != 0 ||
		manifest.Requirements.MinExtractedRelations == nil || *manifest.Requirements.MinExtractedRelations != 0 {
		t.Fatalf("explicit zero or normalization lost: %#v", manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || !strings.Contains(string(encoded), `"max_warning_chunks":0`) {
		t.Fatalf("manifest does not round-trip: %s err=%v", encoded, err)
	}

	invalid := []string{
		`{"schema_version":1,"name":"x","scope":{},"requirements":{},"unknown":true}`,
		`{"schema_version":1,"name":"x","scope":{},"requirements":{}}`,
		`{"schema_version":2,"name":"x","scope":{},"requirements":{"max_warning_chunks":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"requirements":{"max_warning_chunks":-1}}`,
		`{"schema_version":1,"name":"x","scope":{},"requirements":{"min_processing_percent":101}}`,
		`{"schema_version":1,"name":"x","scope":{},"requirements":{"max_warning_chunks":0}} {}`,
	}
	for _, raw := range invalid {
		if _, err := ParseKnowledgeQualityManifest([]byte(raw)); err == nil {
			t.Fatalf("invalid manifest accepted: %s", raw)
		}
	}
}
