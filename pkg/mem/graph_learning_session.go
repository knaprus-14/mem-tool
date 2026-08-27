package mem

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	KnowledgeLearningSchedulerVersion    = 1
	DefaultKnowledgeLearningSessionLimit = 20
	MaxKnowledgeLearningSessionLimit     = 100
)

type KnowledgeLearningGrade string

const (
	KnowledgeLearningGradeAgain KnowledgeLearningGrade = "again"
	KnowledgeLearningGradeHard  KnowledgeLearningGrade = "hard"
	KnowledgeLearningGradeGood  KnowledgeLearningGrade = "good"
	KnowledgeLearningGradeEasy  KnowledgeLearningGrade = "easy"
)

var (
	ErrKnowledgeLearningSessionNotFound = errors.New("knowledge learning session not found")
	ErrKnowledgeLearningAlreadyGraded   = errors.New("knowledge learning item was already graded in this session")
	ErrKnowledgeLearningItemChanged     = errors.New("knowledge learning item or evidence changed")
)

const knowledgeLearningSessionSchema = `
CREATE TABLE IF NOT EXISTS knowledge_learning_sessions (
    id TEXT PRIMARY KEY,
    selection_json TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    route_digest TEXT NOT NULL,
    scheduler_version INTEGER NOT NULL,
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_learning_session_items (
    session_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    node_id TEXT NOT NULL,
    level INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    answer TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    sources_json TEXT NOT NULL,
    scheduled_due TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (session_id, ordinal),
    UNIQUE (session_id, node_id)
);

CREATE TABLE IF NOT EXISTS knowledge_learning_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    grade TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    previous_state_json TEXT NOT NULL,
    next_state_json TEXT NOT NULL,
    created TEXT NOT NULL,
    UNIQUE (session_id, node_id)
);

CREATE TABLE IF NOT EXISTS knowledge_learning_item_state (
    node_id TEXT PRIMARY KEY,
    content_digest TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    due_at TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    ease_permille INTEGER NOT NULL,
    repetitions INTEGER NOT NULL,
    lapses INTEGER NOT NULL,
    last_grade TEXT NOT NULL,
    last_attempt_id INTEGER NOT NULL,
    updated TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_knowledge_learning_session_items_node
    ON knowledge_learning_session_items(node_id, session_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_learning_attempts_node
    ON knowledge_learning_attempts(node_id, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_learning_item_state_due
    ON knowledge_learning_item_state(due_at, node_id);

CREATE TRIGGER IF NOT EXISTS knowledge_learning_sessions_no_update
BEFORE UPDATE ON knowledge_learning_sessions
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning sessions are append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_learning_sessions_no_delete
BEFORE DELETE ON knowledge_learning_sessions
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning sessions are append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_learning_session_items_no_update
BEFORE UPDATE ON knowledge_learning_session_items
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning session items are append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_learning_session_items_no_delete
BEFORE DELETE ON knowledge_learning_session_items
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning session items are append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_learning_attempts_no_update
BEFORE UPDATE ON knowledge_learning_attempts
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning attempt history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_learning_attempts_no_delete
BEFORE DELETE ON knowledge_learning_attempts
BEGIN
    SELECT RAISE(ABORT, 'knowledge learning attempt history is append-only');
END;
`

type KnowledgeLearningSessionStartRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	ExpectedRouteDigest    string                    `json:"expected_route_digest,omitempty"`
	Limit                  int                       `json:"limit,omitempty"`
}

type KnowledgeLearningScheduleState struct {
	NodeID          string                 `json:"node_id"`
	ContentDigest   string                 `json:"content_digest"`
	EvidenceDigest  string                 `json:"evidence_digest"`
	DueAt           string                 `json:"due_at"`
	IntervalSeconds int64                  `json:"interval_seconds"`
	EasePermille    int                    `json:"ease_permille"`
	Repetitions     int                    `json:"repetitions"`
	Lapses          int                    `json:"lapses"`
	LastGrade       KnowledgeLearningGrade `json:"last_grade,omitempty"`
	LastAttemptID   int64                  `json:"last_attempt_id,omitempty"`
	Updated         string                 `json:"updated"`
}

