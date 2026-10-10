package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var (
	ErrQueryInvalid   = errors.New("некорректные параметры поиска")
	ErrQueryLimit     = errors.New("веб-поиск ограничен 20 000 записями и 64 МиБ содержимого с векторами; используйте CLI для большей базы")
	ErrQueryEmbedding = errors.New("не удалось получить вектор запроса; проверьте модель поиска или выберите поиск по словам")
	ErrQueryAnswer    = errors.New("не удалось получить ответ; проверьте модель ответов и её доступность")
	ErrQueryStale     = errors.New("источник изменён или удалён; выполните поиск заново")
)

type QueryRequest struct {
	Query     string   `json:"query"`
	Mode      string   `json:"mode"`
	Document  string   `json:"document,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Important bool     `json:"important"`
	PageFrom  int      `json:"page_from,omitempty"`
	PageTo    int      `json:"page_to,omitempty"`
	Limit     int      `json:"limit"`
	MinScore  float64  `json:"min_score"`
}

type QueryHit struct {
	DocumentRevision string   `json:"document_revision,omitempty"`
	ID               int64    `json:"id"`
	Pin              string   `json:"pin"`
	Title            string   `json:"title"`
	Text             string   `json:"text"`
	Source           string   `json:"source"`
	Document         string   `json:"document,omitempty"`
	CitationID       string   `json:"citation_id"`
	Page             int      `json:"page"`
	Tags             []string `json:"tags"`
	Important        bool     `json:"important"`
	Score            float64  `json:"score"`
	Truncated        bool     `json:"truncated"`
	OCRWarning       bool     `json:"ocr_warning"`
}

type QueryResult struct {
	Hits         []QueryHit `json:"hits"`
	Total        int        `json:"total"`
	Answer       string     `json:"answer,omitempty"`
	Status       string     `json:"status"`
	Sources      []QueryHit `json:"sources"`
	LexicalMode  string     `json:"lexical_mode"`
	Incompatible int        `json:"incompatible_vectors"`
}

func LibraryDocumentID(source, documentID string) string {
	if source == "" {
		return ""
	}
	if strings.HasPrefix(documentID, "web:") && len(documentID) == 36 && strings.Trim(documentID[4:], "0123456789abcdef") == "" {
		return documentID
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func (q *QueryRequest) validate() error {
	q.Query = strings.TrimSpace(q.Query)
	if !utf8.ValidString(q.Query) || utf8.RuneCountInString(q.Query) < 1 || utf8.RuneCountInString(q.Query) > MaxEmbeddingChars {
		return ErrQueryInvalid
	}
	if q.Mode == "" {
		q.Mode = "hybrid"
	}
	if q.Mode != "hybrid" && q.Mode != "text" && q.Mode != "vector" {
		return ErrQueryInvalid
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if q.Limit < 1 || q.Limit > 50 || math.IsNaN(q.MinScore) || math.IsInf(q.MinScore, 0) || q.MinScore < 0 || q.MinScore > 1 || len(q.Tags) > 20 || len(q.Document) > 80 {
		return ErrQueryInvalid
	}
	for _, tag := range q.Tags {
		if strings.TrimSpace(tag) == "" || utf8.RuneCountInString(tag) > 80 {
			return ErrQueryInvalid
		}
	}
	for _, date := range []string{q.From, q.To} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return ErrQueryInvalid
			}
		}
	}
	if q.From != "" && q.To != "" && q.From > q.To {
		return ErrQueryInvalid
	}
	if q.PageFrom < 0 || q.PageTo < 0 || q.PageFrom > 1000000 || q.PageTo > 1000000 || (q.PageTo > 0 && q.PageFrom > q.PageTo) {
		return ErrQueryInvalid
	}
	return nil
}

type contextEntryReader struct {
	ctx context.Context
	tx  *sql.Tx
}

func (q contextEntryReader) Query(s string, args ...any) (applogResult0 *sql.Rows, applogResult1 error) {
	defer applog.Track("pkg.mem.contextEntryReader.Query")(&applogResult1)

	return q.tx.QueryContext(q.ctx, s, args...)
}

func openQueryDB(ctx context.Context, dir string) (*sql.DB, error) {
	info, err := os.Lstat(StorePathIn(dir))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrQueryInvalid
	}
	return sqliteutil.OpenReadOnly(ctx, StorePathIn(dir))
}

// QueryIn reads a consistent bounded snapshot. It never opens a mutating Store,
// rebuilds FTS, recovers imports, writes history or changes the process directory.
func QueryIn(ctx context.Context, dir string, q QueryRequest, answer bool) (applogResult0 QueryResult, applogResult1 error) {
	defer applog.Track("pkg.mem.QueryIn")(&applogResult1)

	result := QueryResult{Hits: []QueryHit{}, Sources: []QueryHit{}, Status: "results", LexicalMode: lexicalFallback}
	if err := q.validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return result, err
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var count, size int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)+length(CAST(title AS BLOB))+length(CAST(tags AS BLOB))+length(CAST(warnings AS BLOB))+length(CAST(source_file AS BLOB))+length(CAST(source_path AS BLOB))),0) FROM entries`).Scan(&count, &size); err != nil {
		return result, err
	}
	if count > 20000 || size > 64<<20 {
		return result, ErrQueryLimit
	}
	entries, vectors, err := loadAllEntries(contextEntryReader{ctx, tx})
	if err != nil {
		return result, err
	}
	options := SearchOptions{Query: q.Query, Tags: q.Tags, From: q.From, To: q.To, Document: q.Document, ImportantOnly: q.Important, PageFrom: q.PageFrom, PageTo: q.PageTo, VectorOnly: q.Mode == "vector", AllBackends: q.Mode == "text"}
	lexical := map[int64]float64{}
	if q.Mode != "vector" {
		lexical = fallbackLexicalScores(entries, q.Query)
		generation, e1 := loadEntryCacheGeneration(tx)
		ftsGeneration, e2 := loadEntryFTSGeneration(tx)
		if e1 == nil && e2 == nil && generation == ftsGeneration {
			if scores, e := queryFTS(contextEntryReader{ctx, tx}, q.Query); e == nil {
				lexical = scores
				result.LexicalMode = lexicalFTS5
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(entries) > 0 && q.Mode != "text" {
		identity, e := EmbeddingIdentityForConfig(cfg)
		if e != nil {
			return result, ErrQueryEmbedding
		}
		options.Backend = identity.Backend
		options.EmbeddingSpace = identity.SpaceID
		options.QueryVector, err = GetEmbeddingContext(ctx, cfg, q.Query)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, ErrQueryEmbedding
		}
		for _, e := range entries {
			if matchesSearchFilters(e, options) && (e.Backend != identity.Backend || !embeddingSpaceCompatible(e.EmbeddingSpace, identity.SpaceID) || len(e.Embedding) != len(options.QueryVector)) {
				result.Incompatible++
			}
		}
	}
	lexical = filterLexicalScores(entries, lexical, options)
	results := scoreSearchSnapshot(entries, vectors, lexical, options)
	ReRankResults(results, q.Query)
	SortSearchResults(results)
	selected := results[:0]
	for _, e := range results {
		if e.Score >= q.MinScore {
			selected = append(selected, e)
		}
	}
	result.Total = len(selected)
	selected = selected[:min(q.Limit, len(selected))]
	for _, e := range selected {
		hit, e2 := queryHit(e, 2000, cfg.Ingest.LowConfidence)
		if e2 != nil {
			return result, e2
		}
		result.Hits = append(result.Hits, hit)
	}
	if !answer {
		return result, ctx.Err()
	}
	if len(selected) == 0 {
		result.Status = "insufficient"
		result.Answer = "Поиск не нашёл подходящих фрагментов. Измените запрос или фильтры."
		return result, nil
	}
	answerCfg := cfg.Answer.WithDefaults()
	answerCfg.MaxTokens = min(answerCfg.MaxTokens, 4096)
	answerCfg.ContextChars = min(answerCfg.ContextChars, 48000)
	answerCfg.TimeoutSeconds = min(answerCfg.TimeoutSeconds, 90)
	provider, err := NewOllamaAnswerProvider(answerCfg)
	if err != nil {
		return result, ErrQueryAnswer
	}
	// Provider context contains source names, not local/server directory paths.
	safe := make([]Entry, len(selected))
	for i, e := range selected {
		e.CitationID, _ = CitationForEntry(e)
		e.CitationLabel = sourceName(e)
		e.SourcePath = sourceName(e)
		if selected[i].SourceFile == "" && selected[i].SourcePath == "" {
			e.SourcePath = ""
		}
		e.SourceFile = e.SourcePath
		e.Warnings = nil
		e.Embedding = nil
		safe[i] = e
	}
	prompt, err := BuildGroundedPromptWithOptions(q.Query, safe, answerCfg.ContextChars, cfg.Ingest.LowConfidence)
	if err != nil {
		return result, ErrQueryAnswer
	}
	if len(prompt.Evidence) == 0 {
		result.Status = "insufficient"
		result.Answer = "Фрагменты не поместились в контекст модели. Уточните запрос."
		return result, nil
	}
	request := AnswerRequest{Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User, MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature}
	schemaMode := UsesGroundedAnswerSchema(answerCfg.Model)
	if schemaMode {
		request, err = GroundedAnswerSchemaRequest(request, prompt.Evidence)
		if err != nil {
			return result, ErrQueryAnswer
		}
	}
	raw, err := provider.Generate(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, ErrQueryAnswer
	}
	validated := ValidateGroundedAnswer(raw, prompt.Evidence)
	if schemaMode {
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	} else if ShouldRetryGroundedAnswerWithSchema(raw, validated) {
		request, err = GroundedAnswerSchemaRequest(request, prompt.Evidence)
		if err != nil {
			return result, ErrQueryAnswer
		}
		raw, err = provider.Generate(ctx, request)
		if err != nil {
			return result, ErrQueryAnswer
		}
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	}
	if validated.Rejected {
		result.Status = "rejected"
		result.Answer = "Модель вернула ответ без корректных ссылок на найденные фрагменты. Ответ не показан; изучите источники или уточните вопрос."
		return result, nil
	}
	// The prompt retains an ordered prefix of selected entries. A long citation
	// can name more than one excerpt, so only its exact model ref selects a hit.
	entryByRef := make(map[string]int, len(prompt.Evidence))
	for i, item := range prompt.Evidence {
		entryByRef[item.EvidenceRef] = i
	}
	for _, used := range validated.Used {
		i, ok := entryByRef[used.EvidenceRef]
		if !ok || i >= len(selected) || groundedEvidenceIdentity(prompt.Evidence[i]) != groundedEvidenceIdentity(used) {
			return result, ErrQueryAnswer
		}
		hit, e2 := queryHit(selected[i], 48000, cfg.Ingest.LowConfidence)
		if e2 != nil {
			return result, e2
		}
		hit.Text = used.Text
		hit.Truncated = used.Truncated
		result.Sources = append(result.Sources, hit)
	}
	// Keep used sources in the same order as the human [1], [2] answer markers.
	check, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer check.Rollback()
	for _, hit := range result.Sources {
		if _, e := readQueryEvidence(ctx, check, hit.ID, hit.Pin); errors.Is(e, ErrQueryStale) {
			result.Status = "stale"
			result.Answer = "Источники изменились во время подготовки ответа. Повторите вопрос."
			return result, nil
		} else if e != nil {
			return result, e
		}
	}
	if err = check.Commit(); err != nil {
		return result, err
	}
	result.Answer = validated.Answer
	result.Status = "answered"
	if validated.Insufficient {
		result.Status = "insufficient"
		result.Answer = "Недостаточно подтверждённых данных в найденных фрагментах. Уточните вопрос или выберите другие источники."
	}
	return result, ctx.Err()
}

func sourceName(e Entry) string {
	path := firstNonEmpty(e.SourcePath, e.SourceFile)
	if path == "" {
		return fmt.Sprintf("Заметка #%d", e.ID)
	}
	return filepath.Base(strings.ReplaceAll(path, `\`, "/"))
}
func queryHit(e Entry, limit int, lowConfidence float64) (QueryHit, error) {
	pin, err := documentRestoreEntryDigest(e)
	if err != nil {
		return QueryHit{}, err
	}
	citation, _ := CitationForEntry(e)
	text := truncateRunes(e.Text, limit)
	if lowConfidence <= 0 {
		lowConfidence = DefaultAnswerLowConfidence
	}
	return QueryHit{ID: e.ID, Pin: pin, Title: e.Title, Text: text, Source: sourceName(e), Document: LibraryDocumentID(firstNonEmpty(e.SourcePath, e.SourceFile), e.DocumentID), CitationID: citation, Page: e.Page, Tags: append([]string{}, e.Tags...), Important: e.Important, Score: e.Score, Truncated: text != e.Text, OCRWarning: e.ExtractionMethod == "ocr" && e.OCRConfidence < lowConfidence}, nil
}

// ReadQueryEvidenceIn binds an opened source to the exact row used in retrieval.
// A chunk ID can survive reimport; checking the numeric ID alone is insufficient.
func ReadQueryEvidenceIn(ctx context.Context, dir string, id int64, pin string) (applogResult0 QueryHit, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadQueryEvidenceIn")(&applogResult1)

	if id < 1 || !validSHA256Digest(pin) {
		return QueryHit{}, ErrQueryInvalid
	}
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
	hit, err := readQueryEvidence(ctx, tx, id, pin)
	if err != nil {
		return QueryHit{}, err
	}
	return hit, tx.Commit()
}

func readQueryEvidence(ctx context.Context, tx *sql.Tx, id int64, pin string) (QueryHit, error) {
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(warnings AS BLOB)) FROM entries WHERE id=?`, id).Scan(&size); errors.Is(err, sql.ErrNoRows) {
		return QueryHit{}, ErrQueryStale
	} else if err != nil {
		return QueryHit{}, err
	}
	if size > 64<<20 {
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
		return QueryHit{}, err
	}
	if hit.Pin != pin {
		return QueryHit{}, ErrQueryStale
	}
	return hit, nil
}
