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
	ErrDocumentAnnotationInvalid  = errors.New("проверьте аннотацию: точная цитата и заметка до 8000 символов, корректные координаты и цвет")
	ErrDocumentAnnotationNotFound = errors.New("аннотация не найдена")
	ErrDocumentAnnotationConflict = errors.New("аннотация изменилась; обновите её перед сохранением")
	ErrDocumentAnnotationRequest  = errors.New("эта попытка уже сохранила другое содержимое; обновите аннотацию")
	ErrDocumentAnnotationStale    = errors.New("фрагмент документа изменился; откройте актуальный текст и выделите цитату заново")
	ErrDocumentAnnotationLimit    = errors.New("достигнут лимит аннотаций: 10000 записей и 64 МиБ, история 64 МиБ, журнал повторов 100000 записей и 16 МиБ; изменения не сохранены")
)

const (
	MaxDocumentAnnotationRunes           = 8000
	maxDocumentAnnotationRows            = 10000
	maxDocumentAnnotationBytes           = 64 << 20
	maxDocumentAnnotationHistoryBytes    = 64 << 20
	maxDocumentAnnotationReceipts        = 100000
	maxDocumentAnnotationReceiptBytes    = 16 << 20
	maxDocumentAnnotationRowBytes        = 128 << 10
	maxDocumentAnnotationHistoryRowBytes = 256 << 10
	maxDocumentAnnotationChunkBytes      = 2 << 20
	documentAnnotationContextRunes       = 48
)

type DocumentAnnotation struct {
	ID               string        `json:"id"`
	Revision         int64         `json:"revision"`
	DocumentID       string        `json:"document_id"`
	DocumentRevision string        `json:"document_revision"`
	CitationID       string        `json:"citation_id"`
	SourceName       string        `json:"source_name"`
	Page             int           `json:"page"`
	BlockIndex       int           `json:"block_index"`
	BlockChunkIndex  int           `json:"block_chunk_index"`
	Quote            string        `json:"quote"`
	Start            int           `json:"start"`
	End              int           `json:"end"`
	Prefix           string        `json:"prefix"`
	Suffix           string        `json:"suffix"`
	Note             string        `json:"note"`
	Color            string        `json:"color"`
	Archived         bool          `json:"archived"`
	Author           string        `json:"author"`
	Created          string        `json:"created"`
	Updated          string        `json:"updated"`
	State            EvidenceState `json:"state"`
}

