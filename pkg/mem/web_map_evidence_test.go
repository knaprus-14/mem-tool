package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func webMapGraphEvidenceFixture(t *testing.T) (string, *Store, string, string, string, EvidenceAnchor, WebMapAIInput) {
	t.Helper()
	dir, s, id, root, input := webAIFixture(t)
	entry, err := s.GetByID(input.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	const graphID = "kn-web-resolved"
	if err = s.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: graphID, Kind: KnowledgeNodeClaim, Label: "Linked claim", Body: "Graph interpretation, never document evidence", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AttachClassicMindMapSource(id, root, ClassicMindMapSource{Kind: ClassicMindMapSourceKnowledgeNode, KnowledgeNodeID: graphID}, 1, "alice", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = EditWebMapNodeIn(context.Background(), dir, "alice", id, root, WebMapNodeEdit{Revision: 2, RequestID: strings.Repeat("c", 32), Label: "Question", Summary: "Study answer"}); err != nil {
		t.Fatal(err)
	}
	input.Revision, input.UseBranch, input.Entries, input.RequestID = 3, true, nil, strings.Repeat("d", 32)
	return dir, s, id, root, graphID, anchor, input
}

func TestWebMapEvidenceGraphAndDirectDedupRedaction(t *testing.T) {
	dir, s, id, root, graphID, anchor, _ := webMapGraphEvidenceFixture(t)
	ctx := context.Background()
	view, err := ReadWebMapBranchIn(ctx, dir, id, root, 3)
	if err != nil || len(view.Manifest.Evidence) != 1 || view.OtherSources != 0 || view.Unavailable != 0 || len(view.Study.Cards) != 1 {
		t.Fatal(view, err)
	}
	card := view.Study.Cards[0]
	if card.EvidenceDigest == "" || card.EvidenceCurrent == nil || !*card.EvidenceCurrent || card.Sources[0].EvidenceState != EvidenceCurrent {
		t.Fatal("missing resolved card pins", card)
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), "private-ai-folder") || strings.Contains(string(data), "Graph interpretation") {
		t.Fatal("server path or graph body leaked into document evidence", string(data))
	}
	if _, _, err = s.AttachClassicMindMapEvidence(id, root, anchor, 3, "alice", "same evidence directly"); err != nil {
		t.Fatal(err)
	}
	view, err = ReadWebMapBranchIn(ctx, dir, id, root, 4)
	if err != nil || len(view.Manifest.Evidence) != 1 {
		t.Fatal("shared direct/graph anchor not deduplicated", view, err)
	}
	short := anchor
	short.Excerpt, short.EvidenceHash = "chunk", ChunkContentHash("chunk")
	tx, err := s.db.Begin()
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
	view, err = ReadWebMapBranchIn(ctx, dir, id, root, 4)
	if err != nil || len(view.Manifest.Evidence) != 2 || view.Manifest.Evidence[0].EvidenceHash == view.Manifest.Evidence[1].EvidenceHash {
		t.Fatal("distinct excerpts were silently dropped", view, err)
	}
	if _, _, err = s.AttachClassicMindMapSource(id, root, ClassicMindMapSource{Kind: ClassicMindMapSourceURL, URL: "http://127.0.0.1:1/not-evidence"}, 4, "alice", "URL is not evidence"); err != nil {
		t.Fatal(err)
	}
	view, err = ReadWebMapBranchIn(ctx, dir, id, root, 5)
	if err != nil || view.OtherSources != 1 || len(view.Manifest.Evidence) != 2 {
		t.Fatal(view, err)
	}
}

func TestWebMapEvidenceGraphPinsStudyAndStaleMissing(t *testing.T) {
	dir, s, id, root, graphID, _, _ := webMapGraphEvidenceFixture(t)
	ctx := context.Background()
	view, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
	if err != nil || len(view.Study.Cards) != 1 {
		t.Fatal(view, err)
	}
	card := view.Study.Cards[0]
	review := WebMapStudyReview{Revision: 3, Manifest: view.Manifest.Digest, CardID: card.ID, RequestID: strings.Repeat("1", 32), Grade: KnowledgeLearningGradeGood}
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, review); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE knowledge_nodes SET body=body||' changed' WHERE id=?`,
		`UPDATE knowledge_nodes SET status='active' WHERE id=?`,
		`UPDATE knowledge_node_evidence SET excerpt='chunk',evidence_hash='` + ChunkContentHash("chunk") + `' WHERE node_id=?`,
	} {
		if _, err = s.db.Exec(statement, graphID); err != nil {
			t.Fatal(err)
		}
		next, e := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
		if e != nil || next.Manifest.Digest == view.Manifest.Digest || next.Study.Cards[0].EvidenceDigest == view.Study.Cards[0].EvidenceDigest || next.Progress[card.ID].LastAttemptID != 0 || next.DueCount != 1 {
			t.Fatal("graph change retained old manifest/schedule", next, e)
		}
		view = next
	}
	review.RequestID = strings.Repeat("2", 32)
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, review); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal("old graph manifest accepted", err)
	}
	for _, state := range []EvidenceState{EvidenceStale, EvidenceMissing} {
		statement := `UPDATE entries SET document_revision='` + ChunkContentHash("new revision") + `'`
		if state == EvidenceMissing {
			statement = `DELETE FROM entries`
		}
		if _, err = s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		view, err = ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
		if err != nil || len(view.Manifest.Evidence) != 0 || view.Unavailable != 1 || view.Study.Cards[0].Sources[0].EvidenceState != state || *view.Study.Cards[0].EvidenceCurrent || view.Progress[card.ID].Current || view.DueCount != 0 {
			t.Fatal("graph source incorrectly current", state, view, err)
		}
	}
	if _, err = s.db.Exec(`DELETE FROM knowledge_nodes WHERE id=?`, graphID); err != nil {
		t.Fatal(err)
	}
	missing, err := ReadWebMapBranchIn(ctx, dir, id, root, 3)
	if err != nil || missing.Unavailable != 1 || missing.Manifest.Digest == view.Manifest.Digest {
		t.Fatal("deleted graph dependency not pinned", missing, err)
	}
}

func TestWebMapEvidenceGraphChangesDuringAnswer(t *testing.T) {
	dir, s, id, root, graphID, _, _ := webMapGraphEvidenceFixture(t)
	ctx := context.Background()
	view, err := ReadWebMapBranchIn(ctx, dir, id, root, 3)
	if err != nil {
		t.Fatal(err)
	}
	service := &ClassicMindMapAIService{Config: AnswerConfig{Model: "fixture", ContextChars: 12000}, Provider: webBranchProviderFunc(func(ctx context.Context, r AnswerRequest) (string, error) {
		if strings.Contains(r.Prompt, "private-ai-folder") {
			t.Fatal("private graph evidence path sent to model")
		}
		if _, err := s.db.Exec(`UPDATE knowledge_nodes SET body='changed during answer' WHERE id=?`, graphID); err != nil {
			t.Fatal(err)
		}
		return `{"claims":[{"text":"Ответ","citations":["E1"]}]}`, nil
	})}
	if _, err = answerWebMapBranchWithService(ctx, dir, id, root, WebMapBranchInput{Revision: 3, Question: "Что известно?", Manifest: view.Manifest.Digest}, service); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal("answer accepted against changed graph", err)
	}
}

func TestWebMapEvidenceGraphAIQueuedAndReadyPins(t *testing.T) {
	for _, phase := range []string{"queued", "ready", "unchanged"} {
		t.Run(phase, func(t *testing.T) {
			dir, s, id, root, graphID, _, input := webMapGraphEvidenceFixture(t)
			ctx := context.Background()
			task, err := StartWebMapAIIn(ctx, dir, "alice", id, root, input)
			if err != nil {
				t.Fatal(err)
			}
			change := func() {
				if _, err := s.db.Exec(`UPDATE knowledge_nodes SET body='changed after task pin' WHERE id=?`, graphID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "queued" {
				change()
			}
			provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Grounded proposal","summary":"Fact","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`}}
			if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
				t.Fatal(worked, e)
			}
			if phase == "queued" {
				if len(provider.requests) != 0 {
					t.Fatal("model called for stale graph")
				}
				tasks, e := ListWebMapAIIn(ctx, dir, "alice", id)
				if e != nil || len(tasks) != 1 || tasks[0].Status != "failed" {
					t.Fatal(tasks, e)
				}
				return
			}
			for _, request := range provider.requests {
				if strings.Contains(request.Prompt, "private-ai-folder") {
					t.Fatal("private graph evidence path sent to model")
				}
			}
			view, e := ReadWebMapAIReviewIn(ctx, dir, "alice", id, task.ID)
			if e != nil || !view.Grounded || len(view.Proposals) != 1 {
				t.Fatal(view, e)
			}
			if phase == "ready" {
				change()
			}
			apply := WebMapAIApply{RequestID: strings.Repeat("e", 32), Revision: 3, Digest: view.Digest, ProposalIDs: []string{view.Proposals[0].ID}}
			result, e := ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply)
			if phase == "ready" {
				if !errors.Is(e, ErrWebMapAIChanged) {
					t.Fatal("ready preview accepted against changed graph", result, e)
				}
				doc, e := s.LoadClassicMindMap(id)
				if e != nil || doc.Map.Revision != 3 || len(doc.Nodes) != 1 {
					t.Fatal("failed apply changed map", doc, e)
				}
			} else {
				if e != nil || result.Revision != 4 {
					t.Fatal(result, e)
				}
				again, e := ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply)
				if e != nil || !again.Replayed {
					t.Fatal("replay rechecked old inputs", again, e)
				}
			}
		})
	}
}

