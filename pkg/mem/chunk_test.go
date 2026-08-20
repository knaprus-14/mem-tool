package mem

import (
	"strings"
	"testing"
)

func TestSplitSentencesPreservesBoundaryCharacters(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"russian", "Первое предложение. Следующая буква не теряется! Третье? Да.", []string{"Первое предложение.", "Следующая буква не теряется!", "Третье?", "Да."}},
		{"quotes and repeated punctuation", "Он спросил: «Правда?!» Ответ был коротким. Yes! Next.", []string{"Он спросил: «Правда?!»", "Ответ был коротким.", "Yes!", "Next."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSentences(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("splitSentences() returned %d parts, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSentenceBasedChunkingDoesNotLoseSentenceText(t *testing.T) {
	input := "Первое предложение. Второе идёт! Третье здесь?"
	for _, strategy := range []string{"sentence", "paragraph"} {
		t.Run(strategy, func(t *testing.T) {
			chunks := ChunkDocument(input, 24, 0, strategy)
			if len(chunks) < 2 {
				t.Fatalf("expected text to be split, got %d chunk(s)", len(chunks))
			}
			var texts []string
			for _, chunk := range chunks {
				texts = append(texts, chunk.Text)
			}
			if got := strings.Join(texts, " "); got != input {
				t.Fatalf("reassembled text = %q, want %q", got, input)
			}
		})
	}
}
