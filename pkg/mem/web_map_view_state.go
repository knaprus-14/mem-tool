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

var (
	ErrWebMapViewStateInvalid  = errors.New("проверьте параметры личного представления карты")
	ErrWebMapViewStateNotFound = errors.New("карта не найдена или удалена")
	ErrWebMapViewStateConflict = errors.New("личное представление изменилось; перечитайте его перед сохранением")
	ErrWebMapViewStateStale    = errors.New("карта изменилась; обновите её перед сохранением представления")
	ErrWebMapViewStateRequest  = errors.New("ключ повтора уже использован для другого представления")
	ErrWebMapViewStateLimit    = errors.New("достигнут предел размера личных представлений карт")
)

const (
	MaxWebMapViewStateBytes        = 128 << 10
	MaxWebMapViewStateCollapsed    = 1000
	MaxWebMapViewStateReceipts     = 128
	maxWebMapViewStateRows         = 10000
	maxWebMapViewStateAllBytes     = 64 << 20
	maxWebMapViewStateReceiptBytes = 64 << 20
)

type WebMapViewState struct {
	DisplayMode     string   `json:"display_mode"`
	Layout          string   `json:"layout"`
	SelectedNodeID  string   `json:"selected_node_id"`
	Collapsed       []string `json:"collapsed"`
	Scale           float64  `json:"scale"`
	ScrollX         float64  `json:"scroll_x"`
	ScrollY         float64  `json:"scroll_y"`
	TableSort       string   `json:"table_sort"`
	TableDescending bool     `json:"table_descending"`
	TableQuery      string   `json:"table_query"`
	TablePage       int      `json:"table_page"`
}

type WebMapViewStateInput struct {
	Revision    int64           `json:"revision"`
	MapRevision int64           `json:"map_revision"`
	RequestID   string          `json:"request_id"`
	State       WebMapViewState `json:"state"`
}

type WebMapViewStateResult struct {
	Revision           int64           `json:"revision"`
	MapRevision        int64           `json:"map_revision"`
	CurrentMapRevision int64           `json:"current_map_revision"`
	State              WebMapViewState `json:"state"`
	Changed            bool            `json:"changed"`
	Replayed           bool            `json:"replayed"`
}

const webMapViewStateSchema = `CREATE TABLE IF NOT EXISTS web_map_view_states(
 actor_key TEXT NOT NULL,map_id TEXT NOT NULL,revision INTEGER NOT NULL,map_revision INTEGER NOT NULL,
 payload TEXT NOT NULL,payload_hash TEXT NOT NULL,PRIMARY KEY(actor_key,map_id));
CREATE TABLE IF NOT EXISTS web_map_view_state_receipts(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,actor_key TEXT NOT NULL,map_id TEXT NOT NULL,request_id TEXT NOT NULL,
 request_hash TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,UNIQUE(actor_key,map_id,request_id));
CREATE INDEX IF NOT EXISTS web_map_view_state_receipts_scope ON web_map_view_state_receipts(actor_key,map_id,seq);
CREATE TRIGGER IF NOT EXISTS web_map_view_state_receipts_no_update BEFORE UPDATE ON web_map_view_state_receipts
 BEGIN SELECT RAISE(ABORT,'map view receipt is immutable'); END;`

func webMapViewActorKey(actor string) (string, error) {
	if !validDocumentAnnotationActor(actor) || strings.TrimSpace(actor) != actor || strings.ContainsFunc(actor, unicode.IsControl) {
		return "", ErrWebMapViewStateInvalid
	}
	return prefixedSHA256([]byte("web-map-view-state\x00" + actor)), nil
}

