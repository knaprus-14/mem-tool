package mem

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKnowledgeLabeledEvalMeasuresClaimsAndContradictionsDeterministically(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	chunks := coverageTestChunks("labeled revision", []string{
		"Рабочее давление установки равно 10 бар.",
		"Рабочее давление установки не должно превышать 8 бар.",
		"Насос окрашен в зелёный цвет.",
	})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	anchors := make([]EvidenceAnchor, len(entries))
	for i, entry := range entries {
		anchors[i], err = EvidenceAnchorForEntry(entry, entry.Text)
		if err != nil {
			t.Fatal(err)
		}
	}
	stale := anchors[0]
	stale.DocumentRevision = ChunkContentHash("superseded revision")
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "claim-pressure-10", Kind: KnowledgeNodeClaim, Label: "Рабочее давление равно 10 бар", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Confidence: 0.95, Evidence: []EvidenceAnchor{anchors[0]}},
		{ID: "claim-pressure-8", Kind: KnowledgeNodeClaim, Label: "Максимальное рабочее давление", Body: "Рабочее давление не превышает 8 бар", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.9, Evidence: []EvidenceAnchor{anchors[1]}},
		{ID: "claim-colour", Kind: KnowledgeNodeClaim, Label: "Насос зелёного цвета", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.7, Evidence: []EvidenceAnchor{anchors[2]}},
		{ID: "claim-stale", Kind: KnowledgeNodeClaim, Label: "Устаревший факт", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.5, Evidence: []EvidenceAnchor{stale}},
	}, Edges: []KnowledgeEdge{
		{ID: "edge-pressure-conflict", From: "claim-pressure-10", To: "claim-pressure-8", Kind: KnowledgeRelationContradicts, Label: "Несовместимые пределы давления", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.9, Evidence: []EvidenceAnchor{anchors[0], anchors[1]}},
		{ID: "edge-colour-conflict", From: "claim-pressure-10", To: "claim-colour", Kind: KnowledgeRelationContradicts, Label: "Ложное противоречие", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.4, Evidence: []EvidenceAnchor{anchors[0], anchors[2]}},
		{ID: "edge-stale-conflict", From: "claim-pressure-10", To: "claim-stale", Kind: KnowledgeRelationContradicts, Label: "Устаревшая связь", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Confidence: 0.4, Evidence: []EvidenceAnchor{stale}},
	}}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	minClaimPrecision, minClaimRecall := 60.0, 70.0
	minContradictionF1 := 50.0
	maxFalsePositiveContradictions := 0
	manifest := normalizeKnowledgeLabeledEvalManifest(KnowledgeLabeledEvalManifest{
		SchemaVersion: KnowledgeLabeledEvalSchemaVersion,
		Name:          "Pressure benchmark",
		Scope:         KnowledgeCoverageOptions{Document: chunks[0].Provenance.SourcePath, Tag: "RF"},
		Claims: []KnowledgeLabeledExpectedClaim{
			{ID: "expected-10", Text: "Рабочее давление установки равно 10 бар", AcceptedCitationIDs: []string{anchors[0].CitationID}},
			{ID: "expected-8", Text: "Предельное рабочее давление составляет 8 бар", Aliases: []string{"Рабочее давление не превышает 8 бар"}},
			{ID: "expected-missing", Text: "Расчётная температура равна 40 градусам"},
		},
		Contradictions: []KnowledgeLabeledExpectedContradiction{
			{ID: "expected-conflict", LeftClaimID: "expected-10", RightClaimID: "expected-8"},
			{ID: "expected-missing-conflict", LeftClaimID: "expected-8", RightClaimID: "expected-missing"},
		},
		Requirements: KnowledgeLabeledEvalRequirements{
			MinClaimPrecision: &minClaimPrecision, MinClaimRecall: &minClaimRecall,
			MinContradictionF1:             &minContradictionF1,
			MaxFalsePositiveContradictions: &maxFalsePositiveContradictions,
		},
	})
	report, err := store.EvaluateKnowledgeLabeledQuality(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatalf("expected failing benchmark, got %#v", report.Checks)
	}
	if report.Claims.Expected != 3 || report.Claims.Predicted != 3 || report.Claims.TruePositive != 2 ||
		report.Claims.FalsePositive != 1 || report.Claims.FalseNegative != 1 {
		t.Fatalf("unexpected claim metrics: %#v", report.Claims)
	}
	if report.Contradictions.Expected != 2 || report.Contradictions.Predicted != 2 || report.Contradictions.TruePositive != 1 ||
		report.Contradictions.FalsePositive != 1 || report.Contradictions.FalseNegative != 1 {
		t.Fatalf("unexpected contradiction metrics: %#v", report.Contradictions)
	}
	if len(report.ClaimMatches) != 2 || report.ClaimMatches[0].ExpectedID != "expected-10" ||
		len(report.FalsePositiveClaims) != 1 || report.FalsePositiveClaims[0].NodeID != "claim-colour" ||
		len(report.FalseNegativeClaims) != 1 || report.FalseNegativeClaims[0].ID != "expected-missing" {
		t.Fatalf("unexpected claim details: %#v", report)
	}
	if len(report.ContradictionMatches) != 1 || report.ContradictionMatches[0].EdgeID != "edge-pressure-conflict" ||
		len(report.FalsePositiveContradictions) != 1 || report.FalsePositiveContradictions[0].EdgeID != "edge-colour-conflict" ||
		len(report.FalseNegativeContradictions) != 1 || report.FalseNegativeContradictions[0].ID != "expected-missing-conflict" {
		t.Fatalf("unexpected contradiction details: %#v", report)
	}
	if report.ExcludedNonCurrentClaims != 1 || report.ExcludedNonCurrentContradictions != 1 {
		t.Fatalf("unexpected non-current exclusions: %#v", report)
	}
	if !strings.HasPrefix(report.ManifestDigest, "sha256:") || !strings.HasPrefix(report.GraphDigest, "sha256:") ||
		!strings.HasPrefix(report.EvidenceStateDigest, "sha256:") || !strings.HasPrefix(report.SnapshotDigest, "sha256:") {
		t.Fatalf("missing reproducibility digests: %#v", report)
	}
	repeated, err := store.EvaluateKnowledgeLabeledQuality(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, repeated) {
		t.Fatalf("labeled evaluation is not deterministic:\nfirst=%#v\nsecond=%#v", report, repeated)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || len(loaded.Nodes) != 4 || len(loaded.Edges) != 3 {
		t.Fatalf("evaluation mutated graph: nodes=%d edges=%d err=%v", len(loaded.Nodes), len(loaded.Edges), err)
	}
}

