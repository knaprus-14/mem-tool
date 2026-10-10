package mem

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxMapAttachmentBytes = 8 << 20
const attachmentPrefix = "attachment:"

// Attachments are immutable, deduplicated blobs in the same transaction as the
// source and history. Undo/redo and portable database backups retain the bytes.
func AttachWebMapFileIn(ctx context.Context, dir, actor, mapID, nodeID string, in WebMapDelete, name string, content []byte) (WebMapNodeEdited, error) {
	if len(content) == 0 || len(content) > MaxMapAttachmentBytes || !utf8.ValidString(name) || len(name) > 512 || strings.ContainsAny(name, "/\\\x00\r\n") || strings.TrimSpace(name) == "" || !webMapExactID(nodeID) {
		return WebMapNodeEdited{}, ErrWebMapSourceInvalid
	}
	hash := strings.TrimPrefix(prefixedSHA256(content), "sha256:")
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, struct{ Node, Name, Hash string }{nodeID, name, hash}, "attach_source:external_file", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS web_map_attachments(id TEXT PRIMARY KEY,content BLOB NOT NULL);`); err != nil {
			return "", err
		}
		var size, found int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(length(content)),0),COUNT(CASE WHEN id=? THEN 1 END) FROM web_map_attachments`, hash).Scan(&size, &found); err != nil {
			return "", err
		}
		if found == 0 && size+int64(len(content)) > 256<<20 {
			return "", ErrWebMapEditLimit
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO web_map_attachments VALUES(?,?)`, hash, content); err != nil {
			return "", err
		}
		var attached ClassicMindMapSource
		return attachClassicMindMapSourceTx(tx, item, nodeID, ClassicMindMapSource{Kind: ClassicMindMapSourceExternalFile, Title: name, Locator: attachmentPrefix + hash}, &attached)
	})
}

func ReadWebMapAttachmentIn(ctx context.Context, dir, mapID, nodeID, sourceID string, revision int64) (string, []byte, error) {
	doc, err := readWebMapDocument(ctx, dir, mapID)
	if err != nil {
		return "", nil, err
	}
	if doc.Map.Revision != revision {
		return "", nil, ErrClassicMindMapRevisionConflict
	}
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		for _, s := range n.Sources {
			if s.ID != sourceID || s.Kind != ClassicMindMapSourceExternalFile || !strings.HasPrefix(s.Locator, attachmentPrefix) {
				continue
			}
			db, e := openQueryDB(ctx, dir)
			if e != nil {
				return "", nil, e
			}
			defer db.Close()
			var content []byte
			e = db.QueryRowContext(ctx, `SELECT content FROM web_map_attachments WHERE id=? AND length(content)<=?`, strings.TrimPrefix(s.Locator, attachmentPrefix), MaxMapAttachmentBytes).Scan(&content)
			if e != nil {
				return "", nil, ErrWebMapSourceConflict
			}
			if attachmentPrefix+strings.TrimPrefix(prefixedSHA256(content), "sha256:") != s.Locator {
				return "", nil, ErrWebMapSourceConflict
			}
			return s.Title, content, nil
		}
	}
	return "", nil, ErrWebMapSourceConflict
}

type WebGraphCandidate struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

func ListWebGraphCandidatesIn(ctx context.Context, dir string, offset int) ([]WebGraphCandidate, error) {
	if offset < 0 || offset > 1000000 {
		return nil, ErrWebMapSourceInvalid
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,substr(label,1,512),kind FROM knowledge_nodes ORDER BY label,id LIMIT 30 OFFSET ?`, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WebGraphCandidate{}
	for rows.Next() {
		var x WebGraphCandidate
		if err = rows.Scan(&x.ID, &x.Label, &x.Kind); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}

func AttachWebMapGraphNodeIn(ctx context.Context, dir, actor, mapID, nodeID string, in WebMapDelete, graphID string) (WebMapNodeEdited, error) {
	if !webMapExactID(nodeID) || validateKnowledgeID(graphID) != nil {
		return WebMapNodeEdited{}, ErrWebMapSourceInvalid
	}
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, struct{ Node, Graph string }{nodeID, graphID}, "attach_source:knowledge_node", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, graphID).Scan(&count); err != nil {
			return "", err
		}
		if count != 1 {
			return "", ErrWebMapSourceConflict
		}
		var attached ClassicMindMapSource
		id, err := attachClassicMindMapSourceTx(tx, item, nodeID, ClassicMindMapSource{Kind: ClassicMindMapSourceKnowledgeNode, KnowledgeNodeID: graphID}, &attached)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrWebMapSourceConflict, err)
		}
		return id, nil
	})
}