func validWebMapViewState(state WebMapViewState) bool {
	if state.DisplayMode != "diagram" && state.DisplayMode != "outline" && state.DisplayMode != "table" {
		return false
	}
	if state.Layout != "right" && state.Layout != "down" && state.Layout != "bilateral" {
		return false
	}
	if state.TableSort != "tree" && state.TableSort != "label" && state.TableSort != "kind" && state.TableSort != "sources" {
		return false
	}
	if !validReadingID(state.SelectedNodeID, "mmn-") || state.Collapsed == nil || len(state.Collapsed) > MaxWebMapViewStateCollapsed ||
		math.IsNaN(state.Scale) || math.IsInf(state.Scale, 0) || state.Scale < 0.05 || state.Scale > 2 ||
		!validReadingScroll(state.ScrollX) || !validReadingScroll(state.ScrollY) || state.TablePage < 0 || state.TablePage > 999 || !validDocumentAnnotationText(state.TableQuery, 200) {
		return false
	}
	seen := make(map[string]bool, len(state.Collapsed))
	for _, id := range state.Collapsed {
		if !validReadingID(id, "mmn-") || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

type webMapViewTopology struct {
	revision int64
	root     string
	nodes    map[string]string
	branches map[string]bool
}

func readWebMapViewTopologyTx(ctx context.Context, tx *sql.Tx, mapID string) (webMapViewTopology, error) {
	out := webMapViewTopology{nodes: map[string]string{}, branches: map[string]bool{}}
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, ErrWebMapViewStateNotFound
	}
	err = tx.QueryRowContext(ctx, `SELECT revision,substr(root_node_id,1,37) FROM mind_maps WHERE id=? AND deleted_at=''`, mapID).Scan(&out.revision, &out.root)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrWebMapViewStateNotFound
	}
	if err != nil {
		return out, err
	}
	if out.revision < 1 || !validReadingID(out.root, "mmn-") {
		return out, ErrWebMapViewStateInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT substr(id,1,37),substr(parent_id,1,37) FROM mind_map_nodes WHERE map_id=? AND deleted_at='' ORDER BY id LIMIT 1001`, mapID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent string
		if err = rows.Scan(&id, &parent); err != nil {
			return out, err
		}
		if !validReadingID(id, "mmn-") || (parent != "" && !validReadingID(parent, "mmn-")) {
			return out, ErrWebMapViewStateInvalid
		}
		out.nodes[id] = parent
		if parent != "" {
			out.branches[parent] = true
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.nodes) > 1000 {
		return out, ErrWebMapViewStateLimit
	}
	if parent, ok := out.nodes[out.root]; !ok || parent != "" {
		return out, ErrWebMapViewStateInvalid
	}
	for id, parent := range out.nodes {
		if parent == "" {
			if id != out.root {
				return out, ErrWebMapViewStateInvalid
			}
		} else if _, ok := out.nodes[parent]; !ok {
			return out, ErrWebMapViewStateInvalid
		}
	}
	return out, nil
}

func defaultWebMapViewState(topology webMapViewTopology) WebMapViewStateResult {
	return WebMapViewStateResult{MapRevision: topology.revision, CurrentMapRevision: topology.revision, State: WebMapViewState{DisplayMode: "diagram", Layout: "right", SelectedNodeID: topology.root, Collapsed: []string{}, Scale: 1, TableSort: "tree"}}
}

func decodeWebMapViewState(payload, hash string) (WebMapViewStateResult, error) {
	var out WebMapViewStateResult
	if len(payload) > MaxWebMapViewStateBytes {
		return out, ErrWebMapViewStateLimit
	}
	if prefixedSHA256([]byte(payload)) != hash || json.Unmarshal([]byte(payload), &out) != nil || out.Revision < 1 || out.MapRevision < 1 || out.MapRevision != out.CurrentMapRevision || out.Changed || out.Replayed || !validWebMapViewState(out.State) {
		return out, ErrWebMapViewStateInvalid
	}
	return out, nil
}

func readWebMapViewStateTx(ctx context.Context, tx *sql.Tx, key, mapID string) (WebMapViewStateResult, bool, error) {
	var out WebMapViewStateResult
	exists, err := annotationTableExists(ctx, tx, "web_map_view_states")
	if err != nil || !exists {
		return out, false, err
	}
	var size, revision, mapRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,map_revision,length(CAST(payload AS BLOB)) FROM web_map_view_states WHERE actor_key=? AND map_id=?`, key, mapID).Scan(&revision, &mapRevision, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if size < 1 || size > MaxWebMapViewStateBytes {
		return out, false, ErrWebMapViewStateLimit
	}
	var payload, hash string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM web_map_view_states WHERE actor_key=? AND map_id=?`, key, mapID).Scan(&payload, &hash); err != nil {
		return out, false, err
	}
	out, err = decodeWebMapViewState(payload, hash)
	if err == nil && (out.Revision != revision || out.MapRevision != mapRevision) {
		err = ErrWebMapViewStateInvalid
	}
	return out, true, err
}

// Reading prunes references only in the response. It neither migrates a legacy
// database nor changes a saved view, map revision, source, snapshot or history.
func ReadWebMapViewStateIn(ctx context.Context, dir, actor, mapID string) (WebMapViewStateResult, error) {
	var out WebMapViewStateResult
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
	stored, found, err := readWebMapViewStateTx(ctx, tx, key, mapID)
	if err != nil {
		return out, err
	}
	if !found {
		return defaultWebMapViewState(topology), nil
	}
	out = stored
	out.CurrentMapRevision = topology.revision
	out.Changed = out.MapRevision != topology.revision
	if _, ok := topology.nodes[out.State.SelectedNodeID]; !ok {
		out.State.SelectedNodeID = topology.root
		out.Changed = true
	}
	out.State.Collapsed = []string{}
	for _, id := range stored.State.Collapsed {
		if topology.branches[id] {
			out.State.Collapsed = append(out.State.Collapsed, id)
		} else {
			out.Changed = true
		}
	}
	return out, nil
}

func readWebMapViewReceiptTx(ctx context.Context, tx *sql.Tx, key, mapID, requestID, digest string) (WebMapViewStateResult, bool, error) {
	var out WebMapViewStateResult
	var size int64
	var requestHash string
	err := tx.QueryRowContext(ctx, `SELECT request_hash,length(CAST(payload AS BLOB)) FROM web_map_view_state_receipts WHERE actor_key=? AND map_id=? AND request_id=?`, key, mapID, requestID).Scan(&requestHash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if requestHash != digest {
		return out, false, ErrWebMapViewStateRequest
	}
	if size < 1 || size > MaxWebMapViewStateBytes {
		return out, false, ErrWebMapViewStateLimit
	}
	var payload, hash string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM web_map_view_state_receipts WHERE actor_key=? AND map_id=? AND request_id=?`, key, mapID, requestID).Scan(&payload, &hash); err != nil {
		return out, false, err
	}
	out, err = decodeWebMapViewState(payload, hash)
	out.Replayed = true
	return out, true, err
}