type KnowledgeLearningSessionItem struct {
	Ordinal        int                             `json:"ordinal"`
	NodeID         string                          `json:"node_id"`
	Kind           KnowledgeNodeKind               `json:"kind"`
	Level          int                             `json:"level"`
	Prompt         string                          `json:"prompt"`
	Answer         string                          `json:"answer"`
	ContentDigest  string                          `json:"content_digest"`
	EvidenceDigest string                          `json:"evidence_digest"`
	Sources        []KnowledgeSelectionSourceRef   `json:"sources"`
	ScheduledDue   string                          `json:"scheduled_due,omitempty"`
	PreviousState  *KnowledgeLearningScheduleState `json:"previous_state,omitempty"`
}

type KnowledgeLearningSessionSummary struct {
	RouteItems  int    `json:"route_items"`
	DueItems    int    `json:"due_items"`
	NewItems    int    `json:"new_items"`
	ReviewItems int    `json:"review_items"`
	Deferred    int    `json:"deferred"`
	NextDue     string `json:"next_due,omitempty"`
}

type KnowledgeLearningSession struct {
	ID               string                          `json:"id,omitempty"`
	ManifestDigest   string                          `json:"manifest_digest"`
	RouteDigest      string                          `json:"route_digest"`
	SchedulerVersion int                             `json:"scheduler_version"`
	Created          string                          `json:"created"`
	Items            []KnowledgeLearningSessionItem  `json:"items"`
	Summary          KnowledgeLearningSessionSummary `json:"summary"`
}

type KnowledgeLearningGradeRequest struct {
	SessionID string                 `json:"session_id"`
	NodeID    string                 `json:"node_id"`
	Grade     KnowledgeLearningGrade `json:"grade"`
}

type KnowledgeLearningAttempt struct {
	ID             int64                           `json:"id"`
	SessionID      string                          `json:"session_id"`
	NodeID         string                          `json:"node_id"`
	Ordinal        int                             `json:"ordinal"`
	Grade          KnowledgeLearningGrade          `json:"grade"`
	ContentDigest  string                          `json:"content_digest"`
	EvidenceDigest string                          `json:"evidence_digest"`
	PreviousState  *KnowledgeLearningScheduleState `json:"previous_state,omitempty"`
	NextState      KnowledgeLearningScheduleState  `json:"next_state"`
	Created        string                          `json:"created"`
}

type KnowledgeLearningGradeResult struct {
	Attempt   KnowledgeLearningAttempt `json:"attempt"`
	Completed int                      `json:"completed"`
	Total     int                      `json:"total"`
}

type KnowledgeLearningHistoryRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	ExpectedRouteDigest    string                    `json:"expected_route_digest,omitempty"`
}

type KnowledgeLearningHistoryItem struct {
	NodeID        string                          `json:"node_id"`
	Kind          KnowledgeNodeKind               `json:"kind"`
	Prompt        string                          `json:"prompt"`
	Level         int                             `json:"level"`
	State         *KnowledgeLearningScheduleState `json:"state,omitempty"`
	Attempts      int                             `json:"attempts"`
	Due           bool                            `json:"due"`
	ScheduleReset bool                            `json:"schedule_reset,omitempty"`
}

type KnowledgeLearningHistory struct {
	ManifestDigest string                         `json:"manifest_digest"`
	RouteDigest    string                         `json:"route_digest"`
	GeneratedAt    string                         `json:"generated_at"`
	Due            int                            `json:"due"`
	New            int                            `json:"new"`
	Scheduled      int                            `json:"scheduled"`
	NextDue        string                         `json:"next_due,omitempty"`
	Items          []KnowledgeLearningHistoryItem `json:"items"`
}

func (s *Store) StartKnowledgeLearningSession(request KnowledgeLearningSessionStartRequest) (KnowledgeLearningSession, error) {
	return s.startKnowledgeLearningSessionAt(request, time.Now().UTC())
}