type DocumentAnnotationCreate struct {
	EntryID   int64  `json:"entry_id"`
	Pin       string `json:"pin"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Quote     string `json:"quote"`
	Note      string `json:"note"`
	Color     string `json:"color"`
	RequestID string `json:"request_id"`
}

// Archived != nil changes only archive state. Otherwise Note is replaced in
// full (including empty text), and Color is required. Source pins never change.
type DocumentAnnotationEdit struct {
	Revision  int64  `json:"revision"`
	Note      string `json:"note"`
	Color     string `json:"color"`
	Archived  *bool  `json:"archived,omitempty"`
	RequestID string `json:"request_id"`
}

type DocumentAnnotationResult struct {
	Annotation DocumentAnnotation `json:"annotation"`
	Replayed   bool               `json:"replayed"`
}

type DocumentAnnotationQuery struct {
	DocumentID string `json:"document_id"`
	Archived   bool   `json:"archived"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

type DocumentAnnotationPage struct {
	Items  []DocumentAnnotation `json:"items"`
	Total  int                  `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

type DocumentAnnotationHistoryItem struct {
	ID         int64              `json:"id"`
	Revision   int64              `json:"revision"`
	Action     string             `json:"action"`
	Actor      string             `json:"actor"`
	Created    string             `json:"created"`
	Annotation DocumentAnnotation `json:"annotation"`
}

type DocumentAnnotationHistory struct {
	Items  []DocumentAnnotationHistoryItem `json:"items"`
	Total  int                             `json:"total"`
	Offset int                             `json:"offset"`
	Limit  int                             `json:"limit"`
}

// Never marshal this internal record directly into a web response.
type documentAnnotationRecord struct {
	Anchor     EvidenceAnchor     `json:"anchor"`
	Annotation DocumentAnnotation `json:"annotation"`
}

const documentAnnotationsSchema = `
CREATE TABLE IF NOT EXISTS document_annotations(
 id TEXT PRIMARY KEY,revision INTEGER NOT NULL CHECK(revision>0),document_id TEXT NOT NULL,
 anchor_json TEXT NOT NULL,start_offset INTEGER NOT NULL,end_offset INTEGER NOT NULL,
 prefix TEXT NOT NULL,suffix TEXT NOT NULL,note TEXT NOT NULL,color TEXT NOT NULL,
 archived INTEGER NOT NULL CHECK(archived IN (0,1)),author TEXT NOT NULL,created TEXT NOT NULL,updated TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS document_annotations_document ON document_annotations(document_id,archived,created,id);
CREATE TRIGGER IF NOT EXISTS document_annotation_anchor_immutable BEFORE UPDATE ON document_annotations
WHEN OLD.id!=NEW.id OR OLD.document_id!=NEW.document_id OR OLD.anchor_json!=NEW.anchor_json OR
 OLD.start_offset!=NEW.start_offset OR OLD.end_offset!=NEW.end_offset OR OLD.prefix!=NEW.prefix OR OLD.suffix!=NEW.suffix OR
 OLD.author!=NEW.author OR OLD.created!=NEW.created
BEGIN SELECT RAISE(ABORT,'annotation source identity is immutable'); END;
CREATE TABLE IF NOT EXISTS document_annotation_history(
 id INTEGER PRIMARY KEY AUTOINCREMENT,annotation_id TEXT NOT NULL,revision INTEGER NOT NULL,
 action TEXT NOT NULL,actor TEXT NOT NULL,created TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,
 UNIQUE(annotation_id,revision));
CREATE INDEX IF NOT EXISTS document_annotation_history_annotation ON document_annotation_history(annotation_id,id);
CREATE TRIGGER IF NOT EXISTS document_annotation_history_no_update BEFORE UPDATE ON document_annotation_history
BEGIN SELECT RAISE(ABORT,'annotation history is append-only'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_history_no_delete BEFORE DELETE ON document_annotation_history
BEGIN SELECT RAISE(ABORT,'annotation history is append-only'); END;
CREATE TABLE IF NOT EXISTS document_annotation_receipts(
 request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,annotation_id TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS document_annotation_receipt_no_update BEFORE UPDATE ON document_annotation_receipts
BEGIN SELECT RAISE(ABORT,'annotation receipt is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_receipt_no_delete BEFORE DELETE ON document_annotation_receipts
BEGIN SELECT RAISE(ABORT,'annotation receipt is immutable'); END;`

const documentAnnotationSizeSQL = `length(CAST(id AS BLOB))+length(CAST(document_id AS BLOB))+length(CAST(anchor_json AS BLOB))+length(CAST(prefix AS BLOB))+length(CAST(suffix AS BLOB))+length(CAST(note AS BLOB))+length(CAST(color AS BLOB))+length(CAST(author AS BLOB))+length(CAST(created AS BLOB))+length(CAST(updated AS BLOB))+64`
const documentAnnotationHistorySizeSQL = `length(CAST(annotation_id AS BLOB))+length(CAST(action AS BLOB))+length(CAST(actor AS BLOB))+length(CAST(created AS BLOB))+length(CAST(payload AS BLOB))+length(CAST(payload_hash AS BLOB))+32`
const documentAnnotationReceiptSizeSQL = `length(CAST(request_id AS BLOB))+length(CAST(request_hash AS BLOB))+length(CAST(annotation_id AS BLOB))+length(CAST(created AS BLOB))+32`

func validDocumentAnnotationID(id string) bool {
	return len(id) == 36 && strings.HasPrefix(id, "ann-") && strings.Trim(id[4:], "0123456789abcdef") == ""
}
func validDocumentAnnotationRequest(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}
func validDocumentAnnotationText(text string, max int) bool {
	return len(text) <= max*4 && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && utf8.RuneCountInString(text) <= max
}
func validDocumentAnnotationColor(color string) bool {
	return color == "yellow" || color == "green" || color == "blue" || color == "pink"
}
func validDocumentAnnotationActor(actor string) bool {
	return strings.TrimSpace(actor) != "" && validDocumentAnnotationText(actor, 128)
}
func documentAnnotationPagination(offset, limit int) (int, error) {
	if limit == 0 {
		limit = 50
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 50 {
		return 0, ErrDocumentAnnotationInvalid
	}
	return limit, nil
}
func documentAnnotationsExistTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_annotations'`).Scan(&n)
	return n == 1, err
}
func ensureDocumentAnnotationsTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, documentAnnotationsSchema)
	return err
}

func resolveDocumentAnnotationStateTx(ctx context.Context, tx *sql.Tx, record documentAnnotationRecord) (EvidenceState, error) {
	if strings.HasPrefix(record.Anchor.SourcePath, documentAnnotationUnboundPrefix) {
		return EvidenceMissing, nil
	}
	a := record.Anchor
	n := record.Annotation
	rows, err := tx.QueryContext(ctx, `SELECT document_revision,chunk_hash,substr(text,?,?)=? FROM entries
WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=? AND COALESCE(NULLIF(source_path,''),source_file,'')=? ORDER BY id LIMIT 2`,
		n.Start+1, n.End-n.Start, a.Excerpt, a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex, a.SourcePath)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	state := EvidenceMissing
	count := 0
	for rows.Next() {
		var revision, hash string
		var match bool
		if err = rows.Scan(&revision, &hash, &match); err != nil {
			return "", err
		}
		count++
		state = EvidenceStale
		if count == 1 && revision == a.DocumentRevision && hash == a.ChunkHash && match {
			state = EvidenceCurrent
		}
	}
	return state, rows.Err()
}

// Reads one bounded record and resolves its current index state in the caller's
// snapshot. It does not create schema or open source paths.
func readDocumentAnnotationTx(ctx context.Context, tx *sql.Tx, id string) (documentAnnotationRecord, error) {
	var record documentAnnotationRecord
	if !validDocumentAnnotationID(id) {
		return record, ErrDocumentAnnotationInvalid
	}
	exists, err := documentAnnotationsExistTx(ctx, tx)
	if err != nil {
		return record, err
	}
	if !exists {
		return record, ErrDocumentAnnotationNotFound
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT `+documentAnnotationSizeSQL+` FROM document_annotations WHERE id=?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrDocumentAnnotationNotFound
	}
	if err != nil {
		return record, err
	}
	if size > maxDocumentAnnotationRowBytes {
		return record, ErrDocumentAnnotationLimit
	}
	a := &record.Annotation
	var anchorJSON string
	err = tx.QueryRowContext(ctx, `SELECT id,revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,archived,author,created,updated FROM document_annotations WHERE id=?`, id).Scan(&a.ID, &a.Revision, &a.DocumentID, &anchorJSON, &a.Start, &a.End, &a.Prefix, &a.Suffix, &a.Note, &a.Color, &a.Archived, &a.Author, &a.Created, &a.Updated)
	if err != nil {
		return record, err
	}
	if json.Unmarshal([]byte(anchorJSON), &record.Anchor) != nil || validateEvidenceAnchor(record.Anchor) != nil {
		return record, ErrDocumentAnnotationInvalid
	}
	anchor := record.Anchor
	a.DocumentRevision = anchor.DocumentRevision
	a.CitationID = anchor.CitationID
	a.SourceName = sourceName(Entry{SourcePath: anchor.SourcePath})
	a.Page = anchor.Page
	a.BlockIndex = anchor.BlockIndex
	a.BlockChunkIndex = anchor.BlockChunkIndex
	a.Quote = anchor.Excerpt
	if a.Start < 0 || a.End <= a.Start || a.End-a.Start != utf8.RuneCountInString(a.Quote) || a.End > maxDocumentAnnotationChunkBytes || a.DocumentID != LibraryDocumentID(anchor.SourcePath, anchor.DocumentID) {
		return record, ErrDocumentAnnotationInvalid
	}
	a.State, err = resolveDocumentAnnotationStateTx(ctx, tx, record)
	return record, err
}

func readDocumentAnnotationReceiptTx(ctx context.Context, tx *sql.Tx, request, digest string) (DocumentAnnotationResult, bool, error) {
	var result DocumentAnnotationResult
	var oldDigest, id string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash,annotation_id,revision FROM document_annotation_receipts WHERE request_id=?`, request).Scan(&oldDigest, &id, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if oldDigest != digest {
		return result, false, ErrDocumentAnnotationRequest
	}
	// A late retry must return the confirmed version, never overwrite the
	// client's knowledge of it with a later user's note or archive action.
	record, err := readDocumentAnnotationHistoryRecordTx(ctx, tx, id, revision)
	result.Annotation = record.Annotation
	result.Replayed = true
	return result, true, err
}

func readDocumentAnnotationHistoryRecordTx(ctx context.Context, tx *sql.Tx, id string, revision int64) (documentAnnotationRecord, error) {
	var record documentAnnotationRecord
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT `+documentAnnotationHistorySizeSQL+` FROM document_annotation_history WHERE annotation_id=? AND revision=?`, id, revision).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrDocumentAnnotationNotFound
	}
	if err != nil {
		return record, err
	}
	if size > maxDocumentAnnotationHistoryRowBytes {
		return record, ErrDocumentAnnotationLimit
	}
	var payload, digest string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM document_annotation_history WHERE annotation_id=? AND revision=?`, id, revision).Scan(&payload, &digest); err != nil {
		return record, err
	}
	if prefixedSHA256([]byte(payload)) != digest || json.Unmarshal([]byte(payload), &record) != nil || record.Annotation.ID != id || record.Annotation.Revision != revision || validateEvidenceAnchor(record.Anchor) != nil {
		return record, ErrDocumentAnnotationInvalid
	}
	a := &record.Annotation
	anchor := record.Anchor
	if a.Start < 0 || a.End <= a.Start || a.End > maxDocumentAnnotationChunkBytes || a.End-a.Start != utf8.RuneCountInString(anchor.Excerpt) || a.Quote != anchor.Excerpt || a.DocumentID != LibraryDocumentID(anchor.SourcePath, anchor.DocumentID) {
		return record, ErrDocumentAnnotationInvalid
	}
	a.SourceName = sourceName(Entry{SourcePath: anchor.SourcePath})
	a.State, err = resolveDocumentAnnotationStateTx(ctx, tx, record)
	return record, err
}

func appendDocumentAnnotationHistoryTx(ctx context.Context, tx *sql.Tx, record documentAnnotationRecord, action, actor string) error {
	// State describes the current index, not a mutable field in historical data.
	record.Annotation.State = ""
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	now := record.Annotation.Updated
	rowBytes := int64(len(payload) + len(record.Annotation.ID) + len(action) + len(actor) + len(now) + 103)
	if rowBytes > maxDocumentAnnotationHistoryRowBytes {
		return ErrDocumentAnnotationLimit
	}
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+documentAnnotationHistorySizeSQL+`),0) FROM document_annotation_history`).Scan(&total); err != nil {
		return err
	}
	if total+rowBytes > maxDocumentAnnotationHistoryBytes {
		return ErrDocumentAnnotationLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_history(annotation_id,revision,action,actor,created,payload,payload_hash) VALUES(?,?,?,?,?,?,?)`, record.Annotation.ID, record.Annotation.Revision, action, actor, now, string(payload), prefixedSHA256(payload))
	return err
}

