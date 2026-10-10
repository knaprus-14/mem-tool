package mem

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadDocumentAnnotationEntryIn opens a bounded, current chunk and returns a
// content pin for selection. Optional pin binds an existing search result.
func ReadDocumentAnnotationEntryIn(ctx context.Context, dir string, id int64, pin string) (QueryHit, error) {
	if id < 1 || (pin != "" && !validSHA256Digest(pin)) {
		return QueryHit{}, ErrQueryInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return QueryHit{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueryHit{}, err
	}
	defer tx.Rollback()
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT `+annotationReaderEntrySizeSQL()+` FROM entries WHERE id=?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return QueryHit{}, ErrQueryStale
	}
	if err != nil {
		return QueryHit{}, err
	}
	if size > 2<<20 {
		return QueryHit{}, ErrQueryLimit
	}
	entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, id)
	if err != nil {
		return QueryHit{}, err
	}
	if len(entries) != 1 {
		return QueryHit{}, ErrQueryStale
	}
	hit, err := queryHit(entries[0], 100000, DefaultAnswerLowConfidence)
	if err != nil {
		return hit, err
	}
	if pin != "" && hit.Pin != pin {
		return QueryHit{}, ErrQueryStale
	}
	hit.DocumentRevision = entries[0].DocumentRevision
	// Legacy/unversioned text stays readable but cannot create a trustworthy target.
	excerpt := strings.TrimSpace(entries[0].Text)
	for _, r := range excerpt {
		excerpt = string(r)
		break
	}
	if _, err = EvidenceAnchorForEntry(entries[0], excerpt); err != nil {
		hit.Pin = ""
	}
	return hit, tx.Commit()
}

func annotationReaderEntrySizeSQL() string {
	columns := strings.Split(storedEntryColumns, ",")
	for i, column := range columns {
		columns[i] = "COALESCE(length(CAST(" + strings.TrimSpace(column) + " AS BLOB)),0)"
	}
	return strings.Join(columns, "+")
}

func documentAnnotationResolvedAnchorTx(ctx context.Context, tx *sql.Tx, id string, revision int64) (documentAnnotationRecord, EvidenceState, error) {
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return record, "", err
	}
	if revision > 0 && record.Annotation.Revision != revision {
		return record, "", ErrDocumentAnnotationConflict
	}
	return record, record.Annotation.State, nil
}