func (s *Store) startKnowledgeLearningSessionAt(request KnowledgeLearningSessionStartRequest, now time.Time) (KnowledgeLearningSession, error) {
	limit := request.Limit
	if limit == 0 {
		limit = DefaultKnowledgeLearningSessionLimit
	}
	if limit < 1 || limit > MaxKnowledgeLearningSessionLimit {
		return KnowledgeLearningSession{}, fmt.Errorf("knowledge learning session limit must be 1..%d", MaxKnowledgeLearningSessionLimit)
	}
	route, err := s.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: request.Selection, ExpectedManifestDigest: request.ExpectedManifestDigest})
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	if request.ExpectedRouteDigest != "" && request.ExpectedRouteDigest != route.Digest {
		return KnowledgeLearningSession{}, fmt.Errorf("%w: learning route changed", ErrKnowledgeSelectionChanged)
	}
	if !route.Ready {
		return KnowledgeLearningSession{}, errors.New("knowledge learning route is not ready")
	}
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil || manifest.Digest != route.ManifestDigest {
		return KnowledgeLearningSession{}, fmt.Errorf("%w: learning selection changed", ErrKnowledgeSelectionChanged)
	}
	review, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	reviewByID := make(map[string]KnowledgeReviewItem, len(review.Items))
	for _, item := range review.Items {
		if item.ObjectType == KnowledgeObjectNode {
			reviewByID[item.ID] = item
		}
	}
	states, err := s.loadKnowledgeLearningStates()
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	result := KnowledgeLearningSession{ManifestDigest: route.ManifestDigest, RouteDigest: route.Digest, SchedulerVersion: KnowledgeLearningSchedulerVersion, Created: now.Format(time.RFC3339Nano)}
	for _, routeItem := range route.Items {
		itemReview := reviewByID[routeItem.ID]
		state, hasState := states[routeItem.ID]
		matching := hasState && state.ContentDigest == itemReview.ContentDigest && state.EvidenceDigest == itemReview.EvidenceDigest
		if matching {
			due, parseErr := time.Parse(time.RFC3339Nano, state.DueAt)
			if parseErr != nil {
				return KnowledgeLearningSession{}, fmt.Errorf("parse learning due time for %q: %w", routeItem.ID, parseErr)
			}
			if due.After(now) {
				result.Summary.Deferred++
				if result.Summary.NextDue == "" || due.Before(mustParseKnowledgeTime(result.Summary.NextDue)) {
					result.Summary.NextDue = state.DueAt
				}
				continue
			}
		}
		if len(result.Items) >= limit {
			result.Summary.Deferred++
			continue
		}
		sessionItem := KnowledgeLearningSessionItem{Ordinal: len(result.Items), NodeID: routeItem.ID, Kind: routeItem.Kind, Level: routeItem.Level, Prompt: routeItem.Prompt, Answer: routeItem.Answer, ContentDigest: itemReview.ContentDigest, EvidenceDigest: itemReview.EvidenceDigest, Sources: routeItem.Sources}
		if matching {
			copyState := state
			sessionItem.ScheduledDue = state.DueAt
			sessionItem.PreviousState = &copyState
			result.Summary.ReviewItems++
		} else {
			result.Summary.NewItems++
		}
		result.Items = append(result.Items, sessionItem)
	}
	result.Summary.RouteItems = len(route.Items)
	result.Summary.DueItems = len(result.Items)
	if len(result.Items) == 0 {
		return result, nil
	}
	result.ID, err = newKnowledgeLearningID("learning-session-")
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	selectionJSON, err := json.Marshal(request.Selection)
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeLearningSession{}, err
	}
	rollback := func(cause error) (KnowledgeLearningSession, error) {
		_ = tx.Rollback()
		return KnowledgeLearningSession{}, cause
	}
	if err := verifyKnowledgeSelectionManifestTx(tx, manifest); err != nil {
		return rollback(err)
	}
	if err := verifyKnowledgeLearningRouteRelationsTx(tx, route); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_learning_sessions
