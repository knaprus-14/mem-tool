package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func classicMindMapAISynthesisFixture(t *testing.T, action ClassicMindMapAIAction) (*Store, *ClassicMindMapAIService, ClassicMindMapAIPreviewRequest, *classicMindMapAIFakeProvider) {
	t.Helper()
	store, _ := newClassicMindMapAITestStore(t)
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Text = strings.Repeat(fmt.Sprintf("Условие %d. ", i), 300)
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("synthesis fixture revision")
	}
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	request := ClassicMindMapAIPreviewRequest{Action: action, Prompt: "Согласуй главные темы", Title: "Карта условий", Scope: ClassicMindMapAIScope{EntryIDs: []int64{entries[0].ID, entries[1].ID}}}
	if action == ClassicMindMapAIExpand {
		doc, err := store.ImportClassicMindMap(CreateClassicMindMapDraft("Исходная карта", "Ручное описание"), "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		request.MapRef, request.NodeRef, request.ExpectedRevision = doc.Map.ID, doc.Map.RootNodeID, doc.Map.Revision
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{
		`{"nodes":[{"ref":"r","parent_ref":"","label":"Порог","summary":"Только при T > 10","body_markdown":"Не применять при T <= 10","kind":"topic","citations":["E1"]},{"ref":"c","parent_ref":"r","label":"Формула I","summary":"Первое условие","body_markdown":"I = U / R","kind":"fact","citations":["E1"]}]}`,
		`{"nodes":[{"ref":"r","parent_ref":"","label":"Порог","summary":"Только при T <= 10","body_markdown":"Не применять при T > 10","kind":"topic","citations":["E2"]},{"ref":"c","parent_ref":"r","label":"Формула i","summary":"Второе условие","body_markdown":"i = u / r","kind":"fact","citations":["E2"]}]}`,
		`{"groups":[{"title":"Разные режимы и условия","members":["N3","N1"]}]}`,
	}}
	service := classicMindMapAITestService(store, provider)
	service.Config.ContextChars = 6500
	return store, service, request, provider
}

func TestClassicMindMapAISynthesisPreservesSubtreesAndPinnedPreview(t *testing.T) {
	for _, action := range []ClassicMindMapAIAction{ClassicMindMapAINewMap, ClassicMindMapAIExpand} {
		t.Run(string(action), func(t *testing.T) {
			store, service, request, provider := classicMindMapAISynthesisFixture(t, action)
			phases := []string{}
			preview, err := PrepareClassicMindMapAIPreview(context.Background(), service, request, func(p ClassicMindMapAIProgress) { phases = append(phases, p.Phase) })
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.requests) != 3 || preview.BatchCount != 2 || preview.Synthesis == nil || preview.Synthesis.InputProposals != 4 || preview.Synthesis.Groups != 1 {
				t.Fatalf("calls=%d preview=%+v", len(provider.requests), preview)
			}
			if !strings.Contains(strings.Join(phases, ","), "synthesizing") {
				t.Fatal("missing synthesis phase")
			}
			for _, label := range []string{"Формула I", "Формула i", "Первое условие", "Второе условие"} {
				if !strings.Contains(provider.requests[2].Prompt, label) {
					t.Fatalf("global model omitted %q", label)
				}
			}
			rows, err := store.db.Query(`SELECT result_json FROM mind_map_generation_batches WHERE run_id=? ORDER BY ordinal`, preview.RunID)
			if err != nil {
				t.Fatal(err)
			}
			original := map[string]ClassicMindMapAIProposal{}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var batch []ClassicMindMapAIProposal
				if err := json.Unmarshal([]byte(raw), &batch); err != nil {
					t.Fatal(err)
				}
				for _, p := range batch {
					original[p.ID] = p
				}
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if len(original) != 4 {
				t.Fatalf("original proposals=%d", len(original))
			}
			seen := map[string]bool{}
			for _, p := range preview.Proposals {
				if p.ParentProposalID != "" && !seen[p.ParentProposalID] {
					t.Fatal("output is not parent-before-child")
				}
				seen[p.ID] = true
				before, exists := original[p.ID]
				if !exists {
					if p.SynthesisRole == "" || len(p.Evidence) != 0 || p.Summary != "" || p.Reason != classicMindMapAISynthesisNote {
						t.Fatal("organizational node pretends to be a sourced claim")
					}
					continue
				}
				if before.ParentProposalID == "" {
					before.ParentProposalID = p.ParentProposalID
				}
				if !reflect.DeepEqual(before, p) {
					t.Fatalf("original content/conditions/structure/evidence changed: before=%+v after=%+v", before, p)
				}
				delete(original, p.ID)
			}
			if len(original) != 0 {
				t.Fatal("dropped original proposals")
			}
			loaded, err := store.LoadClassicMindMapAIPreview(preview.RunID)
			if err != nil || !reflect.DeepEqual(preview.Synthesis, loaded.Synthesis) || preview.ProposalDigest != loaded.ProposalDigest {
				t.Fatalf("durable synthesis lost: %+v %v", loaded, err)
			}
			http := classicMindMapAIHTTPPreviewFrom(loaded, loaded.BatchCount)
			if !reflect.DeepEqual(http.Synthesis, preview.Synthesis) {
				t.Fatal("HTTP lost synthesis metadata")
			}
			var mapCount int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&mapCount); err != nil {
				t.Fatal(err)
			}
			wantMaps := 0
			if action == ClassicMindMapAIExpand {
				wantMaps = 1
			}
			if mapCount != wantMaps {
				t.Fatal("preview mutated maps")
			}
			leaf := preview.Proposals[len(preview.Proposals)-1]
			if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID, ProposalIDs: []string{leaf.ID}}); err == nil {
				t.Fatal("applied child without grouped parent")
			}
			if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID, ExpectedPreviewDigest: "sha256:" + strings.Repeat("0", 64)}); !errors.Is(err, ErrClassicMindMapAIChanged) {
				t.Fatalf("digest mismatch not rejected: %v", err)
			}
			applied, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID, ExpectedPreviewDigest: preview.ProposalDigest})
			if err != nil {
				t.Fatal(err)
			}
			wantNodes := len(preview.Proposals)
			if action == ClassicMindMapAIExpand {
				wantNodes++
			}
			if len(applied.Document.Nodes) != wantNodes {
				t.Fatalf("nodes=%d want=%d", len(applied.Document.Nodes), wantNodes)
			}
			thresholds := 0
			for _, n := range applied.Document.Nodes {
				if n.Summary == classicMindMapAISynthesisNote || n.BodyMarkdown == classicMindMapAISynthesisNote {
					t.Fatal("preview organizational metadata became learning-card content")
				}
				if n.Label == "Порог" {
					thresholds++
				}
			}
			if thresholds != 2 {
				t.Fatal("same-title nodes merged despite different conditions")
			}
		})
	}
}

