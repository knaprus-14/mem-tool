package mem

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrKnowledgeMapSourceNotFound    = errors.New("knowledge map source was not found")
	ErrKnowledgeMapSourceNotCurrent  = errors.New("knowledge map source evidence is not current")
	ErrKnowledgeMapSourceUnsupported = errors.New("knowledge map source cannot be opened at a physical PDF page")
	ErrKnowledgeMapSourceAmbiguous   = errors.New("knowledge map citation resolves to conflicting source coordinates")
)

// ResolveKnowledgeMapPDFSource resolves a citation only through evidence that
// is still current in the active graph. Callers never supply a filesystem path.
func (s *Store) ResolveKnowledgeMapPDFSource(citationID string) (EvidenceAnchor, error) {
	citationID = strings.TrimSpace(citationID)
	if !strings.HasPrefix(citationID, "cite-") || !citationIDPattern.MatchString(citationID) {
		return EvidenceAnchor{}, ErrKnowledgeMapSourceNotFound
	}
	report, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return EvidenceAnchor{}, fmt.Errorf("review knowledge graph source evidence: %w", err)
	}
	var found *EvidenceAnchor
	for _, item := range report.Items {
		for _, resolution := range item.Evidence {
			anchor := resolution.Anchor
			if anchor.CitationID != citationID {
				continue
			}
			if resolution.State != EvidenceCurrent {
				return EvidenceAnchor{}, fmt.Errorf("%w: %s", ErrKnowledgeMapSourceNotCurrent, resolution.State)
			}
			if found != nil && !sameKnowledgeMapSource(*found, anchor) {
				return EvidenceAnchor{}, ErrKnowledgeMapSourceAmbiguous
			}
			copy := anchor
			found = &copy
		}
	}
	if found == nil {
		return EvidenceAnchor{}, ErrKnowledgeMapSourceNotFound
	}
	if found.Page < 1 || !filepath.IsAbs(found.SourcePath) || filepath.Clean(found.SourcePath) != found.SourcePath ||
		!strings.EqualFold(filepath.Ext(found.SourcePath), ".pdf") {
		return EvidenceAnchor{}, ErrKnowledgeMapSourceUnsupported
	}
	return *found, nil
}

func sameKnowledgeMapSource(left, right EvidenceAnchor) bool {
	return left.DocumentID == right.DocumentID && left.DocumentRevision == right.DocumentRevision &&
		left.ChunkHash == right.ChunkHash && left.SourcePath == right.SourcePath && left.Page == right.Page
}