// Receipts have a finite retry window: the last 128 per actor/map, additionally
// bounded by 64 MiB globally. Evicted requests fail the independent view CAS;
// they cannot silently repeat a save. The newest receipt always fits the cap.
func pruneWebMapViewReceiptsTx(ctx context.Context, tx *sql.Tx, key, mapID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_map_view_state_receipts WHERE actor_key=? AND map_id=? AND seq NOT IN (SELECT seq FROM web_map_view_state_receipts WHERE actor_key=? AND map_id=? ORDER BY seq DESC LIMIT 128)`, key, mapID, key, mapID); err != nil {
		return err
	}
	const sizeExpr = `length(CAST(payload AS BLOB))+length(actor_key)+length(map_id)+length(request_id)+length(request_hash)+length(payload_hash)+8`
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+sizeExpr+`),0) FROM web_map_view_state_receipts`).Scan(&total); err != nil {
		return err
	}
	for total > maxWebMapViewStateReceiptBytes {
		var seq, size int64
		if err := tx.QueryRowContext(ctx, `SELECT seq,`+sizeExpr+` FROM web_map_view_state_receipts ORDER BY seq LIMIT 1`).Scan(&seq, &size); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM web_map_view_state_receipts WHERE seq=?`, seq); err != nil {
			return err
		}
		total -= size
	}
	return nil
}

