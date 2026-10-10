package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

type WebMapImportPreviewInput struct {
	Content string `json:"content"`
}

type WebMapImportInput struct {
	Content   string `json:"content"`
	Digest    string `json:"digest"`
	RequestID string `json:"request_id"`
}

func PreviewWebMapImportIn(ctx context.Context, dir string, input WebMapImportPreviewInput) (ClassicMindMapImportPreview, error) {
	if _, err := parseClassicMindMapImport([]byte(input.Content)); err != nil {
		return ClassicMindMapImportPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return ClassicMindMapImportPreview{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClassicMindMapImportPreview{}, err
	}
	defer tx.Rollback()
	plan, err := previewClassicMindMapImportTx(ctx, tx, []byte(input.Content))
	return plan.Preview, err
}

// The receipt and entire new tree/history commit together. A successful retry
// returns the original result even if document revisions have changed since.
func ImportWebMapIn(ctx context.Context, dir, actor string, input WebMapImportInput) (WebMapCreated, error) {
	if len(input.RequestID) != 32 || strings.Trim(input.RequestID, "0123456789abcdef") != "" || len(input.Digest) != 71 || !strings.HasPrefix(input.Digest, "sha256:") {
		return WebMapCreated{}, ErrWebMapInvalid
	}
	if _, err := parseClassicMindMapImport([]byte(input.Content)); err != nil {
		return WebMapCreated{}, err
	}
	encoded, _ := json.Marshal(struct{ Action, Actor, Digest, Content string }{"import-native-json", actor, input.Digest, input.Content})
	requestHash := prefixedSHA256(encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
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
	var prior, hash string
	err = tx.QueryRowContext(ctx, `SELECT map_id,request_hash FROM web_map_creates WHERE request_id=?`, input.RequestID).Scan(&prior, &hash)
	if err == nil {
		if hash != requestHash {
			return WebMapCreated{}, ErrWebMapRequest
		}
		return WebMapCreated{ID: prior, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WebMapCreated{}, err
	}
	plan, err := previewClassicMindMapImportTx(ctx, tx, []byte(input.Content))
	if err != nil {
		return WebMapCreated{}, err
	}
	if plan.Preview.Digest != input.Digest {
		return WebMapCreated{}, ErrClassicMindMapImportPreview
	}
	if _, err = tx.ExecContext(ctx, classicMindMapSchema); err != nil {
		return WebMapCreated{}, err
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
	doc, err := importClassicMindMapPlanTx(tx, plan, actor, "импорт собственного JSON через веб-библиотеку")
	if err != nil {
		return WebMapCreated{}, err
	}
	if err = validateWebMapMutationBounds(ctx, tx, doc.Map.ID); err != nil {
		return WebMapCreated{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_creates VALUES(?,?,?,?)`, input.RequestID, requestHash, doc.Map.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return WebMapCreated{}, err
	}
	return WebMapCreated{ID: doc.Map.ID}, tx.Commit()
}
