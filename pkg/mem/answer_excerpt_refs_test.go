package mem

import (
	"context"
	"strings"
	"testing"
)

func TestGroundedAnswerDistinctExcerptsUseExactReferences(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	full, err := groundedEvidenceForSelectionAnchor(anchor)
	if err != nil {
		t.Fatal(err)
	}
	short := full
	short.Text, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	prompt, err := buildGroundedPromptFromEvidence("Compare the excerpts", []GroundedEvidence{full, short}, 12000)
	if err != nil || len(prompt.Evidence) != 2 || prompt.Evidence[0].Text != full.Text || prompt.Evidence[1].Text != short.Text {
		t.Fatalf("excerpts changed or discarded: prompt=%#v err=%v", prompt, err)
	}
	for i, item := range prompt.Evidence {
		if item.CitationID != anchor.CitationID || item.EvidenceHash == "" || item.EvidenceRef == "" || (i == 1 && item.EvidenceRef == prompt.Evidence[0].EvidenceRef) {
			t.Fatalf("externally defined citation changed or exact reference missing: %#v", prompt.Evidence)
		}
	}
	request, err := GroundedAnswerSchemaRequest(AnswerRequest{Model: "fixture", Prompt: prompt.User}, prompt.Evidence)
	if err != nil || !strings.Contains(string(request.ResponseSchema), `"enum":["E1","E2"]`) {
		t.Fatalf("schema=%s err=%v", request.ResponseSchema, err)
	}
	raw := `{"claims":[{"text":"Both excerpts","citations":["E2","E1"]}]}`
	for _, validate := range []func(string, []GroundedEvidence) AnswerValidation{ValidateGroundedAnswer, ValidateGroundedSchemaAnswer} {
		got := validate(raw, prompt.Evidence)
		if got.Rejected || len(got.Used) != 2 || got.Used[0].Text != short.Text || got.Used[1].Text != full.Text || got.Answer != "Both excerpts [1, стр. 4] [2, стр. 4]" {
			t.Fatalf("distinct references collapsed: %#v", got)
		}
		got = validate(`{"claims":[{"text":"Ambiguous","citations":["`+anchor.CitationID+`"]}]}`, prompt.Evidence)
		if !got.Rejected || !strings.Contains(got.Reason, "ambiguous") {
			t.Fatalf("ambiguous long citation accepted: %#v", got)
		}
	}
	duplicate := append([]GroundedEvidence(nil), prompt.Evidence...)
	duplicate[1] = duplicate[0]
	duplicate[1].EvidenceRef = "E2"
	if got := ValidateGroundedAnswer(raw, duplicate); !got.Rejected || !strings.Contains(got.Reason, "duplicate") {
		t.Fatalf("same excerpt assigned twice: %#v", got)
	}
	if _, err = GroundedAnswerSchemaRequest(AnswerRequest{}, duplicate); err == nil {
		t.Fatal("duplicate excerpt accepted by schema")
	}
	if _, err = buildGroundedPromptFromEvidence("Question", []GroundedEvidence{full, full}, 12000); err == nil {
		t.Fatal("duplicate excerpt accepted by prompt")
	}
	withoutRefs := append([]GroundedEvidence(nil), prompt.Evidence...)
	for i := range withoutRefs {
		withoutRefs[i].EvidenceRef = ""
	}
	if _, err = GroundedAnswerSchemaRequest(AnswerRequest{}, withoutRefs); err == nil {
		t.Fatal("ambiguous citations without excerpt refs accepted by schema")
	}
	if got := ValidateGroundedAnswer(`{"claims":[{"text":"Unique legacy citation","citations":["`+anchor.CitationID+`","E1"]}]}`, prompt.Evidence[:1]); got.Rejected || len(got.Used) != 1 {
		t.Fatalf("unique long citation compatibility lost: %#v", got)
	}
}

func TestClassicAndWebBranchAnswersPreserveDistinctExcerpts(t *testing.T) {
	dir, store, mapID, rootID, _, anchor, _ := webMapGraphEvidenceFixture(t)
	short := anchor
	short.Excerpt, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	if _, _, err := store.AttachClassicMindMapEvidence(mapID, rootID, short, 3, "test", "another excerpt of same chunk"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls := 0
	service := &ClassicMindMapAIService{Config: AnswerConfig{Model: "test-chat", ContextChars: 12000}, Provider: answerRevalidationProviderFunc(func(_ context.Context, request AnswerRequest) (string, error) {
		calls++
		if !strings.Contains(request.Prompt, `"evidence_ref": "E1"`) || !strings.Contains(request.Prompt, `"evidence_ref": "E2"`) {
			t.Fatal("prompt lost an excerpt reference")
		}
		return `{"claims":[{"text":"Both excerpts","citations":["E1","E2"]}]}`, nil
	})}
	manifest, err := store.BuildClassicMindMapBranchManifest(ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: rootID})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := store.AnswerClassicMindMapBranch(ctx, service, ClassicMindMapBranchQuestionRequest{ClassicMindMapBranchRequest: ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: rootID}, Question: "Compare", ExpectedManifestDigest: manifest.Digest})
	if err != nil || len(cli.Sources) != 2 || cli.Sources[0].EvidenceHash == cli.Sources[1].EvidenceHash {
		t.Fatalf("CLI answer=%#v err=%v", cli, err)
	}
	web, err := ReadWebMapBranchIn(ctx, dir, mapID, rootID, 4)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := answerWebMapBranchWithService(ctx, dir, mapID, rootID, WebMapBranchInput{Revision: 4, Question: "Compare", Manifest: web.Manifest.Digest}, service)
	if err != nil || len(answer.Sources) != 2 || calls != 2 {
		t.Fatalf("web answer=%#v calls=%d err=%v", answer, calls, err)
	}
	for _, source := range answer.Sources {
		if source.CitationID != anchor.CitationID || strings.Contains(source.SourcePath, "private-ai-folder") {
			t.Fatalf("citation changed or path disclosed: %#v", source)
		}
	}
}
