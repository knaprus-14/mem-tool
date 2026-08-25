package mem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type classicMindMapAIFakeProvider struct {
	answers  []string
	errors   []error
	requests []AnswerRequest
}

func (provider *classicMindMapAIFakeProvider) Generate(_ context.Context, request AnswerRequest) (string, error) {
	provider.requests = append(provider.requests, request)
	index := len(provider.requests) - 1
	if index < len(provider.errors) && provider.errors[index] != nil {
		return "", provider.errors[index]
	}
	if index >= len(provider.answers) {
		return "", errors.New("fake classic mind map AI response is missing")
	}
	return provider.answers[index], nil
}

func newClassicMindMapAITestStore(t *testing.T) (*Store, []Entry) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if len(entries) != len(chunks) {
		t.Fatalf("entries=%d, want %d", len(entries), len(chunks))
	}
	return store, entries
}

func classicMindMapAITestService(store *Store, provider AnswerProvider) *ClassicMindMapAIService {
	return &ClassicMindMapAIService{
		Store: store, Provider: provider,
		Config: AnswerConfig{Model: "fake-instruct", MaxTokens: DefaultMapGenerationTokens, ContextChars: DefaultAnswerContextChars},
	}
}

func TestClassicMindMapAINewMapPreviewIsNonMutatingAndApplyIsAtomic(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Тема","summary":"Корень","body_markdown":"","kind":"topic","citations":["E1"]},{"ref":"n2","parent_ref":"n1","label":"Факт","summary":"Подробность","body_markdown":"Текст","kind":"fact","citations":["E1"]}]}`}}
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "Построй карту", Title: "Новая карта",
		Scope: ClassicMindMapAIScope{Document: entries[0].SourcePath, EntryIDs: []int64{entries[0].ID}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Grounded || preview.Status != ClassicMindMapAIPreviewReady || len(preview.Proposals) != 2 || preview.EvidenceCount != 1 || preview.BatchCount != 1 {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if maps, err := store.ListClassicMindMaps(true); err != nil || len(maps) != 0 {
		t.Fatalf("preview mutated maps: maps=%#v err=%v", maps, err)
	}
	loaded, err := store.LoadClassicMindMapAIPreview(preview.RunID)
	if err != nil || loaded.ProposalDigest != preview.ProposalDigest || loaded.EvidenceCount != 1 || loaded.BatchCount != 1 {
		t.Fatalf("load preview: %#v, %v", loaded, err)
	}
	result, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{
		RunID: preview.RunID, ExpectedPreviewDigest: preview.ProposalDigest, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Map.Mode != ClassicMindMapModeGenerated || result.Document.Map.Revision != 1 || len(result.Document.Nodes) != 2 {
		t.Fatalf("unexpected published map: %#v", result.Document)
	}
	if len(result.Document.Nodes[0].Sources)+len(result.Document.Nodes[1].Sources) == 0 {
		t.Fatal("grounded nodes lost their evidence sources")
	}
	if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); !errors.Is(err, ErrClassicMindMapAIAlreadyApplied) {
		t.Fatalf("repeat apply error=%v, want already applied", err)
	}
}

func TestClassicMindMapAINewMapBlankScopeUsesWholeActiveBase(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Методы защиты СПС","summary":"Обзор","body_markdown":"","kind":"topic","citations":["E1"]}]}`}}
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "Методы защиты СПС", Title: "Методы защиты СПС",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Grounded || preview.EvidenceCount != len(entries) || len(provider.requests) != 1 {
		t.Fatalf("blank new-map scope did not use the active base: preview=%#v entries=%d requests=%d", preview, len(entries), len(provider.requests))
	}
	for _, entry := range entries {
		if !strings.Contains(provider.requests[0].Prompt, entry.Text) {
			t.Fatalf("active-base evidence #%d is missing from model prompt", entry.ID)
		}
	}
}

func TestClassicMindMapAIExistingActionsApplyOneRevision(t *testing.T) {
	tests := []struct {
		name   string
		action ClassicMindMapAIAction
		answer string
		check  func(*testing.T, ClassicMindMapDocument)
	}{
		{
			name: "expand", action: ClassicMindMapAIExpand,
			answer: `{"nodes":[{"ref":"n1","parent_ref":"","label":"Новая ветка","summary":"Факт","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`,
			check: func(t *testing.T, doc ClassicMindMapDocument) {
				if len(doc.Nodes) != 2 || doc.Nodes[1].Origin != ClassicMindMapNodeGenerated {
					t.Fatalf("expansion was not published: %#v", doc.Nodes)
				}
			},
		},
		{
			name: "fill", action: ClassicMindMapAIFill,
			answer: `{"fill":{"summary":"AI summary","body_markdown":"AI body","citations":["E1"]}}`,
			check: func(t *testing.T, doc ClassicMindMapDocument) {
				root := doc.Nodes[0]
				if !strings.Contains(root.Summary, "manual summary") || !strings.Contains(root.Summary, "AI summary") || !strings.Contains(root.BodyMarkdown, "manual body") || !strings.Contains(root.BodyMarkdown, "AI body") {
					t.Fatalf("fill replaced manual text: %#v", root)
				}
			},
		},
		{
			name: "find sources", action: ClassicMindMapAIFindSources,
			answer: `{"sources":[{"evidence_ref":"E1","reason":"точное подтверждение"}]}`,
			check: func(t *testing.T, doc ClassicMindMapDocument) {
				if len(doc.Nodes[0].Sources) != 1 || doc.Nodes[0].Sources[0].EvidenceState != EvidenceCurrent {
					t.Fatalf("source was not attached: %#v", doc.Nodes[0].Sources)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, entries := newClassicMindMapAITestStore(t)
			doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{
				Title: "Ручная карта", Mode: ClassicMindMapModeManual, Status: ClassicMindMapStatusDraft,
				Nodes: []ClassicMindMapNodeDraft{{Ref: "root", Label: "Корень", Summary: "manual summary", BodyMarkdown: "manual body", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual}},
			}, "test", "create")
			if err != nil {
				t.Fatal(err)
			}
			provider := &classicMindMapAIFakeProvider{answers: []string{test.answer}}
			preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
				Action: test.action, Prompt: "Дополни", MapRef: doc.Map.ID, NodeRef: doc.Map.RootNodeID,
				ExpectedRevision: doc.Map.Revision, Scope: ClassicMindMapAIScope{EntryIDs: []int64{entries[0].ID}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.requests) == 0 || !strings.Contains(provider.requests[0].Prompt, "manual body") || !strings.Contains(provider.requests[0].Prompt, "Ручная карта") {
				t.Fatalf("selected target context was not sent to the model: %#v", provider.requests)
			}
			published, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID, ExpectedRevision: doc.Map.Revision, Actor: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if published.Document.Map.Revision != doc.Map.Revision+1 || published.Document.Map.Mode != ClassicMindMapModeHybrid {
				t.Fatalf("revision/mode=%d/%s", published.Document.Map.Revision, published.Document.Map.Mode)
			}
			changes, err := store.ListClassicMindMapChanges(doc.Map.ID, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) != 2 || changes[0].Action != "ai:"+string(test.action) {
				t.Fatalf("expected one AI history revision, got %#v", changes)
			}
			test.check(t, published.Document)
			if test.action == ClassicMindMapAIFindSources {
				duplicateProvider := &classicMindMapAIFakeProvider{answers: []string{test.answer}}
				duplicate, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, duplicateProvider), ClassicMindMapAIPreviewRequest{
					Action: test.action, Prompt: "Повтори", MapRef: published.Document.Map.ID, NodeRef: published.Document.Map.RootNodeID,
					ExpectedRevision: published.Document.Map.Revision, Scope: ClassicMindMapAIScope{EntryIDs: []int64{entries[0].ID}},
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: duplicate.RunID}); !errors.Is(err, ErrClassicMindMapAINoChanges) {
					t.Fatalf("duplicate source apply err=%v, want no changes", err)
				}
				after, _ := store.LoadClassicMindMap(published.Document.Map.ID)
				if after.Map.Revision != published.Document.Map.Revision {
					t.Fatalf("duplicate source created a no-op revision: %d -> %d", published.Document.Map.Revision, after.Map.Revision)
				}
			}
		})
	}
}