func checkDocumentAnnotationCapacityTx(ctx context.Context, tx *sql.Tx, record documentAnnotationRecord, creating bool) error {
	anchorJSON, err := json.Marshal(record.Anchor)
	if err != nil {
		return err
	}
	a := record.Annotation
	rowBytes := int64(len(a.ID) + len(a.DocumentID) + len(anchorJSON) + len(a.Prefix) + len(a.Suffix) + len(a.Note) + len(a.Color) + len(a.Author) + len(a.Created) + len(a.Updated) + 64)
	if rowBytes > maxDocumentAnnotationRowBytes {
		return ErrDocumentAnnotationLimit
	}
	var count int
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(`+documentAnnotationSizeSQL+`),0) FROM document_annotations WHERE id!=?`, a.ID).Scan(&count, &total); err != nil {
		return err
	}
	if (creating && count >= maxDocumentAnnotationRows) || (!creating && count+1 > maxDocumentAnnotationRows) || total+rowBytes > maxDocumentAnnotationBytes {
		return ErrDocumentAnnotationLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(`+documentAnnotationReceiptSizeSQL+`),0) FROM document_annotation_receipts`).Scan(&count, &total); err != nil {
		return err
	}
	if count >= maxDocumentAnnotationReceipts || total+256 > maxDocumentAnnotationReceiptBytes {
		return ErrDocumentAnnotationLimit
	}
	return nil
}

