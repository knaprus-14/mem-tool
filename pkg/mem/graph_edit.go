package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KnowledgeEditActionEdit string = "edit"
	KnowledgeEditActionUndo string = "undo"
)

var (
	ErrKnowledgeContentChanged    = errors.New("knowledge object content changed")
	ErrKnowledgeEditChanged       = errors.New("knowledge edit history changed")
	ErrKnowledgeEditNotReversible = errors.New("knowledge edit is not reversible")
)

// KnowledgeEditRecord is an immutable content change. Internal IDs and
// digests pin concurrent state but are not presented as primary UI content.
type KnowledgeEditRecord struct {
	ID                    int64               `json:"id"`
	ObjectType            KnowledgeObjectType `json:"object_type"`
	ObjectID              string              `json:"object_id"`
	Action                string              `json:"action"`
	PreviousStatus        KnowledgeStatus     `json:"previous_status"`
	NewStatus             KnowledgeStatus     `json:"new_status"`
	PreviousLabel         string              `json:"previous_label"`
	PreviousBody          string              `json:"previous_body,omitempty"`
	NewLabel              string              `json:"new_label"`
	NewBody               string              `json:"new_body,omitempty"`
	PreviousContentDigest string              `json:"previous_content_digest"`
	NewContentDigest      string              `json:"new_content_digest"`
	EvidenceDigest        string              `json:"evidence_digest"`
	RevertsEditID         int64               `json:"reverts_edit_id,omitempty"`
	Editor                string              `json:"editor"`
	Comment               string              `json:"comment,omitempty"`
	Created               string              `json:"created"`
}

// KnowledgeEditRequest pins the exact content, status, evidence and latest
// edit visible in the live map before replacing a label or body.
type KnowledgeEditRequest struct {
	ObjectType             KnowledgeObjectType `json:"object_type"`
	ID                     string              `json:"id"`
	Editor                 string              `json:"editor"`
	Comment                string              `json:"comment,omitempty"`
	Label                  string              `json:"label"`
	Body                   string              `json:"body,omitempty"`
	ExpectedStatus         KnowledgeStatus     `json:"expected_status"`
	ExpectedContentDigest  string              `json:"expected_content_digest"`
	ExpectedEvidenceDigest string              `json:"expected_evidence_digest"`
	ExpectedEditID         int64               `json:"expected_edit_id"`
}

// KnowledgeEditUndoRequest reverses only the newest edit record and appends a
// new audit row. The original record is never rewritten or deleted.
type KnowledgeEditUndoRequest struct {
	ObjectType             KnowledgeObjectType `json:"object_type"`
	ID                     string              `json:"id"`
	Editor                 string              `json:"editor"`
	Comment                string              `json:"comment,omitempty"`
	ExpectedStatus         KnowledgeStatus     `json:"expected_status"`
	ExpectedContentDigest  string              `json:"expected_content_digest"`
	ExpectedEvidenceDigest string              `json:"expected_evidence_digest"`
	ExpectedEditID         int64               `json:"expected_edit_id"`
}

type KnowledgeEditResult struct {
	ObjectType     KnowledgeObjectType  `json:"object_type"`
	ID             string               `json:"id"`
	PreviousStatus KnowledgeStatus      `json:"previous_status"`
	Status         KnowledgeStatus      `json:"status"`
	Label          string               `json:"label"`
	Body           string               `json:"body,omitempty"`
	ContentDigest  string               `json:"content_digest"`
	EvidenceDigest string               `json:"evidence_digest"`
	Evidence       []EvidenceResolution `json:"evidence"`
	Edit           KnowledgeEditRecord  `json:"edit"`
}

