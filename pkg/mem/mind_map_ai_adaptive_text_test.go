package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type adaptiveTextProvider struct {
	limit         int
	outputLimit   bool
	failAfter     int
	failSynthesis bool
	calls         map[string]int
	accepted      []string
}

func (p *adaptiveTextProvider) MaxOutputTokens() int { return 4096 }

func (p *adaptiveTextProvider) Generate(ctx context.Context, request AnswerRequest) (string, error) {
	if strings.Contains(request.Prompt, "SYNTHESIS_INPUT_JSON_BEGIN") {
		if p.failSynthesis {
			return "", errors.New("injected synthesis interruption")
		}
		return (&resumableMapProvider{calls: map[string]int{}}).Generate(ctx, request)
	}
	_, raw, ok := strings.Cut(request.Prompt, "EVIDENCE_JSON_BEGIN\n")
	if !ok {
		return "", errors.New("missing evidence")
	}
	raw, _, _ = strings.Cut(raw, "\nEVIDENCE_JSON_END")
	var evidence []GroundedEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		return "", err
	}
	if len(evidence) != 1 {
		return "", fmt.Errorf("expected one fragment, got %d", len(evidence))
	}
	e := evidence[0]
	p.calls[e.Text]++
	if len([]rune(e.Text)) > p.limit {
		if p.outputLimit {
			return "", &AnswerTokenLimitError{MaxTokens: request.MaxTokens}
		}
		return "", ErrAnswerContextLimit
	}
	if p.failAfter > 0 && len(p.accepted) == p.failAfter {
		return "", errors.New("injected interruption")
	}
	p.accepted = append(p.accepted, e.Text)
	return fmt.Sprintf(`{"nodes":[{"ref":"a","parent_ref":"","label":"Факт %d","summary":"Подробность","body_markdown":"","kind":"fact","citations":[%q]}]}`, len(p.accepted), e.EvidenceRef), nil
}

func seedSingleAdaptiveChunk(t *testing.T, s *Store, path, text string) {
	t.Helper()
	chunk := validStructuredChunks()[0]
	chunk.Provenance.SourcePath = path
	chunk.ChunkIndex, chunk.TotalChunks = 0, 1
	chunk.Provenance.BlockChunkIndex, chunk.Provenance.BlockTotalChunks = 0, 1
	chunk.Text = text
	chunk.Provenance.ChunkHash = ChunkContentHash(text)
	if err := s.ReplaceDocumentChunks(path, []DocumentChunk{chunk}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMapAdaptiveSingleFragmentResumesAndPublishes(t *testing.T) {
	for _, outputLimit := range []bool{false, true} {
		t.Run(fmt.Sprintf("output_limit_%t", outputLimit), func(t *testing.T) {
			dir, s, in := sourceMapFixture(t)
			var path string
			if err := s.db.QueryRow(`SELECT source_path FROM entries LIMIT 1`).Scan(&path); err != nil {
				t.Fatal(err)
			}
			var source strings.Builder
			for i := 0; i < 48; i++ {
				fmt.Fprintf(&source, "Факт %02d: формула α+β=γ; строка документа.\n", i)
			}
			source.WriteString("ПОСЛЕДНИЙ ФАКТ")
			seedSingleAdaptiveChunk(t, s, path, source.String())
			cfg, err := LoadConfigIn(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Answer.MaxTokens = 4096
			if err = SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
			if err != nil {
				t.Fatal(err)
			}
			p := &adaptiveTextProvider{limit: 300, outputLimit: outputLimit, failAfter: 1, calls: map[string]int{}}
			if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
				t.Fatal(worked, err)
			}
			tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || len(tasks) != 1 || tasks[0].Status != "failed" || !tasks[0].CanResume {
				t.Fatal(tasks, err)
			}
			runID := tasks[0].RunID
			if len(p.accepted) != 1 {
				t.Fatal("expected one saved leaf", p.accepted)
			}
			first := p.accepted[0]
			if _, err = ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
				t.Fatal(err)
			}
			p.failAfter = 0
			p.failSynthesis = true
			if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
				t.Fatal(worked, err)
			}
			tasks, err = ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || tasks[0].Status != "failed" {
				t.Fatal("expected synthesis interruption", tasks, err)
			}
			if strings.Join(p.accepted, "") != source.String() {
				t.Fatal("extraction did not finish before synthesis interruption")
			}
			if _, err = ResumeWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
				t.Fatal(err)
			}
			p.failSynthesis = false
			for i := 0; i < 2; i++ {
				if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
					t.Fatal(worked, err)
				}
			}
			tasks, err = ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || tasks[0].Status != "applied" || tasks[0].RunID != runID {
				t.Fatal(tasks, err)
			}
			if p.calls[first] != 1 || p.calls[source.String()] != 1 {
				t.Fatal("replayed saved work", p.calls)
			}
			for text, count := range p.calls {
				if len([]rune(text)) > p.limit && count != 1 {
					t.Fatal("replayed saved split marker", count)
				}
			}
			if strings.Join(p.accepted, "") != source.String() {
				t.Fatal("lost or reordered source text")
			}
			preview, err := s.LoadClassicMindMapAIPreview(runID)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Evidence) != 1 || preview.Evidence[0].Text != source.String() {
				t.Fatal("immutable manifest changed")
			}
			seen := map[string]bool{}
			for _, proposal := range preview.Proposals {
				for _, anchor := range proposal.Evidence {
					if !classicMindMapAIAnchorInManifest(anchor, preview.Evidence) || anchor.ChunkHash != ChunkContentHash(source.String()) || anchor.EvidenceHash != ChunkContentHash(anchor.Excerpt) {
						t.Fatal("invalid source anchor", anchor)
					}
					seen[anchor.Excerpt] = true
				}
			}
			for _, text := range p.accepted {
				if !seen[text] {
					t.Fatal("missing published excerpt")
				}
			}
			doc, err := s.LoadClassicMindMap(task.MapID)
			if err != nil || doc.Map.Revision != 2 {
				t.Fatal(doc, err)
			}
		})
	}
}