func TestClassicMindMapAISynthesisRejectsIncompleteOrRewritingPlans(t *testing.T) {
	roots := map[string]string{"N1": "one", "N3": "two"}
	for _, bad := range []string{
		`{"groups":[{"title":"T","members":["N1"]}]}`,
		`{"groups":[{"title":"T","members":["N1","N3","N1"]}]}`,
		`{"groups":[{"title":"T","members":["N1","N3","N2"]}]}`,
		`{"groups":[{"title":"T","members":["N1","N3"],"parent_ref":"N1"}]}`,
		`{"groups":[{"title":"T","members":["N1","N3"],"citations":["E1"]}]}`,
		`{"groups":[{"title":"T","members":["N1","N3"]}],"nodes":[]}`,
		`{"groups":[{"title":" ","members":["N1","N3"]}]}`,
		`{"groups":[{"title":"T","members":[]}]}`,
		`{"groups":[{"title":"T","members":["N1","N3"]}]} {}`,
	} {
		if _, err := decodeClassicMindMapAISynthesisPlan(bad, roots, 2); err == nil {
			t.Fatalf("accepted unsafe plan: %s", bad)
		}
	}
	store, service, request, provider := classicMindMapAISynthesisFixture(t, ClassicMindMapAIExpand)
	provider.answers[2] = `{"groups":[{"title":"T","members":["N1"]}]}`
	if _, err := PrepareClassicMindMapAIPreview(context.Background(), service, request, nil); err == nil {
		t.Fatal("incomplete synthesis published")
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM mind_map_generation_runs ORDER BY created DESC LIMIT 1`).Scan(&status); err != nil || status != string(ClassicMindMapAIFailed) {
		t.Fatalf("status=%s err=%v", status, err)
	}
	var previews int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_previews`).Scan(&previews); err != nil || previews != 0 {
		t.Fatal("failed synthesis left applyable preview")
	}
	doc, err := store.LoadClassicMindMap(request.MapRef)
	if err != nil || len(doc.Nodes) != 1 || doc.Map.Revision != request.ExpectedRevision {
		t.Fatalf("failed synthesis mutated map: %+v %v", doc, err)
	}
}

func TestClassicMindMapAISynthesisContextLimitIsExplicitAndKeepsEveryDescriptor(t *testing.T) {
	input := []ClassicMindMapAIProposal{
		{ID: "a", Label: "First", Kind: ClassicMindMapNodeTopic, Summary: strings.Repeat("Я", 500)},
		{ID: "b", Label: "Last", Kind: ClassicMindMapNodeTopic, Summary: "tail summary"},
	}
	request := ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAIExpand, Prompt: "Group"}
	answer, _, _, err := buildClassicMindMapAISynthesisRequest(AnswerConfig{Model: "fake", ContextChars: 10000}, request, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Prompt, `"summary_truncated":true`) || !strings.Contains(answer.Prompt, "tail summary") {
		t.Fatal("truncation unmarked or tail descriptor omitted")
	}
	provider := &classicMindMapAIFakeProvider{}
	_, err = combineClassicMindMapAIProposals(context.Background(), provider, AnswerConfig{Model: "fake", ContextChars: 100}, request, [][]ClassicMindMapAIProposal{{input[0]}, {input[1]}}, nil)
	if !errors.Is(err, ErrClassicMindMapAIResourceLimit) || !strings.Contains(err.Error(), "всех 2") || len(provider.requests) != 0 {
		t.Fatalf("limit was silent/partial: %v calls=%d", err, len(provider.requests))
	}
}

