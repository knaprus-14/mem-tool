package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

// Classic cards reuse the graph's scheduling policy, while retaining their
// own stable node identity and per-actor progress. No generated graph nodes
// are silently created or approved by a self-assessment.
const webMapStudySchema = `
CREATE TABLE IF NOT EXISTS web_map_study_state (
 actor TEXT NOT NULL, map_id TEXT NOT NULL, node_id TEXT NOT NULL,
 state_json TEXT NOT NULL, PRIMARY KEY(actor,map_id,node_id)
);
CREATE TABLE IF NOT EXISTS web_map_study_attempts (
 id INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL, map_id TEXT NOT NULL,
 node_id TEXT NOT NULL, card_id TEXT NOT NULL, request_id TEXT NOT NULL,
 request_hash TEXT NOT NULL, response TEXT NOT NULL, confidence INTEGER,
 prompt TEXT NOT NULL, answer TEXT NOT NULL, map_revision INTEGER NOT NULL, manifest TEXT NOT NULL,
 used_hint INTEGER NOT NULL, state_json TEXT NOT NULL, created TEXT NOT NULL,
 UNIQUE(actor,map_id,request_id)
);
CREATE INDEX IF NOT EXISTS web_map_study_attempts_node ON web_map_study_attempts(actor,map_id,node_id,id);
CREATE TRIGGER IF NOT EXISTS web_map_study_attempts_no_update BEFORE UPDATE ON web_map_study_attempts BEGIN
 SELECT RAISE(ABORT,'study attempts are append-only'); END;
CREATE TRIGGER IF NOT EXISTS web_map_study_attempts_no_delete BEFORE DELETE ON web_map_study_attempts BEGIN
 SELECT RAISE(ABORT,'study attempts are append-only'); END;
`

type WebMapStudyProgress struct {
	KnowledgeLearningScheduleState
	Current bool `json:"current"`
	Due     bool `json:"due"`
}

type WebMapStudyView struct {
	WebMapBranchView
	Progress map[string]WebMapStudyProgress `json:"progress"`
	DueCount int                            `json:"due_count"`
}

type WebMapStudyReview struct {
	Revision          int64                  `json:"revision"`
	Manifest          string                 `json:"manifest"`
	CardID            string                 `json:"card_id"`
	RequestID         string                 `json:"request_id"`
	PreviousAttemptID int64                  `json:"previous_attempt_id"`
	Grade             KnowledgeLearningGrade `json:"grade"`
	Response          string                 `json:"response"`
	Confidence        *int                   `json:"confidence,omitempty"`
	UsedHint          bool                   `json:"used_hint"`
}

type WebMapStudyReviewed struct {
	CardID   string              `json:"card_id"`
	Progress WebMapStudyProgress `json:"progress"`
	Replayed bool                `json:"replayed"`
}

func validWebMapStudyScope(actor, mapID, nodeID string, revision int64) bool {
	return actor != "" && len(actor) <= 256 && utf8.ValidString(actor) && webMapExactID(mapID) && webMapExactID(nodeID) && revision > 0
}

func webMapStudyDigests(card ClassicMindMapStudyCard) (string, string, bool) {
	content, _ := classicMindMapAIDigest(struct{ Front, Back string }{card.Front, card.Back})
	evidence, _ := classicMindMapAIDigest(card.Sources)
	if card.EvidenceDigest != "" {
		evidence = card.EvidenceDigest
	}
	current := true
	if card.EvidenceCurrent != nil {
		current = *card.EvidenceCurrent
	}
	for _, s := range card.Sources {
		if s.Kind == ClassicMindMapSourceEvidence || s.Kind == ClassicMindMapSourceKnowledgeNode {
			if s.EvidenceState != EvidenceCurrent {
				current = false
			}
		}
	}
	return content, evidence, current
}

