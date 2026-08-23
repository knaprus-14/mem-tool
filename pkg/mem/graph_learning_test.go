package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestKnowledgeLearningGenerationCorrectsValidatesAndSavesDrafts(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	parent := KnowledgeNode{ID: "learning-parent", Kind: KnowledgeNodeDefinition, Label: "Закон Ома", Body: "Связь тока, напряжения и сопротивления", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{parent}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{parent.ID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	provider := &selectionAnswerProvider{answers: []string{
		`{"answer":"not the required envelope"}`,
		`{"items":[{"kind":"card","prompt":"Что связывает закон Ома?","answer":"Ток, напряжение и сопротивление.","citations":["E1"]},{"kind":"question","prompt":"Объясните закон Ома.","answer":"Он описывает связь тока, напряжения и сопротивления.","citations":["E1"]}]}`,
	}}
	service := &KnowledgeSelectionAnswerService{Provider: provider, Config: AnswerConfig{Model: "local-chat", BaseURL: "http://127.0.0.1:11434", MaxTokens: 512, ContextChars: 12000, Temperature: 0.1}}
	run, err := store.GenerateKnowledgeLearningCandidates(context.Background(), service, KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 4, Focus: "основные определения"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.GenerationDigest == "" || run.CorrectionRetries != 1 || len(run.Candidates) != 2 || len(provider.requests) != 2 {
		t.Fatalf("unexpected learning run: %#v requests=%d", run, len(provider.requests))
	}
	if len(provider.requests[0].ResponseSchema) == 0 || provider.requests[0].MaxTokens < DefaultMapGenerationTokens || !strings.Contains(provider.requests[0].System, "review candidates") {
		t.Fatalf("learning request lacks strict structured contract: %#v", provider.requests[0])
	}
	for _, candidate := range run.Candidates {
		if len(candidate.Citations) != 1 || candidate.Citations[0] != anchor.CitationID || len(candidate.Sources) != 1 || candidate.Sources[0].Page != anchor.Page {
			t.Fatalf("candidate provenance is not pinned: %#v", candidate)
		}
	}
	if _, err := store.db.Exec(`UPDATE knowledge_learning_runs SET model = 'changed' WHERE id = ?`, run.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("learning run audit could be changed: %v", err)
	}

	saved, err := store.SaveKnowledgeLearningCandidates(KnowledgeLearningSaveRequest{RunID: run.ID, CandidateIndexes: []int{1, 0}, Author: " Руслан ", Comment: "Проверить перед обучением"})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Nodes) != 2 || len(saved.Edges) != 2 || saved.Record.Author != "Руслан" || saved.Record.GenerationDigest != run.GenerationDigest {
		t.Fatalf("unexpected learning save: %#v", saved)
	}
	for _, node := range saved.Nodes {
		if node.Status != KnowledgeStatusDraft || node.Origin != KnowledgeOriginGenerated || len(node.Evidence) != 1 {
			t.Fatalf("learning node is not a grounded generated draft: %#v", node)
		}
		if node.Kind == KnowledgeNodeQuestion && !strings.HasPrefix(node.Body, "Ожидаемый ответ для проверки:") {
			t.Fatalf("open question lost its expected answer: %#v", node)
		}
	}
	for _, edge := range saved.Edges {
		if edge.To != parent.ID || edge.Status != KnowledgeStatusDraft || edge.Origin != KnowledgeOriginGenerated ||
			(edge.Kind != KnowledgeRelationDerivedFrom && edge.Kind != KnowledgeRelationAsks) {
			t.Fatalf("learning provenance edge is invalid: %#v", edge)
		}
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_learning_saves WHERE id = ?`, saved.Record.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("learning save audit could be deleted: %v", err)
	}
	graphBeforeRetry, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveKnowledgeLearningCandidates(KnowledgeLearningSaveRequest{RunID: run.ID, CandidateIndexes: []int{0, 1}, Author: "Руслан"}); err == nil {
		t.Fatal("the same learning subset was saved twice")
	}
	graphAfterRetry, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(graphAfterRetry.Nodes) != len(graphBeforeRetry.Nodes) || len(graphAfterRetry.Edges) != len(graphBeforeRetry.Edges) {
		t.Fatalf("failed duplicate save left partial objects: before=%#v after=%#v", graphBeforeRetry, graphAfterRetry)
	}
}

func TestKnowledgeLearningRejectsUnknownCitationAndCloudKeepsPromptContract(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	node := KnowledgeNode{ID: "learning-validation", Kind: KnowledgeNodeClaim, Label: "Факт", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{node.ID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	invalid := `{"items":[{"kind":"card","prompt":"Вопрос","answer":"Ответ","citations":["E99"]}]}`
	provider := &selectionAnswerProvider{answers: []string{invalid, invalid}}
	service := &KnowledgeSelectionAnswerService{Provider: provider, Config: AnswerConfig{Model: "local-chat", BaseURL: "http://127.0.0.1:11434", ContextChars: 12000}}
	if _, err := store.GenerateKnowledgeLearningCandidates(context.Background(), service, KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 2}); err == nil || !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("unknown citation was accepted: %v", err)
	}
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_learning_runs`).Scan(&runCount); err != nil || runCount != 0 {
		t.Fatalf("rejected generation was persisted: count=%d err=%v", runCount, err)
	}

	cloudProvider := &selectionAnswerProvider{answers: []string{`{"items":[{"kind":"card","prompt":"Вопрос","answer":"Ответ","citations":["E1"]}]}`}}
	cloudService := &KnowledgeSelectionAnswerService{Provider: cloudProvider, Config: AnswerConfig{Model: "gemma4:cloud", BaseURL: "http://127.0.0.1:11434", ContextChars: 12000}}
	if _, err := store.GenerateKnowledgeLearningCandidates(context.Background(), cloudService, KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if len(cloudProvider.requests) != 1 || len(cloudProvider.requests[0].ResponseSchema) != 0 {
		t.Fatalf("cloud model received unsupported response schema: %#v", cloudProvider.requests)
	}
}

func TestKnowledgeLearningSaveFailsClosedAfterSelectionChanges(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	node := KnowledgeNode{ID: "learning-stale", Kind: KnowledgeNodeClaim, Label: "До изменения", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{node.ID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	provider := &selectionAnswerProvider{answers: []string{`{"items":[{"kind":"question","prompt":"Что изменилось?","answer":"Ответ","citations":["E1"]}]}`}}
	service := &KnowledgeSelectionAnswerService{Provider: provider, Config: AnswerConfig{Model: "local-chat", BaseURL: "http://127.0.0.1:11434", ContextChars: 12000}}
	run, err := store.GenerateKnowledgeLearningCandidates(context.Background(), service, KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	node.Label = "После изменения"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{node}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveKnowledgeLearningCandidates(KnowledgeLearningSaveRequest{RunID: run.ID, CandidateIndexes: []int{0}, Author: "Руслан"}); !errors.Is(err, ErrKnowledgeSelectionNotCurrent) {
		t.Fatalf("stale learning run was saved: %v", err)
	}
}
