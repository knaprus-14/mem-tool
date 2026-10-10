package mem

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The proposal is structured, but its validated text is materialized into Body.
// Existing storage, review digests, edits, exports and learning therefore retain
// all qualifiers without a parallel, potentially stale copy of the requirement.
type knowledgeRequirementQuote struct {
	CitationID string `json:"citation_id"`
	Text       string `json:"text"`
}

type knowledgeRequirementProposal struct {
	Statement  knowledgeRequirementQuote   `json:"statement"`
	Conditions []knowledgeRequirementQuote `json:"conditions,omitempty"`
	Exceptions []knowledgeRequirementQuote `json:"exceptions,omitempty"`
	Limits     []knowledgeRequirementQuote `json:"limits,omitempty"`
}

func materializeKnowledgeRequirement(p knowledgeNodeProposal, allowed map[string]GroundedEvidence) (string, []EvidenceAnchor, error) {
	if p.Kind != KnowledgeNodeClaim && p.Kind != KnowledgeNodeConstraint && p.Kind != KnowledgeNodeProcedure {
		return "", nil, fmt.Errorf("requirement is supported only for claim, constraint or procedure")
	}
	r := p.Requirement
	if 1+len(r.Conditions)+len(r.Exceptions)+len(r.Limits) > 32 {
		return "", nil, fmt.Errorf("requirement exceeds 32 quoted parts")
	}
	cited := make(map[string]bool, len(p.Citations))
	for _, id := range p.Citations {
		cited[id] = false
	}
	var parts []string
	var anchors []EvidenceAnchor
	seen := make(map[string]bool)
	for _, group := range []struct {
		label  string
		quotes []knowledgeRequirementQuote
	}{{"Требование", []knowledgeRequirementQuote{r.Statement}}, {"Условие", r.Conditions}, {"Исключение", r.Exceptions}, {"Ограничение", r.Limits}} {
		for _, q := range group.quotes {
			item, ok := allowed[q.CitationID]
			_, attached := cited[q.CitationID]
			original, unique := knowledgeRequirementSourceQuote(item.Text, q.Text)
			if !ok || !attached || !unique {
				return "", nil, fmt.Errorf("requirement %s must be an exact nonempty quote from an attached citation", group.label)
			}
			a, err := evidenceAnchorFromGrounded(item)
			if err != nil {
				return "", nil, err
			}
			a.Excerpt, a.EvidenceHash = original, ChunkContentHash(original)
			if err := validateEvidenceAnchor(a); err != nil {
				return "", nil, err
			}
			cited[q.CitationID] = true
			key := a.CitationID + "\x00" + a.EvidenceHash
			if !seen[key] {
				anchors = append(anchors, a)
				seen[key] = true
			}
			parts = append(parts, group.label+": "+original)
		}
	}
	for _, used := range cited {
		if !used {
			return "", nil, fmt.Errorf("every requirement citation must supply a quoted part")
		}
	}
	return strings.Join(parts, "\n\n"), anchors, nil
}

// Only whitespace may differ. Map the unique match back to original bytes so
// hashes, review and source highlighting never use reconstructed source text.
func knowledgeRequirementSourceQuote(source, quote string) (string, bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return "", false
	}
	if strings.Contains(source, quote) {
		return quote, true
	}
	var normalized []rune
	var starts, ends []int
	for offset, r := range source {
		end := offset + len(string(r))
		if unicode.IsSpace(r) {
			if len(normalized) > 0 && normalized[len(normalized)-1] == ' ' {
				ends[len(ends)-1] = end
				continue
			}
			r = ' '
		}
		normalized = append(normalized, r)
		starts = append(starts, offset)
		ends = append(ends, end)
	}
	needle := strings.Join(strings.Fields(quote), " ")
	text := string(normalized)
	index := strings.Index(text, needle)
	if index < 0 {
		return "", false
	}
	_, width := utf8.DecodeRuneInString(text[index:])
	if strings.Contains(text[index+width:], needle) {
		return "", false
	}
	start := utf8.RuneCountInString(text[:index])
	length := utf8.RuneCountInString(needle)
	return source[starts[start]:ends[start+length-1]], true
}
