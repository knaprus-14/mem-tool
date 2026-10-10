package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type answerRevalidationProviderFunc func(context.Context, AnswerRequest) (string, error)

func (f answerRevalidationProviderFunc) Generate(ctx context.Context, request AnswerRequest) (string, error) {
	return f(ctx, request)
}

func TestAnswerKnowledgeSelectionRevalidatesAfterGeneration(t *testing.T) {
	for _, mutation := range []string{"unchanged", "outside_selection", "selected_body", "edge_endpoints", "source_deleted", "schema_retry"} {
		t.Run(mutation, func(t *testing.T) {
			store, anchor := graphStoreAndAnchor(t)
			defer store.Close()
			entries := store.GetBySourceFile(anchor.SourcePath)
			graph := KnowledgeGraph{Nodes: []KnowledgeNode{
				{ID: "answer-selected", Kind: KnowledgeNodeClaim, Label: "Selected", Body: "Original claim", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
				{ID: "answer-target", Kind: KnowledgeNodeClaim, Label: "Target", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
				{ID: "answer-outside", Kind: KnowledgeNodeClaim, Label: "Outside", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			}, Edges: []KnowledgeEdge{{ID: "answer-relation", From: "answer-selected", To: "answer-target", Kind: KnowledgeRelationSupports,
				Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}
			if err := store.UpsertKnowledgeGraph(graph); err != nil {
				t.Fatal(err)
			}
			selection := KnowledgeSelectionRequest{NodeIDs: []string{graph.Nodes[0].ID}, EdgeIDs: []string{graph.Edges[0].ID}}
			manifest, err := store.BuildKnowledgeSelectionManifest(selection)
			if err != nil || !manifest.Ready {
				t.Fatalf("manifest=%#v err=%v", manifest, err)
			}
			calls := 0
			provider := answerRevalidationProviderFunc(func(context.Context, AnswerRequest) (string, error) {
				calls++
				if mutation == "schema_retry" && calls == 1 {
					return `{"answer":"Answer requires a schema retry"}`, nil
				}
				switch mutation {
				case "selected_body", "schema_retry":
					graph.Nodes[0].Body = "A changed claim"
					err = store.UpsertKnowledgeGraph(graph)
				case "outside_selection":
					graph.Nodes[2].Body = "An unrelated edit"
					err = store.UpsertKnowledgeGraph(graph)
				case "edge_endpoints":
					graph.Edges[0].From, graph.Edges[0].To = graph.Edges[0].To, graph.Edges[0].From
					err = store.UpsertKnowledgeGraph(graph)
				case "source_deleted":
					err = store.DeleteById(entries[0].ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				return `{"claims":[{"text":"Selected answer","citations":["E1"]}]}`, nil
			})
			result, err := store.AnswerKnowledgeSelection(context.Background(), &KnowledgeSelectionAnswerService{
				Provider: provider, Config: AnswerConfig{Model: "test-chat", ContextChars: 12000},
			}, KnowledgeSelectionAnswerRequest{
				Selection: selection, ExpectedManifestDigest: manifest.Digest,
				Mode: KnowledgeSelectionModeQuestion, Question: "What is selected?",
			})
			wantCalls := 1
			if mutation == "schema_retry" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("provider calls=%d, want %d", calls, wantCalls)
			}
			if mutation == "unchanged" || mutation == "outside_selection" {
				if err != nil || result.ManifestDigest != manifest.Digest || !strings.Contains(result.Answer, "Selected answer") {
					t.Fatalf("unchanged selection rejected: result=%#v err=%v", result, err)
				}
			} else if !errors.Is(err, ErrKnowledgeSelectionChanged) || result.Answer != "" || len(result.Sources) != 0 || result.ManifestDigest != "" {
				t.Fatalf("changed selection published an answer: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestAnswerClassicMindMapBranchRevalidatesAfterGeneration(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		pinName := "without_preview_pin"
		if pinned {
			pinName = "with_preview_pin"
		}
		for _, mutation := range []string{"unchanged", "selected_body", "source_deleted", "schema_retry"} {
			t.Run(pinName+"/"+mutation, func(t *testing.T) {
				store, entries := newClassicMindMapAITestStore(t)
				anchor, err := EvidenceAnchorForEntry(entries[0], "chunk")
				if err != nil {
					t.Fatal(err)
				}
				doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{Title: "Answer map", Mode: ClassicMindMapModeHybrid, Nodes: []ClassicMindMapNodeDraft{
					{Ref: "root", Label: "Root", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual},
					{Ref: "branch", ParentRef: "root", Label: "Branch", BodyMarkdown: "Original answer", Kind: ClassicMindMapNodeFact, Origin: ClassicMindMapNodeManual,
						Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceEvidence, Evidence: &anchor}}},
				}}, "test", "answer revalidation fixture")
				if err != nil {
					t.Fatal(err)
				}
				// Exercise legacy label resolution and optional preview pins. The
				// post-generation check must still pin the already resolved IDs.
				branch := ClassicMindMapBranchRequest{MapRef: "Answer map", NodeRef: "Branch"}
				manifest, err := store.BuildClassicMindMapBranchManifest(branch)
				if err != nil {
					t.Fatal(err)
				}
				request := ClassicMindMapBranchQuestionRequest{ClassicMindMapBranchRequest: branch, Question: "What is selected?"}
				if pinned {
					request.ExpectedManifestDigest = manifest.Digest
					request.ExpectedRevision = doc.Map.Revision
					request.ExpectedDigest = doc.Digest
					request.ExpectedStateDigest = doc.StateDigest
				}
				calls := 0
				provider := answerRevalidationProviderFunc(func(context.Context, AnswerRequest) (string, error) {
					calls++
					if mutation == "schema_retry" && calls == 1 {
						return `{"answer":"Answer requires a schema retry"}`, nil
					}
					switch mutation {
					case "selected_body", "schema_retry":
						body := "Changed during generation"
						_, _, err = store.EditClassicMindMapNode(doc.Map.ID, manifest.RootNodeID, ClassicMindMapNodePatch{BodyMarkdown: &body}, doc.Map.Revision, "test", "concurrent edit")
					case "source_deleted":
						err = store.DeleteById(entries[0].ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					return `{"claims":[{"text":"Selected answer","citations":["E1"]}]}`, nil
				})
				result, err := store.AnswerClassicMindMapBranch(context.Background(), &ClassicMindMapAIService{
					Provider: provider, Config: AnswerConfig{Model: "test-chat", ContextChars: 12000},
				}, request)
				wantCalls := 1
				if mutation == "schema_retry" {
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Fatalf("provider calls=%d, want %d", calls, wantCalls)
				}
				if mutation == "unchanged" {
					if err != nil || result.ManifestDigest != manifest.Digest || !strings.Contains(result.Answer, "Selected answer") {
						t.Fatalf("unchanged branch rejected: result=%#v err=%v", result, err)
					}
				} else if err == nil || result.Answer != "" || len(result.Sources) != 0 || result.ManifestDigest != "" {
					t.Fatalf("changed branch published an answer: result=%#v err=%v", result, err)
				}
			})
		}
	}
}
