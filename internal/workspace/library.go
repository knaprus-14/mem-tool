package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type Query struct {
	Kind     string
	Document string
	Offset   int
	Limit    int
}
type Document struct {
	DocumentID string `json:"document_id,omitempty"`
	ID         string `json:"id"`
	ManagedID  string `json:"managed_id,omitempty"`
	Name       string `json:"name"`
	Chunks     int    `json:"chunks"`
	Pages      int    `json:"pages"`
	Source     string `json:"-"`
}
type Entry struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Text      string   `json:"text,omitempty"`
	Tags      []string `json:"tags"`
	Created   string   `json:"created"`
	Page      int      `json:"page"`
	Important bool     `json:"important"`
}
type Library struct {
	SelectedDocument *Document  `json:"selected_document,omitempty"`
	Documents        []Document `json:"documents"`
	Entries          []Entry    `json:"entries"`
	EntryCount       int        `json:"entry_count"`
	DocumentCount    int        `json:"document_count"`
	NoteCount        int        `json:"note_count"`
	Total            int        `json:"total"`
	Offset           int        `json:"offset"`
	Limit            int        `json:"limit"`
}

const sourceExpr = `COALESCE(NULLIF(source_path,''),source_file,'')`

func readDB(ctx context.Context, w Workspace) (*sql.DB, error) {
	path := mem.StorePathIn(filepath.Join(w.Root, mem.MemDirName))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: store.db должен быть обычным файлом", ErrInvalid)
	}
	return sqliteutil.OpenReadOnly(ctx, path)
}

// ReadLibrary reads a consistent, paginated snapshot without opening mem.Store,
// loading vectors, performing migrations, or recovering another process's jobs.
func ReadLibrary(ctx context.Context, w Workspace, q Query) (Library, error) {
	if q.Kind != "" && q.Kind != "documents" && q.Kind != "notes" && q.Kind != "entries" {
		return Library{}, ErrInvalid
	}
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 30
	}
	if q.Offset < 0 {
		return Library{}, ErrInvalid
	}
	result := Library{Documents: []Document{}, Entries: []Entry{}, Limit: q.Limit, Offset: q.Offset}
	db, err := readDB(ctx, w)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN `+sourceExpr+`='' THEN 1 ELSE 0 END),0) FROM entries`).Scan(&result.EntryCount, &result.NoteCount); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sourceExpr+` AS source, COUNT(*), COUNT(DISTINCT CASE WHEN page>0 THEN page END),COALESCE(MIN(document_id),''),COALESCE(MAX(document_id),'') FROM entries WHERE `+sourceExpr+`<>'' GROUP BY source ORDER BY source`)
	if err != nil {
		return result, err
	}
	docs := []Document{}
	for rows.Next() {
		var d Document
		var minID, maxID string
		if err = rows.Scan(&d.Source, &d.Chunks, &d.Pages, &minID, &maxID); err != nil {
			rows.Close()
			return result, err
		}
		d.ID = mem.LibraryDocumentID(d.Source, "")
		if minID == maxID {
			d.DocumentID = minID
		}
		if minID == maxID && strings.HasPrefix(minID, "web:") && len(minID) == 36 && strings.Trim(minID[4:], "0123456789abcdef") == "" {
			d.ManagedID = minID[4:]
			d.ID = minID
		}
		d.Name = displayBase(d.Source)
		docs = append(docs, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.DocumentCount = len(docs)
	if q.Kind == "documents" || q.Kind == "" {
		result.Total = len(docs)
		if q.Offset < len(docs) {
			end := min(q.Offset+q.Limit, len(docs))
			result.Documents = docs[q.Offset:end]
		}
		return result, tx.Commit()
	}
	where := "1=1"
	args := []any{}
	if q.Kind == "notes" {
		where = sourceExpr + `=''`
	} else if q.Kind == "entries" && q.Document != "" {
		found := false
		for _, d := range docs {
			if d.ID == q.Document {
				result.SelectedDocument = &d
				where = sourceExpr + `=?`
				args = append(args, d.Source)
				found = true
				break
			}
		}
		if !found {
			return result, ErrNotFound
		}
	} else if q.Kind != "entries" {
		return result, ErrInvalid
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	args = append(args, q.Limit, q.Offset)
	order := "id DESC"
	if q.Kind == "entries" && q.Document != "" {
		order = "page,block_index,block_chunk_index,chunk_index,id"
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,COALESCE(title,''),COALESCE(tags,'[]'),created,COALESCE(page,0),important FROM entries WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var e Entry
		var tags string
		if err = rows.Scan(&e.ID, &e.Title, &tags, &e.Created, &e.Page, &e.Important); err != nil {
			rows.Close()
			return result, err
		}
		e.Tags = []string{}
		if err = json.Unmarshal([]byte(tags), &e.Tags); err != nil {
			rows.Close()
			return result, fmt.Errorf("теги записи %d: %w", e.ID, err)
		}
		result.Entries = append(result.Entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func ReadEntry(ctx context.Context, w Workspace, id int64) (Entry, error) {
	db, err := readDB(ctx, w)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	defer db.Close()
	var e Entry
	var tags string
	err = db.QueryRowContext(ctx, `SELECT id,COALESCE(title,''),text,COALESCE(tags,'[]'),created,COALESCE(page,0),important FROM entries WHERE id=?`, id).Scan(&e.ID, &e.Title, &e.Text, &tags, &e.Created, &e.Page, &e.Important)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Tags = []string{}
	err = json.Unmarshal([]byte(tags), &e.Tags)
	return e, err
}

type Settings struct {
	Revision           string `json:"revision"`
	Backend            string `json:"backend"`
	EmbeddingModel     string `json:"embedding_model"`
	AnswerModel        string `json:"answer_model"`
	ChunkSize          int    `json:"chunk_size"`
	ChunkOverlap       int    `json:"chunk_overlap"`
	ChunkStrategy      string `json:"chunk_strategy"`
	AnswerTimeout      int    `json:"answer_timeout"`
	AnswerMaxTokens    int    `json:"answer_max_tokens"`
	AnswerContextChars int    `json:"answer_context_chars"`
}

func ReadSettings(w Workspace) (Settings, error) {
	c, err := mem.LoadConfigIn(filepath.Join(w.Root, mem.MemDirName))
	if err != nil {
		return Settings{}, err
	}
	return SettingsFromConfig(c), nil
}

func SettingsFromConfig(c *mem.Config) Settings {
	model := c.Ollama.Model
	if c.Backend == "polza" {
		model = c.Polza.Model
	}
	answer := c.Answer.WithDefaults()
	return Settings{Revision: c.Revision(), Backend: c.Backend, EmbeddingModel: model, AnswerModel: c.Answer.Model, ChunkSize: c.Chunking.MaxSize, ChunkOverlap: c.Chunking.Overlap, ChunkStrategy: c.Chunking.Strategy, AnswerTimeout: answer.TimeoutSeconds, AnswerMaxTokens: answer.MaxTokens, AnswerContextChars: answer.ContextChars}
}
func displayBase(path string) string { return filepath.Base(strings.ReplaceAll(path, `\`, "/")) }
