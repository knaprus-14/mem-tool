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

var ErrWebMapNodeInvalid = errors.New("проверьте поля узла: название до 512 символов, описание и текст до 65536 каждый")
var ErrWebMapEditLimit = errors.New("достигнут предел веб-редактирования: карта до 1000 узлов, 2000 источников, глубины 256 и 4 МиБ данных; история базы до 64 МиБ, журнал повторов до 100000 операций и 16 МиБ; данные сохранены")

// Keep retry keys rather than evicting them: evicting an old key could apply a
// delayed request twice. Capacity is independent from the undo history budget.
const MaxWebMapMutationReceipts = 100000
const maxWebMapMutationReceiptBytes = 16 << 20

var ErrWebMapEditRequest = errors.New("эта попытка уже сохранена с другим содержимым; закройте форму и обновите карту")

type WebMapNodeEdit struct {
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
	Label     string `json:"label"`
	Summary   string `json:"summary"`
	Body      string `json:"body"`
}
type WebMapNodeEdited struct {
	Revision int64  `json:"revision"`
	NodeID   string `json:"node_id,omitempty"`
	Replayed bool   `json:"replayed"`
}

// EditWebMapNodeIn shares the classic edit/history transaction, adding a retry
// receipt before commit. It never opens vectors, runs import recovery or resolves
// source paths. Identifiers are exact, unlike CLI-friendly reference resolution.
func EditWebMapNodeIn(ctx context.Context, dir, actor, mapID, nodeID string, input WebMapNodeEdit) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.EditWebMapNodeIn")(&applogResult1)

	return mutateWebMapNodeIn(ctx, dir, actor, mapID, nodeID, input, false)
}

// AddWebMapNodeIn appends a manual subtopic to the exact selected parent.
func AddWebMapNodeIn(ctx context.Context, dir, actor, mapID, parentID string, input WebMapNodeEdit) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.AddWebMapNodeIn")(&applogResult1)

	return mutateWebMapNodeIn(ctx, dir, actor, mapID, parentID, input, true)
}

