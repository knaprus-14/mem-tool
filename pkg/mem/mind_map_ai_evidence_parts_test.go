package mem

import (
	"context"
	"strings"
	"testing"
)

func TestClassicMapOversizedEvidencePreservesEveryRuneAndPublishes(t *testing.T) {
	s, entries := newClassicMindMapAITestStore(t)
	chunks := validStructuredChunks()
	chunks = chunks[:1]
	chunks[0].ChunkIndex = 0
	chunks[0].TotalChunks = 1
	chunks[0].Provenance.BlockChunkIndex = 0
	chunks[0].Provenance.BlockTotalChunks = 1
	chunks[0].Text = strings.Repeat("Большой русский документ. Формула α+β=γ.\n", 1400) + "ПОСЛЕДНИЙ ФАКТ"
	chunks[0].Provenance.ChunkHash = ChunkContentHash(chunks[0].Text)
	if err := s.ReplaceDocumentChunks(entries[0].SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	p := &resumableMapProvider{calls: map[string]int{}}
	service := classicMindMapAITestService(s, p)
	service.Config.ContextChars = 6500
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), service, ClassicMindMapAIPreviewRequest{Action: ClassicMindMapAINewMap, Prompt: "Построй карту"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, e := range preview.Evidence {
		text.WriteString(e.Text)
		if e.ChunkHash != chunks[0].Provenance.ChunkHash || e.EvidenceHash != ChunkContentHash(e.Text) {
			t.Fatal("provenance changed")
		}
	}
	if text.String() != chunks[0].Text || len(preview.Evidence) < 2 {
		t.Fatal("source truncated")
	}
	if _, err = s.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{RunID: preview.RunID}); err != nil {
		t.Fatal(err)
	}
}

func TestContextLimitClassificationIsNarrow(t *testing.T) {
	for _, s := range []string{"maximum context length is 2048", "prompt is too long"} {
		if !isAnswerContextLimit([]byte(s)) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"unauthorized", "context deadline exceeded", "model not found", "temporary network error"} {
		if isAnswerContextLimit([]byte(s)) {
			t.Fatal(s)
		}
	}
}
