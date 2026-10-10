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
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var (
	ErrReadingWorkspaceInvalid   = errors.New("проверьте параметры рабочего набора чтения")
	ErrReadingWorkspaceNotFound  = errors.New("фрагмент рабочего набора не найден")
	ErrReadingWorkspaceConflict  = errors.New("рабочий набор изменился; перечитайте его перед новой операцией")
	ErrReadingWorkspaceRequest   = errors.New("ключ повтора уже использован для другого изменения рабочего набора")
	ErrReadingWorkspaceDuplicate = errors.New("эта цитата с такой привязкой к карте уже сохранена в рабочем наборе")
	ErrReadingWorkspaceStale     = errors.New("источник или выбранная редакция карты изменились; обновите выбор")
	ErrReadingWorkspaceLimit     = errors.New("достигнут предел размера рабочего набора чтения")
)

const (
	MaxReadingWorkspaceItems           = 24
	MaxReadingWorkspaceTextRunes       = 8000
	MaxReadingWorkspaceJSONBytes       = 1 << 20
	MaxReadingWorkspaceReceipts        = 128
	MaxReadingWorkspaceReceiptBytes    = 8 << 20
	maxReadingWorkspaceActors          = 1000
	maxReadingWorkspaceBytes           = 64 << 20
	maxReadingWorkspaceAllReceiptBytes = 64 << 20
)

type ReadingWorkspaceMapRef struct {
	MapID    string `json:"map_id"`
	NodeID   string `json:"node_id"`
	Revision int64  `json:"revision"`
}

type ReadingWorkspaceItem struct {
	ID               string                  `json:"id"`
	EntryID          int64                   `json:"entry_id"`
	Pin              string                  `json:"pin"`
	DocumentID       string                  `json:"document_id"`
	DocumentRevision string                  `json:"document_revision"`
	CitationID       string                  `json:"citation_id"`
	SourceName       string                  `json:"source_name"`
	Page             int                     `json:"page"`
	BlockIndex       int                     `json:"block_index"`
	BlockChunkIndex  int                     `json:"block_chunk_index"`
	Title            string                  `json:"title"`
	Text             string                  `json:"text"`
	Start            int                     `json:"start"`
	End              int                     `json:"end"`
	Scroll           float64                 `json:"scroll"`
	MapRef           *ReadingWorkspaceMapRef `json:"map_ref,omitempty"`
	SourceState      EvidenceState           `json:"source_state"`
	MapState         string                  `json:"map_state"`
}

type ReadingWorkspace struct {
	Revision int64                  `json:"revision"`
	ActiveID string                 `json:"active_id"`
	Items    []ReadingWorkspaceItem `json:"items"`
}

type ReadingWorkspaceMutation struct {
	Revision  int64                   `json:"revision"`
	RequestID string                  `json:"request_id"`
	Action    string                  `json:"action"`
	ID        string                  `json:"id,omitempty"`
	EntryID   int64                   `json:"entry_id,omitempty"`
	Pin       string                  `json:"pin,omitempty"`
	Start     int                     `json:"start,omitempty"`
	End       int                     `json:"end,omitempty"`
	Scroll    float64                 `json:"scroll,omitempty"`
	MapRef    *ReadingWorkspaceMapRef `json:"map_ref,omitempty"`
}

type ReadingWorkspaceResult struct {
	Workspace ReadingWorkspace `json:"workspace"`
	Replayed  bool             `json:"replayed"`
}

type readingWorkspaceRecord struct {
	Item   ReadingWorkspaceItem
	Anchor EvidenceAnchor
}
type readingWorkspaceStored struct {
	Revision int64
	ActiveID string
	Items    []readingWorkspaceRecord
}

const readingWorkspaceSchema = `CREATE TABLE IF NOT EXISTS reading_workspaces(
 actor_key TEXT PRIMARY KEY,revision INTEGER NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS reading_workspace_receipts(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,actor_key TEXT NOT NULL,request_id TEXT NOT NULL,request_hash TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,
 UNIQUE(actor_key,request_id));
CREATE INDEX IF NOT EXISTS reading_workspace_receipts_actor ON reading_workspace_receipts(actor_key,seq);
CREATE TRIGGER IF NOT EXISTS reading_workspace_receipts_no_update BEFORE UPDATE ON reading_workspace_receipts BEGIN SELECT RAISE(ABORT,'reading receipt is immutable'); END;`

