package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

type WebMapDelete struct {
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
}

// DeleteWebMapIn hides the whole map using the shared deleted_at marker. Its
// immutable history, nodes and document entries stay intact. Cancellation and
// the actor-bound retry receipt commit with the deletion, including for maps
// too large or malformed to render in the browser.
func DeleteWebMapIn(ctx context.Context, dir, actor, mapID string, in WebMapDelete) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.DeleteWebMapIn")(&applogResult1)

	var result WebMapNodeEdited
	if !webMapExactID(mapID) || in.Revision <= 0 || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" {
		return result, ErrWebMapActionInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map string
		Input      WebMapDelete
	}{actor, mapID, in})
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_deletions(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL UNIQUE,revision INTEGER NOT NULL,created TEXT NOT NULL)`); err != nil {
		return result, err
	}
	var hash string
	if _, err = tx.ExecContext(ctx, webMapDeletionHistorySchema); err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision FROM web_map_deletions WHERE request_id=? UNION ALL SELECT request_hash,revision FROM web_map_deletion_history WHERE request_id=?`, in.RequestID, in.RequestID).Scan(&hash, &result.Revision)
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
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, ErrClassicMindMapNotFound
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM mind_maps WHERE id=? AND deleted_at=''`, mapID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClassicMindMapNotFound
	}
	if err != nil {
		return result, err
	}
	if revision != in.Revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE mind_maps SET deleted_at=?,updated=?,revision=revision+1 WHERE id=? AND revision=? AND deleted_at=''`, now, now, mapID, revision)
	if err != nil {
		return result, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return result, ErrClassicMindMapRevisionConflict
	}
	var aiTables int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_ai_tasks'`).Scan(&aiTables); err != nil {
		return result, err
	}
	if aiTables > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='cancelled' WHERE map_id=? AND status IN ('queued','running','ready')`, mapID); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mind_map_generation_runs SET status='cancelled',completed=? WHERE map_id=? AND status IN ('running','preview')`, now, mapID); err != nil {
		return result, err
	}
	result.Revision = revision + 1
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_deletions VALUES(?,?,?,?,?)`, in.RequestID, digest, mapID, result.Revision, now); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

const webMapDeletionHistorySchema = `CREATE TABLE IF NOT EXISTS web_map_deletion_history(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);`

// Restore preserves every node and receipt. Cancelled AI work stays cancelled;
// restoring a map must never restart an old generation against a newer revision.
func RestoreWebMapIn(ctx context.Context, dir, actor, mapID string, in WebMapDelete) (result WebMapNodeEdited, err error) {
	defer applog.Track("pkg.mem.RestoreWebMapIn")(&err)
	if !webMapExactID(mapID) || in.Revision <= 0 || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" {
		return result, ErrWebMapActionInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map, Action string
		Input              WebMapDelete
	}{actor, mapID, "restore", in})
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
	if _, err = tx.ExecContext(ctx, webMapDeletionHistorySchema); err != nil {
		return result, err
	}
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision FROM web_map_deletion_history WHERE request_id=?`, in.RequestID).Scan(&hash, &result.Revision)
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
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, ErrClassicMindMapNotFound
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM mind_maps WHERE id=? AND deleted_at!=''`, mapID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClassicMindMapNotFound
	}
	if err != nil {
		return result, err
	}
	if revision != in.Revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE mind_maps SET deleted_at='',updated=?,revision=revision+1 WHERE id=? AND revision=?`, now, mapID, revision); err != nil {
		return result, err
	}
	var receipts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_deletions'`).Scan(&receipts); err != nil {
		return result, err
	}
	if receipts > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_deletion_history SELECT * FROM web_map_deletions WHERE map_id=?`, mapID); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM web_map_deletions WHERE map_id=?`, mapID); err != nil {
			return result, err
		}
	}
	result.Revision = revision + 1
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_deletion_history VALUES(?,?,?,?,?)`, in.RequestID, digest, mapID, result.Revision, now); err != nil {
		return result, err
	}
	err = tx.Commit()
	return result, err
}