func TestClassicMindMapAIUsesBranchAndNearestAncestorEvidence(t *testing.T) {
	tests := []struct {
		name       string
		attachTo   string
		targetNode string
	}{
		{name: "descendant evidence grounds branch", attachTo: "leaf", targetNode: "branch"},
		{name: "nearest ancestor evidence grounds leaf", attachTo: "root", targetNode: "leaf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, entries := newClassicMindMapAITestStore(t)
			doc, err := store.CreateClassicMindMap("Контекст ветви", "")
			if err != nil {
				t.Fatal(err)
			}
			doc, branch, err := store.AddClassicMindMapNode(doc.Map.ID, doc.Map.RootNodeID, "Ветка", -1,
				ClassicMindMapNodeSubtopic, "", "", doc.Map.Revision, "test", "branch")
			if err != nil {
				t.Fatal(err)
			}
			doc, leaf, err := store.AddClassicMindMapNode(doc.Map.ID, branch.ID, "Лист", -1,
				ClassicMindMapNodeFact, "", "", doc.Map.Revision, "test", "leaf")
			if err != nil {
				t.Fatal(err)
			}
			nodes := map[string]string{"root": doc.Map.RootNodeID, "branch": branch.ID, "leaf": leaf.ID}
			anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
			if err != nil {
				t.Fatal(err)
			}
			doc, _, err = store.AttachClassicMindMapEvidence(doc.Map.ID, nodes[test.attachTo], anchor,
				doc.Map.Revision, "test", "ground branch")
			if err != nil {
				t.Fatal(err)
			}

			provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Продолжение","summary":"Факт","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`}}
			preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
				Action: ClassicMindMapAIExpand, Prompt: "Расширь", MapRef: doc.Map.ID, NodeRef: nodes[test.targetNode],
				ExpectedRevision: doc.Map.Revision, Scope: ClassicMindMapAIScope{UseNodeSources: true},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !preview.Grounded || preview.EvidenceCount != 1 || len(provider.requests) != 1 {
				t.Fatalf("branch evidence was not inherited: preview=%#v requests=%d", preview, len(provider.requests))
			}
			if !strings.Contains(provider.requests[0].Prompt, entries[0].Text) {
				t.Fatalf("inherited evidence text was not sent to the model: %q", provider.requests[0].Prompt)
			}
		})
	}
}

func TestClassicMindMapAIUsesEvidenceFromLinkedKnowledgeNode(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	const knowledgeNodeID = "kn-ai-branch-source"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: knowledgeNodeID, Kind: KnowledgeNodeClaim, Label: "Подтверждённый тезис",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.CreateClassicMindMap("Связь с knowledge map", "")
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err = store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
		Kind: ClassicMindMapSourceKnowledgeNode, KnowledgeNodeID: knowledgeNodeID,
	}, doc.Map.Revision, "test", "knowledge source")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 1 || len(graph.Nodes[0].Evidence) != 1 {
		t.Fatalf("knowledge source fixture was not stored: graph=%#v err=%v", graph, err)
	}
	if len(doc.Nodes) != 1 || len(doc.Nodes[0].Sources) != 1 || doc.Nodes[0].Sources[0].Kind != ClassicMindMapSourceKnowledgeNode {
		t.Fatalf("classic map knowledge source fixture was not attached: %#v", doc.Nodes)
	}
	storedAnchors, err := loadKnowledgeEvidence(store.db, "knowledge_node_evidence", "node_id", knowledgeNodeID)
	if err != nil || len(storedAnchors) != 1 || resolveEvidenceAnchorFromEntries(storedAnchors[0], store.entries).State != EvidenceCurrent {
		t.Fatalf("knowledge source evidence is not current: anchors=%#v err=%v", storedAnchors, err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Продолжение","summary":"Факт","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`}}
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAIExpand, Prompt: "Расширь", MapRef: doc.Map.ID, NodeRef: doc.Map.RootNodeID,
		ExpectedRevision: doc.Map.Revision, Scope: ClassicMindMapAIScope{UseNodeSources: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Grounded || preview.EvidenceCount != 1 || len(provider.requests) != 1 || !strings.Contains(provider.requests[0].Prompt, entries[0].Text) {
		t.Fatalf("knowledge-node evidence was not inherited: preview=%#v requests=%#v", preview, provider.requests)
	}
}

func TestClassicMindMapAINoEvidenceErrorIsActionableRussian(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	doc, err := store.CreateClassicMindMap("Без источников", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{}
	_, err = PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAIExpand, Prompt: "Расширь", MapRef: doc.Map.ID, NodeRef: doc.Map.RootNodeID,
		ExpectedRevision: doc.Map.Revision, Scope: ClassicMindMapAIScope{UseNodeSources: true},
	}, nil)
	if err == nil {
		t.Fatal("empty grounded scope unexpectedly reached the model")
	}
	message := err.Error()
	if !strings.Contains(message, "актуальные фрагменты") || !strings.Contains(message, "выберите документ") {
		t.Fatalf("error is not actionable: %q", message)
	}
	for _, technical := range []string{"grounded", "current versioned evidence", "entry_ids", "use_node_sources"} {
		if strings.Contains(message, technical) {
			t.Fatalf("technical implementation detail %q leaked in error: %q", technical, message)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatalf("model was called without evidence: %d", len(provider.requests))
	}
}

func TestClassicMindMapAIStrictValidationAndTokenRetry(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	valid := `{"nodes":[{"ref":"n1","parent_ref":"","label":"Idea","summary":"","body_markdown":"","kind":"topic","citations":[]}]}`
	provider := &classicMindMapAIFakeProvider{
		answers: []string{"", valid},
		errors:  []error{&AnswerTokenLimitError{MaxTokens: DefaultMapGenerationTokens}, nil},
	}
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "Brainstorm", Scope: ClassicMindMapAIScope{AllowUngrounded: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Grounded || len(provider.requests) != 2 || provider.requests[0].MaxTokens != 4096 || provider.requests[1].MaxTokens != 8192 {
		t.Fatalf("unexpected ungrounded/token retry: preview=%#v requests=%#v", preview, provider.requests)
	}

	badStore, _ := newClassicMindMapAITestStore(t)
	bad := `{"nodes":[{"ref":"n1","parent_ref":"","label":"Idea","summary":"","body_markdown":"","kind":"topic","citations":[]}],"unexpected":true}`
	badProvider := &classicMindMapAIFakeProvider{answers: []string{bad, bad, bad}}
	_, err = PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(badStore, badProvider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "Brainstorm", Scope: ClassicMindMapAIScope{AllowUngrounded: true},
	}, nil)
	if err == nil || len(badProvider.requests) != 3 {
		t.Fatalf("strict validation err=%v calls=%d", err, len(badProvider.requests))
	}
	if maps, listErr := badStore.ListClassicMindMaps(true); listErr != nil || len(maps) != 0 {
		t.Fatalf("invalid preview mutated maps: %#v, %v", maps, listErr)
	}
	var runStatus string
	if err := badStore.db.QueryRow(`SELECT status FROM mind_map_generation_runs ORDER BY created DESC LIMIT 1`).Scan(&runStatus); err != nil || runStatus != string(ClassicMindMapAIFailed) {
		t.Fatalf("failed generation left durable status %q: %v", runStatus, err)
	}
}

func TestClassicMindMapAIApplyRollsBackForParentRevisionLockAndStaleEvidence(t *testing.T) {
	newPreview := func(t *testing.T) (*Store, ClassicMindMapDocument, ClassicMindMapAIPreview) {
		t.Helper()
		store, entries := newClassicMindMapAITestStore(t)
		doc, err := store.CreateClassicMindMap("Карта", "")
		if err != nil {
			t.Fatal(err)
		}
		provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Parent","summary":"","body_markdown":"","kind":"subtopic","citations":["E1"]},{"ref":"n2","parent_ref":"n1","label":"Child","summary":"","body_markdown":"","kind":"fact","citations":["E1"]}]}`}}
		preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
			Action: ClassicMindMapAIExpand, Prompt: "Expand", MapRef: doc.Map.ID, NodeRef: doc.Map.RootNodeID,
			Scope: ClassicMindMapAIScope{EntryIDs: []int64{entries[0].ID}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return store, doc, preview
	}

	t.Run("parent closure", func(t *testing.T) {
		store, doc, preview := newPreview(t)
		_, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID, ProposalIDs: []string{preview.Proposals[1].ID}})
		if err == nil {
			t.Fatal("child-only selection was accepted")
		}
		after, _ := store.LoadClassicMindMap(doc.Map.ID)
		if after.Map.Revision != doc.Map.Revision || len(after.Nodes) != 1 {
			t.Fatalf("rejected selection mutated map: %#v", after)
		}
	})

	t.Run("revision", func(t *testing.T) {
		store, doc, preview := newPreview(t)
		changed, _, err := store.AddClassicMindMapNode(doc.Map.ID, doc.Map.RootNodeID, "Manual", 0, ClassicMindMapNodeNote, "", "", doc.Map.Revision, "test", "manual")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
			t.Fatalf("revision conflict err=%v", err)
		}
		after, _ := store.LoadClassicMindMap(doc.Map.ID)
		if after.Map.Revision != changed.Map.Revision || len(after.Nodes) != 2 {
			t.Fatalf("revision failure was not atomic: %#v", after)
		}
	})

	t.Run("lock", func(t *testing.T) {
		store, doc, preview := newPreview(t)
		if _, err := store.db.Exec(`UPDATE mind_map_nodes SET locked=1 WHERE id=?`, doc.Map.RootNodeID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); !errors.Is(err, ErrClassicMindMapLocked) {
			t.Fatalf("locked target error=%v", err)
		}
		var count int
		_ = store.db.QueryRow(`SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=? AND deleted_at=''`, doc.Map.ID).Scan(&count)
		if count != 1 {
			t.Fatalf("lock failure partially inserted %d nodes", count)
		}
	})

	t.Run("stale evidence", func(t *testing.T) {
		store, doc, preview := newPreview(t)
		chunks := validStructuredChunks()
		revision := ChunkContentHash("changed revision")
		for i := range chunks {
			chunks[i].Text += "-changed"
			chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
			chunks[i].Provenance.DocumentRevision = revision
		}
		if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); !errors.Is(err, ErrClassicMindMapAIChanged) {
			t.Fatalf("stale evidence err=%v", err)
		}
		after, _ := store.LoadClassicMindMap(doc.Map.ID)
		if after.Map.Revision != doc.Map.Revision || len(after.Nodes) != 1 {
			t.Fatalf("stale apply partially mutated map: %#v", after)
		}
	})

	t.Run("stale evidence changed by another process", func(t *testing.T) {
		store, doc, preview := newPreview(t)
		changedText := "changed outside this Store cache"
		if _, err := store.db.Exec(`UPDATE entries SET text=?, document_revision=?, chunk_hash=? WHERE id=(SELECT MIN(id) FROM entries)`,
			changedText, ChunkContentHash("external revision"), ChunkContentHash(changedText)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); !errors.Is(err, ErrClassicMindMapAIChanged) {
			t.Fatalf("external stale evidence err=%v", err)
		}
		after, _ := store.LoadClassicMindMap(doc.Map.ID)
		if after.Map.Revision != doc.Map.Revision || len(after.Nodes) != 1 {
			t.Fatalf("external stale apply partially mutated map: %#v", after)
		}
	})
}