func saveDocumentAnnotationReceiptTx(ctx context.Context, tx *sql.Tx, request, digest string, a DocumentAnnotation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO document_annotation_receipts(request_id,request_hash,annotation_id,revision,created) VALUES(?,?,?,?,?)`, request, digest, a.ID, a.Revision, a.Updated)
	return err
}

func documentAnnotationDigest(actor, action, id string, input any) (string, error) {
	data, err := json.Marshal(struct {
		Actor, Action, ID string
		Input             any
	}{actor, action, id, input})
	return prefixedSHA256(data), err
}

func CreateDocumentAnnotationIn(ctx context.Context, dir, actor string, in DocumentAnnotationCreate) (DocumentAnnotationResult, error) {
	var result DocumentAnnotationResult
	if !validDocumentAnnotationActor(actor) || !validDocumentAnnotationRequest(in.RequestID) || in.EntryID < 1 || !validSHA256Digest(in.Pin) || in.Start < 0 || in.End <= in.Start || in.End > maxDocumentAnnotationChunkBytes || !validDocumentAnnotationText(in.Quote, MaxDocumentAnnotationRunes) || strings.TrimSpace(in.Quote) == "" || in.End-in.Start != utf8.RuneCountInString(in.Quote) || !validDocumentAnnotationText(in.Note, MaxDocumentAnnotationRunes) || !validDocumentAnnotationColor(in.Color) {
		return result, ErrDocumentAnnotationInvalid
	}
	digest, err := documentAnnotationDigest(actor, "create", "", in)
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
	if err = ensureDocumentAnnotationsTx(ctx, tx); err != nil {
		return result, err
	}
	if prior, found, e := readDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest); e != nil || found {
		return prior, e
	}
	var textBytes, entryBytes int64
	entrySizeColumns := strings.Split(storedEntryColumns, ",")
	for i, column := range entrySizeColumns {
		entrySizeColumns[i] = "COALESCE(length(CAST(" + strings.TrimSpace(column) + " AS BLOB)),0)"
	}
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB)),`+strings.Join(entrySizeColumns, "+")+` FROM entries WHERE id=?`, in.EntryID).Scan(&textBytes, &entryBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrDocumentAnnotationStale
	}
	if err != nil {
		return result, err
	}
	if textBytes > maxDocumentAnnotationChunkBytes || entryBytes > 64<<20 {
		return result, ErrDocumentAnnotationLimit
	}
	entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, in.EntryID)
	if err != nil {
		return result, err
	}
	if len(entries) != 1 {
		return result, ErrDocumentAnnotationStale
	}
	entry := entries[0]
	pin, err := documentRestoreEntryDigest(entry)
	if err != nil {
		return result, err
	}
	if pin != in.Pin {
		return result, ErrDocumentAnnotationStale
	}
	if !utf8.ValidString(entry.Text) || strings.ContainsRune(entry.Text, 0) {
		return result, ErrDocumentAnnotationInvalid
	}
	runes := []rune(entry.Text)
	if in.End > len(runes) || string(runes[in.Start:in.End]) != in.Quote {
		return result, ErrDocumentAnnotationInvalid
	}
	anchor, err := EvidenceAnchorForEntry(entry, in.Quote)
	if err != nil {
		return result, ErrDocumentAnnotationInvalid
	}
	id, err := newClassicMindMapID("ann-")
	if err != nil {
		return result, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := documentAnnotationRecord{Anchor: anchor, Annotation: DocumentAnnotation{ID: id, Revision: 1, DocumentID: LibraryDocumentID(anchor.SourcePath, anchor.DocumentID), DocumentRevision: anchor.DocumentRevision, CitationID: anchor.CitationID, SourceName: sourceName(entry), Page: anchor.Page, BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex, Quote: in.Quote, Start: in.Start, End: in.End, Prefix: string(runes[max(0, in.Start-documentAnnotationContextRunes):in.Start]), Suffix: string(runes[in.End:min(len(runes), in.End+documentAnnotationContextRunes)]), Note: in.Note, Color: in.Color, Author: actor, Created: now, Updated: now, State: EvidenceCurrent}}
	state, err := resolveDocumentAnnotationStateTx(ctx, tx, record)
	if err != nil {
		return result, err
	}
	if state != EvidenceCurrent {
		return result, ErrDocumentAnnotationStale
	}
	if err = checkDocumentAnnotationCapacityTx(ctx, tx, record, true); err != nil {
		return result, err
	}
	anchorJSON, err := json.Marshal(anchor)
	if err != nil {
		return result, err
	}
	a := record.Annotation
	_, err = tx.ExecContext(ctx, `INSERT INTO document_annotations(id,revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,archived,author,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Revision, a.DocumentID, string(anchorJSON), a.Start, a.End, a.Prefix, a.Suffix, a.Note, a.Color, false, a.Author, a.Created, a.Updated)
	if err != nil {
		return result, err
	}
	if err = appendDocumentAnnotationHistoryTx(ctx, tx, record, "create", actor); err != nil {
		return result, err
	}
	if err = saveDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest, a); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return DocumentAnnotationResult{Annotation: a}, nil
}

func UpdateDocumentAnnotationIn(ctx context.Context, dir, actor, id string, in DocumentAnnotationEdit) (DocumentAnnotationResult, error) {
	var result DocumentAnnotationResult
	if !validDocumentAnnotationActor(actor) || !validDocumentAnnotationID(id) || in.Revision < 1 || !validDocumentAnnotationRequest(in.RequestID) {
		return result, ErrDocumentAnnotationInvalid
	}
	if in.Archived != nil {
		if in.Note != "" || in.Color != "" {
			return result, ErrDocumentAnnotationInvalid
		}
	} else if !validDocumentAnnotationText(in.Note, MaxDocumentAnnotationRunes) || !validDocumentAnnotationColor(in.Color) {
		return result, ErrDocumentAnnotationInvalid
	}
	digest, err := documentAnnotationDigest(actor, "edit", id, in)
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
	if err = ensureDocumentAnnotationsTx(ctx, tx); err != nil {
		return result, err
	}
	if prior, found, e := readDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest); e != nil || found {
		return prior, e
	}
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return result, err
	}
	a := &record.Annotation
	if a.Revision != in.Revision {
		return result, ErrDocumentAnnotationConflict
	}
	action := "edit"
	if in.Archived != nil {
		a.Archived = *in.Archived
		action = "restore"
		if a.Archived {
			action = "archive"
		}
	} else {
		a.Note = in.Note
		a.Color = in.Color
	}
	a.Revision++
	a.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	if err = checkDocumentAnnotationCapacityTx(ctx, tx, record, false); err != nil {
		return result, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE document_annotations SET revision=?,note=?,color=?,archived=?,updated=? WHERE id=? AND revision=?`, a.Revision, a.Note, a.Color, a.Archived, a.Updated, id, in.Revision)
	if err != nil {
		return result, err
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return result, err
	}
	if count != 1 {
		return result, ErrDocumentAnnotationConflict
	}
	if err = appendDocumentAnnotationHistoryTx(ctx, tx, record, action, actor); err != nil {
		return result, err
	}
	if err = saveDocumentAnnotationReceiptTx(ctx, tx, in.RequestID, digest, *a); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return DocumentAnnotationResult{Annotation: *a}, nil
}