func mutateWebMapNodeIn(ctx context.Context, dir, actor, mapID, nodeID string, input WebMapNodeEdit, adding bool) (WebMapNodeEdited, error) {
	input.Label = strings.TrimSpace(input.Label)
	input.Summary = strings.TrimSpace(input.Summary)
	if mapID == "" || nodeID == "" || len(mapID) > 128 || len(nodeID) > 128 || input.Revision <= 0 || len(input.RequestID) != 32 || strings.Trim(input.RequestID, "0123456789abcdef") != "" || input.Label == "" || !utf8.ValidString(input.Label+input.Summary+input.Body) || utf8.RuneCountInString(input.Label) > 512 || utf8.RuneCountInString(input.Summary) > 65536 || utf8.RuneCountInString(input.Body) > 65536 {
		return WebMapNodeEdited{}, ErrWebMapNodeInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map, Node string
		Input            WebMapNodeEdit
	}{actor, mapID, nodeID, input})
	if adding {
		encoded = append([]byte("add-child\n"), encoded...)
	}
	digest := prefixedSHA256(encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapNodeEdited{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapNodeEdited{}, err
	}
	defer tx.Rollback()
	table := "web_map_node_edits"
	schema := `CREATE TABLE IF NOT EXISTS web_map_node_edits(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL)`
	if adding {
		table = "web_map_node_adds"
		schema = `CREATE TABLE IF NOT EXISTS web_map_node_adds(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL,node_id TEXT NOT NULL)`
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return WebMapNodeEdited{}, err
	}
	var hash string
	var result WebMapNodeEdited
	if adding {
		err = tx.QueryRowContext(ctx, `SELECT request_hash,revision,node_id FROM web_map_node_adds WHERE request_id=?`, input.RequestID).Scan(&hash, &result.Revision, &result.NodeID)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT request_hash,revision FROM web_map_node_edits WHERE request_id=?`, input.RequestID).Scan(&hash, &result.Revision)
	}

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
	var root string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT root_node_id,revision FROM mind_maps WHERE id=? AND deleted_at=''`, mapID).Scan(&root, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClassicMindMapNotFound
	}
	if err != nil {
		return result, err
	}
	if revision != input.Revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	var found string
	err = tx.QueryRowContext(ctx, `SELECT id FROM mind_map_nodes WHERE id=? AND map_id=? AND deleted_at=''`, nodeID, mapID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClassicMindMapNodeNotFound
	}
	if err != nil {
		return result, err
	}
	if err = checkWebMapEditBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	receipts, err := countWebMapNodeReceipts(ctx, tx)
	if err != nil {
		return result, err
	}
	if receipts >= MaxWebMapMutationReceipts {
		return result, ErrWebMapEditLimit
	}

	nodes, err := queryClassicMindMapNodes(tx, mapID, `n.deleted_at=''`)
	if err != nil {
		return result, err
	}
	roots := 0
	for _, n := range nodes {
		if n.ParentID == "" {
			roots++
			if n.ID != root {
				return result, ErrClassicMindMapRenderLimit
			}
		}
	}
	if roots != 1 {
		return result, ErrClassicMindMapRenderLimit
	}
	if err = validateClassicMindMapBrowserRender(nodes); err != nil {
		return result, err
	}
	if adding && (len(nodes) >= 1000 || classicMindMapNodeDepth(nodes, nodeID) >= MaxClassicMindMapDepth) {
		return result, ErrWebMapEditLimit
	}
	patch := ClassicMindMapNodePatch{Label: &input.Label, Summary: &input.Summary, BodyMarkdown: &input.Body}
	action, comment := "edit_node", "текст узла изменён через веб"
	if adding {
		action = "add_node"
		comment = "ветвь добавлена через веб"
	}
	_, change, err := mutateClassicMindMapTx(tx, mapID, input.Revision, actor, comment, action, func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		if adding {
			id, e := newClassicMindMapID("mmn-")
			if e != nil {
				return "", e
			}
			n, e := addClassicMindMapNodeTx(tx, item, nodeID, id, input.Label, -1, ClassicMindMapNodeSubtopic, input.Summary, input.Body)
			if e == nil {
				result.NodeID = n.ID
			}
			return n.ID, e
		}
		n, e := editClassicMindMapNodeTx(tx, item, nodeID, patch)
		return n.ID, e
	})
	if err != nil {
		return result, err
	}
	if err = checkWebMapEditBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	result.Revision = change.NewRevision
	query := "INSERT INTO " + table + " VALUES(?,?,?,?)"
	args := []any{input.RequestID, digest, result.Revision, time.Now().UTC().Format(time.RFC3339Nano)}
	if adding {
		query = "INSERT INTO " + table + " VALUES(?,?,?,?,?)"
		args = append(args, result.NodeID)
	}
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return result, err
	}

	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func checkWebMapEditBounds(ctx context.Context, tx *sql.Tx, mapID string) error {
	var total int64
	for _, table := range []struct {
		name, columns string
		max           int
	}{
		{"mind_maps", "id title description mode status root_node_id created updated", 1},
		{"mind_map_nodes", "id map_id parent_id label summary body_markdown kind origin style_json created updated", 1000},
		{"mind_map_node_sources", "id map_id node_id kind title locator url knowledge_node_id citation_id document_id document_revision chunk_hash evidence_hash source_path excerpt created", 2000},
	} {
		fields := strings.Fields(table.columns)
		for i, col := range fields {
			fields[i] = "length(CAST(" + col + " AS BLOB))"
		}
		key := "map_id"
		if table.name == "mind_maps" {
			key = "id"
		}
		var count int
		var size int64
		err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM("+strings.Join(fields, "+")+"),0) FROM "+table.name+" WHERE "+key+"=? AND deleted_at=''", mapID).Scan(&count, &size)
		if err != nil {
			return err
		}
		if count > table.max {
			return ErrWebMapEditLimit
		}
		total += size
	}
	if total > 4<<20 {
		return ErrWebMapEditLimit
	}
	var history int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(before_json AS BLOB))+length(CAST(after_json AS BLOB))),0) FROM mind_map_changes`).Scan(&history); err != nil {
		return err
	}
	if history > 64<<20 {
		return ErrWebMapEditLimit
	}
	return nil
}

// Count receipts and enforce the combined byte budget, without deleting retry
// keys or changing the hashes of already confirmed edits.
func countWebMapNodeReceipts(ctx context.Context, tx *sql.Tx) (int, error) {
	total := 0
	var bytes int64
	for _, table := range []string{"web_map_node_edits", "web_map_node_adds", "web_map_undos", "web_map_redos", "web_map_actions", "web_map_snapshots"} {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			return 0, err
		}
		if exists == 0 {
			continue
		}
		columns := "request_id||request_hash||revision"
		if table == "web_map_snapshots" {
			columns += "||digest"
		} else {
			columns += "||created"
		}
		if table == "web_map_node_adds" || table == "web_map_actions" {
			columns += "||node_id"
		}
		var count int
		var tableBytes int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(CAST("+columns+" AS BLOB))),0) FROM "+table).Scan(&count, &tableBytes); err != nil {
			return 0, err
		}
		total += count
		bytes += tableBytes
	}
	// Reserve enough for the next bounded receipt before its atomic mutation.
	if bytes+512 > maxWebMapMutationReceiptBytes {
		return 0, ErrWebMapEditLimit
	}
	return total, nil
}
