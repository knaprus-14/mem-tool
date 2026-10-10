package mem

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestClassicMindMapBranchEvidenceMatchesWebStatesAndRestoration(t *testing.T) {
	dir, store, mapID, rootID, _, anchor, _ := webMapGraphEvidenceFixture(t)
	if _, _, err := store.AttachClassicMindMapEvidence(mapID, rootID, anchor, 3, "test", "same anchor directly"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name  string
		state EvidenceState
	}{{"current", EvidenceCurrent}, {"changed_revision", EvidenceStale}, {"deleted", EvidenceMissing}, {"restored", EvidenceCurrent}} {
		t.Run(step.name, func(t *testing.T) {
			switch step.name {
			case "changed_revision":
				if _, err := store.db.Exec(`UPDATE entries SET document_revision=?`, ChunkContentHash("changed revision")); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if _, err := store.ReplaceDocumentWithEmpty(anchor.SourcePath); err != nil {
					t.Fatal(err)
				}
			case "restored":
				chunks := validStructuredChunks()
				for i := range chunks {
					chunks[i].Provenance.SourcePath = anchor.SourcePath
				}
				if err := store.ReplaceDocumentChunks(anchor.SourcePath, chunks); err != nil {
					t.Fatal(err)
				}
			}
			doc, err := store.LoadClassicMindMap(mapID)
			if err != nil {
				t.Fatal(err)
			}
			request := ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: rootID, ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest}
			manifest, err := store.BuildClassicMindMapBranchManifest(request)
			if err != nil {
				t.Fatal(err)
			}
			web, err := readWebMapBranchIn(context.Background(), dir, mapID, rootID, 4, false)
			if err != nil || !reflect.DeepEqual(manifest, web.Manifest) {
				t.Fatalf("CLI/web manifest differ: CLI=%#v web=%#v err=%v", manifest, web.Manifest, err)
			}
			pack, err := store.BuildClassicMindMapStudyPack(request)
			if err != nil || len(pack.Cards) != 1 || pack.ManifestDigest != manifest.Digest {
				t.Fatalf("study=%#v err=%v", pack, err)
			}
			card := pack.Cards[0]
			current := step.state == EvidenceCurrent
			if card.EvidenceCurrent == nil || *card.EvidenceCurrent != current || card.EvidenceDigest != web.Study.Cards[0].EvidenceDigest {
				t.Fatalf("CLI/web study pins differ: CLI=%#v web=%#v", card, web.Study.Cards[0])
			}
			if len(card.Sources) != 2 {
				t.Fatalf("sources=%#v", card.Sources)
			}
			for _, source := range card.Sources {
				if source.EvidenceState != step.state {
					t.Fatalf("source %s has state %s, want %s", source.Kind, source.EvidenceState, step.state)
				}
			}
			wantEvidence := 0
			if current {
				wantEvidence = 1
			}
			if len(manifest.Evidence) != wantEvidence {
				t.Fatalf("evidence=%#v, want %d", manifest.Evidence, wantEvidence)
			}
		})
	}
}

func TestClassicMindMapBranchEvidenceKeepsDistinctExcerpts(t *testing.T) {
	dir, store, mapID, rootID, graphID, anchor, _ := webMapGraphEvidenceFixture(t)
	short := anchor
	short.Excerpt, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", graphID, 1, short); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AttachClassicMindMapEvidence(mapID, rootID, short, 3, "test", "duplicate excerpt directly"); err != nil {
		t.Fatal(err)
	}
	request := ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: rootID, ExpectedRevision: 4}
	manifest, err := store.BuildClassicMindMapBranchManifest(request)
	if err != nil || len(manifest.Evidence) != 2 || manifest.Evidence[0].EvidenceHash == manifest.Evidence[1].EvidenceHash {
		t.Fatalf("distinct excerpts missing: %#v err=%v", manifest, err)
	}
	web, err := readWebMapBranchIn(context.Background(), dir, mapID, rootID, 4, false)
	if err != nil || !reflect.DeepEqual(manifest, web.Manifest) {
		t.Fatalf("CLI/web excerpt context differs: CLI=%#v web=%#v err=%v", manifest, web.Manifest, err)
	}
	for _, evidence := range manifest.Evidence {
		if evidence.Text != anchor.Excerpt && evidence.Text != short.Excerpt {
			t.Fatalf("graph body or unselected chunk entered evidence: %#v", evidence)
		}
	}
}

func TestClassicMindMapBranchEvidencePinsGraphDependency(t *testing.T) {
	_, store, mapID, rootID, graphID, _, _ := webMapGraphEvidenceFixture(t)
	request := ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: rootID, ExpectedRevision: 3}
	manifest, err := store.BuildClassicMindMapBranchManifest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE knowledge_nodes SET body=body||' changed' WHERE id=?`, graphID); err != nil {
		t.Fatal(err)
	}
	changed, err := store.BuildClassicMindMapBranchManifest(request)
	if err != nil || changed.Digest == manifest.Digest || !reflect.DeepEqual(changed.Evidence, manifest.Evidence) {
		t.Fatalf("graph body change not pinned independently of evidence: changed=%#v err=%v", changed, err)
	}
	calls := 0
	service := &ClassicMindMapAIService{Config: AnswerConfig{Model: "test-chat", ContextChars: 12000}, Provider: answerRevalidationProviderFunc(func(_ context.Context, request AnswerRequest) (string, error) {
		calls++
		if strings.Contains(request.Prompt, "Graph interpretation") {
			t.Fatal("graph interpretation was used as document evidence")
		}
		if _, err := store.db.Exec(`UPDATE knowledge_nodes SET body=body||' changed again' WHERE id=?`, graphID); err != nil {
			t.Fatal(err)
		}
		return `{"claims":[{"text":"Answer","citations":["E1"]}]}`, nil
	})}
	question := ClassicMindMapBranchQuestionRequest{ClassicMindMapBranchRequest: request, Question: "What is selected?", ExpectedManifestDigest: manifest.Digest}
	if _, err = store.AnswerClassicMindMapBranch(context.Background(), service, question); err == nil || calls != 0 {
		t.Fatalf("old graph dependency reached provider: calls=%d err=%v", calls, err)
	}
	question.ExpectedManifestDigest = changed.Digest
	if result, err := store.AnswerClassicMindMapBranch(context.Background(), service, question); err == nil || result.Answer != "" || calls != 1 {
		t.Fatalf("changed graph dependency published answer: result=%#v calls=%d err=%v", result, calls, err)
	}
}