(id, selection_json, manifest_digest, route_digest, scheduler_version, created) VALUES (?, ?, ?, ?, ?, ?)`, result.ID, string(selectionJSON), result.ManifestDigest, result.RouteDigest, result.SchedulerVersion, result.Created); err != nil {
		return rollback(fmt.Errorf("append knowledge learning session: %w", err))
	}
	for _, item := range result.Items {
		node, loadErr := loadKnowledgeNode(tx, item.NodeID)
		if loadErr != nil || node.Status != KnowledgeStatusActive || node.Kind != item.Kind {
			return rollback(ErrKnowledgeLearningItemChanged)
		}
		currentContent, digestErr := KnowledgeContentDigest(KnowledgeObjectNode, node.Label, node.Body)
		if digestErr != nil {
			return rollback(digestErr)
		}
		currentEvidence, digestErr := KnowledgeEvidenceDigest(node.Evidence)
		if digestErr != nil || currentContent != item.ContentDigest || currentEvidence != item.EvidenceDigest {
			return rollback(ErrKnowledgeLearningItemChanged)
		}
		resolutions, resolveErr := resolveEvidenceAnchorsFromQuerier(tx, node.Evidence)
		if resolveErr != nil {
			return rollback(resolveErr)
		}
		for _, resolution := range resolutions {
			if resolution.State != EvidenceCurrent {
				return rollback(ErrKnowledgeLearningItemChanged)
			}
		}
		sourcesJSON, marshalErr := json.Marshal(item.Sources)
		if marshalErr != nil {
			return rollback(marshalErr)
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_learning_session_items
(session_id, ordinal, node_id, level, prompt, answer, content_digest, evidence_digest, sources_json, scheduled_due)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, result.ID, item.Ordinal, item.NodeID, item.Level, item.Prompt, item.Answer, item.ContentDigest, item.EvidenceDigest, string(sourcesJSON), item.ScheduledDue); err != nil {
			return rollback(fmt.Errorf("append knowledge learning session item: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeLearningSession{}, err
	}
	return result, nil
}

func verifyKnowledgeLearningRouteRelationsTx(tx *sql.Tx, route KnowledgeLearningRoute) error {
	for _, relation := range route.Relations {
		var from, to string
		var kind KnowledgeRelationKind
		var status KnowledgeStatus
		if err := tx.QueryRow(`SELECT from_node, to_node, kind, status FROM knowledge_edges WHERE id = ?`, relation.ID).Scan(&from, &to, &kind, &status); err != nil {
			return ErrKnowledgeLearningItemChanged
		}
		if from != relation.From || to != relation.To || kind != relation.Kind || status != KnowledgeStatusActive {
			return ErrKnowledgeLearningItemChanged
		}
		anchors, err := loadKnowledgeEvidence(tx, "knowledge_edge_evidence", "edge_id", relation.ID)
		if err != nil || len(anchors) == 0 {
			return ErrKnowledgeLearningItemChanged
		}
		resolutions, resolveErr := resolveEvidenceAnchorsFromQuerier(tx, anchors)
		if resolveErr != nil {
			return ErrKnowledgeLearningItemChanged
		}
		for _, resolution := range resolutions {
			if resolution.State != EvidenceCurrent {
				return ErrKnowledgeLearningItemChanged
			}
		}
	}
	return nil
}

func (s *Store) GradeKnowledgeLearningItem(request KnowledgeLearningGradeRequest) (KnowledgeLearningGradeResult, error) {
	return s.gradeKnowledgeLearningItemAt(request, time.Now().UTC())
}

func (s *Store) gradeKnowledgeLearningItemAt(request KnowledgeLearningGradeRequest, now time.Time) (KnowledgeLearningGradeResult, error) {
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.NodeID = strings.TrimSpace(request.NodeID)
	if err := validateKnowledgeID(request.SessionID); err != nil {
		return KnowledgeLearningGradeResult{}, err
	}
	if err := validateKnowledgeID(request.NodeID); err != nil {
		return KnowledgeLearningGradeResult{}, err
	}
	if !validKnowledgeLearningGrade(request.Grade) {
		return KnowledgeLearningGradeResult{}, errors.New("knowledge learning grade must be again, hard, good, or easy")
	}
	report, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeLearningGradeResult{}, err
	}
	var current KnowledgeReviewItem
	found := false
	for _, item := range report.Items {
		if item.ObjectType == KnowledgeObjectNode && item.ID == request.NodeID {
			current, found = item, true
			break
		}
	}
	if !found || current.Status != KnowledgeStatusActive || current.EvidenceState != EvidenceCurrent || len(current.Evidence) == 0 {
		return KnowledgeLearningGradeResult{}, ErrKnowledgeLearningItemChanged
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeLearningGradeResult{}, err
	}
	rollback := func(cause error) (KnowledgeLearningGradeResult, error) {
		_ = tx.Rollback()
		return KnowledgeLearningGradeResult{}, cause
	}
	var ordinal, total int
	var contentDigest, evidenceDigest string
	err = tx.QueryRow(`SELECT i.ordinal, i.content_digest, i.evidence_digest,
    (SELECT COUNT(*) FROM knowledge_learning_session_items WHERE session_id = i.session_id)
