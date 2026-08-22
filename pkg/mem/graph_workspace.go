package mem

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// KnowledgeWorkspaceCreateRequest creates a user-authored working object from
// one exact source-backed node. The expected values pin the parent state that
// was visible when the author opened the creation form.
type KnowledgeWorkspaceCreateRequest struct {
	ParentNodeID          string            `json:"parent_node_id"`
	Kind                  KnowledgeNodeKind `json:"kind"`
	Label                 string            `json:"label"`
	Body                  string            `json:"body,omitempty"`
	Author                string            `json:"author"`
	Comment               string            `json:"comment,omitempty"`
	ExpectedParentStatus  KnowledgeStatus   `json:"expected_parent_status"`
	ExpectedParentContent string            `json:"expected_parent_content_digest"`
	ExpectedEvidence      string            `json:"expected_evidence_digest"`
}

// KnowledgeWorkspaceCreationRecord is an immutable audit record for a manual
// node and its provenance edge.
type KnowledgeWorkspaceCreationRecord struct {
	ID                  int64                 `json:"id"`
	NodeID              string                `json:"node_id"`
	EdgeID              string                `json:"edge_id"`
	ParentNodeID        string                `json:"parent_node_id"`
	Kind                KnowledgeNodeKind     `json:"kind"`
	RelationKind        KnowledgeRelationKind `json:"relation_kind"`
	Author              string                `json:"author"`
	Comment             string                `json:"comment,omitempty"`
	ContentDigest       string                `json:"content_digest"`
	ParentContentDigest string                `json:"parent_content_digest"`
	EvidenceDigest      string                `json:"evidence_digest"`
	Created             string                `json:"created"`
}

type KnowledgeWorkspaceCreateResult struct {
	Node     KnowledgeNode                    `json:"node"`
	Edge     KnowledgeEdge                    `json:"edge"`
	Evidence []EvidenceResolution             `json:"evidence"`
	Creation KnowledgeWorkspaceCreationRecord `json:"creation"`
}