func readingWorkspaceActorKey(actor string) (string, error) {
	if !validDocumentAnnotationActor(actor) || strings.TrimSpace(actor) != actor || strings.ContainsFunc(actor, unicode.IsControl) {
		return "", ErrReadingWorkspaceInvalid
	}
	return prefixedSHA256([]byte("reading-workspace\x00" + actor)), nil
}
func validReadingID(id, prefix string) bool {
	return len(id) == len(prefix)+32 && strings.HasPrefix(id, prefix) && strings.Trim(id[len(prefix):], "0123456789abcdef") == ""
}
func validReadingMapRef(ref *ReadingWorkspaceMapRef) bool {
	return ref == nil || (validReadingID(ref.MapID, "mm-") && validReadingID(ref.NodeID, "mmn-") && ref.Revision > 0)
}
func validReadingScroll(scroll float64) bool {
	return !math.IsNaN(scroll) && !math.IsInf(scroll, 0) && scroll >= 0 && scroll <= 1
}
func validReadingMutation(in ReadingWorkspaceMutation) bool {
	if in.Revision < 0 || in.Revision == math.MaxInt64 || !validDocumentAnnotationRequest(in.RequestID) || !validReadingScroll(in.Scroll) || !validReadingMapRef(in.MapRef) {
		return false
	}
	if in.Action == "add" {
		return in.ID == "" && in.EntryID > 0 && validSHA256Digest(in.Pin) && in.Start >= 0 && in.End > in.Start && in.End <= maxDocumentAnnotationChunkBytes && in.End-in.Start <= MaxReadingWorkspaceTextRunes && in.Scroll == 0
	}
	if !validReadingID(in.ID, "read-") || in.EntryID != 0 || in.Pin != "" || in.Start != 0 || in.End != 0 || in.MapRef != nil {
		return false
	}
	return in.Action == "position" || ((in.Action == "select" || in.Action == "remove") && in.Scroll == 0)
}

func decodeReadingWorkspace(payload, hash string) (readingWorkspaceStored, error) {
	var stored readingWorkspaceStored
	if len(payload) > MaxReadingWorkspaceJSONBytes {
		return stored, ErrReadingWorkspaceLimit
	}
	if prefixedSHA256([]byte(payload)) != hash || json.Unmarshal([]byte(payload), &stored) != nil || stored.Revision < 1 || len(stored.Items) > MaxReadingWorkspaceItems {
		return stored, ErrReadingWorkspaceInvalid
	}
	seen := map[string]bool{}
	for i := range stored.Items {
		r := &stored.Items[i]
		a := &r.Item
		r.Anchor.Excerpt = a.Text
		if !validReadingID(a.ID, "read-") || seen[a.ID] || a.EntryID < 1 || !validSHA256Digest(a.Pin) || !validReadingScroll(a.Scroll) || !validReadingMapRef(a.MapRef) || !validDocumentAnnotationText(a.Text, MaxReadingWorkspaceTextRunes) || strings.TrimSpace(a.Text) == "" || !validDocumentAnnotationText(a.Title, 256) || a.Start < 0 || a.End <= a.Start || a.End > maxDocumentAnnotationChunkBytes || a.End-a.Start != utf8.RuneCountInString(a.Text) || validateEvidenceAnchor(r.Anchor) != nil || r.Anchor.EvidenceHash != ChunkContentHash(a.Text) || a.DocumentID != LibraryDocumentID(r.Anchor.SourcePath, r.Anchor.DocumentID) || a.DocumentRevision != r.Anchor.DocumentRevision || a.CitationID != r.Anchor.CitationID || a.Page != r.Anchor.Page || a.BlockIndex != r.Anchor.BlockIndex || a.BlockChunkIndex != r.Anchor.BlockChunkIndex {
			return stored, ErrReadingWorkspaceInvalid
		}
		a.SourceName = truncateRunes(sourceName(Entry{SourcePath: r.Anchor.SourcePath}), 256)
		seen[a.ID] = true
	}
	if stored.ActiveID != "" && !seen[stored.ActiveID] {
		return stored, ErrReadingWorkspaceInvalid
	}
	return stored, nil
}

func encodeReadingWorkspace(stored readingWorkspaceStored) ([]byte, error) {
	copy := stored
	copy.Items = append([]readingWorkspaceRecord{}, stored.Items...)
	for i := range copy.Items {
		// Text is stored once; immutable anchor excerpt is reconstructed on read.
		copy.Items[i].Anchor.Excerpt = ""
		copy.Items[i].Item.SourceState = ""
		copy.Items[i].Item.MapState = ""
	}
	payload, err := json.Marshal(copy)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxReadingWorkspaceJSONBytes {
		return nil, ErrReadingWorkspaceLimit
	}
	return payload, nil
}

