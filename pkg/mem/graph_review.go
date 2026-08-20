package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type KnowledgeObjectType string

const (
	KnowledgeObjectNode KnowledgeObjectType = "node"
	KnowledgeObjectEdge KnowledgeObjectType = "edge"

	MaxKnowledgeReviewerRunes = 256
	MaxKnowledgeCommentRunes  = 4096
	MaxKnowledgeApprovalBatch = 500
)

var (
	ErrKnowledgeEvidenceNotCurrent = errors.New("knowledge object evidence is not current")
	ErrKnowledgeEvidenceChanged    = errors.New("knowledge object evidence digest changed")
	ErrKnowledgeEndpointsNotActive = errors.New("knowledge edge endpoints are not active")
)

type KnowledgeReviewItem struct {
	ObjectType       KnowledgeObjectType  `json:"object_type"`
	ID               string               `json:"id"`
	Kind             string               `json:"kind"`
	Label            string               `json:"label,omitempty"`
	Status           KnowledgeStatus      `json:"status"`
	Origin           KnowledgeOrigin      `json:"origin"`
	EvidenceState    EvidenceState        `json:"evidence_state"`
	EvidenceDigest   string               `json:"evidence_digest"`
	ReadyForApproval bool                 `json:"ready_for_approval"`
	Evidence         []EvidenceResolution `json:"evidence"`
}

type KnowledgeReviewSummary struct {
	Total           int `json:"total"`
	Draft           int `json:"draft"`
	Active          int `json:"active"`
	Resolved        int `json:"resolved"`
	Ready           int `json:"ready_for_approval"`
	CurrentEvidence int `json:"current_evidence"`
	StaleEvidence   int `json:"stale_evidence"`
	MissingEvidence int `json:"missing_evidence"`
}

type KnowledgeReviewReport struct {
	Summary KnowledgeReviewSummary `json:"summary"`
	Items   []KnowledgeReviewItem  `json:"items"`
}

type KnowledgeApprovalRequest struct {
	ObjectType             KnowledgeObjectType `json:"object_type"`
	ID                     string              `json:"id"`
	Reviewer               string              `json:"reviewer"`
	Comment                string              `json:"comment,omitempty"`
	ExpectedEvidenceDigest string              `json:"expected_evidence_digest,omitempty"`
}

type KnowledgeApprovalTarget struct {
	ObjectType             KnowledgeObjectType `json:"object_type"`
	ID                     string              `json:"id"`
	ExpectedEvidenceDigest string              `json:"expected_evidence_digest"`
	Comment                string              `json:"comment,omitempty"`
}

// KnowledgeApprovalManifest is the fail-closed on-disk format accepted by the
// CLI. Every target pins the digest exposed by map status --json.
type KnowledgeApprovalManifest struct {
	Reviewer string                    `json:"reviewer"`
	Comment  string                    `json:"comment,omitempty"`
	Objects  []KnowledgeApprovalTarget `json:"objects"`
}

type KnowledgeReviewRecord struct {
	ID             int64               `json:"id"`
	ObjectType     KnowledgeObjectType `json:"object_type"`
	ObjectID       string              `json:"object_id"`
	Action         string              `json:"action"`
	PreviousStatus KnowledgeStatus     `json:"previous_status"`
	NewStatus      KnowledgeStatus     `json:"new_status"`
	Reviewer       string              `json:"reviewer"`
	Comment        string              `json:"comment,omitempty"`
	EvidenceDigest string              `json:"evidence_digest"`
	Created        string              `json:"created"`
}

type KnowledgeApprovalResult struct {
	ObjectType     KnowledgeObjectType   `json:"object_type"`
	ID             string                `json:"id"`
	PreviousStatus KnowledgeStatus       `json:"previous_status"`
	Status         KnowledgeStatus       `json:"status"`
	EvidenceDigest string                `json:"evidence_digest"`
	Evidence       []EvidenceResolution  `json:"evidence"`
	Review         KnowledgeReviewRecord `json:"review"`
}

type KnowledgeBatchApprovalResult struct {
	Approved []KnowledgeApprovalResult `json:"approved"`
}

