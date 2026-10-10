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

var (
	ErrDocumentAnnotationKnowledgeInvalid     = errors.New("выберите тип note, question, card, hypothesis, decision или task; название до 512 и текст до 8000 символов")
	ErrDocumentAnnotationKnowledgeLimit       = errors.New("достигнут лимит связей аннотаций с графом: 10000 связей, 1000 на аннотацию и 64 МиБ; уменьшите объём или размер страницы")
	ErrDocumentAnnotationKnowledgeUnavailable = errors.New("узел графа не найден или таблицы графа недоступны")
)

const (
	maxAnnotationKnowledgeLinks         = 10000
	maxAnnotationKnowledgePerAnnotation = 1000
	maxAnnotationKnowledgeBytes         = 64 << 20
	maxAnnotationKnowledgeRowBytes      = 256 << 10
	maxAnnotationKnowledgePageBytes     = 4 << 20
)

type DocumentAnnotationKnowledgeCreate struct {
	Revision  int64             `json:"revision"`
	Kind      KnowledgeNodeKind `json:"kind"`
	Label     string            `json:"label"`
	Body      string            `json:"body"`
	RequestID string            `json:"request_id"`
}

// This DTO deliberately excludes graph evidence paths and internal locators.
type DocumentAnnotationKnowledgeNode struct {
	ID      string            `json:"id"`
	Kind    KnowledgeNodeKind `json:"kind"`
	Label   string            `json:"label"`
	Body    string            `json:"body"`
	Status  KnowledgeStatus   `json:"status"`
	Origin  KnowledgeOrigin   `json:"origin"`
	Created string            `json:"created"`
	Updated string            `json:"updated"`
}

