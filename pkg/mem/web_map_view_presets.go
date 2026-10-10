package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

const MaxWebMapViewPresets = 20
const maxWebMapViewPresetsBytes = 1 << 20

type WebMapViewPreset struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	MapRevision int64           `json:"map_revision"`
	State       WebMapViewState `json:"state"`
	Changed     bool            `json:"changed"`
}

type WebMapViewPresets struct {
	Revision           int64              `json:"revision"`
	CurrentMapRevision int64              `json:"current_map_revision"`
	Items              []WebMapViewPreset `json:"items"`
}

// The collection CAS covers create/update/rename/delete, including an empty
// collection. Deleting the last item must never reset the revision to zero.
type WebMapViewPresetInput struct {
	WebMapViewStateInput
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

type WebMapViewPresetResult struct {
	Revision    int64  `json:"revision"`
	MapRevision int64  `json:"map_revision"`
	ID          string `json:"id"`
	Operation   string `json:"operation"`
	Replayed    bool   `json:"replayed"`
}

const webMapViewPresetSchema = `CREATE TABLE IF NOT EXISTS web_map_view_presets(
 actor_key TEXT NOT NULL,map_id TEXT NOT NULL,revision INTEGER NOT NULL,
 payload TEXT NOT NULL,payload_hash TEXT NOT NULL,PRIMARY KEY(actor_key,map_id));
CREATE TABLE IF NOT EXISTS web_map_view_preset_receipts(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,actor_key TEXT NOT NULL,map_id TEXT NOT NULL,request_id TEXT NOT NULL,
 request_hash TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,UNIQUE(actor_key,map_id,request_id));
CREATE INDEX IF NOT EXISTS web_map_view_preset_receipts_scope ON web_map_view_preset_receipts(actor_key,map_id,seq);
CREATE TRIGGER IF NOT EXISTS web_map_view_preset_receipts_no_update BEFORE UPDATE ON web_map_view_preset_receipts
 BEGIN SELECT RAISE(ABORT,'map view preset receipt is immutable'); END;`

func validWebMapViewPresetName(name string) bool {
	return strings.TrimSpace(name) == name && name != "" && validDocumentAnnotationText(name, 80) && !strings.ContainsFunc(name, unicode.IsControl)
}

func validWebMapViewPresetOperation(op string) bool {
	return op == "create" || op == "update" || op == "rename" || op == "delete"
}

func readWebMapViewPresetsTx(ctx context.Context, tx *sql.Tx, key, mapID string) (WebMapViewPresets, error) {
	out := WebMapViewPresets{Items: []WebMapViewPreset{}}
	exists, err := annotationTableExists(ctx, tx, "web_map_view_presets")
	if err != nil || !exists {
		return out, err
	}
	var size, revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,length(CAST(payload AS BLOB)) FROM web_map_view_presets WHERE actor_key=? AND map_id=?`, key, mapID).Scan(&revision, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if size < 1 || size > maxWebMapViewPresetsBytes {
		return out, ErrWebMapViewStateLimit
	}
	var payload, hash string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM web_map_view_presets WHERE actor_key=? AND map_id=?`, key, mapID).Scan(&payload, &hash); err != nil {
		return out, err
	}
	if prefixedSHA256([]byte(payload)) != hash || json.Unmarshal([]byte(payload), &out) != nil || out.Revision != revision || revision < 1 || out.CurrentMapRevision != 0 || out.Items == nil || len(out.Items) > MaxWebMapViewPresets {
		return out, ErrWebMapViewStateInvalid
	}
	ids := map[string]bool{}
	for i, item := range out.Items {
		if !validReadingID(item.ID, "mvp-") || ids[item.ID] || !validWebMapViewPresetName(item.Name) || item.MapRevision < 1 || item.Changed || !validWebMapViewState(item.State) {
			return out, ErrWebMapViewStateInvalid
		}
		ids[item.ID] = true
		for _, prior := range out.Items[:i] {
			if strings.EqualFold(prior.Name, item.Name) {
				return out, ErrWebMapViewStateInvalid
			}
		}
	}
	return out, nil
}