// KnowledgeContentDigest pins the user-visible content independently from
// source evidence and review status.
func KnowledgeContentDigest(objectType KnowledgeObjectType, label, body string) (string, error) {
	if objectType != KnowledgeObjectNode && objectType != KnowledgeObjectEdge {
		return "", fmt.Errorf("unsupported knowledge object type %q", objectType)
	}
	if !utf8.ValidString(label) || !utf8.ValidString(body) {
		return "", errors.New("knowledge object content is not valid UTF-8")
	}
	if objectType == KnowledgeObjectEdge && body != "" {
		return "", errors.New("knowledge edge has no body")
	}
	canonical, err := json.Marshal(struct {
		ObjectType KnowledgeObjectType `json:"object_type"`
		Label      string              `json:"label"`
		Body       string              `json:"body"`
	}{ObjectType: objectType, Label: label, Body: body})
	if err != nil {
		return "", fmt.Errorf("encode knowledge content digest: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *Store) EditKnowledgeObject(request KnowledgeEditRequest) (KnowledgeEditResult, error) {
	request, err := normalizeKnowledgeEditRequest(request)
	if err != nil {
		return KnowledgeEditResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeEditResult{}, fmt.Errorf("begin knowledge edit: %w", err)
	}
	rollback := func(cause error) (KnowledgeEditResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeEditResult{}, fmt.Errorf("%v; knowledge edit rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeEditResult{}, cause
	}

	current, err := loadKnowledgeObjectContent(tx, request.ObjectType, request.ID)
	if err != nil {
		return rollback(err)
	}
	if current.status == KnowledgeStatusResolved {
		return rollback(errors.New("resolved knowledge object cannot be edited"))
	}
	if current.status != request.ExpectedStatus {
		return rollback(fmt.Errorf("%w: expected status %q, current status is %q", ErrKnowledgeContentChanged, request.ExpectedStatus, current.status))
	}
	currentDigest, err := KnowledgeContentDigest(request.ObjectType, current.label, current.body)
	if err != nil {
		return rollback(err)
	}
	if currentDigest != request.ExpectedContentDigest {
		return rollback(fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeContentChanged, request.ExpectedContentDigest, currentDigest))
	}
	latest, found, err := latestKnowledgeEdit(tx, request.ObjectType, request.ID)
	if err != nil {
		return rollback(err)
	}
	latestID := int64(0)
	if found {
		latestID = latest.ID
	}
	if latestID != request.ExpectedEditID {
		return rollback(fmt.Errorf("%w: latest edit no longer matches", ErrKnowledgeEditChanged))
	}
	if current.label == request.Label && current.body == request.Body {
		return rollback(errors.New("knowledge edit does not change content"))
	}
	_, evidenceTable, ownerColumn, err := knowledgeObjectTables(request.ObjectType)
	if err != nil {
		return rollback(err)
	}
	evidenceDigest, resolutions, err := loadPinnedCurrentKnowledgeEvidence(tx, evidenceTable, ownerColumn, request.ID, request.ExpectedEvidenceDigest, s.entries)
	if err != nil {
		return rollback(err)
	}
	newStatus := current.status
	if current.status == KnowledgeStatusActive || current.status == KnowledgeStatusRejected {
		newStatus = KnowledgeStatusDraft
	}
	if request.ObjectType == KnowledgeObjectNode && current.status == KnowledgeStatusActive {
		if err := requireNoActiveKnowledgeRelations(tx, request.ID); err != nil {
			return rollback(err)
		}
	}
	newDigest, err := KnowledgeContentDigest(request.ObjectType, request.Label, request.Body)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var updated sql.Result
	if request.ObjectType == KnowledgeObjectNode {
		updated, err = tx.Exec(`UPDATE knowledge_nodes SET label = ?, body = ?, status = ?, updated = ?
WHERE id = ? AND status = ? AND label = ? AND body = ?`, request.Label, request.Body, newStatus, now,
			request.ID, current.status, current.label, current.body)
	} else {
		updated, err = tx.Exec(`UPDATE knowledge_edges SET label = ?, status = ?, updated = ?
WHERE id = ? AND status = ? AND label = ?`, request.Label, newStatus, now,
			request.ID, current.status, current.label)
	}
	if err != nil {
		return rollback(fmt.Errorf("edit knowledge %s %q: %w", request.ObjectType, request.ID, err))
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return rollback(fmt.Errorf("%w: knowledge %s %q changed during edit", ErrKnowledgeContentChanged, request.ObjectType, request.ID))
	}
	record, err := insertKnowledgeEditRecord(tx, KnowledgeEditRecord{
		ObjectType: request.ObjectType, ObjectID: request.ID, Action: KnowledgeEditActionEdit,
		PreviousStatus: current.status, NewStatus: newStatus,
		PreviousLabel: current.label, PreviousBody: current.body, NewLabel: request.Label, NewBody: request.Body,
		PreviousContentDigest: currentDigest, NewContentDigest: newDigest, EvidenceDigest: evidenceDigest,
		Editor: request.Editor, Comment: request.Comment, Created: now,
	})
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeEditResult{}, fmt.Errorf("commit knowledge edit: %w", err)
	}
	return KnowledgeEditResult{
		ObjectType: request.ObjectType, ID: request.ID, PreviousStatus: current.status, Status: newStatus,
		Label: request.Label, Body: request.Body, ContentDigest: newDigest,
		EvidenceDigest: evidenceDigest, Evidence: resolutions, Edit: record,
	}, nil
}

func (s *Store) UndoKnowledgeEdit(request KnowledgeEditUndoRequest) (KnowledgeEditResult, error) {
	request, err := normalizeKnowledgeEditUndoRequest(request)
	if err != nil {
		return KnowledgeEditResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeEditResult{}, fmt.Errorf("begin knowledge edit undo: %w", err)
	}
	rollback := func(cause error) (KnowledgeEditResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeEditResult{}, fmt.Errorf("%v; knowledge edit undo rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeEditResult{}, cause
	}

	current, err := loadKnowledgeObjectContent(tx, request.ObjectType, request.ID)
	if err != nil {
		return rollback(err)
	}
	if current.status != request.ExpectedStatus {
		return rollback(fmt.Errorf("%w: expected status %q, current status is %q", ErrKnowledgeContentChanged, request.ExpectedStatus, current.status))
	}
	currentDigest, err := KnowledgeContentDigest(request.ObjectType, current.label, current.body)
	if err != nil {
		return rollback(err)
	}
	if currentDigest != request.ExpectedContentDigest {
		return rollback(fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeContentChanged, request.ExpectedContentDigest, currentDigest))
	}
	latest, found, err := latestKnowledgeEdit(tx, request.ObjectType, request.ID)
	if err != nil {
		return rollback(err)
	}
	if !found || latest.ID != request.ExpectedEditID || latest.NewStatus != current.status || latest.NewContentDigest != currentDigest {
		return rollback(fmt.Errorf("%w: latest edit or object content no longer matches", ErrKnowledgeEditChanged))
	}
	if latest.Action != KnowledgeEditActionEdit {
		return rollback(fmt.Errorf("%w: latest action is %q", ErrKnowledgeEditNotReversible, latest.Action))
	}
	_, evidenceTable, ownerColumn, err := knowledgeObjectTables(request.ObjectType)
	if err != nil {
		return rollback(err)
	}
	evidenceDigest, resolutions, err := loadPinnedCurrentKnowledgeEvidence(tx, evidenceTable, ownerColumn, request.ID, request.ExpectedEvidenceDigest, s.entries)
	if err != nil {
		return rollback(err)
	}
	if latest.EvidenceDigest != evidenceDigest {
		return rollback(fmt.Errorf("%w: edit evidence no longer matches", ErrKnowledgeEvidenceChanged))
	}
	if request.ObjectType == KnowledgeObjectNode && latest.PreviousStatus == KnowledgeStatusActive {
		if err := requireNoActiveKnowledgeRelations(tx, request.ID); err != nil {
			return rollback(err)
		}
	}
	if request.ObjectType == KnowledgeObjectEdge && latest.PreviousStatus == KnowledgeStatusActive {
		var fromID, toID string
		if err := tx.QueryRow(`SELECT from_node, to_node FROM knowledge_edges WHERE id = ?`, request.ID).Scan(&fromID, &toID); err != nil {
			return rollback(fmt.Errorf("read knowledge edge endpoints: %w", err))
		}
		fromActive, err := knowledgeNodeWillBeActive(tx, fromID, nil)
		if err != nil {
			return rollback(err)
		}
		toActive, err := knowledgeNodeWillBeActive(tx, toID, nil)
		if err != nil {
			return rollback(err)
		}
		if !fromActive || !toActive {
			return rollback(ErrKnowledgeEndpointsNotActive)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var updated sql.Result
	if request.ObjectType == KnowledgeObjectNode {
		updated, err = tx.Exec(`UPDATE knowledge_nodes SET label = ?, body = ?, status = ?, updated = ?
WHERE id = ? AND status = ? AND label = ? AND body = ?`, latest.PreviousLabel, latest.PreviousBody,
			latest.PreviousStatus, now, request.ID, current.status, current.label, current.body)
	} else {
		updated, err = tx.Exec(`UPDATE knowledge_edges SET label = ?, status = ?, updated = ?
WHERE id = ? AND status = ? AND label = ?`, latest.PreviousLabel, latest.PreviousStatus,
			now, request.ID, current.status, current.label)
	}
	if err != nil {
		return rollback(fmt.Errorf("undo knowledge edit %s %q: %w", request.ObjectType, request.ID, err))
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return rollback(fmt.Errorf("%w: knowledge %s %q changed during edit undo", ErrKnowledgeContentChanged, request.ObjectType, request.ID))
	}
	record, err := insertKnowledgeEditRecord(tx, KnowledgeEditRecord{
		ObjectType: request.ObjectType, ObjectID: request.ID, Action: KnowledgeEditActionUndo,
		PreviousStatus: current.status, NewStatus: latest.PreviousStatus,
		PreviousLabel: current.label, PreviousBody: current.body,
		NewLabel: latest.PreviousLabel, NewBody: latest.PreviousBody,
		PreviousContentDigest: currentDigest, NewContentDigest: latest.PreviousContentDigest,
		EvidenceDigest: evidenceDigest, RevertsEditID: latest.ID,
		Editor: request.Editor, Comment: request.Comment, Created: now,
	})
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeEditResult{}, fmt.Errorf("commit knowledge edit undo: %w", err)
	}
	return KnowledgeEditResult{
		ObjectType: request.ObjectType, ID: request.ID, PreviousStatus: current.status,
		Status: latest.PreviousStatus, Label: latest.PreviousLabel, Body: latest.PreviousBody,
		ContentDigest: latest.PreviousContentDigest, EvidenceDigest: evidenceDigest,
		Evidence: resolutions, Edit: record,
	}, nil
}

type knowledgeObjectContent struct {
	label  string
	body   string
	status KnowledgeStatus
}

func loadKnowledgeObjectContent(tx *sql.Tx, objectType KnowledgeObjectType, id string) (knowledgeObjectContent, error) {
	var content knowledgeObjectContent
	var err error
	switch objectType {
	case KnowledgeObjectNode:
		err = tx.QueryRow(`SELECT label, body, status FROM knowledge_nodes WHERE id = ?`, id).Scan(&content.label, &content.body, &content.status)
	case KnowledgeObjectEdge:
		err = tx.QueryRow(`SELECT label, status FROM knowledge_edges WHERE id = ?`, id).Scan(&content.label, &content.status)
	default:
		return content, fmt.Errorf("unsupported knowledge object type %q", objectType)
	}
	if err == sql.ErrNoRows {
		return content, fmt.Errorf("knowledge %s %q not found: %w", objectType, id, sql.ErrNoRows)
	}
	if err != nil {
		return content, fmt.Errorf("read knowledge %s %q content: %w", objectType, id, err)
	}
	return content, nil
}

func requireNoActiveKnowledgeRelations(tx *sql.Tx, nodeID string) error {
	var activeRelations int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_edges WHERE status = ? AND (from_node = ? OR to_node = ?)`,
		KnowledgeStatusActive, nodeID, nodeID).Scan(&activeRelations); err != nil {
		return fmt.Errorf("count active knowledge relations: %w", err)
	}
	if activeRelations > 0 {
		return fmt.Errorf("%w: node %q has %d active relations", ErrKnowledgeActiveRelations, nodeID, activeRelations)
	}
	return nil
}

func normalizeKnowledgeEditRequest(request KnowledgeEditRequest) (KnowledgeEditRequest, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.Editor = strings.TrimSpace(request.Editor)
	request.Comment = strings.TrimSpace(request.Comment)
	request.Label = strings.TrimSpace(request.Label)
	request.Body = strings.TrimSpace(request.Body)
	if err := validateKnowledgeEditPins(request.ObjectType, request.ID, request.Editor, request.Comment,
		request.ExpectedStatus, request.ExpectedContentDigest, request.ExpectedEvidenceDigest, request.ExpectedEditID, false); err != nil {
		return KnowledgeEditRequest{}, err
	}
	if !utf8.ValidString(request.Label) || utf8.RuneCountInString(request.Label) > MaxKnowledgeLabelRunes {
		return KnowledgeEditRequest{}, fmt.Errorf("knowledge label exceeds %d runes", MaxKnowledgeLabelRunes)
	}
	if request.ObjectType == KnowledgeObjectNode {
		if request.Label == "" {
			return KnowledgeEditRequest{}, errors.New("knowledge node label is empty")
		}
		if !utf8.ValidString(request.Body) || utf8.RuneCountInString(request.Body) > MaxKnowledgeBodyRunes {
			return KnowledgeEditRequest{}, fmt.Errorf("knowledge node body exceeds %d runes", MaxKnowledgeBodyRunes)
		}
	} else if request.Body != "" {
		return KnowledgeEditRequest{}, errors.New("knowledge edge has no body")
	}
	return request, nil
}

func normalizeKnowledgeEditUndoRequest(request KnowledgeEditUndoRequest) (KnowledgeEditUndoRequest, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.Editor = strings.TrimSpace(request.Editor)
	request.Comment = strings.TrimSpace(request.Comment)
	if err := validateKnowledgeEditPins(request.ObjectType, request.ID, request.Editor, request.Comment,
		request.ExpectedStatus, request.ExpectedContentDigest, request.ExpectedEvidenceDigest, request.ExpectedEditID, true); err != nil {
		return KnowledgeEditUndoRequest{}, err
	}
	return request, nil
}

func validateKnowledgeEditPins(objectType KnowledgeObjectType, id, editor, comment string, status KnowledgeStatus,
	contentDigest, evidenceDigest string, editID int64, requireEditID bool) error {
	if err := validateKnowledgeID(id); err != nil {
		return err
	}
	if objectType != KnowledgeObjectNode && objectType != KnowledgeObjectEdge {
		return fmt.Errorf("unsupported knowledge object type %q", objectType)
	}
	if editor == "" {
		return errors.New("knowledge editor is empty")
	}
	if !utf8.ValidString(editor) || utf8.RuneCountInString(editor) > MaxKnowledgeReviewerRunes {
		return fmt.Errorf("knowledge editor exceeds %d runes", MaxKnowledgeReviewerRunes)
	}
	if !utf8.ValidString(comment) || utf8.RuneCountInString(comment) > MaxKnowledgeCommentRunes {
		return fmt.Errorf("knowledge edit comment exceeds %d runes", MaxKnowledgeCommentRunes)
	}
	if !validKnowledgeStatus(status) {
		return fmt.Errorf("invalid expected knowledge status %q", status)
	}
	if err := validateSHA256Digest(contentDigest, "expected_content_digest"); err != nil {
		return err
	}
	if err := validateSHA256Digest(evidenceDigest, "expected_evidence_digest"); err != nil {
		return err
	}
	if editID < 0 || requireEditID && editID == 0 {
		return errors.New("knowledge edit undo requires expected_edit_id")
	}
	return nil
}

func validateSHA256Digest(value, field string) error {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("invalid %s", field)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); err != nil {
		return fmt.Errorf("invalid %s", field)
	}
	return nil
}

func latestKnowledgeEdit(tx *sql.Tx, objectType KnowledgeObjectType, objectID string) (KnowledgeEditRecord, bool, error) {
	var record KnowledgeEditRecord
	err := tx.QueryRow(`SELECT id, object_type, object_id, action, previous_status, new_status,
previous_label, previous_body, new_label, new_body, previous_content_digest, new_content_digest,
evidence_digest, reverts_edit_id, editor, comment, created
FROM knowledge_edits WHERE object_type = ? AND object_id = ? ORDER BY id DESC LIMIT 1`, objectType, objectID).Scan(
		&record.ID, &record.ObjectType, &record.ObjectID, &record.Action,
		&record.PreviousStatus, &record.NewStatus, &record.PreviousLabel, &record.PreviousBody,
		&record.NewLabel, &record.NewBody, &record.PreviousContentDigest, &record.NewContentDigest,
		&record.EvidenceDigest, &record.RevertsEditID, &record.Editor, &record.Comment, &record.Created)
	if err == sql.ErrNoRows {
		return KnowledgeEditRecord{}, false, nil
	}
	if err != nil {
		return KnowledgeEditRecord{}, false, fmt.Errorf("read latest knowledge edit: %w", err)
	}
	return record, true, nil
}

func insertKnowledgeEditRecord(tx *sql.Tx, record KnowledgeEditRecord) (KnowledgeEditRecord, error) {
	result, err := tx.Exec(`INSERT INTO knowledge_edits
(object_type, object_id, action, previous_status, new_status, previous_label, previous_body,
new_label, new_body, previous_content_digest, new_content_digest, evidence_digest,
reverts_edit_id, editor, comment, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ObjectType, record.ObjectID,
		record.Action, record.PreviousStatus, record.NewStatus, record.PreviousLabel, record.PreviousBody,
		record.NewLabel, record.NewBody, record.PreviousContentDigest, record.NewContentDigest,
		record.EvidenceDigest, record.RevertsEditID, record.Editor, record.Comment, record.Created)
	if err != nil {
		return KnowledgeEditRecord{}, fmt.Errorf("append knowledge edit: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return KnowledgeEditRecord{}, fmt.Errorf("read knowledge edit ID: %w", err)
	}
	return record, nil
}

func (s *Store) ListLatestKnowledgeEdits() ([]KnowledgeEditRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT e.id, e.object_type, e.object_id, e.action, e.previous_status, e.new_status,
e.previous_label, e.previous_body, e.new_label, e.new_body, e.previous_content_digest,
e.new_content_digest, e.evidence_digest, e.reverts_edit_id, e.editor, e.comment, e.created
FROM knowledge_edits e
WHERE e.id = (SELECT MAX(latest.id) FROM knowledge_edits latest
              WHERE latest.object_type = e.object_type AND latest.object_id = e.object_id)
ORDER BY e.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list latest knowledge edits: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeEditRecord, 0)
	for rows.Next() {
		var record KnowledgeEditRecord
		if err := rows.Scan(&record.ID, &record.ObjectType, &record.ObjectID, &record.Action,
			&record.PreviousStatus, &record.NewStatus, &record.PreviousLabel, &record.PreviousBody,
			&record.NewLabel, &record.NewBody, &record.PreviousContentDigest, &record.NewContentDigest,
			&record.EvidenceDigest, &record.RevertsEditID, &record.Editor, &record.Comment, &record.Created); err != nil {
			return nil, fmt.Errorf("read latest knowledge edit: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list latest knowledge edits: %w", err)
	}
	return records, nil
}

func (s *Store) ListKnowledgeEdits(limit int) ([]KnowledgeEditRecord, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("knowledge edit limit must be between 1 and 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, object_type, object_id, action, previous_status, new_status,
previous_label, previous_body, new_label, new_body, previous_content_digest, new_content_digest,
evidence_digest, reverts_edit_id, editor, comment, created
FROM knowledge_edits ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge edits: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeEditRecord, 0)
	for rows.Next() {
		var record KnowledgeEditRecord
		if err := rows.Scan(&record.ID, &record.ObjectType, &record.ObjectID, &record.Action,
			&record.PreviousStatus, &record.NewStatus, &record.PreviousLabel, &record.PreviousBody,
			&record.NewLabel, &record.NewBody, &record.PreviousContentDigest, &record.NewContentDigest,
			&record.EvidenceDigest, &record.RevertsEditID, &record.Editor, &record.Comment, &record.Created); err != nil {
			return nil, fmt.Errorf("read knowledge edit: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list knowledge edits: %w", err)
	}
	return records, nil
}

// knowledgeContentForUpsert keeps a reviewed manual content override from
// being silently replaced by a later generated extraction. Undoing that edit
// removes the override, so a future extraction may update content again.
func knowledgeContentForUpsert(tx *sql.Tx, objectType KnowledgeObjectType, id, incomingLabel, incomingBody string) (string, string, error) {
	current, err := loadKnowledgeObjectContent(tx, objectType, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return incomingLabel, incomingBody, nil
		}
		return "", "", err
	}
	latest, found, err := latestUnrevertedKnowledgeEdit(tx, objectType, id)
	if err != nil {
		return "", "", err
	}
	if !found {
		return incomingLabel, incomingBody, nil
	}
	currentDigest, err := KnowledgeContentDigest(objectType, current.label, current.body)
	if err != nil {
		return "", "", err
	}
	if currentDigest != latest.NewContentDigest {
		return incomingLabel, incomingBody, nil
	}
	return current.label, current.body, nil
}

func latestUnrevertedKnowledgeEdit(tx *sql.Tx, objectType KnowledgeObjectType, objectID string) (KnowledgeEditRecord, bool, error) {
	var record KnowledgeEditRecord
	err := tx.QueryRow(`SELECT e.id, e.object_type, e.object_id, e.action, e.previous_status, e.new_status,
e.previous_label, e.previous_body, e.new_label, e.new_body, e.previous_content_digest, e.new_content_digest,
e.evidence_digest, e.reverts_edit_id, e.editor, e.comment, e.created
FROM knowledge_edits e
WHERE e.object_type = ? AND e.object_id = ? AND e.action = ?
  AND NOT EXISTS (SELECT 1 FROM knowledge_edits undone WHERE undone.reverts_edit_id = e.id)
ORDER BY e.id DESC LIMIT 1`, objectType, objectID, KnowledgeEditActionEdit).Scan(
		&record.ID, &record.ObjectType, &record.ObjectID, &record.Action,
		&record.PreviousStatus, &record.NewStatus, &record.PreviousLabel, &record.PreviousBody,
		&record.NewLabel, &record.NewBody, &record.PreviousContentDigest, &record.NewContentDigest,
		&record.EvidenceDigest, &record.RevertsEditID, &record.Editor, &record.Comment, &record.Created)
	if err == sql.ErrNoRows {
		return KnowledgeEditRecord{}, false, nil
	}
	if err != nil {
		return KnowledgeEditRecord{}, false, fmt.Errorf("read effective knowledge edit: %w", err)
	}
	return record, true, nil
}
