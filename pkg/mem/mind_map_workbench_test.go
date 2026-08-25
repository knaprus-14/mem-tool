package mem

import (
	"context"
	"strings"
	"testing"
)

func TestClassicMindMapTemplatesCreateDeterministicEditableStructure(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	templates := ListClassicMindMapTemplates()
	if len(templates) < 5 || templates[0].ID == "" {
		t.Fatalf("templates=%#v", templates)
	}
	doc, err := store.CreateClassicMindMapFromTemplate(ClassicMindMapTemplateRequest{TemplateID: "study", Title: "Радиотехника"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Map.Title != "Радиотехника" || doc.Map.Mode != ClassicMindMapModeManual || len(doc.Nodes) != 6 || doc.Map.Revision != 1 {
		t.Fatalf("unexpected template document: %#v", doc)
	}
	if doc.Nodes[0].ID != doc.Map.RootNodeID || doc.Nodes[0].Label != "Радиотехника" {
		t.Fatalf("unexpected root: %#v", doc.Nodes[0])
	}
	if _, err := store.CreateClassicMindMapFromTemplate(ClassicMindMapTemplateRequest{TemplateID: "unknown", Title: "X"}, "test"); err == nil {
		t.Fatal("unknown template was accepted")
	}
}

func TestClassicMindMapBranchManifestAnswerAndStudyPackStayGrounded(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	anchor, err := EvidenceAnchorForEntry(entries[0], "chunk")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{Title: "Учебная карта", Mode: ClassicMindMapModeHybrid, Nodes: []ClassicMindMapNodeDraft{
		{Ref: "root", Label: "Корень", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual},
		{Ref: "branch", ParentRef: "root", Label: "Ветка", Summary: "Краткий ответ", Kind: ClassicMindMapNodeFact, Origin: ClassicMindMapNodeManual,
			Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceEvidence, Evidence: &anchor}}},
		{Ref: "question", ParentRef: "branch", Label: "Что проверить?", BodyMarkdown: "Проверяемый ответ", Kind: ClassicMindMapNodeQuestion, Origin: ClassicMindMapNodeManual},
	}}, "test", "workbench fixture")
	if err != nil {
		t.Fatal(err)
	}
	branch := ClassicMindMapBranchRequest{MapRef: doc.Map.ID, NodeRef: "Ветка", ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest}
	first, err := store.BuildClassicMindMapBranchManifest(branch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BuildClassicMindMapBranchManifest(branch)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == "" || first.Digest != second.Digest || len(first.NodeIDs) != 2 || len(first.Evidence) != 1 || first.Evidence[0].CitationID != anchor.CitationID {
		t.Fatalf("unexpected branch manifest: %#v", first)
	}
	provider := &selectionAnswerProvider{answers: []string{`{"claims":[{"text":"Проверенный ответ по ветви","citations":["E1"]}]}`}}
	answer, err := store.AnswerClassicMindMapBranch(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapBranchQuestionRequest{
		ClassicMindMapBranchRequest: branch, Question: "Что сказано в источнике?", ExpectedManifestDigest: first.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Answer, "Проверенный ответ по ветви") || answer.EvidenceCount != 1 || len(answer.Sources) != 1 || answer.ManifestDigest != first.Digest {
		t.Fatalf("unexpected answer: %#v", answer)
	}
	pack, err := store.BuildClassicMindMapStudyPack(branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Cards) != 2 || !strings.Contains(pack.Markdown, "Что важно знать") || !strings.Contains(pack.Markdown, anchor.SourcePath) || pack.ManifestDigest != first.Digest {
		t.Fatalf("unexpected study pack: %#v", pack)
	}
}

func TestCompareClassicMindMapsReportsChangedAddedAndMovedNodes(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	left, err := store.CreateClassicMindMapFromTemplate(ClassicMindMapTemplateRequest{TemplateID: "decision", Title: "Выбор"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	right, err := store.DuplicateClassicMindMap(left.Map.ID, "Выбор 2")
	if err != nil {
		t.Fatal(err)
	}
	var criteria, variants ClassicMindMapNode
	for _, node := range right.Nodes {
		switch node.Label {
		case "Критерии":
			criteria = node
		case "Варианты":
			variants = node
		}
	}
	newSummary := "Стоимость, безопасность, сроки"
	right, _, err = store.EditClassicMindMapNode(right.Map.ID, criteria.ID, ClassicMindMapNodePatch{Summary: &newSummary}, right.Map.Revision, "test", "change")
	if err != nil {
		t.Fatal(err)
	}
	right, _, err = store.MoveClassicMindMapNode(right.Map.ID, variants.ID, criteria.ID, 0, right.Map.Revision, "test", "move")
	if err != nil {
		t.Fatal(err)
	}
	right, _, err = store.AddClassicMindMapNode(right.Map.ID, right.Map.RootNodeID, "Стоимость", -1, ClassicMindMapNodeFact, "Бюджет", "", right.Map.Revision, "test", "add")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CompareClassicMindMaps(ClassicMindMapCompareRequest{LeftMapRef: left.Map.ID, RightMapRef: right.Map.ID, LeftExpectedDigest: left.Digest, RightExpectedDigest: right.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added < 1 || result.Changed < 1 || result.Moved < 1 || result.Digest == "" {
		t.Fatalf("unexpected comparison: %#v", result)
	}
	repeated, err := store.CompareClassicMindMaps(ClassicMindMapCompareRequest{LeftMapRef: left.Map.ID, RightMapRef: right.Map.ID})
	if err != nil || repeated.Digest != result.Digest || len(repeated.Items) != len(result.Items) {
		t.Fatalf("comparison is not deterministic: first=%#v repeated=%#v err=%v", result, repeated, err)
	}
}
