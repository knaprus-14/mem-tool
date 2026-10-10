package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var (
	ErrNoteInvalid   = errors.New("проверьте заметку: текст до 2000 символов, название до 200, до 20 тегов по 80 символов")
	ErrNoteNotFound  = errors.New("заметка не найдена; фрагменты документов изменяются через импорт")
	ErrNoteConflict  = errors.New("заметка изменилась; перечитайте её перед сохранением")
	ErrNoteRequest   = errors.New("эта попытка уже сохранила другое содержимое; обновите список заметок")
	ErrNoteLimit     = errors.New("достигнут лимит веб-сохранений заметок (100000); используйте CLI")
	ErrNoteReadLimit = errors.New("заметка слишком велика для веб-редактора: до 100000 символов текста и 1 МиБ данных записи")
)

type NoteInput struct {
	ID        int64    `json:"id"`
	Pin       string   `json:"pin"`
	RequestID string   `json:"request_id"`
	Text      *string  `json:"text"` // nil preserves text and embedding when editing metadata
	Title     string   `json:"title"`
	Tags      []string `json:"tags"`
	Important bool     `json:"important"`
}

type NoteView struct {
	ID           int64    `json:"id"`
	Pin          string   `json:"pin"`
	Text         string   `json:"text"`
	Title        string   `json:"title"`
	Tags         []string `json:"tags"`
	Important    bool     `json:"important"`
	TextEditable bool     `json:"text_editable"`
}

type NoteWriteResult struct {
	ID       int64 `json:"id"`
	Replayed bool  `json:"replayed"`
}

