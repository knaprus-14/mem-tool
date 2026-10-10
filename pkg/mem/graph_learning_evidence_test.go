package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestKnowledgeSelectionAndLearningPreserveDistinctExcerptAnchors(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	short := anchor
	short.Excerpt, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	graph := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "excerpt-a", Kind: KnowledgeNodeClaim, Label: "First", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "excerpt-b", Kind: KnowledgeNodeClaim, Label: "Second", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{short}},
	}, Edges: []KnowledgeEdge{{ID: "excerpt-edge", From: "excerpt-a", To: "excerpt-b", Kind: KnowledgeRelationSupports,
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor, short}}}}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"excerpt-a", "excerpt-b"}, EdgeIDs: []string{"excerpt-edge"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil || !manifest.Ready || len(manifest.Evidence) != 2 || manifest.Summary.CurrentEvidence != 2 {
		t.Fatalf("manifest=%#v err=%v", manifest, err)
	}
	repeated, err := store.BuildKnowledgeSelectionManifest(KnowledgeSelectionRequest{NodeIDs: []string{"excerpt-b", "excerpt-a"}, EdgeIDs: selection.EdgeIDs})
	if err != nil || repeated.Digest != manifest.Digest {
		t.Fatalf("excerpt ordering is not deterministic: repeated=%#v err=%v", repeated, err)
	}
	service := &KnowledgeSelectionAnswerService{Config: AnswerConfig{Model: "test-chat", ContextChars: 12000}, Provider: &selectionAnswerProvider{answers: []string{
		`{"claims":[{"text":"Both excerpts","citations":["E1","E2"]}]}`,
		`{"items":[{"kind":"card","prompt":"Only first excerpt?","answer":"First answer","citations":["E1"]},{"kind":"card","prompt":"Both excerpts?","answer":"Second answer","citations":["E2","E1"]}]}`,
	}}}
	answer, err := store.AnswerKnowledgeSelection(context.Background(), service, KnowledgeSelectionAnswerRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Mode: KnowledgeSelectionModeQuestion, Question: "Compare"})
	if err != nil || len(answer.Sources) != 2 || answer.Sources[0].EvidenceHash == answer.Sources[1].EvidenceHash {
		t.Fatalf("selection answer=%#v err=%v", answer, err)
	}
	run, err := store.GenerateKnowledgeLearningCandidates(context.Background(), service, KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 2})
	if err != nil || len(run.Candidates) != 2 || len(run.Candidates[1].Citations) != 1 || len(run.Candidates[1].Sources) != 2 {
		t.Fatalf("learning run=%#v err=%v", run, err)
	}
	if run.Candidates[0].Sources[0].EvidenceHash != manifest.Evidence[0].EvidenceHash {
		t.Fatalf("single-ref candidate was rebound to another excerpt: %#v", run.Candidates[0])
	}
	saved, err := store.SaveKnowledgeLearningCandidates(KnowledgeLearningSaveRequest{RunID: run.ID, CandidateIndexes: []int{0, 1}, Author: "test"})
	if err != nil || len(saved.Nodes) != 2 || len(saved.Nodes[0].Evidence) != 1 || len(saved.Nodes[1].Evidence) != 2 {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	if saved.Nodes[0].Evidence[0].EvidenceHash != manifest.Evidence[0].EvidenceHash || saved.Nodes[0].Evidence[0].Excerpt != manifest.Evidence[0].Text {
		t.Fatalf("save selected the wrong excerpt: %#v", saved.Nodes[0])
	}
	for _, node := range saved.Nodes {
		for _, evidence := range node.Evidence {
			if evidence.CitationID != anchor.CitationID || (evidence.Excerpt != anchor.Excerpt && evidence.Excerpt != short.Excerpt) {
				t.Fatalf("source identity changed: %#v", evidence)
			}
		}
	}
	// The transactional save verifier must also honor the new structural pin.
	if _, err = store.db.Exec(`UPDATE knowledge_edges SET from_node=to_node,to_node=from_node WHERE id=?`, graph.Edges[0].ID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = verifyKnowledgeSelectionManifestTx(tx, manifest); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("changed endpoints passed transaction verifier: %v", err)
	}
}