FROM knowledge_learning_session_items i WHERE i.session_id = ? AND i.node_id = ?`, request.SessionID, request.NodeID).Scan(&ordinal, &contentDigest, &evidenceDigest, &total)
	if err == sql.ErrNoRows {
		return rollback(ErrKnowledgeLearningSessionNotFound)
	}
	if err != nil {
		return rollback(err)
	}
	if current.ContentDigest != contentDigest || current.EvidenceDigest != evidenceDigest {
		return rollback(ErrKnowledgeLearningItemChanged)
	}
	var status KnowledgeStatus
	if err := tx.QueryRow(`SELECT status FROM knowledge_nodes WHERE id = ?`, request.NodeID).Scan(&status); err != nil || status != KnowledgeStatusActive {
		return rollback(ErrKnowledgeLearningItemChanged)
	}
	node, err := loadKnowledgeNode(tx, request.NodeID)
	if err != nil {
		return rollback(ErrKnowledgeLearningItemChanged)
	}
	resolutions, resolveErr := resolveEvidenceAnchorsFromQuerier(tx, node.Evidence)
	if resolveErr != nil {
		return rollback(ErrKnowledgeLearningItemChanged)
	}
	for _, resolution := range resolutions {
		if resolution.State != EvidenceCurrent {
			return rollback(ErrKnowledgeLearningItemChanged)
		}
	}
	dbContent, err := KnowledgeContentDigest(KnowledgeObjectNode, node.Label, node.Body)
	if err != nil {
		return rollback(err)
	}
	dbEvidence, err := KnowledgeEvidenceDigest(node.Evidence)
	if err != nil || dbContent != contentDigest || dbEvidence != evidenceDigest {
		return rollback(ErrKnowledgeLearningItemChanged)
	}
	var duplicate int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_learning_attempts WHERE session_id = ? AND node_id = ?`, request.SessionID, request.NodeID).Scan(&duplicate); err != nil {
		return rollback(err)
	}
	if duplicate != 0 {
		return rollback(ErrKnowledgeLearningAlreadyGraded)
	}
	previous, hasPrevious, err := loadKnowledgeLearningStateTx(tx, request.NodeID)
	if err != nil {
		return rollback(err)
	}
	if hasPrevious && (previous.ContentDigest != contentDigest || previous.EvidenceDigest != evidenceDigest) {
		hasPrevious = false
	}
	next := scheduleKnowledgeLearningReview(request.NodeID, contentDigest, evidenceDigest, previous, hasPrevious, request.Grade, now)
	previousJSON := "null"
	var previousPointer *KnowledgeLearningScheduleState
	if hasPrevious {
		encoded, _ := json.Marshal(previous)
		previousJSON = string(encoded)
		copyState := previous
		previousPointer = &copyState
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return rollback(err)
	}
	created := now.Format(time.RFC3339Nano)
	inserted, err := tx.Exec(`INSERT INTO knowledge_learning_attempts
(session_id, node_id, ordinal, grade, content_digest, evidence_digest, previous_state_json, next_state_json, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.SessionID, request.NodeID, ordinal, request.Grade, contentDigest, evidenceDigest, previousJSON, string(nextJSON), created)
	if err != nil {
		return rollback(fmt.Errorf("append knowledge learning attempt: %w", err))
	}
	attemptID, err := inserted.LastInsertId()
	if err != nil {
		return rollback(err)
	}
	next.LastAttemptID = attemptID
	// The immutable next-state snapshot intentionally has last_attempt_id=0:
	// SQLite assigns the audit ID during INSERT. The mutable projection below
	// records the assigned ID without rewriting append-only history.
	if _, err := tx.Exec(`INSERT INTO knowledge_learning_item_state
(node_id, content_digest, evidence_digest, due_at, interval_seconds, ease_permille, repetitions, lapses, last_grade, last_attempt_id, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(node_id) DO UPDATE SET content_digest=excluded.content_digest, evidence_digest=excluded.evidence_digest,
due_at=excluded.due_at, interval_seconds=excluded.interval_seconds, ease_permille=excluded.ease_permille,
repetitions=excluded.repetitions, lapses=excluded.lapses, last_grade=excluded.last_grade,
last_attempt_id=excluded.last_attempt_id, updated=excluded.updated`, next.NodeID, next.ContentDigest, next.EvidenceDigest, next.DueAt, next.IntervalSeconds, next.EasePermille, next.Repetitions, next.Lapses, next.LastGrade, attemptID, next.Updated); err != nil {
		return rollback(err)
	}
	var completed int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_learning_attempts WHERE session_id = ?`, request.SessionID).Scan(&completed); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeLearningGradeResult{}, err
	}
	next.LastAttemptID = attemptID
	return KnowledgeLearningGradeResult{Attempt: KnowledgeLearningAttempt{ID: attemptID, SessionID: request.SessionID, NodeID: request.NodeID, Ordinal: ordinal, Grade: request.Grade, ContentDigest: contentDigest, EvidenceDigest: evidenceDigest, PreviousState: previousPointer, NextState: next, Created: created}, Completed: completed, Total: total}, nil
}