type DocumentAnnotationKnowledgeLink struct {
	AnnotationID       string `json:"annotation_id"`
	AnnotationRevision int64  `json:"annotation_revision"`
	NodeID             string `json:"node_id"`
	Author             string `json:"author"`
	Created            string `json:"created"`
	ContentDigest      string `json:"content_digest"`
	ReviewDigest       string `json:"review_digest"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type DocumentAnnotationKnowledgeResult struct {
	Node     DocumentAnnotationKnowledgeNode `json:"node"`
	Link     DocumentAnnotationKnowledgeLink `json:"link"`
	Replayed bool                            `json:"replayed"`
}

type DocumentAnnotationKnowledgeItem struct {
	Creation       DocumentAnnotationKnowledgeResult `json:"creation"`
	Node           DocumentAnnotationKnowledgeNode   `json:"node"`
	Annotation     DocumentAnnotation                `json:"annotation"`
	Snapshot       DocumentAnnotation                `json:"snapshot"`
	MissingNode    bool                              `json:"missing_node"`
	NodeChanged    bool                              `json:"node_changed"`
	EvidenceLinked bool                              `json:"evidence_linked"`
}

type DocumentAnnotationKnowledgePage struct {
	Items  []DocumentAnnotationKnowledgeItem `json:"items"`
	Total  int                               `json:"total"`
	Offset int                               `json:"offset"`
	Limit  int                               `json:"limit"`
}

const annotationKnowledgeSchema = `CREATE TABLE IF NOT EXISTS document_annotation_knowledge_links(
 node_id TEXT PRIMARY KEY,annotation_id TEXT NOT NULL,annotation_revision INTEGER NOT NULL,
 author TEXT NOT NULL,created TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS document_annotation_knowledge_links_annotation ON document_annotation_knowledge_links(annotation_id,created,node_id);
CREATE TRIGGER IF NOT EXISTS document_annotation_knowledge_link_no_update BEFORE UPDATE ON document_annotation_knowledge_links BEGIN SELECT RAISE(ABORT,'knowledge annotation link is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_knowledge_link_no_delete BEFORE DELETE ON document_annotation_knowledge_links BEGIN SELECT RAISE(ABORT,'knowledge annotation link is immutable'); END;
CREATE TABLE IF NOT EXISTS document_annotation_knowledge_receipts(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,node_id TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS document_annotation_knowledge_receipt_no_update BEFORE UPDATE ON document_annotation_knowledge_receipts BEGIN SELECT RAISE(ABORT,'knowledge annotation receipt is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_knowledge_receipt_no_delete BEFORE DELETE ON document_annotation_knowledge_receipts BEGIN SELECT RAISE(ABORT,'knowledge annotation receipt is immutable'); END;`

const annotationKnowledgeLinkSizeSQL = `length(CAST(payload AS BLOB))+length(CAST(payload_hash AS BLOB))+length(CAST(node_id AS BLOB))+length(CAST(annotation_id AS BLOB))+length(CAST(author AS BLOB))+length(CAST(created AS BLOB))+8`

func CreateDocumentAnnotationKnowledgeNodeIn(ctx context.Context, dir, actor, id string, in DocumentAnnotationKnowledgeCreate) (DocumentAnnotationKnowledgeResult, error) {
	var out DocumentAnnotationKnowledgeResult
	if !validDocumentAnnotationID(id) || !validDocumentAnnotationActor(actor) || !validDocumentAnnotationRequest(in.RequestID) || in.Revision <= 0 ||
		!validDocumentAnnotationText(in.Label, MaxKnowledgeLabelRunes) || strings.TrimSpace(in.Label) == "" || !validDocumentAnnotationText(in.Body, MaxDocumentAnnotationRunes) {
		return out, ErrDocumentAnnotationKnowledgeInvalid
	}
	if _, err := knowledgeWorkspaceRelationForKind(in.Kind); err != nil {
		return out, ErrDocumentAnnotationKnowledgeInvalid
	}
	digest, err := documentAnnotationDigest(actor, "knowledge_create", id, in)
	if err != nil {
		return out, err
	}
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
	if _, err = tx.ExecContext(ctx, annotationKnowledgeSchema); err != nil {
		return out, err
	}
	var priorHash, priorNode string
	var priorSize int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(request_hash AS BLOB))+length(CAST(node_id AS BLOB)) FROM document_annotation_knowledge_receipts WHERE request_id=?`, in.RequestID).Scan(&priorSize)
	if err == nil {
		if priorSize > 512 {
			return out, ErrDocumentAnnotationKnowledgeLimit
		}
		if err = tx.QueryRowContext(ctx, `SELECT request_hash,node_id FROM document_annotation_knowledge_receipts WHERE request_id=?`, in.RequestID).Scan(&priorHash, &priorNode); err != nil {
			return out, err
		}
		if priorHash != digest {
			return out, ErrDocumentAnnotationRequest
		}
		out, err = readAnnotationKnowledgeCreationTx(ctx, tx, priorNode)
		out.Replayed = true
		return out, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if record.Annotation.Revision != in.Revision {
		return out, ErrDocumentAnnotationConflict
	}
	if record.Annotation.Archived || record.Annotation.State != EvidenceCurrent {
		return out, ErrDocumentAnnotationStale
	}
	for _, table := range []string{"knowledge_nodes", "knowledge_node_evidence"} {
		exists, e := annotationTableExists(ctx, tx, table)
		if e != nil {
			return out, e
		}
		if !exists {
			return out, ErrDocumentAnnotationKnowledgeUnavailable
		}
	}
	nodeID, _, err := newKnowledgeWorkspaceIDs()
	if err != nil {
		return out, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	node := KnowledgeNode{ID: nodeID, Kind: in.Kind, Label: in.Label, Body: in.Body, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Created: now, Updated: now, Evidence: []EvidenceAnchor{record.Anchor}}
	if err = validateKnowledgeNode(node); err != nil {
		return out, ErrDocumentAnnotationKnowledgeInvalid
	}
	contentDigest, err := KnowledgeContentDigest(KnowledgeObjectNode, node.Label, node.Body)
	if err != nil {
		return out, err
	}
	evidenceDigest, err := KnowledgeEvidenceDigest(node.Evidence)
	if err != nil {
		return out, err
	}
	out.Node = annotationKnowledgeNodeDTO(node)
	out.Link = DocumentAnnotationKnowledgeLink{AnnotationID: id, AnnotationRevision: in.Revision, NodeID: nodeID, Author: actor, Created: now, ContentDigest: contentDigest, EvidenceDigest: evidenceDigest, ReviewDigest: knowledgeReviewDigest(knowledgeReviewContent{Type: KnowledgeObjectNode, ID: nodeID, Kind: string(node.Kind), Label: node.Label, Body: node.Body, Origin: node.Origin})}
	payload, err := json.Marshal(out)
	if err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if len(payload) > maxAnnotationKnowledgeRowBytes {
		return DocumentAnnotationKnowledgeResult{}, ErrDocumentAnnotationKnowledgeLimit
	}
	var count, one, bytes, receipts, receiptBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN annotation_id=? THEN 1 ELSE 0 END),0),COALESCE(SUM(length(CAST(payload AS BLOB))+length(CAST(payload_hash AS BLOB))+length(CAST(node_id AS BLOB))+length(CAST(annotation_id AS BLOB))+length(CAST(author AS BLOB))+length(CAST(created AS BLOB))+8),0) FROM document_annotation_knowledge_links`, id).Scan(&count, &one, &bytes); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(request_id AS BLOB))+length(CAST(request_hash AS BLOB))+length(CAST(node_id AS BLOB))),0) FROM document_annotation_knowledge_receipts`).Scan(&receipts, &receiptBytes); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if count >= maxAnnotationKnowledgeLinks || one >= maxAnnotationKnowledgePerAnnotation || bytes+int64(len(payload)+len(actor)+len(id)+len(nodeID)+len(now)+79) > maxAnnotationKnowledgeBytes || receipts >= maxAnnotationKnowledgeLinks || receiptBytes+int64(len(in.RequestID)+len(digest)+len(nodeID)) > 4<<20 {
		return DocumentAnnotationKnowledgeResult{}, ErrDocumentAnnotationKnowledgeLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge_nodes(id,kind,label,body,status,origin,confidence,created,updated) VALUES(?,?,?,?,?,?,?,?,?)`, node.ID, node.Kind, node.Label, node.Body, node.Status, node.Origin, 0, node.Created, node.Updated); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if err = insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", node.ID, 0, record.Anchor); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_knowledge_links VALUES(?,?,?,?,?,?,?)`, nodeID, id, in.Revision, actor, now, string(payload), prefixedSHA256(payload)); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_knowledge_receipts VALUES(?,?,?)`, in.RequestID, digest, nodeID); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return DocumentAnnotationKnowledgeResult{}, err
	}
	return out, nil
}

