package mem

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

var (
	ErrDocumentAnnotationReanchorInvalid = errors.New("выберите точный фрагмент и актуальную версию исходной аннотации для новой привязки")
	ErrDocumentAnnotationReanchorPreview = errors.New("аннотация, фрагмент или связанные объекты изменились; повторите сравнение привязок")
)

type DocumentAnnotationReanchorPreviewInput struct {
	Revision int64  `json:"revision"`
	EntryID  int64  `json:"entry_id"`
	Pin      string `json:"pin"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Quote    string `json:"quote"`
}
type DocumentAnnotationReanchorInput struct {
	Revision  int64  `json:"revision"`
	EntryID   int64  `json:"entry_id"`
	Pin       string `json:"pin"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Quote     string `json:"quote"`
	Digest    string `json:"digest"`
	RequestID string `json:"request_id"`
}
type DocumentAnnotationReanchorTarget struct {
	EntryID          int64  `json:"entry_id"`
	Pin              string `json:"pin"`
	DocumentID       string `json:"document_id"`
	DocumentRevision string `json:"document_revision"`
	CitationID       string `json:"citation_id"`
	SourceName       string `json:"source_name"`
	Page             int    `json:"page"`
	BlockIndex       int    `json:"block_index"`
	BlockChunkIndex  int    `json:"block_chunk_index"`
	Start            int    `json:"start"`
	End              int    `json:"end"`
	Quote            string `json:"quote"`
	Prefix           string `json:"prefix"`
	Suffix           string `json:"suffix"`
}
type DocumentAnnotationReanchorPreview struct {
	Digest             string                           `json:"digest"`
	Old                DocumentAnnotation               `json:"old"`
	Target             DocumentAnnotationReanchorTarget `json:"target"`
	Warnings           []string                         `json:"warnings"`
	RegionCount        int                              `json:"region_count"`
	MapLinkCount       int                              `json:"map_link_count"`
	KnowledgeLinkCount int                              `json:"knowledge_link_count"`
}
type DocumentAnnotationLineage struct {
	ID          string `json:"id"`
	OldID       string `json:"old_id"`
	OldRevision int64  `json:"old_revision"`
	NewID       string `json:"new_id"`
	NewRevision int64  `json:"new_revision"`
	Actor       string `json:"actor"`
	Created     string `json:"created"`
}
type DocumentAnnotationLineagePage struct {
	Items  []DocumentAnnotationLineage `json:"items"`
	Total  int                         `json:"total"`
	Offset int                         `json:"offset"`
	Limit  int                         `json:"limit"`
}
type DocumentAnnotationReanchorResult struct {
	Annotation DocumentAnnotation        `json:"annotation"`
	Replayed   bool                      `json:"replayed"`
	Lineage    DocumentAnnotationLineage `json:"lineage"`
}

const documentAnnotationLineageSchema = `CREATE TABLE IF NOT EXISTS document_annotation_lineage(
 id TEXT PRIMARY KEY,old_id TEXT NOT NULL,new_id TEXT NOT NULL UNIQUE,payload TEXT NOT NULL,payload_hash TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS document_annotation_lineage_old ON document_annotation_lineage(old_id,id);
CREATE TRIGGER IF NOT EXISTS document_annotation_lineage_no_update BEFORE UPDATE ON document_annotation_lineage BEGIN SELECT RAISE(ABORT,'annotation lineage is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_lineage_no_delete BEFORE DELETE ON document_annotation_lineage BEGIN SELECT RAISE(ABORT,'annotation lineage is immutable'); END;`
const annotationLineageSizeSQL = `length(CAST(id AS BLOB))+length(CAST(old_id AS BLOB))+length(CAST(new_id AS BLOB))+length(CAST(payload AS BLOB))+length(CAST(payload_hash AS BLOB))`
const maxAnnotationLineageRowBytes = 4 << 10
const maxAnnotationLineageBytes = 16 << 20

func validAnnotationReanchorInput(id string, in DocumentAnnotationReanchorPreviewInput) bool {
	return validDocumentAnnotationID(id) && in.Revision > 0 && in.EntryID > 0 && validSHA256Digest(in.Pin) && in.Start >= 0 && in.End > in.Start && in.End <= maxDocumentAnnotationChunkBytes && validDocumentAnnotationText(in.Quote, MaxDocumentAnnotationRunes) && strings.TrimSpace(in.Quote) != "" && in.End-in.Start == utf8.RuneCountInString(in.Quote)
}

type annotationReanchorPlan struct {
	Old     documentAnnotationRecord
	Target  documentAnnotationRecord
	Preview DocumentAnnotationReanchorPreview
}