func (s *Store) BuildKnowledgeLearningHistory(request KnowledgeLearningHistoryRequest) (KnowledgeLearningHistory, error) {
	return s.buildKnowledgeLearningHistoryAt(request, time.Now().UTC())
}

func (s *Store) buildKnowledgeLearningHistoryAt(request KnowledgeLearningHistoryRequest, now time.Time) (KnowledgeLearningHistory, error) {
	route, err := s.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: request.Selection, ExpectedManifestDigest: request.ExpectedManifestDigest})
	if err != nil {
		return KnowledgeLearningHistory{}, err
	}
	if request.ExpectedRouteDigest != "" && request.ExpectedRouteDigest != route.Digest {
		return KnowledgeLearningHistory{}, ErrKnowledgeSelectionChanged
	}
	return s.buildKnowledgeLearningHistoryForRouteAt(route, now)
}

func (s *Store) buildKnowledgeLearningHistoryForRouteAt(route KnowledgeLearningRoute, now time.Time) (KnowledgeLearningHistory, error) {
	report, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeLearningHistory{}, err
	}
	reviews := make(map[string]KnowledgeReviewItem, len(report.Items))
	for _, item := range report.Items {
		if item.ObjectType == KnowledgeObjectNode {
			reviews[item.ID] = item
		}
	}
	states, err := s.loadKnowledgeLearningStates()
	if err != nil {
		return KnowledgeLearningHistory{}, err
	}
	result := KnowledgeLearningHistory{ManifestDigest: route.ManifestDigest, RouteDigest: route.Digest, GeneratedAt: now.Format(time.RFC3339Nano)}
	for _, routeItem := range route.Items {
		review := reviews[routeItem.ID]
		state, exists := states[routeItem.ID]
		matching := exists && state.ContentDigest == review.ContentDigest && state.EvidenceDigest == review.EvidenceDigest
		item := KnowledgeLearningHistoryItem{NodeID: routeItem.ID, Kind: routeItem.Kind, Prompt: routeItem.Prompt, Level: routeItem.Level, ScheduleReset: exists && !matching}
		if matching {
			copyState := state
			item.State = &copyState
			due, parseErr := time.Parse(time.RFC3339Nano, state.DueAt)
			if parseErr != nil {
				return KnowledgeLearningHistory{}, parseErr
			}
			item.Due = !due.After(now)
			result.Scheduled++
			if item.Due {
				result.Due++
			} else if result.NextDue == "" || due.Before(mustParseKnowledgeTime(result.NextDue)) {
				result.NextDue = state.DueAt
			}
		} else {
			item.Due = true
			result.New++
			result.Due++
		}
		s.mu.RLock()
		err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_learning_attempts WHERE node_id = ?`, routeItem.ID).Scan(&item.Attempts)
		s.mu.RUnlock()
		if err != nil {
			return KnowledgeLearningHistory{}, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s *Store) loadKnowledgeLearningStates() (map[string]KnowledgeLearningScheduleState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT node_id, content_digest, evidence_digest, due_at, interval_seconds,
ease_permille, repetitions, lapses, last_grade, last_attempt_id, updated FROM knowledge_learning_item_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]KnowledgeLearningScheduleState)
	for rows.Next() {
		var state KnowledgeLearningScheduleState
		if err := rows.Scan(&state.NodeID, &state.ContentDigest, &state.EvidenceDigest, &state.DueAt, &state.IntervalSeconds, &state.EasePermille, &state.Repetitions, &state.Lapses, &state.LastGrade, &state.LastAttemptID, &state.Updated); err != nil {
			return nil, err
		}
		states[state.NodeID] = state
	}
	return states, rows.Err()
}

func loadKnowledgeLearningStateTx(tx *sql.Tx, nodeID string) (KnowledgeLearningScheduleState, bool, error) {
	var state KnowledgeLearningScheduleState
	err := tx.QueryRow(`SELECT node_id, content_digest, evidence_digest, due_at, interval_seconds,
ease_permille, repetitions, lapses, last_grade, last_attempt_id, updated FROM knowledge_learning_item_state WHERE node_id = ?`, nodeID).Scan(&state.NodeID, &state.ContentDigest, &state.EvidenceDigest, &state.DueAt, &state.IntervalSeconds, &state.EasePermille, &state.Repetitions, &state.Lapses, &state.LastGrade, &state.LastAttemptID, &state.Updated)
	if err == sql.ErrNoRows {
		return KnowledgeLearningScheduleState{}, false, nil
	}
	return state, err == nil, err
}

func validKnowledgeLearningGrade(grade KnowledgeLearningGrade) bool {
	switch grade {
	case KnowledgeLearningGradeAgain, KnowledgeLearningGradeHard, KnowledgeLearningGradeGood, KnowledgeLearningGradeEasy:
		return true
	}
	return false
}

func scheduleKnowledgeLearningReview(nodeID, contentDigest, evidenceDigest string, previous KnowledgeLearningScheduleState, hasPrevious bool, grade KnowledgeLearningGrade, now time.Time) KnowledgeLearningScheduleState {
	const day = int64(24 * time.Hour / time.Second)
	ease, repetitions, lapses, priorInterval := 2500, 0, 0, int64(0)
	if hasPrevious {
		ease, repetitions, lapses, priorInterval = previous.EasePermille, previous.Repetitions, previous.Lapses, previous.IntervalSeconds
	}
	var interval int64
	switch grade {
	case KnowledgeLearningGradeAgain:
		interval, repetitions, lapses, ease = 10*60, 0, lapses+1, maxKnowledgeInt(1300, ease-200)
	case KnowledgeLearningGradeHard:
		if repetitions == 0 {
			interval = day
		} else {
			interval = maxKnowledgeInt64(day, priorInterval*120/100)
		}
		repetitions++
		ease = maxKnowledgeInt(1300, ease-150)
	case KnowledgeLearningGradeGood:
		switch repetitions {
		case 0:
			interval = day
		case 1:
			interval = 6 * day
		default:
			interval = maxKnowledgeInt64(day, priorInterval*int64(ease)/1000)
		}
		repetitions++
	case KnowledgeLearningGradeEasy:
		if repetitions == 0 {
			interval = 4 * day
		} else {
			interval = maxKnowledgeInt64(priorInterval+day, priorInterval*int64(ease)*130/100000)
		}
		repetitions++
		ease = minKnowledgeInt(3000, ease+150)
	}
	return KnowledgeLearningScheduleState{NodeID: nodeID, ContentDigest: contentDigest, EvidenceDigest: evidenceDigest, DueAt: now.Add(time.Duration(interval) * time.Second).Format(time.RFC3339Nano), IntervalSeconds: interval, EasePermille: ease, Repetitions: repetitions, Lapses: lapses, LastGrade: grade, Updated: now.Format(time.RFC3339Nano)}
}

func mustParseKnowledgeTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func maxKnowledgeInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
func minKnowledgeInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
func maxKnowledgeInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
