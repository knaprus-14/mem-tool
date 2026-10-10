package mem

import (
	"fmt"
	"strings"
	"unicode"
)

// Subdivide an oversized stored chunk without modifying the document/index.
// Every slice retains its original citation and has its own excerpt hash in
// the immutable manifest. No truncation discards the tail of the source.
func fitClassicMindMapAIEvidence(request ClassicMindMapAIPreviewRequest, base ClassicMindMapDocument, target ClassicMindMapNode, evidence []GroundedEvidence, budget int) ([]GroundedEvidence, error) {
	result := make([]GroundedEvidence, 0, len(evidence))
	var add func(GroundedEvidence) error
	add = func(item GroundedEvidence) error {
		item.EvidenceRef = fmt.Sprintf("E%d", len(result)+1)
		if _, err := buildClassicMindMapAIBatches(request, base, target, []GroundedEvidence{item}, budget); err == nil {
			result = append(result, item)
			return nil
		}
		runes := []rune(item.Text)
		if len(runes) < 256 {
			return fmt.Errorf("%w: инструкции и минимальный фрагмент не помещаются в контекст %d символов", ErrClassicMindMapAIResourceLimit, budget)
		}
		mid := len(runes) / 2
		// Prefer a sentence/paragraph boundary near the middle; preserve bytes of
		// both sides, including delimiters. Fall back to a Unicode rune boundary.
		for i := mid; i < len(runes)*3/4; i++ {
			if runes[i] == '\n' || runes[i] == '.' || runes[i] == ';' {
				mid = i + 1
				break
			}
		}
		if strings.TrimSpace(string(runes[:mid])) == "" || strings.TrimSpace(string(runes[mid:])) == "" {
			// Move the boundary into non-space content when whitespace dominates.
			start, end := 0, len(runes)
			for start < end && unicode.IsSpace(runes[start]) {
				start++
			}
			for end > start && unicode.IsSpace(runes[end-1]) {
				end--
			}
			if end-start < 2 {
				return fmt.Errorf("%w: oversized indivisible evidence", ErrClassicMindMapAIResourceLimit)
			}
			mid = (start + end) / 2
		}
		for _, text := range []string{string(runes[:mid]), string(runes[mid:])} {
			part := item
			part.Text = text
			part.EvidenceHash = ChunkContentHash(text)
			part.Truncated = true
			if err := add(part); err != nil {
				return err
			}
		}
		return nil
	}
	for _, item := range evidence {
		if err := add(item); err != nil {
			return nil, err
		}
	}
	return result, nil
}