func TestParseKnowledgeLabeledEvalManifestIsStrictAndCanonical(t *testing.T) {
	data := []byte(`{
  "schema_version": 1,
  "name": " RF benchmark ",
  "scope": {"document": " C:/docs/manual.pdf "},
  "matching": {},
  "claims": [{"id":"claim-a","text":" Давление равно 10 бар ","aliases":["10 бар"]}],
  "contradictions": [],
  "requirements": {"min_claim_recall": 0, "max_false_positive_claims": 0}
}`)
	manifest, err := ParseKnowledgeLabeledEvalManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "RF benchmark" || manifest.Scope.Document != "C:/docs/manual.pdf" ||
		manifest.Matching.ClaimSimilarityThreshold == nil || *manifest.Matching.ClaimSimilarityThreshold != DefaultKnowledgeLabeledClaimSimilarity ||
		!reflect.DeepEqual(manifest.Matching.Statuses, []KnowledgeStatus{KnowledgeStatusActive, KnowledgeStatusDraft}) ||
		manifest.Requirements.MinClaimRecall == nil || *manifest.Requirements.MinClaimRecall != 0 {
		t.Fatalf("defaults or canonicalization lost: %#v", manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || !strings.Contains(string(encoded), `"claim_similarity_threshold":0.8`) {
		t.Fatalf("canonical manifest does not round-trip: %s err=%v", encoded, err)
	}

	invalid := []string{
		`{"schema_version":1,"name":"x","scope":{},"matching":{},"claims":[],"contradictions":[],"requirements":{"min_claim_recall":0},"unknown":true}`,
		`{"schema_version":2,"name":"x","scope":{},"matching":{},"claims":[],"contradictions":[],"requirements":{"min_claim_recall":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{"claim_similarity_threshold":0},"claims":[],"contradictions":[],"requirements":{"min_claim_recall":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{"statuses":[]},"claims":[],"contradictions":[],"requirements":{"min_claim_recall":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{},"claims":[{"id":"c1","text":"one"}],"contradictions":[{"id":"x1","left_claim_id":"c1","right_claim_id":"missing"}],"requirements":{"min_claim_recall":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{},"claims":[{"id":"c1","text":"one"},{"id":"c2","text":"two"}],"contradictions":[{"id":"x1","left_claim_id":"c1","right_claim_id":"c2"},{"id":"x2","left_claim_id":"c2","right_claim_id":"c1"}],"requirements":{"min_claim_recall":0}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{},"claims":[],"contradictions":[],"requirements":{}}`,
		`{"schema_version":1,"name":"x","scope":{},"matching":{},"claims":[],"contradictions":[],"requirements":{"min_claim_recall":101}} {}`,
	}
	for _, raw := range invalid {
		if _, err := ParseKnowledgeLabeledEvalManifest([]byte(raw)); err == nil {
			t.Fatalf("invalid labeled manifest accepted: %s", raw)
		}
	}
}

func TestKnowledgeLabeledEvalCitationConstraintFailsClosed(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := coverageTestChunks("citation constraint", []string{"Давление равно 10 бар"})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "claim-pressure", Kind: KnowledgeNodeClaim, Label: "Давление равно 10 бар",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	minRecall := 100.0
	wrongCitation := strings.Replace(anchor.CitationID, "-c1", "-c2", 1)
	manifest := normalizeKnowledgeLabeledEvalManifest(KnowledgeLabeledEvalManifest{
		SchemaVersion: 1, Name: "Wrong citation", Scope: KnowledgeCoverageOptions{Document: chunks[0].Provenance.SourcePath},
		Claims:       []KnowledgeLabeledExpectedClaim{{ID: "expected-pressure", Text: "Давление равно 10 бар", AcceptedCitationIDs: []string{wrongCitation}}},
		Requirements: KnowledgeLabeledEvalRequirements{MinClaimRecall: &minRecall},
	})
	report, err := store.EvaluateKnowledgeLabeledQuality(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Claims.TruePositive != 0 || report.Claims.FalsePositive != 1 || report.Claims.FalseNegative != 1 {
		t.Fatalf("wrong citation did not fail closed: %#v", report)
	}
}
