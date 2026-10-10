package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var ErrWebMapHistoryInvalid = errors.New("проверьте параметры истории карты")
var ErrWebMapUndoUnavailable = errors.New("это изменение сейчас нельзя отменить; обновите историю карты")

var ErrWebMapRedoUnavailable = errors.New("это изменение сейчас нельзя вернуть; обновите историю карты")

var ErrWebMapRedoDiverged = fmt.Errorf("%w: после отмены появились другие изменения; сначала отмените более новые действия или продолжите работу с текущей картой", ErrClassicMindMapRevisionConflict)

type WebMapChange struct {
	NodeID      string `json:"node_id,omitempty"`
	NodeLabel   string `json:"node_label,omitempty"`
	NodePresent bool   `json:"node_present"`
	ID          int64  `json:"id"`
	Revision    int64  `json:"revision"`
	Action      string `json:"action"`
	Created     string `json:"created"`
	Reverts     int64  `json:"reverts"`
	Reverted    bool   `json:"reverted"`
}
type WebMapHistory struct {
	Items      []WebMapChange `json:"items"`
	Revision   int64          `json:"revision"`
	RedoID     int64          `json:"redo_id"`
	UndoID     int64          `json:"undo_id"`
	NextBefore int64          `json:"next_before"`
}
type WebMapUndo struct {
	Revision  int64  `json:"revision"`
	ChangeID  int64  `json:"change_id"`
	RequestID string `json:"request_id"`
}

// The candidate follows the classic undo stack, including earlier actions after undo.
const webMapUndoCandidate = `SELECT c.id FROM mind_map_changes c WHERE c.map_id=?
 AND c.action NOT LIKE 'undo:%' AND c.action!='create_map'
 AND NOT EXISTS (SELECT 1 FROM mind_map_changes u WHERE u.reverts_change_id=c.id)
 ORDER BY c.id DESC LIMIT 1`

const webMapRedoCandidate = `SELECT u.id FROM mind_map_changes u WHERE u.map_id=? AND u.action LIKE 'undo:%' AND u.reverts_change_id>0
AND NOT EXISTS (SELECT 1 FROM mind_map_changes r WHERE r.map_id=u.map_id AND r.reverts_change_id=u.id)
ORDER BY u.id DESC LIMIT 1`

func webMapRevision(ctx context.Context, tx *sql.Tx, id string) (int64, error) {
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrClassicMindMapNotFound
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM mind_maps WHERE id=? AND deleted_at=''`, id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrClassicMindMapNotFound
	}
	return revision, err
}

// ListWebMapHistoryIn returns bounded metadata only. Snapshots, actors, comments and paths stay in the database.
func ListWebMapHistoryIn(ctx context.Context, dir, id string, before int64) (applogResult0 WebMapHistory, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapHistoryIn")(&applogResult1)

	result := WebMapHistory{Items: []WebMapChange{}}
	if id == "" || len(id) > 128 || before < 0 {
		return result, ErrWebMapHistoryInvalid
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
	result.Revision, err = webMapRevision(ctx, tx, id)
	if err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, webMapUndoCandidate, id).Scan(&result.UndoID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	err = tx.QueryRowContext(ctx, webMapRedoCandidate, id).Scan(&result.RedoID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.new_revision,substr(c.action,1,80),substr(c.created,1,64),c.reverts_change_id,
 EXISTS(SELECT 1 FROM mind_map_changes u WHERE u.reverts_change_id=c.id),
 COALESCE(n.id,''),COALESCE(substr(n.label,1,512),''),COALESCE(n.deleted_at='',0)
 FROM mind_map_changes c LEFT JOIN mind_map_nodes n ON n.id=c.target_node_id AND n.map_id=c.map_id AND length(n.id)<=128 WHERE c.map_id=? AND (?=0 OR c.id<?) ORDER BY c.id DESC LIMIT 31`, id, before, before)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item WebMapChange
		if err = rows.Scan(&item.ID, &item.Revision, &item.Action, &item.Created, &item.Reverts, &item.Reverted, &item.NodeID, &item.NodeLabel, &item.NodePresent); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Items) > 30 {
		result.Items = result.Items[:30]
		result.NextBefore = result.Items[29].ID
	}
	return result, tx.Commit()
}

// UndoWebMapIn uses the classic undo transaction without source resolution or opening vectors.
func UndoWebMapIn(ctx context.Context, dir, actor, id string, input WebMapUndo) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.UndoWebMapIn")(&applogResult1)

	return restoreWebMapHistoryIn(ctx, dir, actor, id, input, false)
}

// RedoWebMapIn re-applies the action referenced by the exact selected undo record.
func RedoWebMapIn(ctx context.Context, dir, actor, id string, input WebMapUndo) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.RedoWebMapIn")(&applogResult1)

	return restoreWebMapHistoryIn(ctx, dir, actor, id, input, true)
}

