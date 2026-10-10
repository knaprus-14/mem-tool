package mem

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeRequirementWhitespaceRestoresOriginalAndRejectsOtherChanges(t *testing.T) {
	for _, tc := range []struct {
		source, quote, want string
		ok                  bool
	}{
		{"Начало. При  давлении\nне\tболее 0,6 МПа. Конец.", "При давлении не более 0,6 МПа.", "При  давлении\nне\tболее 0,6 МПа.", true},
		{"Давление  0,6 МПа.", "Давление 6 МПа.", "", false},
		{"Насос не  работает.", "Насос работает.", "", false},
		{"Насос  работает.", "насос работает.", "", false},
		{"Насос  работает. Насос\nработает.", "Насос работает.", "", false},
		{"Насос работает.", "", "", false},
	} {
		got, ok := knowledgeRequirementSourceQuote(tc.source, tc.quote)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("quote %q: got %q %v want %q %v", tc.quote, got, ok, tc.want, tc.ok)
		}
	}
}

func TestKnowledgeRequirementQuotesRoundTripAndRejectInventedQualifiers(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	chunks := coverageTestChunks("requirement revision", []string{"При  температуре не менее 5 °C допускается использовать насос, кроме резервного. Давление не более 0,6 МПа."})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	prompt, err := BuildKnowledgeExtractionPrompt("requirements", entries, 12000, 65)
	if err != nil {
		t.Fatal(err)
	}
	citation := prompt.Evidence[0].CitationID
	quote := func(text string) knowledgeRequirementQuote {
		return knowledgeRequirementQuote{CitationID: citation, Text: text}
	}
	confidence := 0.8
	p := knowledgeNodeProposal{Ref: "r", Kind: KnowledgeNodeClaim, Label: "Насос", Body: "Эта неподтверждённая интерпретация должна быть заменена", Confidence: &confidence, Citations: []string{citation}, Requirement: &knowledgeRequirementProposal{
		Statement: quote("При температуре не менее 5 °C допускается использовать насос, кроме резервного."), Conditions: []knowledgeRequirementQuote{quote("При температуре не менее 5 °C")}, Exceptions: []knowledgeRequirementQuote{quote("кроме резервного")}, Limits: []knowledgeRequirementQuote{quote("Давление не более 0,6 МПа.")},
	}}
	decode := func(p knowledgeNodeProposal) (KnowledgeExtractionResult, error) {
		raw, _ := json.Marshal(knowledgeExtractionEnvelope{Nodes: []knowledgeNodeProposal{p}})
		return DecodeKnowledgeExtraction(string(raw), prompt.Evidence)
	}
	result, err := decode(p)
	if err != nil {
		t.Fatal(err)
	}
	node := result.Graph.Nodes[0]
	if node.Status != KnowledgeStatusDraft || strings.Contains(node.Body, "неподтверждённая") || !strings.Contains(node.Body, "Исключение: кроме резервного") || len(node.Evidence) != 4 {
		t.Fatalf("lost qualified requirement: %+v", node)
	}
	if err := store.UpsertKnowledgeGraph(result.Graph); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].Body != node.Body {
		t.Fatalf("requirement did not persist: %+v", loaded)
	}
	for _, a := range loaded.Nodes[0].Evidence {
		if a.EvidenceHash != ChunkContentHash(a.Excerpt) || !strings.Contains(entries[0].Text, a.Excerpt) {
			t.Fatalf("invalid exact anchor: %+v", a)
		}
	}
	context, err := store.knowledgeMapSourceContext(t.Context(), citation)
	if err != nil || context.Text != entries[0].Text {
		t.Fatalf("full context: %+v %v", context, err)
	}
	p.Requirement.Limits[0] = quote("Давление не более 6 МПа.")
	if _, err := decode(p); err == nil {
		t.Fatal("invented numerical limit accepted")
	}
	p.Requirement.Limits[0] = quote("Давление не более 0,6 МПа.")
	p.Requirement.Limits[0].CitationID = "cite-unknown"
	if _, err := decode(p); err == nil {
		t.Fatal("unattached citation accepted")
	}
	p.Requirement.Limits = nil
	p.Requirement.Statement.Text = ""
	if _, err := decode(p); err == nil {
		t.Fatal("empty statement accepted")
	}
}

func TestKnowledgeLabeledQualityRejectsMeaningChangingMatches(t *testing.T) {
	for _, tc := range []struct {
		name, want, got string
		required        []string
	}{
		{"negation", "Применение допускается", "Применение не допускается", nil},
		{"decimal", "Рабочее давление установки равно 0,6 МПа", "Рабочее давление установки равно 6 МПа", nil},
		{"unit", "Рабочее давление установки равно 6 МПа", "Рабочее давление установки равно 6 кПа", nil},
		{"unit without space", "Рабочее давление установки равно 6МПа", "Рабочее давление установки равно 6кПа", nil},
		{"obligation", "Насос должен иметь резервное питание", "Насос может иметь резервное питание", nil},
		{"limit direction", "Рабочее давление не более 6 бар", "Рабочее давление не менее 6 бар", nil},
		{"exception", "Источники воды: водоемы, резервуары или водопроводы", "Источники воды: водоемы, резервуары или водопроводы", []string{"за исключением агрегатных АУП-ТРВ-ВД"}},
		{"condition", "Насос работает при температуре выше 5 градусов", "Насос работает при температуре ниже 5 градусов", []string{"выше 5 градусов"}},
		{"comparison", "Давление < 6 бар", "Давление > 6 бар", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := KnowledgeLabeledExpectedClaim{Text: tc.want, RequiredFragments: tc.required}
			// The exact matching title must not mask a wrong body.
			p := KnowledgeLabeledClaimPrediction{Label: tc.want, Body: tc.got}
			if score := knowledgeLabeledClaimSimilarity(expected, p); score != 0 {
				t.Fatalf("unsafe match score %v", score)
			}
		})
	}
	expected := KnowledgeLabeledExpectedClaim{Text: "Давление не более 0,6 МПа", RequiredFragments: []string{"не более"}}
	if score := knowledgeLabeledClaimSimilarity(expected, KnowledgeLabeledClaimPrediction{Body: "Давление не более 0.6 МПа"}); score < 0.8 {
		t.Fatalf("decimal separator equivalence lost: %v", score)
	}
	expected = KnowledgeLabeledExpectedClaim{Text: "Применение не допускается", Aliases: []string{"Применение запрещается"}}
	if score := knowledgeLabeledClaimSimilarity(expected, KnowledgeLabeledClaimPrediction{Body: "Применение запрещается"}); score != 1 {
		t.Fatalf("explicit reviewed alias lost: %v", score)
	}
}
