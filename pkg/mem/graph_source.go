package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

var (
	ErrKnowledgeMapSourceNotFound    = errors.New("knowledge map source was not found")
	ErrKnowledgeMapSourceNotCurrent  = errors.New("knowledge map source evidence is not current")
	ErrKnowledgeMapSourceUnsupported = errors.New("knowledge map source cannot be opened at a physical PDF page")
	ErrKnowledgeMapSourceAmbiguous   = errors.New("knowledge map citation resolves to conflicting source coordinates")
	ErrKnowledgeMapSourceInvalidPin  = errors.New("knowledge map source revision pin is invalid")
)

// Pins select evidence already present in the graph. They never accept a path,
// supply new evidence or authorize access to a historical/newer physical file.
type knowledgeMapSourcePin struct {
	Revision     string
	ChunkHash    string
	EvidenceHash string
}

func parseKnowledgeMapSourcePin(values url.Values) (knowledgeMapSourcePin, error) {
	keys := []string{"revision", "chunk_hash", "evidence_hash"}
	present := 0
	for _, key := range keys {
		if _, ok := values[key]; ok {
			present++
		}
	}
	if present == 0 {
		return knowledgeMapSourcePin{}, nil
	}
	for _, key := range keys {
		if len(values[key]) != 1 || !isSHA256ContentHash(values[key][0]) {
			return knowledgeMapSourcePin{}, ErrKnowledgeMapSourceInvalidPin
		}
	}
	return knowledgeMapSourcePin{values.Get(keys[0]), values.Get(keys[1]), values.Get(keys[2])}, nil
}

// ResolveKnowledgeMapPDFSource resolves a citation only through evidence that
// is still current in the active graph. Callers never supply a filesystem path.
func (s *Store) ResolveKnowledgeMapPDFSource(citationID string) (applogResult0 EvidenceAnchor, applogResult1 error) {
	defer applog.Track("pkg.mem.Store.ResolveKnowledgeMapPDFSource")(&applogResult1)
	return s.resolveKnowledgeMapPDFSourcePinned(citationID, knowledgeMapSourcePin{})
}

func (s *Store) resolveKnowledgeMapPDFSourcePinned(citationID string, pin knowledgeMapSourcePin) (EvidenceAnchor, error) {
	anchor, err := s.resolveKnowledgeMapSourcePinned(citationID, pin)
	if err != nil {
		return EvidenceAnchor{}, err
	}
	if anchor.Page < 1 || !filepath.IsAbs(anchor.SourcePath) || filepath.Clean(anchor.SourcePath) != anchor.SourcePath ||
		!strings.EqualFold(filepath.Ext(anchor.SourcePath), ".pdf") {
		return EvidenceAnchor{}, ErrKnowledgeMapSourceUnsupported
	}
	return anchor, nil
}

func (s *Store) resolveKnowledgeMapSource(citationID string) (EvidenceAnchor, error) {
	return s.resolveKnowledgeMapSourcePinned(citationID, knowledgeMapSourcePin{})
}

func (s *Store) resolveKnowledgeMapSourcePinned(citationID string, pin knowledgeMapSourcePin) (EvidenceAnchor, error) {
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
			if pin != (knowledgeMapSourcePin{}) && (anchor.DocumentRevision != pin.Revision || anchor.ChunkHash != pin.ChunkHash || anchor.EvidenceHash != pin.EvidenceHash) {
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
	return *found, nil
}

type knowledgeMapSourceText struct {
	Anchor EvidenceAnchor `json:"anchor"`
	Text   string         `json:"text"`
}

// Fetch the full imported chunk, not an arbitrary path or a newer revision.
func (s *Store) knowledgeMapSourceContext(ctx context.Context, citationID string) (knowledgeMapSourceText, error) {
	return s.knowledgeMapSourceContextPinned(ctx, citationID, knowledgeMapSourcePin{})
}

func (s *Store) knowledgeMapSourceContextPinned(ctx context.Context, citationID string, pin knowledgeMapSourcePin) (knowledgeMapSourceText, error) {
	a, err := s.resolveKnowledgeMapSourcePinned(citationID, pin)
	if err != nil {
		return knowledgeMapSourceText{}, err
	}
	var text string
	err = s.db.QueryRowContext(ctx, `SELECT text FROM entries WHERE document_id=? AND document_revision=? AND chunk_hash=? AND source_path=? AND page=? AND block_index=? AND block_chunk_index=?`,
		a.DocumentID, a.DocumentRevision, a.ChunkHash, a.SourcePath, a.Page, a.BlockIndex, a.BlockChunkIndex).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgeMapSourceText{}, ErrKnowledgeMapSourceNotCurrent
	}
	if err != nil {
		return knowledgeMapSourceText{}, err
	}
	if ChunkContentHash(text) != a.ChunkHash || !strings.Contains(text, a.Excerpt) {
		return knowledgeMapSourceText{}, ErrKnowledgeMapSourceNotCurrent
	}
	return knowledgeMapSourceText{Anchor: a, Text: text}, nil
}

func sameKnowledgeMapSource(left, right EvidenceAnchor) bool {
	return left.DocumentID == right.DocumentID && left.DocumentRevision == right.DocumentRevision &&
		left.ChunkHash == right.ChunkHash && left.SourcePath == right.SourcePath && left.Page == right.Page
}