// CreateKnowledgeWorkspaceNode creates a draft manual note or question and a
// draft provenance edge in one transaction. Evidence is copied from the pinned
// parent only after every anchor is verified against the current corpus.
func (s *Store) CreateKnowledgeWorkspaceNode(request KnowledgeWorkspaceCreateRequest) (KnowledgeWorkspaceCreateResult, error) {
	request, err := normalizeKnowledgeWorkspaceCreateRequest(request)
	if err != nil {
		return KnowledgeWorkspaceCreateResult{}, err
	}
	nodeID, edgeID, err := newKnowledgeWorkspaceIDs()
	if err != nil {
		return KnowledgeWorkspaceCreateResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeWorkspaceCreateResult{}, fmt.Errorf("begin knowledge workspace creation: %w", err)
	}
	rollback := func(cause error) (KnowledgeWorkspaceCreateResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeWorkspaceCreateResult{}, fmt.Errorf("%v; knowledge workspace rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeWorkspaceCreateResult{}, cause
	}

	parent, err := loadKnowledgeObjectContent(tx, KnowledgeObjectNode, request.ParentNodeID)
	if err != nil {
		return rollback(err)
	}
	if parent.status != request.ExpectedParentStatus ||
		(parent.status != KnowledgeStatusActive && parent.status != KnowledgeStatusDraft) {
		return rollback(fmt.Errorf("%w: parent status is %q", ErrKnowledgeContentChanged, parent.status))
	}
	parentDigest, err := KnowledgeContentDigest(KnowledgeObjectNode, parent.label, parent.body)
	if err != nil {
		return rollback(err)
	}
	if parentDigest != request.ExpectedParentContent {
		return rollback(fmt.Errorf("%w: parent content no longer matches", ErrKnowledgeContentChanged))
	}
	anchors, err := loadKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", request.ParentNodeID)
	if err != nil {
		return rollback(fmt.Errorf("read parent knowledge evidence: %w", err))
	}
	evidenceDigest, err := KnowledgeEvidenceDigest(anchors)
	if err != nil {
		return rollback(err)
	}
	if evidenceDigest != request.ExpectedEvidence {
		return rollback(fmt.Errorf("%w: parent evidence no longer matches", ErrKnowledgeEvidenceChanged))
	}
	resolutions := make([]EvidenceResolution, 0, len(anchors))
	for _, anchor := range anchors {
		resolution := resolveEvidenceAnchorFromEntries(anchor, s.entries)
		resolutions = append(resolutions, resolution)
		if resolution.State != EvidenceCurrent {
			return rollback(fmt.Errorf("%w: %s is %s", ErrKnowledgeEvidenceNotCurrent, anchor.CitationID, resolution.State))
		}
	}

	relationKind := KnowledgeRelationDerivedFrom
	if request.Kind == KnowledgeNodeQuestion {
		relationKind = KnowledgeRelationAsks
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	node := KnowledgeNode{
		ID: nodeID, Kind: request.Kind, Label: request.Label, Body: request.Body,
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual,
		Created: now, Updated: now, Evidence: append([]EvidenceAnchor(nil), anchors...),
	}
	edge := KnowledgeEdge{
		ID: edgeID, From: nodeID, To: request.ParentNodeID, Kind: relationKind,
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual,
		Created: now, Updated: now, Evidence: append([]EvidenceAnchor(nil), anchors...),
	}
	if err := validateKnowledgeNode(node); err != nil {
		return rollback(err)
	}
	if err := validateKnowledgeEdge(edge); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_nodes
(id, kind, label, body, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, node.ID, node.Kind, node.Label, node.Body,
		node.Status, node.Origin, node.Confidence, node.Created, node.Updated); err != nil {
		return rollback(fmt.Errorf("create knowledge workspace node: %w", err))
	}
	for ordinal, anchor := range node.Evidence {
		if err := insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", node.ID, ordinal, anchor); err != nil {
			return rollback(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_edges
(id, from_node, to_node, kind, label, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, edge.ID, edge.From, edge.To, edge.Kind, edge.Label,
		edge.Status, edge.Origin, edge.Confidence, edge.Created, edge.Updated); err != nil {
		return rollback(fmt.Errorf("create knowledge workspace edge: %w", err))
	}
	for ordinal, anchor := range edge.Evidence {
		if err := insertKnowledgeEvidence(tx, "knowledge_edge_evidence", "edge_id", edge.ID, ordinal, anchor); err != nil {
			return rollback(err)
		}
	}
	contentDigest, err := KnowledgeContentDigest(KnowledgeObjectNode, node.Label, node.Body)
	if err != nil {
		return rollback(err)
	}
	record := KnowledgeWorkspaceCreationRecord{
		NodeID: node.ID, EdgeID: edge.ID, ParentNodeID: request.ParentNodeID,
		Kind: node.Kind, RelationKind: edge.Kind, Author: request.Author, Comment: request.Comment,
		ContentDigest: contentDigest, ParentContentDigest: parentDigest,
		EvidenceDigest: evidenceDigest, Created: now,
	}
	record, err = insertKnowledgeWorkspaceCreation(tx, record)
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeWorkspaceCreateResult{}, fmt.Errorf("commit knowledge workspace creation: %w", err)
	}
	return KnowledgeWorkspaceCreateResult{Node: node, Edge: edge, Evidence: resolutions, Creation: record}, nil
}

func normalizeKnowledgeWorkspaceCreateRequest(request KnowledgeWorkspaceCreateRequest) (KnowledgeWorkspaceCreateRequest, error) {
	request.ParentNodeID = strings.TrimSpace(request.ParentNodeID)
	request.Label = strings.TrimSpace(request.Label)
	request.Body = strings.TrimSpace(request.Body)
	request.Author = strings.TrimSpace(request.Author)
	request.Comment = strings.TrimSpace(request.Comment)
	if err := validateKnowledgeID(request.ParentNodeID); err != nil {
		return KnowledgeWorkspaceCreateRequest{}, fmt.Errorf("invalid parent node: %w", err)
	}
	if request.Kind != KnowledgeNodeNote && request.Kind != KnowledgeNodeQuestion {
		return KnowledgeWorkspaceCreateRequest{}, errors.New("knowledge workspace kind must be note or question")
	}
	if request.Label == "" || !utf8.ValidString(request.Label) || utf8.RuneCountInString(request.Label) > MaxKnowledgeLabelRunes {
		return KnowledgeWorkspaceCreateRequest{}, fmt.Errorf("knowledge workspace label must contain 1..%d runes", MaxKnowledgeLabelRunes)
	}
	if !utf8.ValidString(request.Body) || utf8.RuneCountInString(request.Body) > MaxKnowledgeBodyRunes {
		return KnowledgeWorkspaceCreateRequest{}, fmt.Errorf("knowledge workspace body exceeds %d runes", MaxKnowledgeBodyRunes)
	}
	if request.Author == "" || !utf8.ValidString(request.Author) || utf8.RuneCountInString(request.Author) > MaxKnowledgeReviewerRunes {
		return KnowledgeWorkspaceCreateRequest{}, fmt.Errorf("knowledge workspace author must contain 1..%d runes", MaxKnowledgeReviewerRunes)
	}
	if !utf8.ValidString(request.Comment) || utf8.RuneCountInString(request.Comment) > MaxKnowledgeCommentRunes {
		return KnowledgeWorkspaceCreateRequest{}, fmt.Errorf("knowledge workspace comment exceeds %d runes", MaxKnowledgeCommentRunes)
	}
	if !validKnowledgeStatus(request.ExpectedParentStatus) {
		return KnowledgeWorkspaceCreateRequest{}, errors.New("invalid expected parent status")
	}
	if err := validateSHA256Digest(request.ExpectedParentContent, "expected_parent_content_digest"); err != nil {
		return KnowledgeWorkspaceCreateRequest{}, err
	}
	if err := validateSHA256Digest(request.ExpectedEvidence, "expected_evidence_digest"); err != nil {
		return KnowledgeWorkspaceCreateRequest{}, err
	}
	return request, nil
}

func newKnowledgeWorkspaceIDs() (string, string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate knowledge workspace ID: %w", err)
	}
	token := hex.EncodeToString(raw)
	return "manual-node-" + token, "manual-edge-" + token, nil
}

func insertKnowledgeWorkspaceCreation(tx *sql.Tx, record KnowledgeWorkspaceCreationRecord) (KnowledgeWorkspaceCreationRecord, error) {
	result, err := tx.Exec(`INSERT INTO knowledge_workspace_creations
(node_id, edge_id, parent_node_id, kind, relation_kind, author, comment,
 content_digest, parent_content_digest, evidence_digest, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.NodeID, record.EdgeID, record.ParentNodeID,
		record.Kind, record.RelationKind, record.Author, record.Comment, record.ContentDigest,
		record.ParentContentDigest, record.EvidenceDigest, record.Created)
	if err != nil {
		return KnowledgeWorkspaceCreationRecord{}, fmt.Errorf("append knowledge workspace creation: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return KnowledgeWorkspaceCreationRecord{}, fmt.Errorf("read knowledge workspace creation ID: %w", err)
	}
	return record, nil
}

func (s *Store) ListKnowledgeWorkspaceCreations(limit int) ([]KnowledgeWorkspaceCreationRecord, error) {
	if limit <= 0 || limit > 10000 {
		return nil, errors.New("knowledge workspace creation limit must be between 1 and 10000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, node_id, edge_id, parent_node_id, kind, relation_kind,
author, comment, content_digest, parent_content_digest, evidence_digest, created
FROM knowledge_workspace_creations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge workspace creations: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeWorkspaceCreationRecord, 0)
	for rows.Next() {
		var record KnowledgeWorkspaceCreationRecord
		if err := rows.Scan(&record.ID, &record.NodeID, &record.EdgeID, &record.ParentNodeID,
			&record.Kind, &record.RelationKind, &record.Author, &record.Comment,
			&record.ContentDigest, &record.ParentContentDigest, &record.EvidenceDigest, &record.Created); err != nil {
			return nil, fmt.Errorf("read knowledge workspace creation: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list knowledge workspace creations: %w", err)
	}
	return records, nil
}