func ReadDocumentAnnotationIn(ctx context.Context, dir, id string) (DocumentAnnotation, error) {
	if !validDocumentAnnotationID(id) {
		return DocumentAnnotation{}, ErrDocumentAnnotationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return DocumentAnnotation{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentAnnotation{}, err
	}
	defer tx.Rollback()
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	return record.Annotation, err
}

func ListDocumentAnnotationsIn(ctx context.Context, dir string, q DocumentAnnotationQuery) (DocumentAnnotationPage, error) {
	page := DocumentAnnotationPage{Items: []DocumentAnnotation{}, Offset: q.Offset}
	limit, err := documentAnnotationPagination(q.Offset, q.Limit)
	if err != nil {
		return page, err
	}
	page.Limit = limit
	if len(q.DocumentID) > 128 || !utf8.ValidString(q.DocumentID) || strings.ContainsAny(q.DocumentID, "\x00\r\n") {
		return page, ErrDocumentAnnotationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return page, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	exists, err := documentAnnotationsExistTx(ctx, tx)
	if err != nil || !exists {
		return page, err
	}
	where := `archived=?`
	args := []any{q.Archived}
	if q.DocumentID != "" {
		where += ` AND document_id=?`
		args = append(args, q.DocumentID)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotations WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM document_annotations WHERE `+where+` ORDER BY created DESC,id DESC LIMIT ? OFFSET ?`, append(args, limit, q.Offset)...)
	if err != nil {
		return page, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for _, id := range ids {
		record, e := readDocumentAnnotationTx(ctx, tx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, record.Annotation)
	}
	return page, nil
}

func ReadDocumentAnnotationHistoryIn(ctx context.Context, dir, id string, offset, limit int) (DocumentAnnotationHistory, error) {
	page := DocumentAnnotationHistory{Items: []DocumentAnnotationHistoryItem{}, Offset: offset}
	if !validDocumentAnnotationID(id) {
		return page, ErrDocumentAnnotationInvalid
	}
	var err error
	limit, err = documentAnnotationPagination(offset, limit)
	if err != nil {
		return page, err
	}
	page.Limit = limit
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return page, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	current, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return page, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_annotation_history WHERE annotation_id=?`, id).Scan(&page.Total); err != nil {
		return page, err
	}
	// Only bounded identifiers/sizes are loaded before accepting each payload.
	rows, err := tx.QueryContext(ctx, `SELECT id,`+documentAnnotationHistorySizeSQL+` FROM document_annotation_history WHERE annotation_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return page, err
	}
	ids := []int64{}
	for rows.Next() {
		var historyID, size int64
		if err = rows.Scan(&historyID, &size); err != nil {
			rows.Close()
			return page, err
		}
		if size > maxDocumentAnnotationHistoryRowBytes {
			rows.Close()
			return page, ErrDocumentAnnotationLimit
		}
		ids = append(ids, historyID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for _, historyID := range ids {
		var item DocumentAnnotationHistoryItem
		var payload, digest string
		err = tx.QueryRowContext(ctx, `SELECT id,revision,action,actor,created,payload,payload_hash FROM document_annotation_history WHERE id=? AND annotation_id=?`, historyID, id).Scan(&item.ID, &item.Revision, &item.Action, &item.Actor, &item.Created, &payload, &digest)
		if err != nil {
			return page, err
		}
		var saved documentAnnotationRecord
		if prefixedSHA256([]byte(payload)) != digest || json.Unmarshal([]byte(payload), &saved) != nil || saved.Annotation.ID != id || saved.Annotation.Revision != item.Revision || saved.Anchor != current.Anchor || saved.Annotation.Start != current.Annotation.Start || saved.Annotation.End != current.Annotation.End {
			return page, ErrDocumentAnnotationInvalid
		}
		item.Annotation = saved.Annotation
		item.Annotation.State = current.Annotation.State
		// Never trust denormalized path-like source names from a stored snapshot.
		item.Annotation.SourceName = current.Annotation.SourceName
		page.Items = append(page.Items, item)
	}
	return page, nil
}