func readReadingWorkspaceTx(ctx context.Context, tx *sql.Tx, key string) (readingWorkspaceStored, error) {
	stored := readingWorkspaceStored{Items: []readingWorkspaceRecord{}}
	exists, err := annotationTableExists(ctx, tx, "reading_workspaces")
	if err != nil || !exists {
		return stored, err
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(payload AS BLOB)) FROM reading_workspaces WHERE actor_key=?`, key).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, nil
	}
	if err != nil {
		return stored, err
	}
	if size > MaxReadingWorkspaceJSONBytes {
		return stored, ErrReadingWorkspaceLimit
	}
	var payload, hash string
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision,payload,payload_hash FROM reading_workspaces WHERE actor_key=?`, key).Scan(&revision, &payload, &hash); err != nil {
		return stored, err
	}
	stored, err = decodeReadingWorkspace(payload, hash)
	if err == nil && stored.Revision != revision {
		err = ErrReadingWorkspaceInvalid
	}
	return stored, err
}

func readingMapStateTx(ctx context.Context, tx *sql.Tx, ref *ReadingWorkspaceMapRef) (string, error) {
	if ref == nil {
		return "", nil
	}
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return "", err
	}
	if !exists {
		return "missing", nil
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT m.revision FROM mind_maps m JOIN mind_map_nodes n ON n.map_id=m.id AND n.id=? AND n.deleted_at='' WHERE m.id=? AND m.deleted_at=''`, ref.NodeID, ref.MapID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if revision != ref.Revision {
		return "changed", nil
	}
	return "current", nil
}

func resolveReadingWorkspaceTx(ctx context.Context, tx *sql.Tx, stored readingWorkspaceStored) (ReadingWorkspace, error) {
	out := ReadingWorkspace{Revision: stored.Revision, ActiveID: stored.ActiveID, Items: make([]ReadingWorkspaceItem, 0, len(stored.Items))}
	for _, r := range stored.Items {
		var err error
		r.Item.SourceState, err = resolveDocumentAnnotationStateTx(ctx, tx, documentAnnotationRecord{Anchor: r.Anchor, Annotation: DocumentAnnotation{Start: r.Item.Start, End: r.Item.End}})
		if err != nil {
			return out, err
		}
		r.Item.MapState, err = readingMapStateTx(ctx, tx, r.Item.MapRef)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, r.Item)
	}
	return out, nil
}

// ReadReadingWorkspaceIn never creates schema, changes pins or opens files.
func ReadReadingWorkspaceIn(ctx context.Context, dir, actor string) (ReadingWorkspace, error) {
	key, err := readingWorkspaceActorKey(actor)
	if err != nil {
		return ReadingWorkspace{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return ReadingWorkspace{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReadingWorkspace{}, err
	}
	defer tx.Rollback()
	stored, err := readReadingWorkspaceTx(ctx, tx, key)
	if err != nil {
		return ReadingWorkspace{}, err
	}
	return resolveReadingWorkspaceTx(ctx, tx, stored)
}

func addReadingWorkspaceItemTx(ctx context.Context, tx *sql.Tx, in ReadingWorkspaceMutation) (readingWorkspaceRecord, error) {
	var record readingWorkspaceRecord
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB)) FROM entries WHERE id=?`, in.EntryID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrReadingWorkspaceStale
	}
	if err != nil {
		return record, err
	}
	if size > maxDocumentAnnotationChunkBytes {
		return record, ErrReadingWorkspaceLimit
	}
	var text string
	if err = tx.QueryRowContext(ctx, `SELECT text FROM entries WHERE id=?`, in.EntryID).Scan(&text); err != nil {
		return record, err
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return record, ErrReadingWorkspaceInvalid
	}
	runes := []rune(text)
	if in.End > len(runes) || strings.TrimSpace(string(runes[in.Start:in.End])) == "" {
		return record, ErrReadingWorkspaceInvalid
	}
	selected, err := annotationReanchorTargetTx(ctx, tx, DocumentAnnotationReanchorPreviewInput{EntryID: in.EntryID, Pin: in.Pin, Start: in.Start, End: in.End, Quote: string(runes[in.Start:in.End])})
	if errors.Is(err, ErrDocumentAnnotationStale) {
		return record, ErrReadingWorkspaceStale
	}
	if errors.Is(err, ErrDocumentAnnotationLimit) {
		return record, ErrReadingWorkspaceLimit
	}
	if errors.Is(err, ErrDocumentAnnotationReanchorInvalid) {
		return record, ErrReadingWorkspaceInvalid
	}
	if err != nil {
		return record, err
	}
	var title string
	if err = tx.QueryRowContext(ctx, `SELECT substr(title,1,256) FROM entries WHERE id=?`, in.EntryID).Scan(&title); err != nil {
		return record, err
	}
	if !validDocumentAnnotationText(title, 256) {
		return record, ErrReadingWorkspaceInvalid
	}
	mapState, err := readingMapStateTx(ctx, tx, in.MapRef)
	if err != nil {
		return record, err
	}
	if in.MapRef != nil && mapState != "current" {
		return record, ErrReadingWorkspaceStale
	}
	id, err := newClassicMindMapID("read-")
	if err != nil {
		return record, err
	}
	a := selected.Annotation
	record = readingWorkspaceRecord{Anchor: selected.Anchor, Item: ReadingWorkspaceItem{ID: id, EntryID: in.EntryID, Pin: in.Pin, DocumentID: a.DocumentID, DocumentRevision: a.DocumentRevision, CitationID: a.CitationID, SourceName: truncateRunes(a.SourceName, 256), Page: a.Page, BlockIndex: a.BlockIndex, BlockChunkIndex: a.BlockChunkIndex, Title: title, Text: a.Quote, Start: a.Start, End: a.End, MapRef: in.MapRef}}
	return record, nil
}

