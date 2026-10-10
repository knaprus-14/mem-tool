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

var ErrNoteHistoryLimit = errors.New("история заметок достигла лимита: 10000 состояний или 256 МиБ; операция отменена без изменения заметки")
var ErrNoteSnapshot = errors.New("сохранённая версия недоступна или повреждена")

type noteSnapshot struct {
	Entry  Entry     `json:"entry"`
	Vector []float32 `json:"vector"`
}

type NoteHistoryItem struct {
	VersionID int64  `json:"version_id"`
	ID        int64  `json:"id"`
	Pin       string `json:"pin"`
	Title     string `json:"title"`
	Action    string `json:"action"`
	Created   string `json:"created"`
}

type NoteHistoryPage struct {
	Items  []NoteHistoryItem `json:"items"`
	Total  int               `json:"total"`
	Offset int               `json:"offset"`
	Limit  int               `json:"limit"`
}

type NoteVersionView struct {
	NoteHistoryItem
	Note NoteView `json:"note"`
}

type NoteAction struct {
	Action     string `json:"action"`
	ID         int64  `json:"id"`
	Pin        string `json:"pin"`
	VersionID  int64  `json:"version_id"`
	VersionPin string `json:"version_pin"`
	RequestID  string `json:"request_id"`
}

func ensureNoteHistory(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_note_history(
version_id INTEGER PRIMARY KEY AUTOINCREMENT,entry_id INTEGER NOT NULL,pin TEXT NOT NULL,
entry_pin TEXT NOT NULL,title TEXT NOT NULL,action TEXT NOT NULL,created TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS web_note_history_entry ON web_note_history(entry_id,version_id);
CREATE TABLE IF NOT EXISTS web_note_trash(entry_id INTEGER PRIMARY KEY,version_id INTEGER NOT NULL UNIQUE);`)
	return err
}

func appendNoteHistory(ctx context.Context, tx *sql.Tx, e Entry, action string) (NoteHistoryItem, error) {
	if e.SourceFile != "" || e.SourcePath != "" || e.DocumentID != "" {
		return NoteHistoryItem{}, ErrNoteNotFound
	}
	if err := ensureNoteHistory(ctx, tx); err != nil {
		return NoteHistoryItem{}, err
	}
	entryPin, err := documentRestoreEntryDigest(e)
	if err != nil {
		return NoteHistoryItem{}, err
	}
	if action == "before" {
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT entry_pin FROM web_note_history WHERE entry_id=? ORDER BY version_id DESC LIMIT 1`, e.ID).Scan(&previous)
		if err == nil && previous == entryPin {
			return NoteHistoryItem{}, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return NoteHistoryItem{}, err
		}
	}
	payload, err := json.Marshal(noteSnapshot{Entry: e, Vector: e.Embedding})
	if err != nil {
		return NoteHistoryItem{}, err
	}
	if len(payload) > 2<<20 {
		return NoteHistoryItem{}, ErrNoteReadLimit
	}
	var count, bytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM web_note_history`).Scan(&count, &bytes); err != nil {
		return NoteHistoryItem{}, err
	}
	if count >= 10000 || bytes+int64(len(payload)) > 256<<20 {
		return NoteHistoryItem{}, ErrNoteHistoryLimit
	}
	item := NoteHistoryItem{ID: e.ID, Pin: prefixedSHA256(payload), Title: e.Title, Action: action, Created: time.Now().UTC().Format(time.RFC3339Nano)}
	result, err := tx.ExecContext(ctx, `INSERT INTO web_note_history(entry_id,pin,entry_pin,title,action,created,payload) VALUES(?,?,?,?,?,?,?)`, e.ID, item.Pin, entryPin, e.Title, action, item.Created, payload)
	if err != nil {
		return item, err
	}
	item.VersionID, err = result.LastInsertId()
	return item, err
}

func noteHistoryExists(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_note_history'`).Scan(&count)
	return count == 1, err
}

