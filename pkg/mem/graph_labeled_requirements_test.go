package mem

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeLabeledRequirementKindsAreExplicitAndBackwardCompatible(t *testing.T) {
	s, anchor := graphStoreAndAnchor(t)
	defer s.Close()
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "fact", Kind: KnowledgeNodeClaim, Label: "Расход воды", Body: "Расход равен 10 литрам", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "limit", Kind: KnowledgeNodeConstraint, Label: "Предел давления", Body: "Давление не более 6 бар", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "step", Kind: KnowledgeNodeProcedure, Label: "Проверка", Body: "Проверить насос перед пуском", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "note", Kind: KnowledgeNodeNote, Label: "Заметка", Body: "Личное мнение", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}}
	if err := s.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	recall := 100.0
	m := KnowledgeLabeledEvalManifest{SchemaVersion: 1, Name: "requirements", Claims: []KnowledgeLabeledExpectedClaim{{ID: "expected-fact", Text: graph.Nodes[0].Body}}, Requirements: KnowledgeLabeledEvalRequirements{MinClaimRecall: &recall}}
	legacy, err := s.EvaluateKnowledgeLabeledQuality(m)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Passed || legacy.Claims.Predicted != 1 {
		t.Fatalf("legacy benchmark changed: %+v", legacy.Claims)
	}
	m.Matching.ClaimKinds = []KnowledgeNodeKind{KnowledgeNodeProcedure, KnowledgeNodeClaim, KnowledgeNodeConstraint}
	for _, n := range graph.Nodes[1:3] {
		m.Claims = append(m.Claims, KnowledgeLabeledExpectedClaim{ID: "expected-" + n.ID, Text: n.Body})
	}
	encoded, _ := json.Marshal(m)
	parsed, err := ParseKnowledgeLabeledEvalManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.EvaluateKnowledgeLabeledQuality(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.Claims.TruePositive != 3 || report.Claims.Predicted != 3 {
		t.Fatalf("typed requirements not evaluated: %+v", report.Claims)
	}
	if report.SnapshotDigest == legacy.SnapshotDigest {
		t.Fatal("kind selection is not pinned")
	}
	for _, match := range report.ClaimMatches {
		if match.Prediction.Kind == "" {
			t.Fatal("prediction lost its kind")
		}
	}
	for _, bad := range [][]KnowledgeNodeKind{{}, {KnowledgeNodeNote}, {KnowledgeNodeClaim, KnowledgeNodeClaim}} {
		m.Matching.ClaimKinds = bad
		encoded, _ = json.Marshal(m)
		// omitempty erases an explicit empty slice; exercise it as real JSON.
		if len(bad) == 0 {
			encoded = []byte(strings.Replace(string(encoded), `"matching":{`, `"matching":{"claim_kinds":[],`, 1))
		}
		if _, err := ParseKnowledgeLabeledEvalManifest(encoded); err == nil {
			t.Fatalf("invalid kinds accepted: %v", bad)
		}
	}
}

func TestKnowledgeLabeledEvalReadsQualifiersAfter4096Characters(t *testing.T) {
	s, anchor := graphStoreAndAnchor(t)
	defer s.Close()
	expected := strings.Repeat("Насос работает. ", 250)
	body := expected + strings.Repeat("Насос работает. ", 60) + "Насос не работает."
	node := KnowledgeNode{ID: "long-requirement", Kind: KnowledgeNodeClaim, Label: "Насос", Body: body, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
	if err := s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	recall := 100.0
	m := KnowledgeLabeledEvalManifest{SchemaVersion: 1, Name: "long body", Claims: []KnowledgeLabeledExpectedClaim{{ID: "expected", Text: expected}}, Requirements: KnowledgeLabeledEvalRequirements{MinClaimRecall: &recall}}
	report, err := s.EvaluateKnowledgeLabeledQuality(m)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Claims.TruePositive != 0 || len(report.FalsePositiveClaims) != 1 {
		t.Fatalf("tail negation ignored: %+v", report.Claims)
	}
	if report.FalsePositiveClaims[0].Body != body {
		t.Fatal("report hides the disqualifying tail")
	}
}

func TestKnowledgeLabeledRequirementRepeatedQualifier(t *testing.T) {
	expected := KnowledgeLabeledExpectedClaim{Text: "Водоснабжение должно обеспечиваться по 1 категории надежности по СП 31.13330.", RequiredFragments: []string{"по 1 категории надежности", "СП 31.13330"}}
	body := "Требование: Водоснабжение должно обеспечиваться по 1 категории надежности по  СП 31.13330."
	prediction := KnowledgeLabeledClaimPrediction{Body: body + "\n\nОграничение: по 1 категории надежности"}
	if score := knowledgeLabeledClaimSimilarity(expected, prediction); score != 1 {
		t.Fatalf("redundant quoted limit changed score: %v", score)
	}
	for _, tail := range []string{"Ограничение: по 2 категории надежности", "Исключение: кроме складов", "Условие: если насос включен", "Ограничение: Водоснабжение не требуется"} {
		prediction.Body = body + "\n\n" + tail
		if score := knowledgeLabeledClaimSimilarity(expected, prediction); score != 0 {
			t.Fatalf("additional qualifier hidden: %s (%v)", tail, score)
		}
	}
	for _, tail := range []string{"Условие: насос включен", "Дополнение: по 1 категории надежности", "Ограничение: категории надеж", "Ограничение: "} {
		original := body + "\n\n" + tail
		if got := knowledgeLabeledRequirementScoringText(original); got != original {
			t.Fatalf("nonredundant or malformed tail discarded: %q", tail)
		}
	}
}