func TestWebMapEvidenceBoundsEmptyAndCancellation(t *testing.T) {
	for _, mode := range []string{"body", "anchors", "entries", "warnings", "extraction_method", "empty", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, id, root, graphID, _, _ := webMapGraphEvidenceFixture(t)
			ctx := context.Background()
			var err error
			switch mode {
			case "body":
				_, err = s.db.Exec(`UPDATE knowledge_nodes SET body=CAST(zeroblob(?) AS TEXT) WHERE id=?`, (16<<20)+1, graphID)
			case "anchors":
				_, err = s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2000) INSERT INTO knowledge_node_evidence SELECT e.node_id,n.i,e.citation_id,e.document_id,e.document_revision,e.chunk_hash,e.evidence_hash,e.source_path,e.page,e.block_index,e.block_chunk_index,e.excerpt FROM knowledge_node_evidence e,n WHERE e.node_id=? AND e.ordinal=0`, graphID)
			case "entries":
				_, err = s.db.Exec(`UPDATE entries SET text=CAST(zeroblob(?) AS TEXT)`, (16<<20)+1)
			case "warnings":
				_, err = s.db.Exec(`UPDATE entries SET warnings=CAST(zeroblob(?) AS TEXT)`, (16<<20)+1)
			case "extraction_method":
				_, err = s.db.Exec(`UPDATE entries SET extraction_method=CAST(zeroblob(?) AS TEXT)`, (16<<20)+1)
			case "empty":
				_, err = s.db.Exec(`DELETE FROM knowledge_node_evidence WHERE node_id=?`, graphID)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := ReadWebMapBranchIn(ctx, dir, id, root, 3)
			if mode == "empty" {
				if err != nil || view.Unavailable != 1 || len(view.Manifest.Evidence) != 0 || *view.Study.Cards[0].EvidenceCurrent {
					t.Fatal("unanchored graph treated as document evidence", view, err)
				}
			} else if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation ignored", err)
				}
			} else if !errors.Is(err, ErrWebMapEditLimit) {
				t.Fatal("resource bound ignored", err)
			}
		})
	}
}

func TestWebMapEvidenceQualityMetadataManifestProviderAndPins(t *testing.T) {
	for _, kind := range []string{"graph", "direct"} {
		t.Run(kind, func(t *testing.T) {
			dir, s, id, root, _, anchor, input := webMapGraphEvidenceFixture(t)
			ctx := context.Background()
			if kind == "direct" {
				doc, _, err := s.AttachClassicMindMapEvidence(id, root, anchor, input.Revision, "alice", "direct fixture")
				if err != nil {
					t.Fatal(err)
				}
				doc, err = s.DetachClassicMindMapSource(id, root, doc.Nodes[0].Sources[0].ID, doc.Map.Revision, "alice", "remove graph fixture")
				if err != nil {
					t.Fatal(err)
				}
				input.Revision = doc.Map.Revision
			}
			if _, err := s.db.Exec(`UPDATE entries SET extraction_method='ocr',ocr_confidence=21.5,warnings='["stored quality warning"]'`); err != nil {
				t.Fatal(err)
			}
			view, err := ReadWebMapBranchIn(ctx, dir, id, root, input.Revision)
			if err != nil || len(view.Manifest.Evidence) != 1 {
				t.Fatal(view, err)
			}
			g := view.Manifest.Evidence[0]
			if g.OCRConfidence != 21.5 || len(g.Warnings) != 2 || !containsString(g.Warnings, "stored quality warning") || !strings.HasPrefix(g.Warnings[1], "OCR confidence 21.5 is below") || g.EvidenceHash != anchor.EvidenceHash {
				t.Fatal("quality metadata lost in manifest", g)
			}
			assertPrompt := func(r AnswerRequest) {
				t.Helper()
				if !strings.Contains(r.Prompt, "stored quality warning") || !strings.Contains(r.Prompt, "OCR confidence 21.5 is below") || !strings.Contains(r.Prompt, "ocr_confidence") || !strings.Contains(r.Prompt, "21.5") {
					t.Fatal("quality metadata lost in model prompt", r.Prompt)
				}
			}
			service := &ClassicMindMapAIService{Config: AnswerConfig{Model: "fixture", ContextChars: 12000}, Provider: webBranchProviderFunc(func(ctx context.Context, r AnswerRequest) (string, error) {
				assertPrompt(r)
				return `{"claims":[{"text":"Ответ с учётом низкого качества OCR","citations":["E1"]}]}`, nil
			})}
			if _, err = answerWebMapBranchWithService(ctx, dir, id, root, WebMapBranchInput{Revision: input.Revision, Question: "Что известно?", Manifest: view.Manifest.Digest}, service); err != nil {
				t.Fatal(err)
			}
			task, err := StartWebMapAIIn(ctx, dir, "alice", id, root, input)
			if err != nil {
				t.Fatal(err)
			}
			provider := webBranchProviderFunc(func(ctx context.Context, r AnswerRequest) (string, error) {
				assertPrompt(r)
				return `{"nodes":[{"ref":"n1","parent_ref":"","label":"OCR proposal","summary":"Needs verification","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`, nil
			})
			if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
				t.Fatal(worked, e)
			}
			review, err := ReadWebMapAIReviewIn(ctx, dir, "alice", id, task.ID)
			if err != nil || len(review.Proposals) != 1 {
				t.Fatal(review, err)
			}
			previousAI := task.InputDigest
			for _, statement := range []string{
				`UPDATE entries SET ocr_confidence=42`,
				`UPDATE entries SET warnings='["other quality warning"]'`,
				`UPDATE entries SET extraction_method='native'`,
				// Derived GroundedEvidence is identical for this method-only change.
				`UPDATE entries SET extraction_method='native-v2'`,
			} {
				if _, err = s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				next, e := ReadWebMapBranchIn(ctx, dir, id, root, input.Revision)
				if e != nil || len(next.Manifest.Evidence) != 1 || next.Manifest.Digest == view.Manifest.Digest || next.Study.Cards[0].EvidenceDigest == view.Study.Cards[0].EvidenceDigest || next.Manifest.Evidence[0].EvidenceHash != g.EvidenceHash {
					t.Fatal("quality-only change did not invalidate pins", statement, next, e)
				}
				_, _, _, _, digest, e := webAIInputs(ctx, dir, id, root, input)
				if e != nil || digest == previousAI {
					t.Fatal("quality-only change did not invalidate AI input", statement, digest, e)
				}
				view, previousAI = next, digest
			}
			if _, err = ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, WebMapAIApply{RequestID: strings.Repeat("e", 32), Revision: input.Revision, Digest: review.Digest, ProposalIDs: []string{review.Proposals[0].ID}}); !errors.Is(err, ErrWebMapAIChanged) {
				t.Fatal("ready AI applied against changed quality metadata", err)
			}
			// An invalid stored warning array must not silently become no warnings.
			if _, err = s.db.Exec(`UPDATE entries SET warnings='[1]'`); err != nil {
				t.Fatal(err)
			}
			if _, err = ReadWebMapBranchIn(ctx, dir, id, root, input.Revision); err == nil || !strings.Contains(err.Error(), "warnings") {
				t.Fatal("malformed quality metadata ignored", err)
			}
		})
	}
}
