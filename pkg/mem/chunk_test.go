package mem

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestChunkDocumentNeverExceedsConfiguredRuneLimit(t *testing.T) {
	tests := []struct {
		strategy string
		text     string
	}{
		{"paragraph", strings.Repeat("абзац один два три\n\n", 12)},
		{"sentence", strings.Repeat("Первое предложение. Второе предложение! ", 12)},
		{"fixed", strings.Repeat("данные ", 40)},
	}
	const maxSize = 36
	const overlap = 12

	for _, tt := range tests {
		t.Run(tt.strategy, func(t *testing.T) {
			chunks := ChunkDocument(tt.text, maxSize, overlap, tt.strategy)
			if len(chunks) < 2 {
				t.Fatalf("test setup produced %d chunk(s)", len(chunks))
			}
			for i, chunk := range chunks {
				if chunk.Index != i {
					t.Fatalf("chunk index=%d, want %d", chunk.Index, i)
				}
				if size := utf8.RuneCountInString(chunk.Text); size > maxSize {
					t.Fatalf("chunk %d has %d runes, max=%d: %q", i, size, maxSize, chunk.Text)
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