// SaveWebMapViewStateIn replaces only this actor's view. Every accepted save
// advances its revision, even for identical state; the map is never mutated.
// Exact retry recovery precedes current-map checks and storage admission.
// Replay returns the original confirmed result (apart from Replayed); callers
// should GET afterwards to obtain current topology and pruned references.
// Admission must not write the database or start another mutation.
func SaveWebMapViewStateIn(ctx context.Context, dir, actor, mapID string, in WebMapViewStateInput, admit func() error) (WebMapViewStateResult, error) {
	var out WebMapViewStateResult
	key, err := webMapViewActorKey(actor)
	if err != nil || !validReadingID(mapID, "mm-") || in.Revision < 0 || in.Revision == math.MaxInt64 || in.MapRevision < 1 || !validDocumentAnnotationRequest(in.RequestID) || !validWebMapViewState(in.State) {
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
	if _, err = tx.ExecContext(ctx, webMapViewStateSchema); err != nil {
		return out, err
	}
	prior, found, err := readWebMapViewReceiptTx(ctx, tx, key, mapID, in.RequestID, digest)
	if err != nil {
		return out, err
	}
	if found {
		return prior, nil
	}
	stored, _, err := readWebMapViewStateTx(ctx, tx, key, mapID)
	if err != nil {
		return out, err
	}
	if stored.Revision != in.Revision {
		return out, ErrWebMapViewStateConflict
	}
	topology, err := readWebMapViewTopologyTx(ctx, tx, mapID)
	if err != nil {
		return out, err
	}
	if topology.revision != in.MapRevision {
		return out, ErrWebMapViewStateStale
	}
	if _, ok := topology.nodes[in.State.SelectedNodeID]; !ok {
		return out, ErrWebMapViewStateInvalid
	}
	for _, id := range in.State.Collapsed {
		if !topology.branches[id] {
			return out, ErrWebMapViewStateInvalid
		}
	}
	if admit != nil {
		if err = admit(); err != nil {
			return out, err
		}
	}
	out = WebMapViewStateResult{Revision: in.Revision + 1, MapRevision: topology.revision, CurrentMapRevision: topology.revision, State: in.State}
	out.State.Collapsed = append([]string{}, in.State.Collapsed...)
	payload, err := json.Marshal(out)
	if err != nil {
		return WebMapViewStateResult{}, err
	}
	if len(payload) > MaxWebMapViewStateBytes {
		return WebMapViewStateResult{}, ErrWebMapViewStateLimit
	}
	var count, total int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))+length(actor_key)+length(map_id)+length(payload_hash)+16),0) FROM web_map_view_states WHERE NOT(actor_key=? AND map_id=?)`, key, mapID).Scan(&count, &total); err != nil {
		return WebMapViewStateResult{}, err
	}
	hash := prefixedSHA256(payload)
	if count >= maxWebMapViewStateRows || total+int64(len(payload)+len(key)+len(mapID)+len(hash)+16) > maxWebMapViewStateAllBytes {
		return WebMapViewStateResult{}, ErrWebMapViewStateLimit
	}
	if in.Revision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO web_map_view_states(actor_key,map_id,revision,map_revision,payload,payload_hash) VALUES(?,?,?,?,?,?)`, key, mapID, out.Revision, out.MapRevision, string(payload), hash)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE web_map_view_states SET revision=?,map_revision=?,payload=?,payload_hash=? WHERE actor_key=? AND map_id=? AND revision=?`, out.Revision, out.MapRevision, string(payload), hash, key, mapID, in.Revision)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				err = ErrWebMapViewStateConflict
			}
		}
	}
	if err != nil {
		return WebMapViewStateResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_view_state_receipts(actor_key,map_id,request_id,request_hash,payload,payload_hash) VALUES(?,?,?,?,?,?)`, key, mapID, in.RequestID, digest, string(payload), hash); err != nil {
		return WebMapViewStateResult{}, err
	}
	if err = pruneWebMapViewReceiptsTx(ctx, tx, key, mapID); err != nil {
		return WebMapViewStateResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return WebMapViewStateResult{}, err
	}
	return out, nil
}
