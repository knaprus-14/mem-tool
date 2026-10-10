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
		parts, ok := splitClassicMindMapAIEvidence(item)
		if !ok {
			return fmt.Errorf("%w: инструкции и минимальный фрагмент не помещаются в контекст %d символов", ErrClassicMindMapAIResourceLimit, budget)
		}
		for _, part := range parts {
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

// This is the v1 split rule used by durable split_text_v1 checkpoints. Keep it
// deterministic across restarts and retain every byte, including whitespace.
func splitClassicMindMapAIEvidence(item GroundedEvidence) ([]GroundedEvidence, bool) {
	runes := []rune(item.Text)
	if len(runes) < 256 {
		return nil, false
	}
	mid := len(runes) / 2
	for i := mid; i < len(runes)*3/4; i++ {
		if runes[i] == '\n' || runes[i] == '.' || runes[i] == ';' {
			mid = i + 1
			break
		}
	}
	if strings.TrimSpace(string(runes[:mid])) == "" || strings.TrimSpace(string(runes[mid:])) == "" {
		start, end := 0, len(runes)
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		if end-start < 2 {
			return nil, false
		}
		mid = (start + end) / 2
	}
	parts := make([]GroundedEvidence, 0, 2)
	for _, text := range []string{string(runes[:mid]), string(runes[mid:])} {
		part := item
		part.Text = text
		part.EvidenceHash = ChunkContentHash(text)
		part.Truncated = true
		parts = append(parts, part)
	}
	return parts, true
}