func TestAdaptiveIdenticalTextLeavesHaveDistinctIDs(t *testing.T) {
	s, entries := newClassicMindMapAITestStore(t)
	text := strings.Repeat("Ж", 1024)
	seedSingleAdaptiveChunk(t, s, entries[0].SourcePath, text)
	p := &adaptiveTextProvider{limit: 128, calls: map[string]int{}}
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(s, p), ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAINewMap, Prompt: "Создай карту"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.accepted) != 8 || strings.Join(p.accepted, "") != text {
		t.Fatal("missing repeated fragment", len(p.accepted))
	}
	if _, err = s.LoadClassicMindMapAIPreview(preview.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptiveMinimalFragmentFailureIsBounded(t *testing.T) {
	s, entries := newClassicMindMapAITestStore(t)
	seedSingleAdaptiveChunk(t, s, entries[0].SourcePath, strings.Repeat("Я", 1024))
	p := &adaptiveTextProvider{limit: 0, calls: map[string]int{}}
	_, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(s, p), ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAINewMap, Prompt: "Создай карту"}, nil)
	if !errors.Is(err, ErrAnswerContextLimit) {
		t.Fatal(err)
	}
	count := 0
	for _, n := range p.calls {
		count += n
	}
	if count != 4 {
		t.Fatal("unbounded or missing subdivision", count)
	}
}

func TestAdaptiveExcerptValidationRejectsForgedProvenance(t *testing.T) {
	s, entries := newClassicMindMapAITestStore(t)
	p := &adaptiveTextProvider{limit: 300, calls: map[string]int{}}
	seedSingleAdaptiveChunk(t, s, entries[0].SourcePath, strings.Repeat("Документ αβγ. ", 60))
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), classicMindMapAITestService(s, p), ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAINewMap, Prompt: "Создай карту"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var anchor EvidenceAnchor
	for _, proposal := range preview.Proposals {
		if len(proposal.Evidence) > 0 {
			anchor = proposal.Evidence[0]
			break
		}
	}
	if anchor.Excerpt == "" {
		t.Fatal("missing excerpt")
	}
	valid := ClassicMindMapAIPreview{Evidence: preview.Evidence, Proposals: []ClassicMindMapAIProposal{{ID: "test", Evidence: []EvidenceAnchor{anchor}}}}
	if err := validateStoredClassicMindMapAIPreview(valid); err != nil {
		t.Fatal("valid sub-excerpt rejected", err)
	}
	for _, mode := range []string{"excerpt", "hash", "revision", "chunk", "path", "page", "document", "block", "block_chunk", "citation"} {
		t.Run(mode, func(t *testing.T) {
			bad := anchor
			switch mode {
			case "excerpt":
				bad.Excerpt = "Придуманный факт"
				bad.EvidenceHash = ChunkContentHash(bad.Excerpt)
			case "hash":
				bad.EvidenceHash = ChunkContentHash("other")
			case "revision":
				bad.DocumentRevision = ChunkContentHash("other")
			case "chunk":
				bad.ChunkHash = ChunkContentHash("other")
			case "path":
				bad.SourcePath += ".other"
			case "page":
				bad.Page++
			case "document":
				bad.DocumentID += "x"
			case "block":
				bad.BlockIndex++
			case "block_chunk":
				bad.BlockChunkIndex++
			case "citation":
				bad.CitationID += "x"
			}
			check := ClassicMindMapAIPreview{Evidence: preview.Evidence, Proposals: []ClassicMindMapAIProposal{{ID: "test", Evidence: []EvidenceAnchor{bad}}}}
			if err := validateStoredClassicMindMapAIPreview(check); !errors.Is(err, ErrClassicMindMapAIChanged) {
				t.Fatal("accepted forgery", err)
			}
		})
	}
}