// This transaction-local selection deliberately uses the current entry pin,
// exact Unicode offsets and recomputed content hash. It neither searches for a
// similar quotation nor follows a caller-provided locator.
func annotationReanchorTargetTx(ctx context.Context, tx *sql.Tx, in DocumentAnnotationReanchorPreviewInput) (documentAnnotationRecord, error) {
	var record documentAnnotationRecord
	var textBytes, entryBytes int64
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB)),`+annotationReaderEntrySizeSQL()+` FROM entries WHERE id=?`, in.EntryID).Scan(&textBytes, &entryBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrDocumentAnnotationStale
	}
	if err != nil {
		return record, err
	}
	if textBytes > maxDocumentAnnotationChunkBytes || entryBytes > 64<<20 {
		return record, ErrDocumentAnnotationLimit
	}
	entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, in.EntryID)
	if err != nil {
		return record, err
	}
	if len(entries) != 1 {
		return record, ErrDocumentAnnotationStale
	}
	entry := entries[0]
	pin, err := documentRestoreEntryDigest(entry)
	if err != nil {
		return record, err
	}
	if pin != in.Pin {
		return record, ErrDocumentAnnotationStale
	}
	if !utf8.ValidString(entry.Text) || strings.ContainsRune(entry.Text, 0) {
		return record, ErrDocumentAnnotationReanchorInvalid
	}
	runes := []rune(entry.Text)
	if in.End > len(runes) || string(runes[in.Start:in.End]) != in.Quote {
		return record, ErrDocumentAnnotationReanchorInvalid
	}
	anchor, err := EvidenceAnchorForEntry(entry, in.Quote)
	if err != nil {
		return record, ErrDocumentAnnotationStale
	}
	record = documentAnnotationRecord{Anchor: anchor, Annotation: DocumentAnnotation{DocumentID: LibraryDocumentID(anchor.SourcePath, anchor.DocumentID), DocumentRevision: anchor.DocumentRevision, CitationID: anchor.CitationID, SourceName: sourceName(entry), Page: anchor.Page, BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex, Quote: in.Quote, Start: in.Start, End: in.End, Prefix: string(runes[max(0, in.Start-documentAnnotationContextRunes):in.Start]), Suffix: string(runes[in.End:min(len(runes), in.End+documentAnnotationContextRunes)])}}
	record.Annotation.State, err = resolveDocumentAnnotationStateTx(ctx, tx, record)
	if err != nil {
		return record, err
	}
	if record.Annotation.State != EvidenceCurrent {
		return record, ErrDocumentAnnotationStale
	}
	return record, nil
}

func annotationReanchorRelatedCountsTx(ctx context.Context, tx *sql.Tx, id string) (regions, links int, err error) {
	exists, err := annotationTableExists(ctx, tx, "document_annotation_regions")
	if err != nil {
		return 0, 0, err
	}
	if exists {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotation_regions WHERE annotation_id=?`, id).Scan(&regions); err != nil {
			return 0, 0, err
		}
	}
	exists, err = annotationTableExists(ctx, tx, "document_annotation_map_links")
	if err != nil {
		return 0, 0, err
	}
	if exists {
		// Same visible map/node/source conditions as the public links reader.
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotation_map_links l
JOIN mind_maps m ON m.id=l.map_id AND m.deleted_at=''
JOIN mind_map_nodes n ON n.id=l.node_id AND n.map_id=l.map_id AND n.deleted_at=''
JOIN mind_map_node_sources s ON s.id=l.source_id AND s.node_id=l.node_id AND s.map_id=l.map_id AND s.deleted_at=''
WHERE l.annotation_id=?`, id).Scan(&links)
	}
	return regions, links, err
}

func previewAnnotationReanchorTx(ctx context.Context, tx *sql.Tx, id string, in DocumentAnnotationReanchorPreviewInput) (annotationReanchorPlan, error) {
	var plan annotationReanchorPlan
	old, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return plan, err
	}
	if old.Annotation.Revision != in.Revision {
		return plan, ErrDocumentAnnotationConflict
	}
	target, err := annotationReanchorTargetTx(ctx, tx, in)
	if err != nil {
		return plan, err
	}
	regions, links, err := annotationReanchorRelatedCountsTx(ctx, tx, id)
	if err != nil {
		return plan, err
	}
	knowledgeLinks := 0
	if exists, e := annotationTableExists(ctx, tx, "document_annotation_knowledge_links"); e != nil {
		return plan, e
	} else if exists {
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotation_knowledge_links WHERE annotation_id=?`, id).Scan(&knowledgeLinks); e != nil {
			return plan, e
		}
	}
	a := target.Annotation
	plan = annotationReanchorPlan{Old: old, Target: target, Preview: DocumentAnnotationReanchorPreview{Old: old.Annotation, Target: DocumentAnnotationReanchorTarget{EntryID: in.EntryID, Pin: in.Pin, DocumentID: a.DocumentID, DocumentRevision: a.DocumentRevision, CitationID: a.CitationID, SourceName: a.SourceName, Page: a.Page, BlockIndex: a.BlockIndex, BlockChunkIndex: a.BlockChunkIndex, Start: a.Start, End: a.End, Quote: a.Quote, Prefix: a.Prefix, Suffix: a.Suffix}, RegionCount: regions, MapLinkCount: links, Warnings: []string{
		"Будет создана отдельная активная аннотация с новой привязкой. Исходная аннотация и её история сохраняются.",
		"Комментарий и цвет копируются. Автор новой аннотации — пользователь, подтвердивший привязку; происхождение сохранится отдельной записью.",
		"Области PDF и связи с картами остаются у исходной аннотации и автоматически не переносятся.",
	}}}
	plan.Preview.KnowledgeLinkCount = knowledgeLinks
	plan.Preview.Warnings = append(plan.Preview.Warnings, "Объекты графа знаний остаются связаны с прежней аннотацией. Новая привязка не заменяет их доказательства и текст.")
	if old.Annotation.State == EvidenceMissing {
		plan.Preview.Warnings = append(plan.Preview.Warnings, "Исходный источник отсутствует. Новый фрагмент выбран вручную; сходство с сохранённой цитатой не означает автоматического подтверждения.")
	}
	raw, err := json.Marshal(struct {
		Old, Target                    documentAnnotationRecord
		Selection                      DocumentAnnotationReanchorPreviewInput
		Regions, Links, KnowledgeLinks int
	}{old, target, in, regions, links, knowledgeLinks})
	plan.Preview.Digest = prefixedSHA256(raw)
	return plan, err
}

