package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"strings"
	"time"
	"unicode/utf8"
)

type WebMapSnapshotInput struct {
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}
type WebMapSnapshotSaved struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Replayed bool   `json:"replayed"`
}
type WebMapSnapshotPage struct {
	Revision   int64                    `json:"revision"`
	Items      []ClassicMindMapSnapshot `json:"items"`
	NextBefore int64                    `json:"next_before"`
}

func CreateWebMapSnapshotIn(ctx context.Context, dir, actor, mapID string, in WebMapSnapshotInput) (applogResult0 WebMapSnapshotSaved, applogResult1 error) {
	defer applog.Track("pkg.mem.CreateWebMapSnapshotIn")(&applogResult1)

	var result WebMapSnapshotSaved
	in.Reason = strings.TrimSpace(in.Reason)
	if !webMapExactID(mapID) || in.Revision <= 0 || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 512 || !utf8.ValidString(in.Reason) {
		return result, ErrWebMapActionInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map string
		Input      WebMapSnapshotInput
	}{actor, mapID, in})
	hash := prefixedSHA256(encoded)
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_snapshots(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL)`); err != nil {
		return result, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision,digest FROM web_map_snapshots WHERE request_id=?`, in.RequestID).Scan(&previous, &result.Revision, &result.Digest)
	if err == nil {
		if previous != hash {
			return result, ErrWebMapEditRequest
		}
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	revision, err := webMapRevision(ctx, tx, mapID)
	if err != nil {
		return result, err
	}
	if revision != in.Revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	if err = validateWebMapMutationBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	count, err := countWebMapNodeReceipts(ctx, tx)
	if err != nil {
		return result, err
	}
	if count >= MaxWebMapMutationReceipts {
		return result, ErrWebMapEditLimit
	}
	var bytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(document_json AS BLOB))),0) FROM mind_map_snapshots`).Scan(&bytes); err != nil {
		return result, err
	}
	if bytes > 60<<20 {
		return result, ErrWebMapEditLimit
	}
	result.Digest, err = createClassicMindMapSnapshotTx(tx, mapID, in.Reason, in.Revision)
	if err != nil {
		return result, err
	}
	result.Revision = in.Revision
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(document_json AS BLOB))),0) FROM mind_map_snapshots`).Scan(&bytes); err != nil {
		return result, err
	}
	if bytes > 64<<20 {
		return result, ErrWebMapEditLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_snapshots VALUES(?,?,?,?)`, in.RequestID, hash, result.Revision, result.Digest); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func ListWebMapSnapshotsIn(ctx context.Context, dir, mapID string, before int64) (applogResult0 WebMapSnapshotPage, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapSnapshotsIn")(&applogResult1)

	page := WebMapSnapshotPage{Items: []ClassicMindMapSnapshot{}}
	if !webMapExactID(mapID) || before < 0 {
		return page, ErrWebMapActionInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return page, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	page.Revision, err = webMapRevision(ctx, tx, mapID)
	if err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,map_id,revision,substr(reason,1,512),substr(document_digest,1,80),substr(created,1,64) FROM mind_map_snapshots WHERE map_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT 31`, mapID, before, before)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item ClassicMindMapSnapshot
		if err = rows.Scan(&item.ID, &item.MapID, &item.Revision, &item.Reason, &item.DocumentDigest, &item.Created); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if len(page.Items) > 30 {
		page.Items = page.Items[:30]
		page.NextBefore = page.Items[29].ID
	}
	return page, tx.Commit()
}