func readNoteSnapshot(ctx context.Context, tx *sql.Tx, id, version int64) (NoteHistoryItem, Entry, error) {
	var item NoteHistoryItem
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT length(payload) FROM web_note_history WHERE entry_id=? AND version_id=?`, id, version).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return item, Entry{}, ErrNoteSnapshot
	}
	if err != nil {
		return item, Entry{}, err
	}
	if size > 2<<20 {
		return item, Entry{}, ErrNoteSnapshot
	}
	var payload []byte
	var entryPin string
	err = tx.QueryRowContext(ctx, `SELECT version_id,entry_id,pin,title,action,created,payload,entry_pin FROM web_note_history WHERE entry_id=? AND version_id=?`, id, version).Scan(&item.VersionID, &item.ID, &item.Pin, &item.Title, &item.Action, &item.Created, &payload, &entryPin)
	if err != nil {
		return item, Entry{}, err
	}
	var snap noteSnapshot
	if prefixedSHA256(payload) != item.Pin || json.Unmarshal(payload, &snap) != nil {
		return item, Entry{}, ErrNoteSnapshot
	}
	e := snap.Entry
	e.Embedding = snap.Vector
	if e.ID != id || e.SourceFile != "" || e.SourcePath != "" || e.DocumentID != "" || e.Dims != len(e.Embedding) {
		return item, Entry{}, ErrNoteSnapshot
	}
	pin, err := documentRestoreEntryDigest(e)
	if err != nil || pin != entryPin {
		return item, Entry{}, ErrNoteSnapshot
	}
	return item, e, nil
}

// ListNoteHistoryIn returns summaries only; payloads/vectors are never sent in lists.
// id=0 selects the trash. Reads do not create or migrate any tables.
func ListNoteHistoryIn(ctx context.Context, dir string, id int64, offset int) (applogResult0 NoteHistoryPage, applogResult1 error) {
	defer applog.Track("pkg.mem.ListNoteHistoryIn")(&applogResult1)

	page := NoteHistoryPage{Items: []NoteHistoryItem{}, Offset: offset, Limit: 30}
	if id < 0 || offset < 0 || offset > 10000 {
		return page, ErrNoteInvalid
	}
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
	exists, err := noteHistoryExists(ctx, tx)
	if err != nil || !exists {
		return page, err
	}
	from := `web_note_history h`
	where := `h.entry_id=?`
	args := []any{id}
	if id == 0 {
		from += ` JOIN web_note_trash t ON t.version_id=h.version_id AND t.entry_id=h.entry_id`
		where = `1=1`
		args = nil
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+` WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, page.Limit, offset)
	rows, err := tx.QueryContext(ctx, `SELECT h.version_id,h.entry_id,h.pin,h.title,h.action,h.created FROM `+from+` WHERE `+where+` ORDER BY h.version_id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item NoteHistoryItem
		if err = rows.Scan(&item.VersionID, &item.ID, &item.Pin, &item.Title, &item.Action, &item.Created); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func ReadNoteVersionIn(ctx context.Context, dir string, id, version int64) (applogResult0 NoteVersionView, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadNoteVersionIn")(&applogResult1)

	if id < 1 || version < 1 {
		return NoteVersionView{}, ErrNoteInvalid
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return NoteVersionView{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NoteVersionView{}, err
	}
	defer tx.Rollback()
	exists, err := noteHistoryExists(ctx, tx)
	if err != nil {
		return NoteVersionView{}, err
	}
	if !exists {
		return NoteVersionView{}, ErrNoteSnapshot
	}
	item, e, err := readNoteSnapshot(ctx, tx, id, version)
	if err != nil {
		return NoteVersionView{}, err
	}
	note, err := noteView(e)
	if err != nil {
		return NoteVersionView{}, err
	}
	return NoteVersionView{NoteHistoryItem: item, Note: note}, tx.Commit()
}

func restoreNoteSnapshot(ctx context.Context, tx *sql.Tx, e Entry) error {
	tags, err := tagsToJSON(e.Tags)
	if err != nil {
		return err
	}
	vector, err := floatsToBytes(e.Embedding)
	if err != nil {
		return err
	}
	warnings, err := json.Marshal(e.Warnings)
	if err != nil {
		return err
	}
	columns := strings.Split(strings.ReplaceAll(storedEntryColumns, "\n", ""), ",")
	assignments := []string{}
	marks := []string{}
	for _, column := range columns {
		column = strings.TrimSpace(column)
		marks = append(marks, "?")
		if column != "id" {
			assignments = append(assignments, column+"=excluded."+column)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO entries (`+storedEntryColumns+`) VALUES (`+strings.Join(marks, ",")+`) ON CONFLICT(id) DO UPDATE SET `+strings.Join(assignments, ","),
		e.ID, e.Title, e.Text, tags, e.Created, e.Backend, e.EmbeddingModel, e.EmbeddingSpace, e.Dims, vector, e.SourceFile, e.ChunkLabel, e.ChunkIndex, e.TotalChunks, e.DocumentID, e.DocumentRevision, e.ChunkHash, e.SourcePath, e.MediaType, e.Page, e.BlockIndex, e.BlockMarker, e.BlockChunkIndex, e.BlockTotalChunks, e.ExtractionMethod, e.OCRConfidence, string(warnings), boolToInt(e.Important))
	return err
}