func readReadingReceiptTx(ctx context.Context, tx *sql.Tx, key, request, digest string) (readingWorkspaceStored, bool, error) {
	var stored readingWorkspaceStored
	var size int64
	var oldHash string
	err := tx.QueryRowContext(ctx, `SELECT request_hash,length(CAST(payload AS BLOB)) FROM reading_workspace_receipts WHERE actor_key=? AND request_id=?`, key, request).Scan(&oldHash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, false, nil
	}
	if err != nil {
		return stored, false, err
	}
	if oldHash != digest {
		return stored, false, ErrReadingWorkspaceRequest
	}
	if size > MaxReadingWorkspaceJSONBytes {
		return stored, false, ErrReadingWorkspaceLimit
	}
	var payload, hash string
	if err = tx.QueryRowContext(ctx, `SELECT payload,payload_hash FROM reading_workspace_receipts WHERE actor_key=? AND request_id=?`, key, request).Scan(&payload, &hash); err != nil {
		return stored, false, err
	}
	stored, err = decodeReadingWorkspace(payload, hash)
	return stored, true, err
}

// Receipts are immutable while retained, but deliberately finite: at most 128
// and 8 MiB per actor, 64 MiB globally. Oldest receipts are removed atomically.
// A retry outside this window fails revision CAS; it never repeats an action.
func pruneReadingReceiptsTx(ctx context.Context, tx *sql.Tx, key string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM reading_workspace_receipts WHERE actor_key=? AND seq NOT IN (SELECT seq FROM reading_workspace_receipts WHERE actor_key=? ORDER BY seq DESC LIMIT 128)`, key, key); err != nil {
		return err
	}
	for _, scope := range []struct {
		where string
		args  []any
		max   int64
	}{{" WHERE actor_key=?", []any{key}, MaxReadingWorkspaceReceiptBytes}, {"", nil, maxReadingWorkspaceAllReceiptBytes}} {
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(payload AS BLOB))+length(actor_key)+length(request_id)+length(request_hash)+length(payload_hash)+8),0) FROM reading_workspace_receipts`+scope.where, scope.args...).Scan(&total); err != nil {
			return err
		}
		for total > scope.max {
			var seq, size int64
			if err := tx.QueryRowContext(ctx, `SELECT seq,length(CAST(payload AS BLOB))+length(actor_key)+length(request_id)+length(request_hash)+length(payload_hash)+8 FROM reading_workspace_receipts`+scope.where+` ORDER BY seq LIMIT 1`, scope.args...).Scan(&seq, &size); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM reading_workspace_receipts WHERE seq=?`, seq); err != nil {
				return err
			}
			total -= size
		}
	}
	return nil
}

// Every accepted action advances revision, including selecting the active item
// or storing the same scroll. Exact duplicate adds are rejected. Position never
// activates an item; removal of the active item leaves active_id empty.
func MutateReadingWorkspaceIn(ctx context.Context, dir, actor string, in ReadingWorkspaceMutation) (ReadingWorkspaceResult, error) {
	return MutateReadingWorkspaceWithAdmissionIn(ctx, dir, actor, in, nil)
}

// MutateReadingWorkspaceWithAdmissionIn checks optional external storage
// admission only for a new add, after exact receipt lookup and revision CAS.
// A lost-ACK retry remains available when storage fills after the original
// success. The callback must not write this database or start another mutation.
func MutateReadingWorkspaceWithAdmissionIn(ctx context.Context, dir, actor string, in ReadingWorkspaceMutation, admit func() error) (ReadingWorkspaceResult, error) {
	var result ReadingWorkspaceResult
	key, err := readingWorkspaceActorKey(actor)
	if err != nil || !validReadingMutation(in) {
		return result, ErrReadingWorkspaceInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return result, err
	}
	digest := prefixedSHA256(raw)
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
	if _, err = tx.ExecContext(ctx, readingWorkspaceSchema); err != nil {
		return result, err
	}
	prior, found, err := readReadingReceiptTx(ctx, tx, key, in.RequestID, digest)
	if err != nil {
		return result, err
	}
	if found {
		result.Workspace, err = resolveReadingWorkspaceTx(ctx, tx, prior)
		result.Replayed = true
		return result, err
	}
	stored, err := readReadingWorkspaceTx(ctx, tx, key)
	if err != nil {
		return result, err
	}
	if stored.Revision != in.Revision {
		return result, ErrReadingWorkspaceConflict
	}
	if in.Action == "add" {
		if admit != nil {
			if err = admit(); err != nil {
				return result, err
			}
		}
		if len(stored.Items) >= MaxReadingWorkspaceItems {
			return result, ErrReadingWorkspaceLimit
		}
		record, e := addReadingWorkspaceItemTx(ctx, tx, in)
		if e != nil {
			return result, e
		}
		for _, old := range stored.Items {
			if old.Anchor == record.Anchor && old.Item.Start == record.Item.Start && old.Item.End == record.Item.End {
				oldMap, newMap := old.Item.MapRef, record.Item.MapRef
				if (oldMap == nil && newMap == nil) || (oldMap != nil && newMap != nil && *oldMap == *newMap) {
					return result, ErrReadingWorkspaceDuplicate
				}
			}
		}
		stored.Items = append(stored.Items, record)
		stored.ActiveID = record.Item.ID
	} else {
		index := -1
		for i, r := range stored.Items {
			if r.Item.ID == in.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return result, ErrReadingWorkspaceNotFound
		}
		switch in.Action {
		case "select":
			stored.ActiveID = in.ID
		case "position":
			stored.Items[index].Item.Scroll = in.Scroll
		case "remove":
			stored.Items = append(stored.Items[:index], stored.Items[index+1:]...)
			if stored.ActiveID == in.ID {
				stored.ActiveID = ""
			}
		}
	}
	stored.Revision++
	payload, err := encodeReadingWorkspace(stored)
	if err != nil {
		return result, err
	}
	var count, total int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))),0) FROM reading_workspaces WHERE actor_key<>?`, key).Scan(&count, &total); err != nil {
		return result, err
	}
	if count >= maxReadingWorkspaceActors || total+int64(len(payload)) > maxReadingWorkspaceBytes {
		return result, ErrReadingWorkspaceLimit
	}
	hash := prefixedSHA256(payload)
	if in.Revision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO reading_workspaces(actor_key,revision,payload,payload_hash) VALUES(?,?,?,?)`, key, stored.Revision, string(payload), hash)
	} else {
		var update sql.Result
		update, err = tx.ExecContext(ctx, `UPDATE reading_workspaces SET revision=?,payload=?,payload_hash=? WHERE actor_key=? AND revision=?`, stored.Revision, string(payload), hash, key, in.Revision)
		if err == nil {
			n, e := update.RowsAffected()
			if e != nil {
				err = e
			} else if n != 1 {
				err = ErrReadingWorkspaceConflict
			}
		}
	}
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO reading_workspace_receipts(actor_key,request_id,request_hash,payload,payload_hash) VALUES(?,?,?,?,?)`, key, in.RequestID, digest, string(payload), hash); err != nil {
		return result, err
	}
	if err = pruneReadingReceiptsTx(ctx, tx, key); err != nil {
		return result, err
	}
	result.Workspace, err = resolveReadingWorkspaceTx(ctx, tx, stored)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
