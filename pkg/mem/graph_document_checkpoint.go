package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Version one recorded only citation coordinates, which deliberately survive a
// reimport. Keep those tables as historical data; their unknown inputs cannot
// establish completion of a current document pass.
const documentGraphSchema = `
CREATE TABLE IF NOT EXISTS knowledge_document_chunks_v2 (
 citation_id TEXT PRIMARY KEY, source_digest TEXT NOT NULL, input_digest TEXT NOT NULL,
 status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS knowledge_document_windows_v2 (
 citation_id TEXT NOT NULL, input_digest TEXT NOT NULL, window_hash TEXT NOT NULL,
 PRIMARY KEY(citation_id,input_digest,window_hash));`

const (
	documentSemanticWindowSize = 6000
	documentSemanticOverlap    = 256
)

type documentChunkPin struct {
	CitationID   string
	SourceDigest string
	InputDigest  string
}

// Hash the source independently of generation settings so read-only coverage
// can reject the last pass immediately after reimport, before a new run starts.
func documentSourceDigest(entry Entry) (string, error) {
	evidence := groundedEvidenceForEntry(entry, entry.Text, 0)
	data, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	return ChunkContentHash(string(data)), nil
}

func documentConfigurationDigest(answer AnswerConfig, focus string, lowConfidence float64) (string, error) {
	if lowConfidence <= 0 {
		lowConfidence = DefaultAnswerLowConfidence
	}
	data, err := json.Marshal(struct {
		Version       int
		Answer        AnswerConfig
		Focus         string
		LowConfidence float64
		System        string
		WindowSize    int
		Overlap       int
	}{2, answer, strings.TrimSpace(focus), lowConfidence,
		knowledgeExtractionSystemPrompt + knowledgeExtractionShapeGuidance,
		documentSemanticWindowSize, documentSemanticOverlap})
	if err != nil {
		return "", err
	}
	return ChunkContentHash(string(data)), nil
}

func documentInputPin(entry Entry, configurationDigest string) (documentChunkPin, error) {
	source, err := documentSourceDigest(entry)
	if err != nil {
		return documentChunkPin{}, err
	}
	id, _ := CitationForEntry(entry)
	return documentChunkPin{CitationID: id, SourceDigest: source,
		InputDigest: ChunkContentHash(source + "\x00" + configurationDigest)}, nil
}

// Set the requested pass for the entire document atomically. Identical inputs
// retain partial progress; changing either source or settings resets completion.
func (s *Store) prepareDocumentChunks(ctx context.Context, entries []Entry, configurationDigest string) ([]documentChunkPin, error) {
	pins := make([]documentChunkPin, len(entries))
	for i, entry := range entries {
		var err error
		pins[i], err = documentInputPin(entry, configurationDigest)
		if err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, pin := range pins {
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT input_digest FROM knowledge_document_chunks_v2 WHERE citation_id=?`, pin.CitationID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if previous != pin.InputDigest {
			// Returning to an older configuration/revision is a fresh pass: its
			// old window results may have been replaced by intervening outputs.
			if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge_document_windows_v2 WHERE citation_id=? AND input_digest=?`, pin.CitationID, pin.InputDigest); err != nil {
				return nil, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_document_chunks_v2
 (citation_id,source_digest,input_digest,status,reason) VALUES(?,?,?,'pending','')
 ON CONFLICT(citation_id) DO UPDATE SET
 source_digest=excluded.source_digest,input_digest=excluded.input_digest,
 status=CASE WHEN input_digest=excluded.input_digest THEN status ELSE 'pending' END,
 reason=CASE WHEN input_digest=excluded.input_digest THEN reason ELSE '' END`,
			pin.CitationID, pin.SourceDigest, pin.InputDigest)
		if err != nil {
			return nil, err
		}
	}
	return pins, tx.Commit()
}

func (s *Store) documentChunkStatus(ctx context.Context, pin documentChunkPin) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM knowledge_document_chunks_v2
 WHERE citation_id=? AND source_digest=? AND input_digest=?`, pin.CitationID, pin.SourceDigest, pin.InputDigest).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("document extraction input changed; start a new pass")
	}
	return status, err
}

func (s *Store) saveDocumentChunk(ctx context.Context, pin documentChunkPin, status, reason string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE knowledge_document_chunks_v2 SET status=?,reason=?
 WHERE citation_id=? AND source_digest=? AND input_digest=?`, status, reason, pin.CitationID, pin.SourceDigest, pin.InputDigest)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("document extraction input changed; start a new pass")
	}
	return err
}

// Reuse a legacy source node only when its complete saved anchor is identical.
// This retains manual labels/bodies and existing links on an unchanged edition.
// A new edition gets a new identity; neither old evidence nor its edits move.
func (s *Store) documentSourceSectionID(ctx context.Context, anchor EvidenceAnchor, window int) (string, error) {
	legacyID := "chunk-source-" + documentGraphID(anchor.CitationID+":"+strconv.Itoa(window))
	return s.documentSourceNodeID(ctx, legacyID, "chunk-source-", KnowledgeNodeSection, anchor, window)
}

func (s *Store) documentSourceNodeID(ctx context.Context, legacyID, prefix string, expectedKind KnowledgeNodeKind, anchor EvidenceAnchor, window int) (string, error) {
	var kind KnowledgeNodeKind
	var origin KnowledgeOrigin
	err := s.db.QueryRowContext(ctx, `SELECT kind,origin FROM knowledge_nodes WHERE id=?`, legacyID).Scan(&kind, &origin)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && kind == expectedKind && origin == KnowledgeOriginSource {
		anchors, err := loadKnowledgeEvidence(s.db, "knowledge_node_evidence", "node_id", legacyID)
		if err != nil {
			return "", err
		}
		if equalEvidenceAnchors(anchors, []EvidenceAnchor{anchor}) {
			return legacyID, nil
		}
	}
	data, err := json.Marshal(struct {
		Anchor EvidenceAnchor
		Window int
	}{anchor, window})
	if err != nil {
		return "", err
	}
	return prefix + documentGraphID("v2\x00"+string(data)), nil
}
