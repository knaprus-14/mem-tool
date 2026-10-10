package mem

import "errors"

// A citation names a chunk, while its evidence hash names the exact excerpt.
// Legacy evidence without a hash still has a deterministic text identity.
func groundedEvidenceIdentity(item GroundedEvidence) string {
	hash := item.EvidenceHash
	if hash == "" {
		hash = ChunkContentHash(item.Text)
	}
	return item.CitationID + "\x00" + hash
}

// Short refs identify exact excerpts. The long citation remains a compatible
// alias only when it identifies one excerpt in this particular model context.
func groundedEvidenceReferences(evidence []GroundedEvidence) (map[string]GroundedEvidence, map[string]bool, error) {
	allowed := make(map[string]GroundedEvidence, len(evidence)*2)
	ambiguous := make(map[string]bool)
	seen := make(map[string]bool, len(evidence))
	byCitation := make(map[string]GroundedEvidence, len(evidence))
	for _, item := range evidence {
		if !citationIDPattern.MatchString(item.CitationID) {
			return nil, nil, errors.New("evidence contains malformed citation ID")
		}
		key := groundedEvidenceIdentity(item)
		if seen[key] {
			return nil, nil, errors.New("evidence contains duplicate citation excerpt")
		}
		seen[key] = true
		if _, exists := byCitation[item.CitationID]; exists {
			ambiguous[item.CitationID] = true
		}
		byCitation[item.CitationID] = item
		if item.EvidenceRef != "" {
			if !evidenceRefPattern.MatchString(item.EvidenceRef) {
				return nil, nil, errors.New("evidence contains malformed evidence ref")
			}
			if _, exists := allowed[item.EvidenceRef]; exists {
				return nil, nil, errors.New("evidence contains duplicate answer reference")
			}
			allowed[item.EvidenceRef] = item
		}
	}
	for id, item := range byCitation {
		if !ambiguous[id] {
			allowed[id] = item
		}
	}
	return allowed, ambiguous, nil
}
