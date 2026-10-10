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

var ErrWebMapActionInvalid = errors.New("проверьте действие, выбранный узел, родителя и порядок ветви")
var ErrWebMapStructure = errors.New("корень нельзя перемещать или удалять; новый родитель должен находиться вне перемещаемой ветви")

type WebMapAction struct {
	Revision  int64                    `json:"revision"`
	RequestID string                   `json:"request_id"`
	ParentID  string                   `json:"parent_id,omitempty"`
	Position  int                      `json:"position"`
	Mode      ClassicMindMapDeleteMode `json:"mode,omitempty"`
	Locked    *bool                    `json:"locked,omitempty"`
	Kind      *ClassicMindMapNodeKind  `json:"kind,omitempty"`
}

// ActWebMapNodeIn applies a structured operation using the same transaction as
// the CLI. The browser cannot supply SQL, file paths, command lines or actors.
func ActWebMapNodeIn(ctx context.Context, dir, actor, mapID, nodeID, action string, in WebMapAction) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.ActWebMapNodeIn")(&applogResult1)

	if !webMapExactID(nodeID) {
		return WebMapNodeEdited{}, ErrWebMapActionInvalid
	}
	switch action {
	case "move":
		if !webMapExactID(in.ParentID) || in.Position < -1 || in.Position > 1000 || in.Mode != "" || in.Locked != nil || in.Kind != nil {
			return WebMapNodeEdited{}, ErrWebMapActionInvalid
		}
	case "delete":
		if (in.Mode != ClassicMindMapDeleteBranch && in.Mode != ClassicMindMapDeletePromoteChildren) || in.ParentID != "" || in.Locked != nil || in.Kind != nil {
			return WebMapNodeEdited{}, ErrWebMapActionInvalid
		}
	case "properties":
		if (in.Locked == nil && in.Kind == nil) || (in.Kind != nil && !validClassicMindMapNodeKind(*in.Kind)) || in.ParentID != "" || in.Mode != "" {
			return WebMapNodeEdited{}, ErrWebMapActionInvalid
		}
	default:
		return WebMapNodeEdited{}, ErrWebMapActionInvalid
	}
	historyAction := map[string]string{"move": "move_node", "delete": "delete_node:" + string(in.Mode), "properties": "edit_node"}[action]
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, struct {
		Node, Action string
		Input        WebMapAction
	}{nodeID, action, in}, historyAction, func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := exactWebMapNode(tx, item.ID, nodeID)
		if err != nil {
			return "", err
		}
		switch action {
		case "move":
			parent, err := exactWebMapNode(tx, item.ID, in.ParentID)
			if err != nil {
				return "", err
			}
			nodes, err := queryClassicMindMapNodes(tx, item.ID, `n.deleted_at=''`)
			if err != nil {
				return "", err
			}
			if node.ID == item.RootNodeID || node.ID == parent.ID || classicMindMapDescendant(nodes, node.ID, parent.ID) {
				return "", ErrWebMapStructure
			}
			if classicMindMapNodeDepth(nodes, parent.ID)+classicMindMapSubtreeHeight(nodes, node.ID) > MaxClassicMindMapDepth {
				return "", ErrWebMapEditLimit
			}
			moved, err := moveClassicMindMapNodeTx(tx, item, nodeID, in.ParentID, in.Position)
			return moved.ID, err
		case "delete":
			if node.ID == item.RootNodeID {
				return "", ErrWebMapStructure
			}
			_, err := deleteClassicMindMapNodeTx(tx, item, nodeID, in.Mode)
			return node.ID, err
		default:
			updated, err := editClassicMindMapNodeTx(tx, item, nodeID, ClassicMindMapNodePatch{Locked: in.Locked, Kind: in.Kind})
			return updated.ID, err
		}
	})
}

func webMapExactID(id string) bool { return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id }

func exactWebMapNode(tx *sql.Tx, mapID, nodeID string) (ClassicMindMapNode, error) {
	nodes, err := queryClassicMindMapNodes(tx, mapID, `n.id=? AND n.deleted_at=''`, nodeID)
	if err != nil {
		return ClassicMindMapNode{}, err
	}
	if len(nodes) != 1 {
		return ClassicMindMapNode{}, ErrClassicMindMapNodeNotFound
	}
	return nodes[0], nil
}

// runWebMapMutation bounds snapshots before loading them and commits the change
// and its retry receipt together. Existing alpha receipts remain compatible.
func runWebMapMutation(ctx context.Context, dir, actor, mapID string, revision int64, requestID string, payload any, action string, change classicMindMapMutation) (WebMapNodeEdited, error) {
	var result WebMapNodeEdited
	if !webMapExactID(mapID) || revision <= 0 || len(requestID) != 32 || strings.Trim(requestID, "0123456789abcdef") != "" {
		return result, ErrWebMapActionInvalid
	}
	encoded, err := json.Marshal(struct {
		Actor, Map, Action string
		Payload            any
	}{actor, mapID, action, payload})
	if err != nil {
		return result, err
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_actions(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,node_id TEXT NOT NULL,created TEXT NOT NULL)`); err != nil {
		return result, err
	}
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision,node_id FROM web_map_actions WHERE request_id=?`, requestID).Scan(&hash, &result.Revision, &result.NodeID)
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
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM mind_maps WHERE id=? AND deleted_at=''`, mapID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClassicMindMapNotFound
	}
	if err != nil {
		return result, err
	}
	if current != revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	if err = validateWebMapMutationBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	receipts, err := countWebMapNodeReceipts(ctx, tx)
	if err != nil {
		return result, err
	}
	if receipts >= MaxWebMapMutationReceipts {
		return result, ErrWebMapEditLimit
	}
	_, history, err := mutateClassicMindMapTx(tx, mapID, revision, actor, "изменено через веб", action, func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		id, e := change(tx, item)
		result.NodeID = id
		return id, e
	})
	if err != nil {
		return result, err
	}
	if err = validateWebMapMutationBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	result.Revision = history.NewRevision
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_actions VALUES(?,?,?,?,?)`, requestID, digest, result.Revision, result.NodeID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func validateWebMapMutationBounds(ctx context.Context, tx *sql.Tx, mapID string) error {
	if err := checkWebMapEditBounds(ctx, tx, mapID); err != nil {
		return err
	}
	nodes, err := queryClassicMindMapNodes(tx, mapID, `n.deleted_at=''`)
	if err != nil {
		return err
	}
	var root string
	if err = tx.QueryRowContext(ctx, `SELECT root_node_id FROM mind_maps WHERE id=?`, mapID).Scan(&root); err != nil {
		return err
	}
	roots := 0
	for _, n := range nodes {
		if n.ParentID == "" {
			roots++
			if n.ID != root {
				return ErrClassicMindMapRenderLimit
			}
		}
	}
	if roots != 1 {
		return ErrClassicMindMapRenderLimit
	}
	return validateClassicMindMapBrowserRender(nodes)
}