func TestClassicMindMapAIEvidenceManifestHardCapIsExplicit(t *testing.T) {
	entries := make([]Entry, DefaultClassicMindMapAIEvidenceLimit+1)
	revision := ChunkContentHash("wide-scope-revision")
	for i := range entries {
		text := fmt.Sprintf("wide evidence %05d", i)
		entries[i] = Entry{
			ID: int64(i + 1), Text: text, DocumentID: "doc-wide", DocumentRevision: revision,
			ChunkHash: ChunkContentHash(text), SourcePath: "C:/docs/wide.pdf", Page: 1,
			BlockIndex: i, BlockChunkIndex: 0, BlockTotalChunks: 1,
		}
	}
	request := ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "wide",
		Scope: ClassicMindMapAIScope{Document: "doc-wide"},
	}
	selector := &Store{}
	if selected, err := selector.selectClassicMindMapAIEntriesLocked(entries, request, ClassicMindMapNode{}, ClassicMindMapDocument{}); err == nil || selected != nil ||
		!strings.Contains(err.Error(), fmt.Sprintf("выбрано %d", DefaultClassicMindMapAIEvidenceLimit+1)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("порога %d", DefaultClassicMindMapAIEvidenceLimit)) ||
		!strings.Contains(err.Error(), "--limit") {
		t.Fatalf("unbounded wide scope selected=%d err=%v", len(selected), err)
	}

	request.Scope.Limit = 0
	selected, err := selector.selectClassicMindMapAIEntriesLocked(entries[:DefaultClassicMindMapAIEvidenceLimit], request, ClassicMindMapNode{}, ClassicMindMapDocument{})
	if err != nil || len(selected) != DefaultClassicMindMapAIEvidenceLimit {
		t.Fatalf("exact automatic boundary count=%d err=%v", len(selected), err)
	}

	request.Scope.Limit = DefaultClassicMindMapAIEvidenceLimit
	selected, err = selector.selectClassicMindMapAIEntriesLocked(entries, request, ClassicMindMapNode{}, ClassicMindMapDocument{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != DefaultClassicMindMapAIEvidenceLimit || selected[0].ID != 1 || selected[len(selected)-1].ID != int64(DefaultClassicMindMapAIEvidenceLimit) {
		t.Fatalf("explicit bounded selection is not deterministic: count=%d first=%d last=%d", len(selected), selected[0].ID, selected[len(selected)-1].ID)
	}

	request.Scope.Limit = MaxClassicMindMapAIEvidence
	selected, err = selector.selectClassicMindMapAIEntriesLocked(entries, request, ClassicMindMapNode{}, ClassicMindMapDocument{})
	if err != nil || len(selected) != len(entries) {
		t.Fatalf("explicit limit up to hard maximum should be accepted: count=%d err=%v", len(selected), err)
	}

	store, _ := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{}
	_, err = PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(store, provider), ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "too large limit",
		Scope: ClassicMindMapAIScope{AllowUngrounded: true, Limit: MaxClassicMindMapAIEvidence + 1},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("between 0 and %d", MaxClassicMindMapAIEvidence)) || len(provider.requests) != 0 {
		t.Fatalf("over-maximum limit err=%v provider_calls=%d", err, len(provider.requests))
	}
}
