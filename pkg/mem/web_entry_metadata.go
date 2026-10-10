package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

// Metadata edits retain text, provenance, creation time and embedding bytes.
// Pins cover the complete row, including concurrent CLI/import changes.
func readMetadataEntry(ctx context.Context, tx *sql.Tx, id int64) (Entry, error) {
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(title AS BLOB)) FROM entries WHERE id=?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNoteNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if size > 1<<20 {
		return Entry{}, ErrNoteReadLimit
	}
	items, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, id)
	if err != nil {
		return Entry{}, err
	}
	if len(items) != 1 {
		return Entry{}, ErrNoteNotFound
	}
	return items[0], nil
}

func ReadEntryMetadataIn(ctx context.Context, dir string, id int64) (out NoteView, err error) {
	defer applog.Track("pkg.mem.ReadEntryMetadataIn")(&err)
	if id < 1 {
		return out, ErrNoteInvalid
	}
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
	e, err := readMetadataEntry(ctx, tx, id)
	if err != nil {
		return out, err
	}
	pin, err := documentRestoreEntryDigest(e)
	if err != nil {
		return out, err
	}
	return NoteView{ID: e.ID, Pin: pin, Title: e.Title, Tags: append([]string{}, e.Tags...), Important: e.Important}, nil
}

func SaveEntryMetadataIn(ctx context.Context, dir, actor string, in NoteInput) (out NoteWriteResult, err error) {
	defer applog.Track("pkg.mem.SaveEntryMetadataIn")(&err)
	if in.ID < 1 || in.Text != nil {
		return out, ErrNoteInvalid
	}
	if err = in.validate(); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	payload, _ := json.Marshal(struct {
		Actor string
		Input NoteInput
	}{actor, in})
	digest := prefixedSHA256(payload)
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_entry_metadata(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,entry_id INTEGER NOT NULL,before_json TEXT NOT NULL,after_json TEXT NOT NULL,created TEXT NOT NULL);`); err != nil {
		return out, err
	}
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,entry_id FROM web_entry_metadata WHERE request_id=?`, in.RequestID).Scan(&hash, &out.ID)
	if err == nil {
		if hash != digest {
			return out, ErrNoteRequest
		}
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_entry_metadata`).Scan(&count); err != nil {
		return out, err
	}
	if count >= 100000 {
		return out, ErrNoteLimit
	}
	e, err := readMetadataEntry(ctx, tx, in.ID)
	if err != nil {
		return out, err
	}
	pin, err := documentRestoreEntryDigest(e)
	if err != nil {
		return out, err
	}
	if pin != in.Pin {
		return out, ErrNoteConflict
	}
	before, _ := json.Marshal(NoteView{ID: e.ID, Title: e.Title, Tags: e.Tags, Important: e.Important})
	tags, err := tagsToJSON(in.Tags)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE entries SET title=?,tags=?,important=? WHERE id=?`, in.Title, tags, boolToInt(in.Important), in.ID); err != nil {
		return out, err
	}
	after, _ := json.Marshal(NoteView{ID: in.ID, Title: in.Title, Tags: in.Tags, Important: in.Important})
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_entry_metadata VALUES(?,?,?,?,?,?)`, in.RequestID, digest, in.ID, string(before), string(after), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return out, err
	}
	out.ID = in.ID
	err = tx.Commit()
	return out, err
}