func ReadWebMapStudyIn(ctx context.Context, dir, actor, mapID, nodeID string, revision int64) (WebMapStudyView, error) {
	result := WebMapStudyView{Progress: map[string]WebMapStudyProgress{}}
	if !validWebMapStudyScope(actor, mapID, nodeID, revision) {
		return result, ErrWebMapBranchInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.WebMapBranchView, err = readWebMapBranchTx(ctx, tx, mapID, nodeID, revision, true)
	if err != nil {
		return result, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_study_state'`).Scan(&exists); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	for _, card := range result.Study.Cards {
		content, evidence, current := webMapStudyDigests(card)
		state := KnowledgeLearningScheduleState{NodeID: card.NodeID}
		if exists > 0 {
			var raw string
			err = tx.QueryRowContext(ctx, `SELECT state_json FROM web_map_study_state WHERE actor=? AND map_id=? AND node_id=?`, actor, mapID, card.NodeID).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return result, err
			}
			if err == nil {
				if err = json.Unmarshal([]byte(raw), &state); err != nil {
					return result, err
				}
			}
		}
		if state.ContentDigest != content || state.EvidenceDigest != evidence {
			state = KnowledgeLearningScheduleState{NodeID: card.NodeID}
		}
		due := current && (state.DueAt == "" || !mustParseKnowledgeTime(state.DueAt).After(now))
		result.Progress[card.ID] = WebMapStudyProgress{KnowledgeLearningScheduleState: state, Current: current, Due: due}
		if due {
			result.DueCount++
		}
	}
	return result, tx.Commit()
}