func TestClassicMindMapAISynthesisCompatibilityAndHostOnlyRoles(t *testing.T) {
	provider := &classicMindMapAIFakeProvider{}
	cfg := AnswerConfig{Model: "fake", ContextChars: 10000}
	for _, action := range []ClassicMindMapAIAction{ClassicMindMapAINewMap, ClassicMindMapAIExpand, ClassicMindMapAIFill} {
		batches := [][]ClassicMindMapAIProposal{{{ID: "one", Label: "One"}}, nil}
		if action == ClassicMindMapAIFill {
			batches = append(batches, []ClassicMindMapAIProposal{{ID: "two", Summary: "Extra"}})
		}
		if _, err := combineClassicMindMapAIProposals(context.Background(), provider, cfg, ClassicMindMapAIPreviewRequest{Action: action}, batches, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatal("single-batch/fill contract changed")
	}
	batch := classicMindMapAIBatch{ID: "test"}
	_, _, err := decodeClassicMindMapAIResponse(`{"nodes":[{"ref":"r","parent_ref":"","label":"X","summary":"","body_markdown":"","kind":"topic","citations":[],"synthesis_role":"theme"}]}`, ClassicMindMapAINewMap, batch, false)
	if err == nil {
		t.Fatal("model can forge host-only organizational role")
	}
}

func TestClassicMindMapAISynthesisCancellationCannotPublish(t *testing.T) {
	store, service, request, _ := classicMindMapAISynthesisFixture(t, ClassicMindMapAIExpand)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := PrepareClassicMindMapAIPreview(ctx, service, request, func(p ClassicMindMapAIProgress) {
		if p.Phase == "synthesizing" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM mind_map_generation_runs ORDER BY created DESC LIMIT 1`).Scan(&status); err != nil || status != string(ClassicMindMapAICancelled) {
		t.Fatalf("status=%s err=%v", status, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_previews`).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled synthesis has preview")
	}
}

func TestWebSourceMapMultiBatchSynthesisAutoPublishAndUndo(t *testing.T) {
	dir, store, input := sourceMapFixture(t)
	var sourcePath, documentID string
	if err := store.db.QueryRow(`SELECT source_path,document_id FROM entries LIMIT 1`).Scan(&sourcePath, &documentID); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Text = strings.Repeat(fmt.Sprintf("Условие %d. ", i), 300)
		chunks[i].Provenance.SourcePath, chunks[i].Provenance.DocumentID = sourcePath, documentID
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("web synthesis fixture revision")
	}
	if err := store.ReplaceDocumentChunks(sourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.ContextChars = 6500
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", input)
	if err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{
		sourceMapResponse,
		strings.ReplaceAll(sourceMapResponse, `"E1"`, `"E2"`),
		`{"groups":[{"title":"Общая тема разных частей","members":["N1","N3"]}]}`,
	}}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
		t.Fatalf("preview: %v %v", worked, err)
	}
	review, err := ReadWebMapAIReviewIn(ctx, dir, "alice", task.MapID, task.ID)
	if err != nil || review.Synthesis == nil || review.Synthesis.Groups != 1 || review.Synthesis.InputProposals != 4 {
		t.Fatalf("web review lost grouping: %+v %v", review, err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("calls=%d, want two local and one global", len(provider.requests))
	}
	for _, req := range provider.requests {
		if strings.Contains(req.Prompt, "private-ai-folder") {
			t.Fatal("grouping leaked a private source path")
		}
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
		t.Fatalf("autopublish: %v %v", worked, err)
	}
	doc, err := store.LoadClassicMindMap(task.MapID)
	if err != nil || len(doc.Nodes) != 6 || doc.Map.Revision != 2 {
		t.Fatalf("grouped map publication: nodes=%d rev=%d err=%v", len(doc.Nodes), doc.Map.Revision, err)
	}
	if len(provider.requests) != 3 {
		t.Fatal("publication called model again")
	}
	history, err := ListWebMapHistoryIn(ctx, dir, task.MapID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UndoWebMapIn(ctx, dir, "alice", task.MapID, WebMapUndo{Revision: history.Revision, ChangeID: history.UndoID, RequestID: strings.Repeat("f", 32)}); err != nil {
		t.Fatal(err)
	}
	doc, err = store.LoadClassicMindMap(task.MapID)
	if err != nil || len(doc.Nodes) != 1 {
		t.Fatalf("grouping undo incomplete: %+v %v", doc, err)
	}
}
