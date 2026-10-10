package mem

import "fmt"

// New candidates pin each excerpt in Sources. Old stored runs only have long
// citation IDs: they remain usable exactly when those IDs are unambiguous.
func knowledgeLearningCandidateAnchors(candidate KnowledgeLearningCandidate, evidence []GroundedEvidence) ([]EvidenceAnchor, error) {
	byIdentity := make(map[string]GroundedEvidence, len(evidence))
	byCitation := make(map[string][]GroundedEvidence, len(evidence))
	for _, item := range evidence {
		byIdentity[groundedEvidenceIdentity(item)] = item
		byCitation[item.CitationID] = append(byCitation[item.CitationID], item)
	}
	refs := candidate.Sources
	if len(refs) == 0 {
		for _, id := range candidate.Citations {
			refs = append(refs, KnowledgeSelectionSourceRef{CitationID: id})
		}
	}
	if len(refs) == 0 || len(refs) > MaxKnowledgeLearningCitations {
		return nil, fmt.Errorf("%w: learning candidate has invalid source count", ErrKnowledgeSelectionChanged)
	}
	wanted := make(map[string]bool, len(candidate.Citations))
	for _, id := range candidate.Citations {
		wanted[id] = true
	}
	seen := make(map[string]bool, len(refs))
	found := make(map[string]bool, len(wanted))
	anchors := make([]EvidenceAnchor, 0, len(refs))
	for _, ref := range refs {
		var item GroundedEvidence
		var ok bool
		if ref.EvidenceHash != "" {
			item, ok = byIdentity[ref.CitationID+"\x00"+ref.EvidenceHash]
		} else if choices := byCitation[ref.CitationID]; len(choices) == 1 {
			item, ok = choices[0], true
		}
		if !ok || !wanted[ref.CitationID] {
			return nil, fmt.Errorf("%w: learning candidate source disappeared or is ambiguous", ErrKnowledgeSelectionChanged)
		}
		identity := groundedEvidenceIdentity(item)
		if seen[identity] {
			return nil, fmt.Errorf("%w: learning candidate source is duplicated", ErrKnowledgeSelectionChanged)
		}
		seen[identity], found[ref.CitationID] = true, true
		anchor, err := evidenceAnchorFromGrounded(item)
		if err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	if len(found) != len(wanted) {
		return nil, fmt.Errorf("%w: learning candidate citations differ from sources", ErrKnowledgeSelectionChanged)
	}
	return anchors, nil
}