func TestKnowledgeLearningLegacyCitationSourcesRemainUnambiguous(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		name := "unique"
		if ambiguous {
			name = "ambiguous"
		}
		t.Run(name, func(t *testing.T) {
			store, anchor := graphStoreAndAnchor(t)
			defer store.Close()
			anchors := []EvidenceAnchor{anchor}
			if ambiguous {
				short := anchor
				short.Excerpt, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
				anchors = append(anchors, short)
			}
			if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: "legacy-learning-parent", Kind: KnowledgeNodeClaim, Label: "Parent", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: anchors}}}); err != nil {
				t.Fatal(err)
			}
			selection := KnowledgeSelectionRequest{NodeIDs: []string{"legacy-learning-parent"}}
			manifest, err := store.BuildKnowledgeSelectionManifest(selection)
			if err != nil {
				t.Fatal(err)
			}
			candidate := KnowledgeLearningCandidate{Kind: KnowledgeNodeCard, Prompt: "Legacy card", Answer: "Legacy answer", Citations: []string{anchor.CitationID},
				Sources: []KnowledgeSelectionSourceRef{{CitationID: anchor.CitationID, SourcePath: anchor.SourcePath, Page: anchor.Page, BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex}}}
			encoded, err := json.Marshal(candidate)
			if err != nil || strings.Contains(string(encoded), "evidence_hash") {
				t.Fatalf("legacy serialized candidate changed: %s err=%v", encoded, err)
			}
			run := KnowledgeLearningRun{ID: "learning-run-legacy", Selection: selection, ManifestDigest: manifest.Digest, Candidates: []KnowledgeLearningCandidate{candidate}, Model: "legacy", Created: "2026-10-01T00:00:00Z"}
			run.GenerationDigest, err = knowledgeLearningGenerationDigest(run.ManifestDigest, run.Candidates)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.insertKnowledgeLearningRun(run); err != nil {
				t.Fatal(err)
			}
			saved, err := store.SaveKnowledgeLearningCandidates(KnowledgeLearningSaveRequest{RunID: run.ID, CandidateIndexes: []int{0}, Author: "test"})
			if ambiguous {
				if !errors.Is(err, ErrKnowledgeSelectionChanged) || len(saved.Nodes) != 0 {
					t.Fatalf("ambiguous old candidate saved: %#v err=%v", saved, err)
				}
			} else if err != nil || len(saved.Nodes) != 1 || len(saved.Nodes[0].Evidence) != 1 || saved.Nodes[0].Evidence[0] != anchor {
				t.Fatalf("legacy unique candidate no longer resolves identically: %#v err=%v", saved, err)
			}
		})
	}
}

func TestKnowledgeLearningTruncatedExcerptMapsToOriginalExactAnchor(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	full, err := groundedEvidenceForSelectionAnchor(anchor)
	if err != nil {
		t.Fatal(err)
	}
	short := full
	short.Text, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	manifest := []GroundedEvidence{short, full}
	prompt, err := buildKnowledgeLearningPrompt("Questions", manifest, 12000)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the bounded builder's final-prefix truncation without relying
	// on incidental prompt string lengths in a particular release.
	prompt.Evidence[1].Text = "ch"
	prompt.Evidence[1].EvidenceHash = ChunkContentHash("ch")
	prompt.Evidence[1].Truncated = true
	raw := `{"items":[{"kind":"card","prompt":"Question","answer":"Answer","citations":["E2"]}]}`
	candidates, err := validateKnowledgeLearningCandidates(raw, prompt.Evidence, manifest, 1)
	if err != nil || len(candidates) != 1 || candidates[0].Sources[0].EvidenceHash != anchor.EvidenceHash {
		t.Fatalf("truncated excerpt mapped to wrong original: %#v err=%v", candidates, err)
	}
	prompt.Evidence[1].Text = "not a prefix"
	prompt.Evidence[1].EvidenceHash = ChunkContentHash(prompt.Evidence[1].Text)
	if _, err = validateKnowledgeLearningCandidates(raw, prompt.Evidence, manifest, 1); err == nil {
		t.Fatal("unrelated truncated excerpt accepted")
	}
}
