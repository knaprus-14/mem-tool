package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var ErrWebMapSnapshotNotFound = errors.New("снимок этой карты не найден")
var ErrWebMapSnapshotInvalid = errors.New("снимок повреждён или не подходит для восстановления")
var ErrWebMapSnapshotChanged = errors.New("снимок изменился; заново откройте его просмотр")

type WebMapSnapshotRestore struct {
	Revision       int64  `json:"revision"`
	SnapshotDigest string `json:"snapshot_digest"`
	RequestID      string `json:"request_id"`
}

// Preview deliberately exposes no locators, filesystem paths or arbitrary style
// JSON. The digest pins the stored document, not this redacted projection.
type WebMapSnapshotNode struct {
	WebMapNode
	Origin  ClassicMindMapNodeOrigin `json:"origin"`
	Sources []WebMapSource           `json:"sources"`
}

type WebMapSnapshotPreview struct {
	Revision         int64                  `json:"revision"`
	Snapshot         ClassicMindMapSnapshot `json:"snapshot"`
	Map              ClassicMindMap         `json:"map"`
	Nodes            []WebMapSnapshotNode   `json:"nodes"`
	CurrentNodeCount int                    `json:"current_node_count"`
}

// ReadWebMapSnapshotIn reads only bounded stored data, even when the mutation
// quota is exhausted. It never opens a file named by a historical source.
func ReadWebMapSnapshotIn(ctx context.Context, dir, mapID string, snapshotID, revision int64) (result WebMapSnapshotPreview, err error) {
	defer applog.Track("pkg.mem.ReadWebMapSnapshotIn")(&err)
	if !webMapExactID(mapID) || snapshotID <= 0 || revision <= 0 {
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
	result.Revision, err = webMapRevision(ctx, tx, mapID)
	if err != nil {
		return result, err
	}
	if result.Revision != revision {
		return result, ErrClassicMindMapRevisionConflict
	}
	var doc ClassicMindMapDocument
	result.Snapshot, doc, err = readWebMapSnapshotTx(ctx, tx, mapID, snapshotID)
	if err != nil {
		return result, err
	}
	result.Map = doc.Map
	result.Nodes = make([]WebMapSnapshotNode, 0, len(doc.Nodes))
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=? AND deleted_at=''`, mapID).Scan(&result.CurrentNodeCount); err != nil {
		return result, err
	}
	for _, n := range doc.Nodes {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		item := WebMapSnapshotNode{WebMapNode: WebMapNode{ID: n.ID, ParentID: n.ParentID, Position: n.Position, Label: n.Label, Summary: n.Summary, Body: n.BodyMarkdown, Kind: n.Kind, Locked: n.Locked, SourceCount: len(n.Sources)}, Origin: n.Origin, Sources: []WebMapSource{}}
		for _, source := range n.Sources {
			s, e := webMapSnapshotSource(ctx, tx, source)
			if e != nil {
				return result, e
			}
			item.Sources = append(item.Sources, s)
		}
		result.Nodes = append(result.Nodes, item)
	}
	return result, tx.Commit()
}

func webMapSnapshotSource(ctx context.Context, tx *sql.Tx, s ClassicMindMapSource) (WebMapSource, error) {
	item := WebMapSource{ID: s.ID, Kind: s.Kind, Title: s.Title, State: string(EvidenceMissing)}
	switch s.Kind {
	case ClassicMindMapSourceEvidence:
		if err := boundWebMapEvidence(tx, *s.Evidence); err != nil {
			return item, err
		}
		resolved, err := resolveClassicMindMapEvidenceWithQuery(tx, *s.Evidence)
		if err != nil {
			return item, err
		}
		item.State = string(resolved.State)
		redactWebMapSource(&s)
		item.Title, item.Excerpt, item.CitationID, item.Page = s.Title, s.Evidence.Excerpt, s.Evidence.CitationID, s.Evidence.Page
	case ClassicMindMapSourceURL:
		item.URL, item.State = s.URL, "url" // A saved URL is not verified evidence.
	case ClassicMindMapSourceKnowledgeNode:
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, s.KnowledgeNodeID).Scan(&count); err != nil {
			return item, err
		}
		item.GraphNodeID = s.KnowledgeNodeID
		if count == 1 {
			item.State = "graph_node"
		} // Existence does not certify its evidence.
	case ClassicMindMapSourceExternalFile:
		item.Title, item.State = "Локальный файл", "local_file"
		if strings.HasPrefix(s.Locator, attachmentPrefix) {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_attachments'`).Scan(&exists); err != nil {
				return item, err
			}
			var found int
			if exists == 1 {
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_attachments WHERE id=?`, strings.TrimPrefix(s.Locator, attachmentPrefix)).Scan(&found); err != nil {
					return item, err
				}
			}
			item.State = string(EvidenceMissing)
			if found == 1 {
				item.State, item.Attachment = "attachment", true
			}
		}
	}
	return item, nil
}

func readWebMapSnapshotTx(ctx context.Context, tx *sql.Tx, mapID string, snapshotID int64) (meta ClassicMindMapSnapshot, doc ClassicMindMapDocument, err error) {
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(document_json AS BLOB)) FROM mind_map_snapshots WHERE id=? AND map_id=?`, snapshotID, mapID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, doc, ErrWebMapSnapshotNotFound
	}
	if err != nil {
		return meta, doc, err
	}
	if size <= 0 || size > 8<<20 {
		return meta, doc, ErrWebMapEditLimit
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT id,map_id,revision,substr(reason,1,512),document_digest,substr(created,1,64),document_json FROM mind_map_snapshots WHERE id=? AND map_id=? AND length(document_digest)<=80`, snapshotID, mapID).Scan(&meta.ID, &meta.MapID, &meta.Revision, &meta.Reason, &meta.DocumentDigest, &meta.Created, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, doc, ErrWebMapSnapshotInvalid
	}
	if err != nil {
		return meta, doc, err
	}
	if json.Unmarshal([]byte(raw), &doc) != nil || doc.Version != ClassicMindMapFormatVersion || doc.Map.ID != mapID || doc.Map.Revision != meta.Revision || doc.Digest != meta.DocumentDigest {
		return meta, doc, ErrWebMapSnapshotInvalid
	}
	if len(doc.Nodes) == 0 || len(doc.Nodes) > 1000 {
		return meta, doc, ErrWebMapEditLimit
	}
	if !validClassicMindMapMode(doc.Map.Mode) || !validClassicMindMapStatus(doc.Map.Status) || validateClassicMindMapText("title", doc.Map.Title, MaxClassicMindMapTitleRunes, true) != nil || validateClassicMindMapText("description", doc.Map.Description, MaxClassicMindMapTextRunes, false) != nil {
		return meta, doc, ErrWebMapSnapshotInvalid
	}
	ids, sources, roots := map[string]bool{}, map[string]bool{}, 0
	for _, n := range doc.Nodes {
		if !webMapExactID(n.ID) || ids[n.ID] || n.MapID != mapID || n.Position < 0 || validateClassicMindMapNodeFields(n.Label, n.Summary, n.BodyMarkdown, n.Kind, n.Origin, n.Style) != nil {
			return meta, doc, ErrWebMapSnapshotInvalid
		}
		ids[n.ID] = true
		if n.ParentID == "" {
			roots++
			if n.ID != doc.Map.RootNodeID {
				return meta, doc, ErrWebMapSnapshotInvalid
			}
		}
		for _, s := range n.Sources {
			if !webMapExactID(s.ID) || sources[s.ID] || s.MapID != mapID || s.NodeID != n.ID || s.Position < 0 || validateClassicMindMapSource(s) != nil {
				return meta, doc, ErrWebMapSnapshotInvalid
			}
			sources[s.ID] = true
		}
	}
	if len(sources) > 2000 {
		return meta, doc, ErrWebMapEditLimit
	}
	if roots != 1 {
		return meta, doc, ErrWebMapSnapshotInvalid
	}
	if err = validateClassicMindMapBrowserRender(doc.Nodes); err != nil {
		return meta, doc, err
	}
	var canonical ClassicMindMapDocument
	canonical, _, err = finalizeClassicMindMapDocument(doc)
	if err != nil {
		return meta, doc, err
	}
	if canonical.Digest != meta.DocumentDigest {
		return meta, doc, ErrWebMapSnapshotInvalid
	}
	return meta, canonical, nil
}

// RestoreWebMapSnapshotIn has the same full-state contract as undo: lock flags
// and manual content are restored, and the replaced state remains in history.
// Documents, attachment blobs, other maps and immutable snapshots are untouched.
func RestoreWebMapSnapshotIn(ctx context.Context, dir, actor, mapID string, snapshotID int64, in WebMapSnapshotRestore) (result WebMapNodeEdited, err error) {
	defer applog.Track("pkg.mem.RestoreWebMapSnapshotIn")(&err)
	if !webMapExactID(mapID) || snapshotID <= 0 || in.Revision <= 0 || !validSHA256Digest(in.SnapshotDigest) || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" {
		return result, ErrWebMapHistoryInvalid
	}
	encoded, _ := json.Marshal(struct {
		Actor, Map string
		Snapshot   int64
		Input      WebMapSnapshotRestore
	}{actor, mapID, snapshotID, in})
	digest := prefixedSHA256(append([]byte("restore_snapshot\n"), encoded...))
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
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,revision,node_id FROM web_map_actions WHERE request_id=?`, in.RequestID).Scan(&previous, &result.Revision, &result.NodeID)
	if err == nil {
		if previous != digest {
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
	meta, desired, err := readWebMapSnapshotTx(ctx, tx, mapID, snapshotID)
	if err != nil {
		return result, err
	}
	if meta.DocumentDigest != in.SnapshotDigest {
		return result, ErrWebMapSnapshotChanged
	}
	// Corrupt historical IDs must never let the upsert helper steal another map's rows.
	for _, n := range desired.Nodes {
		var foreign int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_nodes WHERE id=? AND map_id<>?`, n.ID, mapID).Scan(&foreign); err != nil {
			return result, err
		}
		if foreign != 0 {
			return result, ErrWebMapSnapshotInvalid
		}
		for _, s := range n.Sources {
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_node_sources WHERE id=? AND map_id<>?`, s.ID, mapID).Scan(&foreign); err != nil {
				return result, err
			}
			if foreign != 0 {
				return result, ErrWebMapSnapshotInvalid
			}
		}
	}
	before, err := loadClassicMindMapDocument(tx, mapID)
	if err != nil {
		return result, err
	}
	before, beforeJSON, err := finalizeClassicMindMapDocument(before)
	if err != nil {
		return result, err
	}
	result.Revision, result.NodeID = revision+1, desired.Map.RootNodeID
	if err = restoreClassicMindMapDocumentTx(tx, mapID, desired, result.Revision); err != nil {
		return result, err
	}
	after, err := loadClassicMindMapDocument(tx, mapID)
	if err != nil {
		return result, err
	}
	after, afterJSON, err := finalizeClassicMindMapDocument(after)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,target_node_id,before_json,after_json,before_digest,after_digest,actor,comment,created) VALUES(?,?,?,'restore_snapshot',?,?,?,?,?,?,?,?)`, mapID, revision, result.Revision, result.NodeID, string(beforeJSON), string(afterJSON), before.Digest, after.Digest, normalizeClassicMindMapActor(actor), fmt.Sprintf("восстановлен снимок %d (ревизия %d) через веб", snapshotID, meta.Revision), now)
	if err != nil {
		return result, err
	}
	if err = validateWebMapMutationBounds(ctx, tx, mapID); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_actions VALUES(?,?,?,?,?)`, in.RequestID, digest, result.Revision, result.NodeID, now); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