func annotationKnowledgeNodeDTO(node KnowledgeNode) DocumentAnnotationKnowledgeNode {
	return DocumentAnnotationKnowledgeNode{ID: node.ID, Kind: node.Kind, Label: node.Label, Body: node.Body, Status: node.Status, Origin: node.Origin, Created: node.Created, Updated: node.Updated}
}

func readAnnotationKnowledgeCreationTx(ctx context.Context, tx *sql.Tx, nodeID string) (DocumentAnnotationKnowledgeResult, error) {
	var out DocumentAnnotationKnowledgeResult
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT `+annotationKnowledgeLinkSizeSQL+` FROM document_annotation_knowledge_links WHERE node_id=?`, nodeID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrDocumentAnnotationNotFound
	}
	if err != nil {
		return out, err
	}
	if size > maxAnnotationKnowledgeRowBytes {
		return out, ErrDocumentAnnotationKnowledgeLimit
	}
	var payload, digest, id string
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash,annotation_id,annotation_revision FROM document_annotation_knowledge_links WHERE node_id=?`, nodeID).Scan(&payload, &digest, &id, &revision); err != nil {
		return out, err
	}
	if prefixedSHA256([]byte(payload)) != digest || json.Unmarshal([]byte(payload), &out) != nil || out.Node.ID != nodeID || out.Link.NodeID != nodeID || out.Link.AnnotationID != id || out.Link.AnnotationRevision != revision {
		return DocumentAnnotationKnowledgeResult{}, ErrDocumentAnnotationKnowledgeInvalid
	}
	return out, nil
}

func ListDocumentAnnotationKnowledgeLinksIn(ctx context.Context, dir, id string, offset, limit int) (DocumentAnnotationKnowledgePage, error) {
	return listAnnotationKnowledgeLinksIn(ctx, dir, id, "", offset, limit)
}

func ListKnowledgeNodeDocumentAnnotationsIn(ctx context.Context, dir, nodeID string, offset, limit int) (DocumentAnnotationKnowledgePage, error) {
	return listAnnotationKnowledgeLinksIn(ctx, dir, "", nodeID, offset, limit)
}