// GET does not migrate or write. Old references are adapted only in its result;
// rename/delete must retain other items' original state and map revision.
func ReadWebMapViewPresetsIn(ctx context.Context, dir, actor, mapID string) (WebMapViewPresets, error) {
	var out WebMapViewPresets
	key, err := webMapViewActorKey(actor)
	if err != nil || !validReadingID(mapID, "mm-") {
		return out, ErrWebMapViewStateInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return out, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	topology, err := readWebMapViewTopologyTx(ctx, tx, mapID)
	if err != nil {
		return out, err
	}
	out, err = readWebMapViewPresetsTx(ctx, tx, key, mapID)
	if err != nil {
		return out, err
	}
	out.CurrentMapRevision = topology.revision
	for i := range out.Items {
		item := &out.Items[i]
		item.Changed = item.MapRevision != topology.revision
		if _, ok := topology.nodes[item.State.SelectedNodeID]; !ok {
			item.State.SelectedNodeID = topology.root
			item.Changed = true
		}
		old := item.State.Collapsed
		item.State.Collapsed = []string{}
		for _, id := range old {
			if topology.branches[id] {
				item.State.Collapsed = append(item.State.Collapsed, id)
			} else {
				item.Changed = true
			}
		}
	}
	return out, nil
}

func readWebMapViewPresetReceiptTx(ctx context.Context, tx *sql.Tx, key, mapID, requestID, digest string) (WebMapViewPresetResult, bool, error) {
	var out WebMapViewPresetResult
	var size int64
	var requestHash string
	err := tx.QueryRowContext(ctx, `SELECT request_hash,length(CAST(payload AS BLOB)) FROM web_map_view_preset_receipts WHERE actor_key=? AND map_id=? AND request_id=?`, key, mapID, requestID).Scan(&requestHash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if requestHash != digest {
		return out, false, ErrWebMapViewStateRequest
	}
	if size < 1 || size > 1024 {
		return out, false, ErrWebMapViewStateLimit
	}
	var payload, hash string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM web_map_view_preset_receipts WHERE actor_key=? AND map_id=? AND request_id=?`, key, mapID, requestID).Scan(&payload, &hash); err != nil {
		return out, false, err
	}
	if prefixedSHA256([]byte(payload)) != hash || json.Unmarshal([]byte(payload), &out) != nil || out.Revision < 1 || out.MapRevision < 1 || !validReadingID(out.ID, "mvp-") || !validWebMapViewPresetOperation(out.Operation) || out.Replayed {
		return out, false, ErrWebMapViewStateInvalid
	}
	out.Replayed = true
	return out, true, nil
}

func pruneWebMapViewPresetReceiptsTx(ctx context.Context, tx *sql.Tx, key, mapID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM web_map_view_preset_receipts WHERE actor_key=? AND map_id=? AND seq NOT IN (SELECT seq FROM web_map_view_preset_receipts WHERE actor_key=? AND map_id=? ORDER BY seq DESC LIMIT 128)`, key, mapID, key, mapID)
	if err != nil {
		return err
	}
	const sizeExpr = `length(CAST(payload AS BLOB))+length(actor_key)+length(map_id)+length(request_id)+length(request_hash)+length(payload_hash)+8`
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+sizeExpr+`),0) FROM web_map_view_preset_receipts`).Scan(&total); err != nil {
		return err
	}
	for total > 16<<20 {
		var seq, size int64
		if err = tx.QueryRowContext(ctx, `SELECT seq,`+sizeExpr+` FROM web_map_view_preset_receipts ORDER BY seq LIMIT 1`).Scan(&seq, &size); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM web_map_view_preset_receipts WHERE seq=?`, seq); err != nil {
			return err
		}
		total -= size
	}
	return nil
}

func SaveWebMapViewPresetIn(ctx context.Context, dir, actor, mapID string, in WebMapViewPresetInput, admit func() error) (WebMapViewPresetResult, error) {
	var out WebMapViewPresetResult
	key, err := webMapViewActorKey(actor)
	if err != nil || !validReadingID(mapID, "mm-") || !validReadingID(in.ID, "mvp-") || !validWebMapViewPresetOperation(in.Operation) || in.Revision < 0 || in.Revision == math.MaxInt64 || in.MapRevision < 1 || !validDocumentAnnotationRequest(in.RequestID) || !validWebMapViewState(in.State) {
		return out, ErrWebMapViewStateInvalid
	}
	if in.Operation == "delete" {
		if in.Name != "" {
			return out, ErrWebMapViewStateInvalid
		}
	} else if !validWebMapViewPresetName(in.Name) {
		return out, ErrWebMapViewStateInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, ErrWebMapViewStateInvalid
	}
	if len(raw) > MaxWebMapViewStateBytes {
		return out, ErrWebMapViewStateLimit
	}
	digest := prefixedSHA256(raw)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return out, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, webMapViewPresetSchema); err != nil {
		return out, err
	}
	prior, found, err := readWebMapViewPresetReceiptTx(ctx, tx, key, mapID, in.RequestID, digest)
	if err != nil {
		return out, err
	}
	if found {
		return prior, nil
	}
	set, err := readWebMapViewPresetsTx(ctx, tx, key, mapID)
	if err != nil {
		return out, err
	}
	if set.Revision != in.Revision {
		return out, ErrWebMapViewStateConflict
	}
	topology, err := readWebMapViewTopologyTx(ctx, tx, mapID)
	if err != nil {
		return out, err
	}
	if topology.revision != in.MapRevision {
		return out, ErrWebMapViewStateStale
	}
	if in.Operation == "create" || in.Operation == "update" {
		if _, ok := topology.nodes[in.State.SelectedNodeID]; !ok {
			return out, ErrWebMapViewStateInvalid
		}
		for _, id := range in.State.Collapsed {
			if !topology.branches[id] {
				return out, ErrWebMapViewStateInvalid
			}
		}
	}
	index := -1
	for i, item := range set.Items {
		if item.ID == in.ID {
			index = i
		} else if in.Operation != "delete" && strings.EqualFold(item.Name, in.Name) {
			return out, ErrWebMapViewStateConflict
		}
	}
	if in.Operation == "create" {
		if index >= 0 {
			return out, ErrWebMapViewStateConflict
		}
		if len(set.Items) >= MaxWebMapViewPresets {
			return out, ErrWebMapViewStateLimit
		}
		set.Items = append(set.Items, WebMapViewPreset{ID: in.ID, Name: in.Name, MapRevision: topology.revision, State: in.State})
	} else {
		if index < 0 {
			return out, ErrWebMapViewStateConflict
		}
		switch in.Operation {
		case "update":
			set.Items[index] = WebMapViewPreset{ID: in.ID, Name: in.Name, MapRevision: topology.revision, State: in.State}
		case "rename":
			set.Items[index].Name = in.Name
		case "delete":
			set.Items = append(set.Items[:index], set.Items[index+1:]...)
		}
	}
	if admit != nil && in.Operation != "delete" {
		if err = admit(); err != nil {
			return out, err
		}
	}
	set.Revision++
	payload, err := json.Marshal(set)
	if err != nil {
		return out, err
	}
	if len(payload) > maxWebMapViewPresetsBytes {
		return out, ErrWebMapViewStateLimit
	}
	hash := prefixedSHA256(payload)
	var count, total int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))+length(actor_key)+length(map_id)+length(payload_hash)+8),0) FROM web_map_view_presets WHERE NOT(actor_key=? AND map_id=?)`, key, mapID).Scan(&count, &total); err != nil {
		return out, err
	}
	if (count >= 10000 || total+int64(len(payload)+len(key)+len(mapID)+len(hash)+8) > 64<<20) && in.Operation != "delete" {
		return out, ErrWebMapViewStateLimit
	}
	if in.Revision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO web_map_view_presets(actor_key,map_id,revision,payload,payload_hash) VALUES(?,?,?,?,?)`, key, mapID, set.Revision, string(payload), hash)
	} else {
		var changed sql.Result
		changed, err = tx.ExecContext(ctx, `UPDATE web_map_view_presets SET revision=?,payload=?,payload_hash=? WHERE actor_key=? AND map_id=? AND revision=?`, set.Revision, string(payload), hash, key, mapID, in.Revision)
		if err == nil {
			var n int64
			n, err = changed.RowsAffected()
			if err == nil && n != 1 {
				err = ErrWebMapViewStateConflict
			}
		}
	}
	if err != nil {
		return out, err
	}
	out = WebMapViewPresetResult{Revision: set.Revision, MapRevision: topology.revision, ID: in.ID, Operation: in.Operation}
	receipt, err := json.Marshal(out)
	if err != nil {
		return WebMapViewPresetResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_view_preset_receipts(actor_key,map_id,request_id,request_hash,payload,payload_hash) VALUES(?,?,?,?,?,?)`, key, mapID, in.RequestID, digest, string(receipt), prefixedSHA256(receipt)); err != nil {
		return WebMapViewPresetResult{}, err
	}
	if err = pruneWebMapViewPresetReceiptsTx(ctx, tx, key, mapID); err != nil {
		return WebMapViewPresetResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return WebMapViewPresetResult{}, err
	}
	return out, nil
}