func ReadDocumentAnnotationSourceIn(ctx context.Context, dir, id string) (WebMapDocument, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return WebMapDocument{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WebMapDocument{}, err
	}
	defer tx.Rollback()
	record, state, err := documentAnnotationResolvedAnchorTx(ctx, tx, id, 0)
	if err != nil {
		return WebMapDocument{}, err
	}
	out, err := readWebEvidenceDocumentTx(ctx, tx, record.Anchor, state)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func OpenDocumentAnnotationPDFIn(ctx context.Context, dir, id string, revision int64, local bool) (*os.File, func(), error) {
	if revision <= 0 {
		return nil, nil, ErrDocumentAnnotationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	record, state, err := documentAnnotationResolvedAnchorTx(ctx, tx, id, revision)
	if err != nil {
		return nil, nil, err
	}
	if state != EvidenceCurrent || !strings.EqualFold(filepath.Ext(record.Anchor.SourcePath), ".pdf") {
		return nil, nil, ErrDocumentAnnotationStale
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return openWebEvidencePDF(ctx, db, dir, record.Anchor, local)
}

// Links do not mutate the immutable annotation. They use ordinary map sources,
// so map undo/redo/snapshot semantics and document freshness stay authoritative.
const documentAnnotationLinksSchema = `CREATE TABLE IF NOT EXISTS document_annotation_map_links (
 annotation_id TEXT NOT NULL, map_id TEXT NOT NULL, node_id TEXT NOT NULL,
 source_id TEXT NOT NULL, annotation_revision INTEGER NOT NULL,
 actor TEXT NOT NULL, created TEXT NOT NULL,
 PRIMARY KEY(annotation_id,source_id)
);
CREATE INDEX IF NOT EXISTS document_annotation_map_links_node ON document_annotation_map_links(map_id,node_id);`

type WebMapAnnotationAttach struct {
	Revision           int64  `json:"revision"`
	AnnotationID       string `json:"annotation_id"`
	AnnotationRevision int64  `json:"annotation_revision"`
	RequestID          string `json:"request_id"`
}

var ErrDocumentAnnotationAlreadyAttached = errors.New("Эта цитата уже прикреплена к узлу. Выберите другой узел или более точный фрагмент текста.")

func AttachDocumentAnnotationToWebMapIn(ctx context.Context, dir, actor, mapID, nodeID string, in WebMapAnnotationAttach) (WebMapNodeEdited, error) {
	if !webMapExactID(nodeID) || in.AnnotationRevision <= 0 {
		return WebMapNodeEdited{}, ErrDocumentAnnotationInvalid
	}
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, struct {
		Node  string
		Input WebMapAnnotationAttach
	}{nodeID, in}, "attach_annotation", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := exactWebMapNode(tx, mapID, nodeID)
		if err != nil {
			return "", err
		}
		if node.Locked {
			return "", ErrClassicMindMapLocked
		}
		record, state, err := documentAnnotationResolvedAnchorTx(ctx, tx, in.AnnotationID, in.AnnotationRevision)
		if err != nil {
			return "", err
		}
		if record.Annotation.Archived || state != EvidenceCurrent {
			return "", ErrDocumentAnnotationStale
		}
		if _, err = tx.ExecContext(ctx, documentAnnotationLinksSchema); err != nil {
			return "", err
		}
		var all, one int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN annotation_id=? THEN 1 ELSE 0 END),0) FROM document_annotation_map_links`, in.AnnotationID).Scan(&all, &one); err != nil {
			return "", err
		}
		if all >= 20000 || one >= 1000 {
			return "", ErrDocumentAnnotationLimit
		}
		a := record.Anchor
		var sourceID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' AND kind='evidence' AND citation_id=? AND evidence_hash=? ORDER BY position LIMIT 1`, mapID, nodeID, a.CitationID, a.EvidenceHash).Scan(&sourceID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if sourceID == "" {
			if err = boundWebMapEvidence(tx, a); err != nil {
				return "", err
			}
			var source ClassicMindMapSource
			if _, err = attachClassicMindMapEvidenceTx(tx, item, nodeID, a, &source); err != nil {
				return "", err
			}
			sourceID = source.ID
		} else {
			// Every link must introduce a source into the map snapshot. Reusing an
			// existing source would leave an invisible mutation that undo cannot revert.
			return "", ErrDocumentAnnotationAlreadyAttached
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_map_links VALUES(?,?,?,?,?,?,?)`, in.AnnotationID, mapID, nodeID, sourceID, in.AnnotationRevision, actor, time.Now().UTC().Format(time.RFC3339Nano))
		return nodeID, err
	})
}

type DocumentAnnotationMapLink struct {
	MapID       string `json:"map_id"`
	MapTitle    string `json:"map_title"`
	MapRevision int64  `json:"map_revision"`
	NodeID      string `json:"node_id"`
	NodeLabel   string `json:"node_label"`
	SourceID    string `json:"source_id"`
}
type DocumentAnnotationLinkPage struct {
	Items []DocumentAnnotationMapLink `json:"items"`
	Total int                         `json:"total"`
	Limit int                         `json:"limit"`
}

func ListDocumentAnnotationMapLinksIn(ctx context.Context, dir, id string) (DocumentAnnotationLinkPage, error) {
	out := DocumentAnnotationLinkPage{Items: []DocumentAnnotationMapLink{}, Limit: 100}
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
	if _, err = readDocumentAnnotationTx(ctx, tx, id); err != nil {
		return out, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_annotation_map_links'`).Scan(&exists); err != nil {
		return out, err
	}
	if exists == 0 {
		return out, nil
	}
	const from = ` FROM document_annotation_map_links l JOIN mind_maps m ON m.id=l.map_id AND m.deleted_at='' JOIN mind_map_nodes n ON n.id=l.node_id AND n.map_id=l.map_id AND n.deleted_at='' JOIN mind_map_node_sources s ON s.id=l.source_id AND s.node_id=l.node_id AND s.map_id=l.map_id AND s.deleted_at='' WHERE l.annotation_id=?`
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(m.title AS BLOB))+length(CAST(n.label AS BLOB))),0)`+from, id).Scan(&out.Total, &size); err != nil {
		return out, err
	}
	if size > 4<<20 {
		return out, ErrDocumentAnnotationLimit
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.title,m.revision,n.id,n.label,s.id`+from+` ORDER BY m.updated DESC,n.position,s.position LIMIT 100`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item DocumentAnnotationMapLink
		if err = rows.Scan(&item.MapID, &item.MapTitle, &item.MapRevision, &item.NodeID, &item.NodeLabel, &item.SourceID); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