func listAnnotationKnowledgeLinksIn(ctx context.Context, dir, annotationID, nodeID string, offset, limit int) (DocumentAnnotationKnowledgePage, error) {
	out := DocumentAnnotationKnowledgePage{Items: []DocumentAnnotationKnowledgeItem{}, Offset: offset}
	var err error
	out.Limit, err = documentAnnotationPagination(offset, limit)
	if err != nil {
		return out, err
	}
	if annotationID != "" {
		if !validDocumentAnnotationID(annotationID) {
			return out, ErrDocumentAnnotationInvalid
		}
	} else if validateKnowledgeID(nodeID) != nil {
		return out, ErrDocumentAnnotationKnowledgeInvalid
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
	if annotationID != "" {
		if _, err = readDocumentAnnotationTx(ctx, tx, annotationID); err != nil {
			return out, err
		}
	}
	exists, err := annotationTableExists(ctx, tx, "document_annotation_knowledge_links")
	if err != nil {
		return out, err
	}
	if !exists {
		if nodeID != "" {
			if err = annotationKnowledgeNodeExistsTx(ctx, tx, nodeID); err != nil {
				return out, err
			}
		}
		return out, tx.Commit()
	}
	field, value := "annotation_id", annotationID
	if nodeID != "" {
		field, value = "node_id", nodeID
	}
	where := ` FROM document_annotation_knowledge_links WHERE ` + field + `=?`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+where, value).Scan(&out.Total); err != nil {
		return out, err
	}
	if out.Total == 0 && nodeID != "" {
		if err = annotationKnowledgeNodeExistsTx(ctx, tx, nodeID); err != nil {
			return out, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id,`+annotationKnowledgeLinkSizeSQL+where+` ORDER BY created DESC,node_id LIMIT ? OFFSET ?`, value, out.Limit, offset)
	if err != nil {
		return out, err
	}
	ids := []string{}
	var allocated int64
	for rows.Next() {
		var id string
		var size int64
		if err = rows.Scan(&id, &size); err != nil {
			rows.Close()
			return out, err
		}
		allocated += size
		if size > maxAnnotationKnowledgeRowBytes || allocated > maxAnnotationKnowledgePageBytes {
			rows.Close()
			return out, ErrDocumentAnnotationKnowledgeLimit
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		item, e := readAnnotationKnowledgeItemTx(ctx, tx, id, &allocated)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, item)
	}
	return out, tx.Commit()
}

func annotationKnowledgeNodeExistsTx(ctx context.Context, tx *sql.Tx, id string) error {
	exists, err := annotationTableExists(ctx, tx, "knowledge_nodes")
	if err != nil {
		return err
	}
	if !exists {
		return ErrDocumentAnnotationKnowledgeUnavailable
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrDocumentAnnotationKnowledgeUnavailable
	}
	return nil
}

func readAnnotationKnowledgeItemTx(ctx context.Context, tx *sql.Tx, nodeID string, allocated *int64) (DocumentAnnotationKnowledgeItem, error) {
	var out DocumentAnnotationKnowledgeItem
	var err error
	out.Creation, err = readAnnotationKnowledgeCreationTx(ctx, tx, nodeID)
	if err != nil {
		return out, err
	}
	link := out.Creation.Link
	// Preflight both annotation payloads before reading them into the page.
	var currentSize, historySize int64
	if err = tx.QueryRowContext(ctx, `SELECT `+documentAnnotationSizeSQL+` FROM document_annotations WHERE id=?`, link.AnnotationID).Scan(&currentSize); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT `+documentAnnotationHistorySizeSQL+` FROM document_annotation_history WHERE annotation_id=? AND revision=?`, link.AnnotationID, link.AnnotationRevision).Scan(&historySize); err != nil {
		return out, err
	}
	*allocated += currentSize + historySize
	if *allocated > maxAnnotationKnowledgePageBytes {
		return out, ErrDocumentAnnotationKnowledgeLimit
	}
	current, err := readDocumentAnnotationTx(ctx, tx, link.AnnotationID)
	if err != nil {
		return out, err
	}
	snapshot, err := readDocumentAnnotationHistoryRecordTx(ctx, tx, link.AnnotationID, link.AnnotationRevision)
	if err != nil {
		return out, err
	}
	out.Annotation, out.Snapshot = current.Annotation, snapshot.Annotation
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(label AS BLOB))+length(CAST(body AS BLOB))+length(CAST(kind AS BLOB))+length(CAST(status AS BLOB))+length(CAST(origin AS BLOB))+length(CAST(created AS BLOB))+length(CAST(updated AS BLOB)) FROM knowledge_nodes WHERE id=?`, nodeID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		out.MissingNode = true
		out.NodeChanged = true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	*allocated += size
	if size > 1<<20 || *allocated > maxAnnotationKnowledgePageBytes {
		return out, ErrDocumentAnnotationKnowledgeLimit
	}
	n := &out.Node
	n.ID = nodeID
	if err = tx.QueryRowContext(ctx, `SELECT kind,label,body,status,origin,created,updated FROM knowledge_nodes WHERE id=?`, nodeID).Scan(&n.Kind, &n.Label, &n.Body, &n.Status, &n.Origin, &n.Created, &n.Updated); err != nil {
		return out, err
	}
	a := snapshot.Anchor
	var count, matches int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN citation_id=? AND evidence_hash=? AND document_id=? AND document_revision=? AND chunk_hash=? AND source_path=? AND excerpt=? AND page=? AND block_index=? AND block_chunk_index=? THEN 1 ELSE 0 END),0) FROM knowledge_node_evidence WHERE node_id=?`, a.CitationID, a.EvidenceHash, a.DocumentID, a.DocumentRevision, a.ChunkHash, a.SourcePath, a.Excerpt, a.Page, a.BlockIndex, a.BlockChunkIndex, nodeID).Scan(&count, &matches); err != nil {
		return out, err
	}
	out.EvidenceLinked = matches > 0
	review := knowledgeReviewDigest(knowledgeReviewContent{Type: KnowledgeObjectNode, ID: nodeID, Kind: string(n.Kind), Label: n.Label, Body: n.Body, Origin: n.Origin})
	out.NodeChanged = review != link.ReviewDigest || n.Status != out.Creation.Node.Status || count != 1 || !out.EvidenceLinked
	return out, nil
}