func ReviewWebMapStudyIn(ctx context.Context, dir, actor, mapID, nodeID string, in WebMapStudyReview) (WebMapStudyReviewed, error) {
	var result WebMapStudyReviewed
	if !validWebMapStudyScope(actor, mapID, nodeID, in.Revision) || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || len(in.Manifest) != 64 || strings.Trim(in.Manifest, "0123456789abcdef") != "" || len(in.CardID) > 128 || in.CardID == "" || !validKnowledgeLearningGrade(in.Grade) || in.PreviousAttemptID < 0 || !utf8.ValidString(in.Response) || utf8.RuneCountInString(in.Response) > 8000 || (in.Confidence != nil && (*in.Confidence < 0 || *in.Confidence > 100)) {
		return result, ErrWebMapBranchInvalid
	}
	hash, _ := classicMindMapAIDigest(struct {
		Actor, Map, Node string
		Input            WebMapStudyReview
	}{actor, mapID, nodeID, in})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, webMapStudySchema); err != nil {
		return result, err
	}
	var priorHash, raw string
	var attemptID int64
	err = tx.QueryRowContext(ctx, `SELECT id,request_hash,state_json FROM web_map_study_attempts WHERE actor=? AND map_id=? AND request_id=?`, actor, mapID, in.RequestID).Scan(&attemptID, &priorHash, &raw)
	if err == nil {
		if priorHash != hash {
			return result, ErrWebMapEditRequest
		}
		if err = json.Unmarshal([]byte(raw), &result.Progress.KnowledgeLearningScheduleState); err != nil {
			return result, err
		}
		result.Progress.LastAttemptID = attemptID
		result.Progress.Current = true
		result.CardID = in.CardID
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	view, err := readWebMapBranchTx(ctx, tx, mapID, nodeID, in.Revision, true)
	if err != nil {
		return result, err
	}
	if view.Manifest.Digest != in.Manifest {
		return result, ErrWebMapBranchChanged
	}
	var card ClassicMindMapStudyCard
	for _, c := range view.Study.Cards {
		if c.ID == in.CardID {
			card = c
			break
		}
	}
	if card.ID == "" {
		return result, ErrWebMapBranchChanged
	}
	content, evidence, current := webMapStudyDigests(card)
	if !current {
		return result, ErrWebMapBranchChanged
	}
	var previous KnowledgeLearningScheduleState
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM web_map_study_state WHERE actor=? AND map_id=? AND node_id=?`, actor, mapID, card.NodeID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	hasPrevious := err == nil
	if hasPrevious {
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			return result, err
		}
	}
	if previous.ContentDigest != content || previous.EvidenceDigest != evidence {
		previous = KnowledgeLearningScheduleState{}
		hasPrevious = false
	}
	if previous.LastAttemptID != in.PreviousAttemptID {
		return result, ErrWebMapBranchChanged
	}
	var bytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(response||prompt||answer||state_json AS BLOB))),0) FROM web_map_study_attempts`).Scan(&bytes); err != nil {
		return result, err
	}
	if bytes+int64(len(in.Response)+len(card.Front)+len(card.Back))+4096 > 64<<20 {
		return result, ErrWebMapEditLimit
	}
	now := time.Now().UTC()
	state := scheduleKnowledgeLearningReview(card.NodeID, content, evidence, previous, hasPrevious, in.Grade, now)
	rawState, err := json.Marshal(state)
	if err != nil {
		return result, err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO web_map_study_attempts(actor,map_id,node_id,card_id,request_id,request_hash,response,confidence,prompt,answer,map_revision,manifest,used_hint,state_json,created) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, actor, mapID, card.NodeID, card.ID, in.RequestID, hash, in.Response, in.Confidence, card.Front, card.Back, in.Revision, in.Manifest, in.UsedHint, string(rawState), state.Updated)
	if err != nil {
		return result, err
	}
	state.LastAttemptID, err = inserted.LastInsertId()
	if err != nil {
		return result, err
	}
	rawState, err = json.Marshal(state)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_study_state(actor,map_id,node_id,state_json) VALUES(?,?,?,?) ON CONFLICT(actor,map_id,node_id) DO UPDATE SET state_json=excluded.state_json`, actor, mapID, card.NodeID, string(rawState)); err != nil {
		return result, err
	}
	result.CardID = card.ID
	result.Progress = WebMapStudyProgress{KnowledgeLearningScheduleState: state, Current: true, Due: false}
	return result, tx.Commit()
}

type WebMapStudyAttempt struct {
	ID             int64                  `json:"id"`
	Response       string                 `json:"response"`
	Confidence     *int                   `json:"confidence,omitempty"`
	UsedHint       bool                   `json:"used_hint"`
	Prompt         string                 `json:"prompt"`
	Answer         string                 `json:"answer"`
	Revision       int64                  `json:"revision"`
	Grade          KnowledgeLearningGrade `json:"grade"`
	Created        string                 `json:"created"`
	MatchesCurrent bool                   `json:"matches_current"`
}

type WebMapStudyHistory struct {
	Items []WebMapStudyAttempt `json:"items"`
	More  bool                 `json:"more"`
}

// The bounded history belongs to the authenticated actor, not the map's author.
func ReadWebMapStudyHistoryIn(ctx context.Context, dir, actor, mapID, nodeID string, revision int64, cardID string) (WebMapStudyHistory, error) {
	result := WebMapStudyHistory{Items: []WebMapStudyAttempt{}}
	if !validWebMapStudyScope(actor, mapID, nodeID, revision) || cardID == "" || len(cardID) > 128 {
		return result, ErrWebMapBranchInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	view, err := readWebMapBranchTx(ctx, tx, mapID, nodeID, revision, true)
	if err != nil {
		return result, err
	}
	var card ClassicMindMapStudyCard
	for _, c := range view.Study.Cards {
		if c.ID == cardID {
			card = c
			break
		}
	}
	if card.ID == "" {
		return result, ErrWebMapBranchChanged
	}
	content, evidence, current := webMapStudyDigests(card)
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_study_attempts'`).Scan(&exists); err != nil {
		return result, err
	}
	if exists == 0 {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,response,confidence,used_hint,prompt,answer,map_revision,state_json,created FROM web_map_study_attempts WHERE actor=? AND map_id=? AND node_id=? ORDER BY id DESC LIMIT 21`, actor, mapID, card.NodeID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	bytes := 0
	for rows.Next() {
		var item WebMapStudyAttempt
		var confidence sql.NullInt64
		var raw string
		var state KnowledgeLearningScheduleState
		if err = rows.Scan(&item.ID, &item.Response, &confidence, &item.UsedHint, &item.Prompt, &item.Answer, &item.Revision, &raw, &item.Created); err != nil {
			return result, err
		}
		bytes += len(item.Response) + len(item.Prompt) + len(item.Answer) + len(raw)
		if len(result.Items) == 20 || bytes > 2<<20 {
			result.More = true
			break
		}
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return result, err
		}
		if confidence.Valid {
			v := int(confidence.Int64)
			item.Confidence = &v
		}
		item.Grade = state.LastGrade
		item.MatchesCurrent = current && state.ContentDigest == content && state.EvidenceDigest == evidence
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