func restoreWebMapHistoryIn(ctx context.Context, dir, actor, id string, input WebMapUndo, redo bool) (WebMapNodeEdited, error) {
	var result WebMapNodeEdited
	if id == "" || len(id) > 128 || input.Revision <= 0 || input.ChangeID <= 0 || len(input.RequestID) != 32 || strings.Trim(input.RequestID, "0123456789abcdef") != "" {
		return result, ErrWebMapHistoryInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map string
		Input      WebMapUndo
	}{actor, id, input})
	if redo {
		encoded = append([]byte("redo\n"), encoded...)
	}
	digest := prefixedSHA256(encoded)
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
	table, candidateQuery, unavailable := "web_map_undos", webMapUndoCandidate, ErrWebMapUndoUnavailable
	if redo {
		table, candidateQuery, unavailable = "web_map_redos", webMapRedoCandidate, ErrWebMapRedoUnavailable
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+table+"(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL)"); err != nil {
		return result, err
	}
	var hash string
	err = tx.QueryRowContext(ctx, "SELECT request_hash,revision FROM "+table+" WHERE request_id=?", input.RequestID).Scan(&hash, &result.Revision)
	if err == nil {
		if hash != digest {
			return result, ErrWebMapEditRequest
		}
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	revision, err := webMapRevision(ctx, tx, id)
	if err != nil {
		return result, err
	}
	if revision != input.Revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	var candidate int64
	err = tx.QueryRowContext(ctx, candidateQuery, id).Scan(&candidate)
	if errors.Is(err, sql.ErrNoRows) || err == nil && candidate != input.ChangeID {
		return result, unavailable
	}
	if err != nil {
		return result, err
	}
	if err = checkWebMapEditBounds(ctx, tx, id); err != nil {
		return result, err
	}
	count, err := countWebMapNodeReceipts(ctx, tx)
	if err != nil {
		return result, err
	}
	if count >= MaxWebMapMutationReceipts {
		return result, ErrWebMapEditLimit
	}
	var beforeSize, afterSize int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(before_json AS BLOB)),length(CAST(after_json AS BLOB)) FROM mind_map_changes WHERE id=? AND map_id=?`, candidate, id).Scan(&beforeSize, &afterSize)
	if err != nil {
		return result, err
	}
	if beforeSize > 8<<20 || afterSize > 8<<20 {
		return result, ErrWebMapEditLimit
	}
	snapshotID, snapshotField := candidate, "before_json"
	if redo {
		err = tx.QueryRowContext(ctx, `SELECT o.id,length(CAST(o.before_json AS BLOB)),length(CAST(o.after_json AS BLOB)) FROM mind_map_changes u JOIN mind_map_changes o ON o.id=u.reverts_change_id AND o.map_id=u.map_id WHERE u.id=? AND u.map_id=?`, candidate, id).Scan(&snapshotID, &beforeSize, &afterSize)
		if errors.Is(err, sql.ErrNoRows) {
			return result, unavailable
		}
		if err != nil {
			return result, err
		}
		if beforeSize > 8<<20 || afterSize > 8<<20 {
			return result, ErrWebMapEditLimit
		}
		snapshotField = "after_json"
	}
	// Validate the restore target before writing its rows; post-check enforces data/history size.
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT "+snapshotField+" FROM mind_map_changes WHERE id=?", snapshotID).Scan(&raw); err != nil {
		return result, err
	}
	var previous ClassicMindMapDocument
	if err = json.Unmarshal([]byte(raw), &previous); err != nil {
		return result, unavailable
	}
	if previous.Map.ID != id || len(previous.Nodes) == 0 || len(previous.Nodes) > 1000 {
		return result, ErrWebMapEditLimit
	}
	if err = validateClassicMindMapBrowserRender(previous.Nodes); err != nil {
		return result, err
	}
	sources := 0
	for _, n := range previous.Nodes {
		sources += len(n.Sources)
	}
	if sources > 2000 {
		return result, ErrWebMapEditLimit
	}
	roots := 0
	for _, n := range previous.Nodes {
		if n.ParentID == "" {
			roots++
			if n.ID != previous.Map.RootNodeID {
				return result, ErrClassicMindMapRenderLimit
			}
		}
	}
	if roots != 1 {
		return result, ErrClassicMindMapRenderLimit
	}
	var change ClassicMindMapChange
	if redo {
		_, change, err = redoClassicMindMapChangeTx(tx, id, input.Revision, actor, "отменённое изменение возвращено через веб")
	} else {
		_, change, err = undoClassicMindMapChangeTx(tx, id, candidate, input.Revision, actor, "изменение отменено через веб")
	}
	if redo && errors.Is(err, ErrClassicMindMapRevisionConflict) {
		return result, ErrWebMapRedoDiverged
	}
	if err != nil {
		return result, err
	}
	if err = checkWebMapEditBounds(ctx, tx, id); err != nil {
		return result, err
	}
	result.Revision = change.NewRevision
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?,?,?)", input.RequestID, digest, result.Revision, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
