package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

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

type WebMapDuplicate struct {
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
	Title     string `json:"title"`
}

func DuplicateWebMapIn(ctx context.Context, dir, owner, mapID string, in WebMapDuplicate) (applogResult0 WebMapCreated, applogResult1 error) {
	defer applog.Track("pkg.mem.DuplicateWebMapIn")(&applogResult1)

	in.Title = strings.TrimSpace(in.Title)
	if !webMapExactID(mapID) || in.Revision <= 0 || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || in.Title == "" || !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) > 512 {
		return WebMapCreated{}, ErrWebMapInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	encoded, _ := json.Marshal(struct {
		Action, Owner, Map string
		Input              WebMapDuplicate
	}{"duplicate", owner, mapID, in})
	digest := prefixedSHA256(encoded)
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapCreated{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapCreated{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_creates(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL,created TEXT NOT NULL)`); err != nil {
		return WebMapCreated{}, err
	}
	var id, hash string
	err = tx.QueryRowContext(ctx, `SELECT map_id,request_hash FROM web_map_creates WHERE request_id=?`, in.RequestID).Scan(&id, &hash)
	if err == nil {
		if hash != digest {
			return WebMapCreated{}, ErrWebMapRequest
		}
		return WebMapCreated{ID: id, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WebMapCreated{}, err
	}
	source, err := readWebMapDocumentTx(ctx, tx, mapID)
	if err != nil {
		return WebMapCreated{}, err
	}
	if source.Map.Revision != in.Revision {
		return WebMapCreated{}, ErrClassicMindMapRevisionConflict
	}
	var maps, receipts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_maps`).Scan(&maps); err != nil {
		return WebMapCreated{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_creates`).Scan(&receipts); err != nil {
		return WebMapCreated{}, err
	}
	if maps >= 1000 || receipts >= 1000 {
		return WebMapCreated{}, ErrWebMapLimit
	}
	doc, err := importClassicMindMapTx(tx, duplicateClassicMindMapDraft(source, in.Title), owner, "создана независимая копия через веб")
	if err != nil {
		return WebMapCreated{}, err
	}
	if err = validateWebMapMutationBounds(ctx, tx, doc.Map.ID); err != nil {
		return WebMapCreated{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_creates VALUES(?,?,?,?)`, in.RequestID, digest, doc.Map.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return WebMapCreated{}, err
	}
	return WebMapCreated{ID: doc.Map.ID}, tx.Commit()
}