// KnowledgeEvidenceDigest returns an order-independent digest of the complete
// trusted anchors. It pins the exact revisions, coordinates and excerpts that
// a reviewer inspected.
func KnowledgeEvidenceDigest(anchors []EvidenceAnchor) (string, error) {
	encoded := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		if err := validateEvidenceAnchor(anchor); err != nil {
			return "", err
		}
		value, err := json.Marshal(anchor)
		if err != nil {
			return "", fmt.Errorf("encode knowledge evidence digest: %w", err)
		}
		encoded = append(encoded, string(value))
	}
	sort.Strings(encoded)
	canonical, err := json.Marshal(encoded)
	if err != nil {
		return "", fmt.Errorf("encode canonical knowledge evidence: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ReviewKnowledgeGraph reports the freshness of every source anchor. It does
// not mutate graph state; callers can use ReadyForApproval as a review queue.
func (s *Store) ReviewKnowledgeGraph() (KnowledgeReviewReport, error) {
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeReviewReport{}, err
	}
	report := KnowledgeReviewReport{Items: make([]KnowledgeReviewItem, 0, len(graph.Nodes)+len(graph.Edges))}
	nodeStatuses := make(map[string]KnowledgeStatus, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeStatuses[node.ID] = node.Status
		item, err := s.reviewKnowledgeItem(KnowledgeObjectNode, node.ID, string(node.Kind), node.Label, node.Status, node.Origin, node.Evidence)
		if err != nil {
			return KnowledgeReviewReport{}, err
		}
		report.Items = append(report.Items, item)
	}
	for _, edge := range graph.Edges {
		item, err := s.reviewKnowledgeItem(KnowledgeObjectEdge, edge.ID, string(edge.Kind), edge.Label, edge.Status, edge.Origin, edge.Evidence)
		if err != nil {
			return KnowledgeReviewReport{}, err
		}
		item.ReadyForApproval = item.ReadyForApproval && nodeStatuses[edge.From] == KnowledgeStatusActive && nodeStatuses[edge.To] == KnowledgeStatusActive
		report.Items = append(report.Items, item)
	}
	for _, item := range report.Items {
		report.Summary.Total++
		switch item.Status {
		case KnowledgeStatusDraft:
			report.Summary.Draft++
		case KnowledgeStatusActive:
			report.Summary.Active++
		case KnowledgeStatusResolved:
			report.Summary.Resolved++
		}
		if item.ReadyForApproval {
			report.Summary.Ready++
		}
		for _, evidence := range item.Evidence {
			switch evidence.State {
			case EvidenceCurrent:
				report.Summary.CurrentEvidence++
			case EvidenceStale:
				report.Summary.StaleEvidence++
			case EvidenceMissing:
				report.Summary.MissingEvidence++
			}
		}
	}
	return report, nil
}

func (s *Store) reviewKnowledgeItem(objectType KnowledgeObjectType, id, kind, label string, status KnowledgeStatus, origin KnowledgeOrigin, anchors []EvidenceAnchor) (KnowledgeReviewItem, error) {
	digest, err := KnowledgeEvidenceDigest(anchors)
	if err != nil {
		return KnowledgeReviewItem{}, fmt.Errorf("digest knowledge %s %q evidence: %w", objectType, id, err)
	}
	item := KnowledgeReviewItem{
		ObjectType: objectType, ID: id, Kind: kind, Label: label, Status: status, Origin: origin,
		EvidenceState: EvidenceCurrent, EvidenceDigest: digest, Evidence: make([]EvidenceResolution, 0, len(anchors)),
	}
	for _, anchor := range anchors {
		resolution := s.ResolveEvidenceAnchor(anchor)
		item.Evidence = append(item.Evidence, resolution)
		if resolution.State == EvidenceMissing || (resolution.State == EvidenceStale && item.EvidenceState != EvidenceMissing) {
			item.EvidenceState = resolution.State
		}
	}
	item.ReadyForApproval = status == KnowledgeStatusDraft && len(item.Evidence) > 0 && item.EvidenceState == EvidenceCurrent
	return item, nil
}

// ApproveKnowledgeObject preserves the original API while recording its actor.
// Interactive callers should use ApproveKnowledgeObjectWithReview instead.
func (s *Store) ApproveKnowledgeObject(objectType KnowledgeObjectType, id string) (KnowledgeApprovalResult, error) {
	return s.ApproveKnowledgeObjectWithReview(KnowledgeApprovalRequest{
		ObjectType: objectType, ID: id, Reviewer: "legacy-api",
	})
}

func (s *Store) ApproveKnowledgeObjectWithReview(request KnowledgeApprovalRequest) (KnowledgeApprovalResult, error) {
	result, err := s.approveKnowledgeObjects([]KnowledgeApprovalRequest{request}, false)
	if err != nil {
		return KnowledgeApprovalResult{}, err
	}
	return result.Approved[0], nil
}

// ApproveKnowledgeObjects applies a pinned batch atomically. All objects and
// audit records commit together; one invalid/stale target rolls everything back.
func (s *Store) ApproveKnowledgeObjects(requests []KnowledgeApprovalRequest) (KnowledgeBatchApprovalResult, error) {
	return s.approveKnowledgeObjects(requests, true)
}

type preparedKnowledgeApproval struct {
	request     KnowledgeApprovalRequest
	table       string
	previous    KnowledgeStatus
	fromID      string
	toID        string
	digest      string
	resolutions []EvidenceResolution
	inputIndex  int
}

func (s *Store) approveKnowledgeObjects(requests []KnowledgeApprovalRequest, requireDigest bool) (KnowledgeBatchApprovalResult, error) {
	if len(requests) == 0 {
		return KnowledgeBatchApprovalResult{}, errors.New("knowledge approval batch is empty")
	}
	if len(requests) > MaxKnowledgeApprovalBatch {
		return KnowledgeBatchApprovalResult{}, fmt.Errorf("knowledge approval batch exceeds %d objects", MaxKnowledgeApprovalBatch)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeBatchApprovalResult{}, fmt.Errorf("begin knowledge approval: %w", err)
	}
	rollback := func(cause error) (KnowledgeBatchApprovalResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeBatchApprovalResult{}, fmt.Errorf("%v; knowledge approval rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeBatchApprovalResult{}, cause
	}

	prepared := make([]preparedKnowledgeApproval, 0, len(requests))
	plannedNodes := make(map[string]bool)
	seen := make(map[string]bool, len(requests))
	for i, raw := range requests {
		request, err := normalizeKnowledgeApprovalRequest(raw, requireDigest)
		if err != nil {
			return rollback(fmt.Errorf("approval request %d: %w", i, err))
		}
		key := string(request.ObjectType) + "\x00" + request.ID
		if seen[key] {
			return rollback(fmt.Errorf("approval request %d duplicates %s %q", i, request.ObjectType, request.ID))
		}
		seen[key] = true

		table, evidenceTable, ownerColumn, err := knowledgeObjectTables(request.ObjectType)
		if err != nil {
			return rollback(err)
		}
		item := preparedKnowledgeApproval{request: request, table: table, inputIndex: i}
		if err := tx.QueryRow("SELECT status FROM "+table+" WHERE id = ?", request.ID).Scan(&item.previous); err != nil {
			if err == sql.ErrNoRows {
				return rollback(fmt.Errorf("knowledge %s %q not found", request.ObjectType, request.ID))
			}
			return rollback(fmt.Errorf("read knowledge %s %q: %w", request.ObjectType, request.ID, err))
		}
		if item.previous != KnowledgeStatusDraft {
			return rollback(fmt.Errorf("knowledge %s %q has status %q; only draft objects can be approved", request.ObjectType, request.ID, item.previous))
		}
		if request.ObjectType == KnowledgeObjectEdge {
			if err := tx.QueryRow(`SELECT from_node, to_node FROM knowledge_edges WHERE id = ?`, request.ID).Scan(&item.fromID, &item.toID); err != nil {
				return rollback(fmt.Errorf("read knowledge edge endpoints: %w", err))
			}
		} else {
			plannedNodes[request.ID] = true
		}
		anchors, err := loadKnowledgeEvidence(tx, evidenceTable, ownerColumn, request.ID)
		if err != nil {
			return rollback(fmt.Errorf("read knowledge approval evidence: %w", err))
		}
		if len(anchors) == 0 {
			return rollback(fmt.Errorf("knowledge %s %q has no evidence", request.ObjectType, request.ID))
		}
		item.digest, err = KnowledgeEvidenceDigest(anchors)
		if err != nil {
			return rollback(fmt.Errorf("digest knowledge %s %q evidence: %w", request.ObjectType, request.ID, err))
		}
		if request.ExpectedEvidenceDigest != "" && request.ExpectedEvidenceDigest != item.digest {
			return rollback(fmt.Errorf("%w: knowledge %s %q expected %s, current %s", ErrKnowledgeEvidenceChanged, request.ObjectType, request.ID, request.ExpectedEvidenceDigest, item.digest))
		}
		item.resolutions = make([]EvidenceResolution, 0, len(anchors))
		for _, anchor := range anchors {
			resolution := resolveEvidenceAnchorFromEntries(anchor, s.entries)
			item.resolutions = append(item.resolutions, resolution)
			if resolution.State != EvidenceCurrent {
				return rollback(fmt.Errorf("%w: %s is %s", ErrKnowledgeEvidenceNotCurrent, anchor.CitationID, resolution.State))
			}
		}
		prepared = append(prepared, item)
	}

	for _, item := range prepared {
		if item.request.ObjectType != KnowledgeObjectEdge {
			continue
		}
		fromActive, err := knowledgeNodeWillBeActive(tx, item.fromID, plannedNodes)
		if err != nil {
			return rollback(err)
		}
		toActive, err := knowledgeNodeWillBeActive(tx, item.toID, plannedNodes)
		if err != nil {
			return rollback(err)
		}
		if !fromActive || !toActive {
			return rollback(fmt.Errorf("%w: %s active=%t, %s active=%t", ErrKnowledgeEndpointsNotActive, item.fromID, fromActive, item.toID, toActive))
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	results := make([]KnowledgeApprovalResult, len(prepared))
	for phase := 0; phase < 2; phase++ {
		for _, item := range prepared {
			isEdge := item.request.ObjectType == KnowledgeObjectEdge
			if (phase == 0 && isEdge) || (phase == 1 && !isEdge) {
				continue
			}
			updated, err := tx.Exec("UPDATE "+item.table+" SET status = ?, updated = ? WHERE id = ? AND status = ?",
				KnowledgeStatusActive, now, item.request.ID, KnowledgeStatusDraft)
			if err != nil {
				return rollback(fmt.Errorf("approve knowledge %s %q: %w", item.request.ObjectType, item.request.ID, err))
			}
			rows, err := updated.RowsAffected()
			if err != nil || rows != 1 {
				return rollback(fmt.Errorf("knowledge %s %q changed during approval", item.request.ObjectType, item.request.ID))
			}
			review, err := appendKnowledgeReview(tx, item, now)
			if err != nil {
				return rollback(err)
			}
			results[item.inputIndex] = KnowledgeApprovalResult{
				ObjectType: item.request.ObjectType, ID: item.request.ID, PreviousStatus: item.previous,
				Status: KnowledgeStatusActive, EvidenceDigest: item.digest, Evidence: item.resolutions, Review: review,
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeBatchApprovalResult{}, fmt.Errorf("commit knowledge approval: %w", err)
	}
	return KnowledgeBatchApprovalResult{Approved: results}, nil
}

func normalizeKnowledgeApprovalRequest(request KnowledgeApprovalRequest, requireDigest bool) (KnowledgeApprovalRequest, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.Reviewer = strings.TrimSpace(request.Reviewer)
	request.Comment = strings.TrimSpace(request.Comment)
	request.ExpectedEvidenceDigest = strings.TrimSpace(request.ExpectedEvidenceDigest)
	if err := validateKnowledgeID(request.ID); err != nil {
		return KnowledgeApprovalRequest{}, err
	}
	if request.ObjectType != KnowledgeObjectNode && request.ObjectType != KnowledgeObjectEdge {
		return KnowledgeApprovalRequest{}, fmt.Errorf("unsupported knowledge object type %q", request.ObjectType)
	}
	if request.Reviewer == "" {
		return KnowledgeApprovalRequest{}, errors.New("knowledge approval reviewer is empty")
	}
	if !utf8.ValidString(request.Reviewer) || utf8.RuneCountInString(request.Reviewer) > MaxKnowledgeReviewerRunes {
		return KnowledgeApprovalRequest{}, fmt.Errorf("knowledge approval reviewer exceeds %d runes", MaxKnowledgeReviewerRunes)
	}
	if !utf8.ValidString(request.Comment) || utf8.RuneCountInString(request.Comment) > MaxKnowledgeCommentRunes {
		return KnowledgeApprovalRequest{}, fmt.Errorf("knowledge approval comment exceeds %d runes", MaxKnowledgeCommentRunes)
	}
	if requireDigest && request.ExpectedEvidenceDigest == "" {
		return KnowledgeApprovalRequest{}, errors.New("batch approval requires expected_evidence_digest")
	}
	if request.ExpectedEvidenceDigest != "" && (len(request.ExpectedEvidenceDigest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(request.ExpectedEvidenceDigest, "sha256:")) {
		return KnowledgeApprovalRequest{}, errors.New("invalid expected_evidence_digest")
	}
	if request.ExpectedEvidenceDigest != "" {
		if _, err := hex.DecodeString(strings.TrimPrefix(request.ExpectedEvidenceDigest, "sha256:")); err != nil {
			return KnowledgeApprovalRequest{}, errors.New("invalid expected_evidence_digest")
		}
	}
	return request, nil
}

func knowledgeObjectTables(objectType KnowledgeObjectType) (table, evidenceTable, ownerColumn string, err error) {
	switch objectType {
	case KnowledgeObjectNode:
		return "knowledge_nodes", "knowledge_node_evidence", "node_id", nil
	case KnowledgeObjectEdge:
		return "knowledge_edges", "knowledge_edge_evidence", "edge_id", nil
	default:
		return "", "", "", fmt.Errorf("unsupported knowledge object type %q", objectType)
	}
}

func knowledgeNodeWillBeActive(tx *sql.Tx, id string, planned map[string]bool) (bool, error) {
	if planned[id] {
		return true, nil
	}
	var status KnowledgeStatus
	if err := tx.QueryRow(`SELECT status FROM knowledge_nodes WHERE id = ?`, id).Scan(&status); err != nil {
		return false, fmt.Errorf("read edge endpoint %q status: %w", id, err)
	}
	return status == KnowledgeStatusActive, nil
}

func appendKnowledgeReview(tx *sql.Tx, item preparedKnowledgeApproval, created string) (KnowledgeReviewRecord, error) {
	record := KnowledgeReviewRecord{
		ObjectType: item.request.ObjectType, ObjectID: item.request.ID, Action: "approve",
		PreviousStatus: item.previous, NewStatus: KnowledgeStatusActive,
		Reviewer: item.request.Reviewer, Comment: item.request.Comment,
		EvidenceDigest: item.digest, Created: created,
	}
	result, err := tx.Exec(`INSERT INTO knowledge_reviews
(object_type, object_id, action, previous_status, new_status, reviewer, comment, evidence_digest, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ObjectType, record.ObjectID, record.Action,
		record.PreviousStatus, record.NewStatus, record.Reviewer, record.Comment, record.EvidenceDigest, record.Created)
	if err != nil {
		return KnowledgeReviewRecord{}, fmt.Errorf("append knowledge review: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return KnowledgeReviewRecord{}, fmt.Errorf("read knowledge review ID: %w", err)
	}
	return record, nil
}

// ListKnowledgeReviews returns newest records first. The table is append-only;
// re-extraction and later evidence changes do not rewrite earlier decisions.
func (s *Store) ListKnowledgeReviews(limit int) ([]KnowledgeReviewRecord, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("knowledge review limit must be between 1 and 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, object_type, object_id, action, previous_status, new_status,
reviewer, comment, evidence_digest, created FROM knowledge_reviews ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge reviews: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeReviewRecord, 0)
	for rows.Next() {
		var record KnowledgeReviewRecord
		if err := rows.Scan(&record.ID, &record.ObjectType, &record.ObjectID, &record.Action,
			&record.PreviousStatus, &record.NewStatus, &record.Reviewer, &record.Comment,
			&record.EvidenceDigest, &record.Created); err != nil {
			return nil, fmt.Errorf("read knowledge review: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list knowledge reviews: %w", err)
	}
	return records, nil
}