// ApplyNoteActionIn moves notes to trash or restores a pinned saved version.
// No model or external process is called. The history, entry and receipt commit together.
func ApplyNoteActionIn(ctx context.Context, dir string, input NoteAction) (applogResult0 NoteWriteResult, applogResult1 error) {
	defer applog.Track("pkg.mem.ApplyNoteActionIn")(&applogResult1)

	if input.ID < 1 || len(input.RequestID) != 32 || strings.Trim(input.RequestID, "0123456789abcdef") != "" {
		return NoteWriteResult{}, ErrNoteInvalid
	}
	if input.Action != "delete" && input.Action != "restore" && input.Action != "restore-version" {
		return NoteWriteResult{}, ErrNoteInvalid
	}
	if input.Action != "restore" && !validSHA256Digest(input.Pin) {
		return NoteWriteResult{}, ErrNoteInvalid
	}
	if input.Action != "delete" && (input.VersionID < 1 || !validSHA256Digest(input.VersionPin)) {
		return NoteWriteResult{}, ErrNoteInvalid
	}
	encoded, _ := json.Marshal(input)
	digest := prefixedSHA256(encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return NoteWriteResult{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NoteWriteResult{}, err
	}
	defer tx.Rollback()
	if result, found, err := noteReceipt(ctx, tx, input.RequestID, digest); err != nil || found {
		return result, err
	}
	if err = prepareNoteWrite(ctx, tx); err != nil {
		return NoteWriteResult{}, err
	}
	if err = ensureNoteHistory(ctx, tx); err != nil {
		return NoteWriteResult{}, err
	}
	var current Entry
	if input.Action == "restore" {
		var trashVersion, exists int64
		err = tx.QueryRowContext(ctx, `SELECT version_id FROM web_note_trash WHERE entry_id=?`, input.ID).Scan(&trashVersion)
		if errors.Is(err, sql.ErrNoRows) || err == nil && trashVersion != input.VersionID {
			return NoteWriteResult{}, ErrNoteConflict
		}
		if err != nil {
			return NoteWriteResult{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE id=?`, input.ID).Scan(&exists); err != nil {
			return NoteWriteResult{}, err
		}
		if exists != 0 {
			return NoteWriteResult{}, ErrNoteConflict
		}
	} else {
		current, err = readNoteEntry(ctx, tx, input.ID)
		if err != nil {
			return NoteWriteResult{}, err
		}
		pin, e := documentRestoreEntryDigest(current)
		if e != nil {
			return NoteWriteResult{}, e
		}
		if pin != input.Pin {
			return NoteWriteResult{}, ErrNoteConflict
		}
	}
	if input.Action == "delete" {
		item, e := appendNoteHistory(ctx, tx, current, "deleted")
		if e != nil {
			return NoteWriteResult{}, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO web_note_trash(entry_id,version_id) VALUES(?,?)`, input.ID, item.VersionID); err != nil {
			return NoteWriteResult{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM entries WHERE id=?`, input.ID); err != nil {
			return NoteWriteResult{}, err
		}
	} else {
		item, snapshot, e := readNoteSnapshot(ctx, tx, input.ID, input.VersionID)
		if e != nil {
			return NoteWriteResult{}, e
		}
		if item.Pin != input.VersionPin {
			return NoteWriteResult{}, ErrNoteSnapshot
		}
		if input.Action == "restore-version" {
			if _, err = appendNoteHistory(ctx, tx, current, "before"); err != nil {
				return NoteWriteResult{}, err
			}
		}
		// Fresh timestamp invalidates stale forms even when restoring identical content.
		snapshot.Created = time.Now().UTC().Format(time.RFC3339Nano)
		if err = restoreNoteSnapshot(ctx, tx, snapshot); err != nil {
			return NoteWriteResult{}, err
		}
		if _, err = appendNoteHistory(ctx, tx, snapshot, "restored"); err != nil {
			return NoteWriteResult{}, err
		}
		if input.Action == "restore" {
			if _, err = tx.ExecContext(ctx, `DELETE FROM web_note_trash WHERE entry_id=?`, input.ID); err != nil {
				return NoteWriteResult{}, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_note_writes(request_id,request_hash,entry_id,created) VALUES(?,?,?,?)`, input.RequestID, digest, input.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return NoteWriteResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return NoteWriteResult{}, err
	}
	return NoteWriteResult{ID: input.ID}, nil
}

func prepareNoteWrite(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_note_writes(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,entry_id INTEGER NOT NULL,created TEXT NOT NULL)`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_note_writes`).Scan(&count); err != nil {
		return err
	}
	if count >= 100000 {
		return ErrNoteLimit
	}
	return nil
}