func PreviewDocumentAnnotationReanchorIn(ctx context.Context, dir, id string, in DocumentAnnotationReanchorPreviewInput) (DocumentAnnotationReanchorPreview, error) {
	if !validAnnotationReanchorInput(id, in) {
		return DocumentAnnotationReanchorPreview{}, ErrDocumentAnnotationReanchorInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return DocumentAnnotationReanchorPreview{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentAnnotationReanchorPreview{}, err
	}
	defer tx.Rollback()
	plan, err := previewAnnotationReanchorTx(ctx, tx, id, in)
	return plan.Preview, err
}

func readAnnotationLineageTx(ctx context.Context, tx *sql.Tx, newID string) (DocumentAnnotationLineage, error) {
	var out DocumentAnnotationLineage
	exists, err := annotationTableExists(ctx, tx, "document_annotation_lineage")
	if err != nil {
		return out, err
	}
	if !exists {
		return out, ErrDocumentAnnotationNotFound
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT `+annotationLineageSizeSQL+` FROM document_annotation_lineage WHERE new_id=?`, newID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrDocumentAnnotationNotFound
	}
	if err != nil {
		return out, err
	}
	if size > maxAnnotationLineageRowBytes {
		return out, ErrDocumentAnnotationLimit
	}
	var id, oldID, raw, hash string
	if err = tx.QueryRowContext(ctx, `SELECT id,old_id,payload,payload_hash FROM document_annotation_lineage WHERE new_id=?`, newID).Scan(&id, &oldID, &raw, &hash); err != nil {
		return out, err
	}
	if prefixedSHA256([]byte(raw)) != hash || json.Unmarshal([]byte(raw), &out) != nil || out.ID != id || out.OldID != oldID || out.NewID != newID || len(out.ID) != 38 || !strings.HasPrefix(out.ID, "alink-") || strings.Trim(out.ID[6:], "0123456789abcdef") != "" || !validDocumentAnnotationID(out.OldID) || !validDocumentAnnotationID(out.NewID) || out.OldID == out.NewID || out.OldRevision < 1 || out.NewRevision != 1 || !validDocumentAnnotationActor(out.Actor) {
		return out, ErrDocumentAnnotationReanchorInvalid
	}
	if _, err = time.Parse(time.RFC3339Nano, out.Created); err != nil {
		return out, ErrDocumentAnnotationReanchorInvalid
	}
	return out, nil
}

func ReanchorDocumentAnnotationIn(ctx context.Context, dir, actor, id string, in DocumentAnnotationReanchorInput) (DocumentAnnotationReanchorResult, error) {
	var result DocumentAnnotationReanchorResult
	selection := DocumentAnnotationReanchorPreviewInput{Revision: in.Revision, EntryID: in.EntryID, Pin: in.Pin, Start: in.Start, End: in.End, Quote: in.Quote}
	if !validAnnotationReanchorInput(id, selection) || !validDocumentAnnotationActor(actor) || !validDocumentAnnotationRequest(in.RequestID) || !isSHA256ContentHash(in.Digest) {
		return result, ErrDocumentAnnotationReanchorInvalid
	}
	digest, err := documentAnnotationDigest(actor, "reanchor", id, in)
	if err != nil {
		return result, err
	}
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
	exists, err := documentAnnotationsExistTx(ctx, tx)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, ErrDocumentAnnotationNotFound
	}
	// Exact late retries survive both subsequent annotation edits and vanished
	// target entries. No new history, links or quota consumption on replay.
	if prior, found, e := readDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest); e != nil || found {
		if e != nil {
			return result, e
		}
		lineage, e := readAnnotationLineageTx(ctx, tx, prior.Annotation.ID)
		if e != nil {
			return result, e
		}
		if lineage.OldID != id || lineage.OldRevision != in.Revision || lineage.NewRevision != prior.Annotation.Revision {
			return result, ErrDocumentAnnotationReanchorInvalid
		}
		return DocumentAnnotationReanchorResult{Annotation: prior.Annotation, Replayed: true, Lineage: lineage}, nil
	}
	plan, err := previewAnnotationReanchorTx(ctx, tx, id, selection)
	if err != nil {
		return result, err
	}
	if plan.Preview.Digest != in.Digest {
		return result, ErrDocumentAnnotationReanchorPreview
	}
	newID, err := newClassicMindMapID("ann-")
	if err != nil {
		return result, err
	}
	lineageID, err := newClassicMindMapID("alink-")
	if err != nil {
		return result, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := plan.Target
	a := &record.Annotation
	a.ID, a.Revision, a.Note, a.Color, a.Author, a.Created, a.Updated = newID, 1, plan.Old.Annotation.Note, plan.Old.Annotation.Color, actor, now, now
	if err = checkDocumentAnnotationCapacityTx(ctx, tx, record, true); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, documentAnnotationLineageSchema); err != nil {
		return result, err
	}
	lineage := DocumentAnnotationLineage{ID: lineageID, OldID: id, OldRevision: in.Revision, NewID: newID, NewRevision: 1, Actor: actor, Created: now}
	raw, err := json.Marshal(lineage)
	if err != nil {
		return result, err
	}
	var count int
	var totalBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(`+annotationLineageSizeSQL+`),0) FROM document_annotation_lineage`).Scan(&count, &totalBytes); err != nil {
		return result, err
	}
	rowBytes := int64(len(raw) + len(lineageID) + len(id) + len(newID) + 71)
	if count >= maxDocumentAnnotationRows || rowBytes > maxAnnotationLineageRowBytes || totalBytes+rowBytes > maxAnnotationLineageBytes {
		return result, ErrDocumentAnnotationLimit
	}
	anchorJSON, err := json.Marshal(record.Anchor)
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_annotations(id,revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,archived,author,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Revision, a.DocumentID, string(anchorJSON), a.Start, a.End, a.Prefix, a.Suffix, a.Note, a.Color, false, a.Author, a.Created, a.Updated)
	if err != nil {
		return result, err
	}
	if err = appendDocumentAnnotationHistoryTx(ctx, tx, record, "reanchor", actor); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_lineage(id,old_id,new_id,payload,payload_hash) VALUES(?,?,?,?,?)`, lineageID, id, newID, string(raw), prefixedSHA256(raw)); err != nil {
		return result, err
	}
	if err = saveDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest, *a); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return DocumentAnnotationReanchorResult{Annotation: *a, Lineage: lineage}, nil
}

func ListDocumentAnnotationLineageIn(ctx context.Context, dir, id string, offset, limit int) (DocumentAnnotationLineagePage, error) {
	out := DocumentAnnotationLineagePage{Items: []DocumentAnnotationLineage{}, Offset: offset}
	if !validDocumentAnnotationID(id) {
		return out, ErrDocumentAnnotationReanchorInvalid
	}
	var err error
	limit, err = documentAnnotationPagination(offset, limit)
	if err != nil {
		return out, err
	}
	out.Limit = limit
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
	exists, err := annotationTableExists(ctx, tx, "document_annotation_lineage")
	if err != nil || !exists {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotation_lineage WHERE old_id=? OR new_id=?`, id, id).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT substr(new_id,1,37),length(CAST(new_id AS BLOB)) FROM document_annotation_lineage WHERE old_id=? OR new_id=? ORDER BY rowid DESC LIMIT ? OFFSET ?`, id, id, limit, offset)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var key string
		var size int
		if err = rows.Scan(&key, &size); err != nil {
			rows.Close()
			return out, err
		}
		if size != 36 || !validDocumentAnnotationID(key) {
			rows.Close()
			return out, ErrDocumentAnnotationReanchorInvalid
		}
		ids = append(ids, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, key := range ids {
		item, err := readAnnotationLineageTx(ctx, tx, key)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