func (n *NoteInput) validate() error {
	if n.ID < 0 || len(n.RequestID) != 32 || strings.Trim(n.RequestID, "0123456789abcdef") != "" || (n.ID > 0 && !validSHA256Digest(n.Pin)) || (n.ID == 0 && (n.Pin != "" || n.Text == nil)) {
		return ErrNoteInvalid
	}
	if n.Text != nil && (!utf8.ValidString(*n.Text) || strings.TrimSpace(*n.Text) == "" || utf8.RuneCountInString(*n.Text) > MaxEmbeddingChars || strings.ContainsRune(*n.Text, 0)) {
		return ErrNoteInvalid
	}
	n.Title = strings.TrimSpace(n.Title)
	if !utf8.ValidString(n.Title) || utf8.RuneCountInString(n.Title) > 200 || strings.ContainsRune(n.Title, 0) || len(n.Tags) > 20 {
		return ErrNoteInvalid
	}
	tags := []string{}
	seen := map[string]bool{}
	for _, tag := range n.Tags {
		tag = strings.TrimSpace(tag)
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > 80 || strings.ContainsAny(tag, "\x00\r\n") {
			return ErrNoteInvalid
		}
		if tag != "" && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	n.Tags = tags
	return nil
}

// readNoteEntry loads one bounded manual entry, never a source-anchored chunk.
func readNoteEntry(ctx context.Context, tx *sql.Tx, id int64) (Entry, error) {
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(title AS BLOB))+length(CAST(COALESCE(warnings,'') AS BLOB)) FROM entries WHERE id=? AND COALESCE(source_file,'')='' AND COALESCE(source_path,'')='' AND COALESCE(document_id,'')=''`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNoteNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if size > 1<<20 {
		return Entry{}, ErrNoteReadLimit
	}
	entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, id)
	if err != nil {
		return Entry{}, err
	}
	if len(entries) != 1 {
		return Entry{}, ErrNoteNotFound
	}
	if utf8.RuneCountInString(entries[0].Text) > 100000 {
		return Entry{}, ErrNoteReadLimit
	}
	return entries[0], nil
}

func ReadNoteIn(ctx context.Context, dir string, id int64) (applogResult0 NoteView, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadNoteIn")(&applogResult1)

	if id < 1 {
		return NoteView{}, ErrNoteInvalid
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return NoteView{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NoteView{}, err
	}
	defer tx.Rollback()
	e, err := readNoteEntry(ctx, tx, id)
	if err != nil {
		return NoteView{}, err
	}
	view, err := noteView(e)
	if err != nil {
		return NoteView{}, err
	}
	return view, tx.Commit()
}

func noteView(e Entry) (NoteView, error) {
	pin, err := documentRestoreEntryDigest(e)
	if err != nil {
		return NoteView{}, err
	}
	return NoteView{ID: e.ID, Pin: pin, Text: e.Text, Title: e.Title, Tags: append([]string{}, e.Tags...), Important: e.Important, TextEditable: utf8.RuneCountInString(e.Text) <= MaxEmbeddingChars}, nil
}

func noteReceipt(ctx context.Context, tx *sql.Tx, request, digest string) (NoteWriteResult, bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_note_writes'`).Scan(&exists); err != nil {
		return NoteWriteResult{}, false, err
	}
	if exists == 0 {
		return NoteWriteResult{}, false, nil
	}
	var savedDigest string
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash,entry_id FROM web_note_writes WHERE request_id=?`, request).Scan(&savedDigest, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return NoteWriteResult{}, false, nil
	}
	if err != nil {
		return NoteWriteResult{}, false, err
	}
	if savedDigest != digest {
		return NoteWriteResult{}, false, ErrNoteRequest
	}
	return NoteWriteResult{ID: id, Replayed: true}, true, nil
}

// SaveNoteIn prepares vectors before reserving SQLite's writer, then compares the
// complete row pin and commits the entry with a durable retry receipt. It neither
// loads the vector corpus nor recovers other processes' import runs.
func SaveNoteIn(ctx context.Context, dir string, input NoteInput) (applogResult0 NoteWriteResult, applogResult1 error) {
	defer applog.Track("pkg.mem.SaveNoteIn")(&applogResult1)

	if err := input.validate(); err != nil {
		return NoteWriteResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	encoded, err := json.Marshal(input)
	if err != nil {
		return NoteWriteResult{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	ro, err := openQueryDB(ctx, dir)
	if err != nil {
		return NoteWriteResult{}, err
	}
	read, err := ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		ro.Close()
		return NoteWriteResult{}, err
	}
	result, found, err := noteReceipt(ctx, read, input.RequestID, digest)
	var previous Entry
	if err == nil && !found && input.ID > 0 {
		previous, err = readNoteEntry(ctx, read, input.ID)
		if err == nil {
			var pin string
			pin, err = documentRestoreEntryDigest(previous)
			if err == nil && pin != input.Pin {
				err = ErrNoteConflict
			}
		}
	}
	read.Rollback()
	ro.Close()
	if err != nil || found {
		return result, err
	}
	text := previous.Text
	if input.Text != nil {
		text = *input.Text
	}
	vector := previous.Embedding
	identity := EmbeddingIdentity{Backend: previous.Backend, Model: previous.EmbeddingModel, SpaceID: previous.EmbeddingSpace}
	if input.ID == 0 || text != previous.Text {
		cfg, e := LoadConfigIn(dir)
		if e != nil {
			return NoteWriteResult{}, e
		}
		identity, err = EmbeddingIdentityForConfig(cfg)
		if err != nil {
			return NoteWriteResult{}, ErrQueryEmbedding
		}
		vector, err = GetEmbeddingContext(ctx, cfg, text)
		if ctx.Err() != nil {
			return NoteWriteResult{}, ctx.Err()
		}
		if err != nil {
			return NoteWriteResult{}, ErrQueryEmbedding
		}
	}
	embedding, err := floatsToBytes(vector)
	if err != nil {
		return NoteWriteResult{}, err
	}
	tags, err := tagsToJSON(input.Tags)
	if err != nil {
		return NoteWriteResult{}, err
	}
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
	if result, found, err = noteReceipt(ctx, tx, input.RequestID, digest); err != nil || found {
		return result, err
	}
	if input.ID > 0 {
		latest, e := readNoteEntry(ctx, tx, input.ID)
		if e != nil {
			return NoteWriteResult{}, e
		}
		pin, e := documentRestoreEntryDigest(latest)
		if e != nil {
			return NoteWriteResult{}, e
		}
		if pin != input.Pin {
			return NoteWriteResult{}, ErrNoteConflict
		}
	}
	if err = prepareNoteWrite(ctx, tx); err != nil {
		return NoteWriteResult{}, err
	}
	if input.ID > 0 {
		if _, err = appendNoteHistory(ctx, tx, previous, "before"); err != nil {
			return NoteWriteResult{}, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := input.ID
	if id == 0 {
		res, e := tx.ExecContext(ctx, `INSERT INTO entries(title,text,tags,created,backend,embedding_model,embedding_space,dims,embedding,important) VALUES(?,?,?,?,?,?,?,?,?,?)`, input.Title, text, tags, now, identity.Backend, identity.Model, identity.SpaceID, len(vector), embedding, boolToInt(input.Important))
		if e != nil {
			return NoteWriteResult{}, e
		}
		id, err = res.LastInsertId()
		if err != nil {
			return NoteWriteResult{}, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE entries SET title=?,text=?,tags=?,created=?,backend=?,embedding_model=?,embedding_space=?,dims=?,embedding=?,important=? WHERE id=?`, input.Title, text, tags, now, identity.Backend, identity.Model, identity.SpaceID, len(vector), embedding, boolToInt(input.Important), id)
		if err != nil {
			return NoteWriteResult{}, err
		}
	}
	stored, err := readNoteEntry(ctx, tx, id)
	if err != nil {
		return NoteWriteResult{}, err
	}
	action := "edited"
	if input.ID == 0 {
		action = "created"
	}
	if _, err = appendNoteHistory(ctx, tx, stored, action); err != nil {
		return NoteWriteResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_note_writes(request_id,request_hash,entry_id,created) VALUES(?,?,?,?)`, input.RequestID, digest, id, now); err != nil {
		return NoteWriteResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return NoteWriteResult{}, err
	}
	return NoteWriteResult{ID: id}, nil
}
