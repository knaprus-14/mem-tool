package mem

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, без CGO
)

// Entry — одна запись в базе памяти
type Entry struct {
	ID               int64     `json:"id"`
	Title            string    `json:"title,omitempty"`
	Text             string    `json:"text"`
	Tags             []string  `json:"tags,omitempty"`
	Created          string    `json:"created"`
	Backend          string    `json:"backend"`
	Dims             int       `json:"dims"`
	Embedding        []float32 `json:"-"`
	Score            float64   `json:"-"` // итоговый score результата поиска
	VectorScore      float64   `json:"vector_score,omitempty"`
	LexicalScore     float64   `json:"lexical_score,omitempty"`
	FusionScore      float64   `json:"fusion_score,omitempty"`
	LexicalHit       bool      `json:"-"`
	CitationID       string    `json:"citation_id,omitempty"`
	CitationLabel    string    `json:"citation_label,omitempty"`
	SourceFile       string    `json:"source_file,omitempty"`
	ChunkLabel       string    `json:"chunk_label,omitempty"`
	ChunkIndex       int       `json:"chunk_index,omitempty"`
	TotalChunks      int       `json:"total_chunks,omitempty"`
	DocumentID       string    `json:"document_id,omitempty"`
	DocumentRevision string    `json:"document_revision,omitempty"`
	ChunkHash        string    `json:"chunk_hash,omitempty"`
	SourcePath       string    `json:"source_path,omitempty"`
	MediaType        string    `json:"media_type,omitempty"`
	Page             int       `json:"page,omitempty"`
	BlockIndex       int       `json:"block_index,omitempty"`
	BlockMarker      string    `json:"block_marker,omitempty"`
	BlockChunkIndex  int       `json:"block_chunk_index,omitempty"`
	BlockTotalChunks int       `json:"block_total_chunks,omitempty"`
	ExtractionMethod string    `json:"extraction_method,omitempty"`
	OCRConfidence    float64   `json:"ocr_confidence,omitempty"`
	Warnings         []string  `json:"warnings,omitempty"`
	Important        bool      `json:"important,omitempty"`
}

// Provenance identifies an imported document and the source block from which
// a stored chunk was derived. Page is zero when the extractor cannot know it.
type Provenance struct {
	DocumentID       string
	DocumentRevision string
	ChunkHash        string
	SourcePath       string
	MediaType        string
	Page             int
	BlockIndex       int
	BlockMarker      string
	BlockChunkIndex  int
	BlockTotalChunks int
	ExtractionMethod string
	OCRConfidence    float64
	Warnings         []string
}

// DocumentChunk is one fully embedded chunk prepared for an atomic document
// replacement. All chunks passed to ReplaceDocumentChunks are committed or
// rolled back together.
type DocumentChunk struct {
	Text        string
	Title       string
	Tags        []string
	Backend     string
	Embedding   []float32
	ChunkLabel  string
	ChunkIndex  int
	TotalChunks int
	Important   bool
	Provenance  Provenance
}

// Store — потокобезопасное хранилище векторов на базе SQLite
type Store struct {
	mu           sync.RWMutex
	db           *sql.DB
	entries      []Entry
	vectors      [][]float32
	lexicalMode  string
	lexicalDirty bool
}

// Схема БД (создаётся при инициализации)
const storeSchema = `
CREATE TABLE IF NOT EXISTS entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '[]',
    created TEXT NOT NULL,
    backend TEXT NOT NULL,
    dims INTEGER NOT NULL,
    embedding BLOB NOT NULL,
    source_file TEXT NOT NULL DEFAULT '',
    chunk_label TEXT NOT NULL DEFAULT '',
    chunk_index INTEGER NOT NULL DEFAULT 0,
    total_chunks INTEGER NOT NULL DEFAULT 0,
    document_id TEXT NOT NULL DEFAULT '',
	 document_revision TEXT NOT NULL DEFAULT '',
	 chunk_hash TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    page INTEGER NOT NULL DEFAULT 0,
    block_index INTEGER NOT NULL DEFAULT 0,
    block_marker TEXT NOT NULL DEFAULT '',
	 block_chunk_index INTEGER NOT NULL DEFAULT 0,
	 block_total_chunks INTEGER NOT NULL DEFAULT 0,
	 extraction_method TEXT NOT NULL DEFAULT '',
	 ocr_confidence REAL NOT NULL DEFAULT -1,
	 warnings TEXT NOT NULL DEFAULT '[]',
    important INTEGER NOT NULL DEFAULT 0
);

-- Дедупликация: одна (source_file, chunk_index) пара = одна запись
-- (только для записей с реальным source_file, ручные add не индексируются)
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_chunk
    ON entries(source_file, chunk_index)
    WHERE source_file != '';

CREATE INDEX IF NOT EXISTS idx_backend ON entries(backend);
`

const upsertChunkSQL = `INSERT INTO entries
		(title, text, tags, created, backend, dims, embedding,
		 source_file, chunk_label, chunk_index, total_chunks,
		 document_id, document_revision, chunk_hash,
		 source_path, media_type, page, block_index, block_marker,
		 block_chunk_index, block_total_chunks,
		 extraction_method, ocr_confidence, warnings, important)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_file, chunk_index) WHERE source_file != ''
		DO UPDATE SET
			text = excluded.text,
			title = excluded.title,
			tags = excluded.tags,
			embedding = excluded.embedding,
			dims = excluded.dims,
			chunk_label = excluded.chunk_label,
			total_chunks = excluded.total_chunks,
			document_id = excluded.document_id,
			document_revision = excluded.document_revision,
			chunk_hash = excluded.chunk_hash,
			source_path = excluded.source_path,
			media_type = excluded.media_type,
			page = excluded.page,
			block_index = excluded.block_index,
			block_marker = excluded.block_marker,
			block_chunk_index = excluded.block_chunk_index,
			block_total_chunks = excluded.block_total_chunks,
			extraction_method = excluded.extraction_method,
			ocr_confidence = excluded.ocr_confidence,
			warnings = excluded.warnings,
			important = excluded.important,
			created = excluded.created,
			backend = excluded.backend`

// newStore открывает (или создаёт) SQLite-базу в указанной директории
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dir, "store.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("открытие БД: %w", err)
	}

	// Создаём схему в транзакции — SQLite поддерживает transactional DDL.
	// Без транзакции при сбое между CREATE TABLE и CREATE INDEX база может
	// остаться в полу-применённом состоянии (таблица без индексов).
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("создание схемы: %w", err)
	}

	s := &Store{db: db, lexicalMode: initLexicalIndex(db), lexicalDirty: true}
	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema выполняет все DDL-выражения из storeSchema внутри одной транзакции.
// Если хотя бы одно выражение упадёт — все ранее применённые откатываются.
func initSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(storeSchema); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(knowledgeGraphSchema); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(corpusAnalysisRunSchema); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := migrateEntryProvenance(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		// Commit может упасть, если другая горутина уже создала схему —
		// это нормально для CREATE TABLE IF NOT EXISTS, просто проглатываем.
		if err2 := tx.Rollback(); err2 != nil {
			return err
		}
		// Fallback: если commit не прошёл по любой причине, проверим через Exec напрямую —
		// CREATE TABLE IF NOT EXISTS идемпотентен, безопасно.
		if _, err := db.Exec(storeSchema); err != nil {
			return err
		}
		if _, err := db.Exec(knowledgeGraphSchema); err != nil {
			return err
		}
		if _, err := db.Exec(corpusAnalysisRunSchema); err != nil {
			return err
		}
		if err := migrateEntryProvenanceDB(db); err != nil {
			return err
		}
	}
	return nil
}

type schemaQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	Exec(string, ...any) (sql.Result, error)
}

func migrateEntryProvenance(tx *sql.Tx) error   { return ensureEntryProvenanceColumns(tx) }
func migrateEntryProvenanceDB(db *sql.DB) error { return ensureEntryProvenanceColumns(db) }

// ensureEntryProvenanceColumns makes existing Stage 1 databases readable by
// adding nullable-by-default metadata columns in place. Existing rows retain
// their original source_file/chunk metadata unchanged.
func ensureEntryProvenanceColumns(q schemaQuerier) error {
	rows, err := q.Query(`PRAGMA table_info(entries)`)
	if err != nil {
		return fmt.Errorf("inspect entries schema: %w", err)
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read entries schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct{ name, definition string }{
		{"document_id", "TEXT NOT NULL DEFAULT ''"},
		{"document_revision", "TEXT NOT NULL DEFAULT ''"},
		{"chunk_hash", "TEXT NOT NULL DEFAULT ''"},
		{"source_path", "TEXT NOT NULL DEFAULT ''"},
		{"media_type", "TEXT NOT NULL DEFAULT ''"},
		{"page", "INTEGER NOT NULL DEFAULT 0"},
		{"block_index", "INTEGER NOT NULL DEFAULT 0"},
		{"block_marker", "TEXT NOT NULL DEFAULT ''"},
		{"block_chunk_index", "INTEGER NOT NULL DEFAULT 0"},
		{"block_total_chunks", "INTEGER NOT NULL DEFAULT 0"},
		{"extraction_method", "TEXT NOT NULL DEFAULT ''"},
		{"ocr_confidence", "REAL NOT NULL DEFAULT -1"},
		{"warnings", "TEXT NOT NULL DEFAULT '[]'"},
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE entries ADD COLUMN %s %s", column.name, column.definition)
		if _, err := q.Exec(statement); err != nil {
			return fmt.Errorf("add entries.%s: %w", column.name, err)
		}
	}
	return nil
}

// Close закрывает соединение с БД. Должен вызываться через defer сразу после NewStore:
//
//	defer store, err := mem.NewStore(...)
//	if err != nil { ... }
//	defer store.Close()
func (s *Store) Close() error {
	return s.db.Close()
}

// loadAll загружает все записи в оперативный кэш при старте.
// При повреждении embedding/tags в БД — пропускаем запись (с пометкой в stderr
// через возвращаемый wrapped error), чтобы битый row не сломал загрузку всей базы.
func (s *Store) loadAll() error {
	rows, err := s.db.Query(`SELECT id, title, text, tags, created, backend, dims, embedding,
		source_file, chunk_label, chunk_index, total_chunks,
		document_id, document_revision, chunk_hash,
		source_path, media_type, page, block_index, block_marker,
		block_chunk_index, block_total_chunks,
		extraction_method, ocr_confidence, warnings, important
		FROM entries ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e Entry
		var tagsJSON string
		var warningsJSON string
		var embBytes []byte
		var important int

		if err := rows.Scan(&e.ID, &e.Title, &e.Text, &tagsJSON, &e.Created, &e.Backend,
			&e.Dims, &embBytes, &e.SourceFile, &e.ChunkLabel, &e.ChunkIndex,
			&e.TotalChunks, &e.DocumentID, &e.DocumentRevision, &e.ChunkHash,
			&e.SourcePath, &e.MediaType, &e.Page,
			&e.BlockIndex, &e.BlockMarker, &e.BlockChunkIndex, &e.BlockTotalChunks,
			&e.ExtractionMethod, &e.OCRConfidence,
			&warningsJSON, &important); err != nil {
			return err
		}

		tags, err := tagsFromJSON(tagsJSON)
		if err != nil {
			return fmt.Errorf("запись #%d: %w", e.ID, err)
		}
		embedding, err := bytesToFloats(embBytes)
		if err != nil {
			return fmt.Errorf("запись #%d: %w", e.ID, err)
		}
		e.Tags = tags
		if err := json.Unmarshal([]byte(warningsJSON), &e.Warnings); err != nil {
			return fmt.Errorf("запись #%d: повреждены warnings: %w", e.ID, err)
		}
		e.Embedding = embedding
		e.Important = important != 0

		s.entries = append(s.entries, e)
		s.vectors = append(s.vectors, e.Embedding)
	}
	return rows.Err()
}

// === Сериализация ===
//
// Эти хелперы возвращают ошибки явно — раньше ошибки binary.Read/Write и
// json.Marshal/Unmarshal тихо проглатывались. Повреждённый BLOB (например,
// обрезанный из-за крэша при записи) приводил к молчаливому nil-слайсу,
// и запись «исчезала» из поиска без всякого уведомления.

func floatsToBytes(v []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, v); err != nil {
		return nil, fmt.Errorf("сериализация float32: %w", err)
	}
	return buf.Bytes(), nil
}

func bytesToFloats(b []byte) ([]float32, error) {
	n := len(b) / 4
	if n == 0 {
		return nil, nil
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("повреждённый BLOB: длина %d не кратна 4 (float32 = 4 байта)", len(b))
	}
	v := make([]float32, n)
	if err := binary.Read(bytes.NewReader(b), binary.LittleEndian, v); err != nil {
		return nil, fmt.Errorf("десериализация float32: %w", err)
	}
	return v, nil
}

// FloatsToBytes — публичная обёртка для переиспользования в других пакетах
// (например, pkg/fileindex). Семантика идентична floatsToBytes.
func FloatsToBytes(v []float32) ([]byte, error) { return floatsToBytes(v) }

// BytesToFloats — публичная обёртка для переиспользования в других пакетах.
func BytesToFloats(b []byte) ([]float32, error) { return bytesToFloats(b) }

func tagsToJSON(tags []string) (string, error) {
	if tags == nil {
		return "[]", nil
	}
	data, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("сериализация тегов: %w", err)
	}
	return string(data), nil
}

func tagsFromJSON(s string) ([]string, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil, fmt.Errorf("десериализация тегов: %w", err)
	}
	return tags, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func cloneEntry(entry Entry) Entry {
	entry.Tags = append([]string(nil), entry.Tags...)
	entry.Embedding = append([]float32(nil), entry.Embedding...)
	entry.Warnings = append([]string(nil), entry.Warnings...)
	return entry
}

// === CRUD ===

// Add добавляет ручную запись (без source_file, без дедупликации)
func (s *Store) Add(text string, title string, tags []string, backend string, embedding []float32, important bool) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	// Копируем tags и embedding — caller может мутировать свои слайсы после возврата,
	// а Store хранит свои копии в кэше (s.entries/s.vectors).
	tagsCopy := append([]string(nil), tags...)
	embCopy := append([]float32(nil), embedding...)

	tagsStr, err := tagsToJSON(tagsCopy)
	if err != nil {
		return nil, fmt.Errorf("сериализация тегов: %w", err)
	}
	embBytes, err := floatsToBytes(embCopy)
	if err != nil {
		return nil, fmt.Errorf("сериализация embedding: %w", err)
	}

	res, err := s.db.Exec(`INSERT INTO entries
		(title, text, tags, created, backend, dims, embedding, important)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		title, text, tagsStr, now, backend, len(embCopy),
		embBytes, boolToInt(important))
	if err != nil {
		return nil, err
	}
	id, lastErr := res.LastInsertId()
	if lastErr != nil {
		return nil, fmt.Errorf("получение LastInsertId: %w", lastErr)
	}

	entry := Entry{
		ID: id, Title: title, Text: text, Tags: tagsCopy,
		Created: now, Backend: backend, Dims: len(embCopy),
		Embedding: embCopy, Important: important,
	}
	s.entries = append(s.entries, entry)
	s.vectors = append(s.vectors, embCopy)
	s.lexicalDirty = true
	result := cloneEntry(entry)
	return &result, nil
}

// AddChunk добавляет чанк документа с дедупликацией по (source_file, chunk_index):
//   - если такой чанк уже есть и текст совпал → ничего не делает
//   - если текст изменился → обновляет (новый эмбеддинг)
//   - если такого чанка нет → вставляет
func (s *Store) AddChunk(text string, title string, tags []string, backend string, embedding []float32,
	sourceFile, chunkLabel string, chunkIndex, totalChunks int, important bool) (*Entry, error) {
	return s.addChunk(text, title, tags, backend, embedding, sourceFile, chunkLabel,
		chunkIndex, totalChunks, important, Provenance{SourcePath: sourceFile, OCRConfidence: -1})
}

// AddDocumentChunk stores an imported chunk together with page/block
// provenance. SourcePath is also the canonical source identity used for safe
// repeat-import UPSERT behavior.
func (s *Store) AddDocumentChunk(text string, title string, tags []string, backend string, embedding []float32,
	chunkLabel string, chunkIndex, totalChunks int, important bool, provenance Provenance) (*Entry, error) {
	if provenance.SourcePath == "" {
		return nil, fmt.Errorf("document chunk provenance has an empty source path")
	}
	chunk := DocumentChunk{
		Text: text, Backend: backend, Embedding: embedding,
		ChunkIndex: chunkIndex, TotalChunks: totalChunks, Provenance: provenance,
	}
	if err := validateDocumentChunk(chunk, provenance.SourcePath, chunkIndex, totalChunks); err != nil {
		return nil, err
	}
	return s.addChunk(text, title, tags, backend, embedding, provenance.SourcePath,
		chunkLabel, chunkIndex, totalChunks, important, provenance)
}

func (s *Store) addChunk(text string, title string, tags []string, backend string, embedding []float32,
	sourceFile, chunkLabel string, chunkIndex, totalChunks int, important bool, provenance Provenance) (*Entry, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	// Копируем tags и embedding — caller может мутировать свои слайсы после возврата,
	// а Store хранит свои копии в кэше (s.entries/s.vectors).
	tagsCopy := append([]string(nil), tags...)
	embCopy := append([]float32(nil), embedding...)
	embBytes, err := floatsToBytes(embCopy)
	if err != nil {
		return nil, fmt.Errorf("сериализация embedding: %w", err)
	}
	tagsStr, err := tagsToJSON(tagsCopy)
	if err != nil {
		return nil, fmt.Errorf("сериализация тегов: %w", err)
	}
	warningsCopy := append([]string(nil), provenance.Warnings...)
	warningsBytes, err := json.Marshal(warningsCopy)
	if err != nil {
		return nil, fmt.Errorf("сериализация warnings: %w", err)
	}
	impInt := boolToInt(important)

	// Пытаемся вставить; если конфликт по (source_file, chunk_index) — обновляем
	_, err = s.db.Exec(upsertChunkSQL,
		title, text, tagsStr, now, backend, len(embCopy), embBytes,
		sourceFile, chunkLabel, chunkIndex, totalChunks,
		provenance.DocumentID, provenance.DocumentRevision, provenance.ChunkHash,
		provenance.SourcePath, provenance.MediaType,
		provenance.Page, provenance.BlockIndex, provenance.BlockMarker,
		provenance.BlockChunkIndex, provenance.BlockTotalChunks,
		provenance.ExtractionMethod, provenance.OCRConfidence, string(warningsBytes), impInt)

	if err != nil {
		return nil, err
	}

	// LastInsertId ненадёжен для UPSERT: при UPDATE SQLite может вернуть ID
	// предыдущей вставки в соединении. Всегда читаем фактическую строку по
	// уникальному ключу, иначе in-memory cache получает дубликат.
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM entries WHERE source_file = ? AND chunk_index = ?`,
		sourceFile, chunkIndex).Scan(&id); err != nil {
		return nil, err
	}

	// Обновляем кэш в памяти
	entry := Entry{
		ID: id, Title: title, Text: text, Tags: tagsCopy,
		Created: now, Backend: backend, Dims: len(embCopy),
		Embedding: embCopy, SourceFile: sourceFile, ChunkLabel: chunkLabel,
		ChunkIndex: chunkIndex, TotalChunks: totalChunks,
		DocumentID: provenance.DocumentID, DocumentRevision: provenance.DocumentRevision,
		ChunkHash: provenance.ChunkHash, SourcePath: provenance.SourcePath,
		MediaType: provenance.MediaType, Page: provenance.Page,
		BlockIndex: provenance.BlockIndex, BlockMarker: provenance.BlockMarker,
		BlockChunkIndex: provenance.BlockChunkIndex, BlockTotalChunks: provenance.BlockTotalChunks,
		ExtractionMethod: provenance.ExtractionMethod, OCRConfidence: provenance.OCRConfidence,
		Warnings:  warningsCopy,
		Important: important,
	}
	cacheUpdated := false
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i] = entry
			s.vectors[i] = embCopy
			cacheUpdated = true
			break
		}
	}
	if !cacheUpdated {
		s.entries = append(s.entries, entry)
		s.vectors = append(s.vectors, embCopy)
	}
	s.lexicalDirty = true
	result := cloneEntry(entry)
	return &result, nil
}

type preparedDocumentChunk struct {
	entry         Entry
	tagsJSON      string
	embeddingBLOB []byte
	warningsJSON  string
}

// ChunkContentHash identifies the exact text embedded for one stored chunk.
// It is separate from the source anchor so an anchor can remain stable while
// its evidence text changes across document revisions.
func ChunkContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func isSHA256ContentHash(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validateDocumentChunk(chunk DocumentChunk, sourcePath string, position, total int) error {
	if strings.TrimSpace(sourcePath) == "" {
		return fmt.Errorf("document replacement has an empty source path")
	}
	if total <= 0 || position < 0 || position >= total {
		return fmt.Errorf("document chunk has invalid global index/total %d/%d", position, total)
	}
	if chunk.ChunkIndex != position || chunk.TotalChunks != total {
		return fmt.Errorf("document chunk %d has inconsistent index/total %d/%d", position, chunk.ChunkIndex, chunk.TotalChunks)
	}
	if chunk.Provenance.SourcePath != sourcePath {
		return fmt.Errorf("document chunk %d source path does not match replacement identity", position)
	}
	if strings.TrimSpace(chunk.Text) == "" {
		return fmt.Errorf("document chunk %d has empty text", position)
	}
	if strings.TrimSpace(chunk.Backend) == "" {
		return fmt.Errorf("document chunk %d has an empty embedding backend", position)
	}
	if len(chunk.Embedding) == 0 {
		return fmt.Errorf("document chunk %d has an empty embedding", position)
	}
	for dimension, value := range chunk.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("document chunk %d embedding dimension %d is not finite", position, dimension)
		}
	}
	p := chunk.Provenance
	if p.Page < 0 || p.BlockIndex < 0 || p.BlockChunkIndex < 0 || p.BlockTotalChunks < 0 {
		return fmt.Errorf("document chunk %d has negative provenance coordinates", position)
	}
	if p.BlockTotalChunks == 0 && p.BlockChunkIndex != 0 {
		return fmt.Errorf("document chunk %d has a block-local index without a block total", position)
	}
	if p.BlockTotalChunks > 0 && p.BlockChunkIndex >= p.BlockTotalChunks {
		return fmt.Errorf("document chunk %d has inconsistent block-local index/total %d/%d", position, p.BlockChunkIndex, p.BlockTotalChunks)
	}
	if math.IsNaN(p.OCRConfidence) || math.IsInf(p.OCRConfidence, 0) || p.OCRConfidence < -1 || p.OCRConfidence > 100 {
		return fmt.Errorf("document chunk %d has invalid OCR confidence %v", position, p.OCRConfidence)
	}
	if strings.TrimSpace(p.DocumentID) == "" {
		if p.DocumentID != "" || p.DocumentRevision != "" || p.ChunkHash != "" || p.MediaType != "" || p.Page != 0 || p.BlockIndex != 0 || p.BlockMarker != "" ||
			p.ExtractionMethod != "" || p.OCRConfidence != -1 || p.BlockTotalChunks != 0 || len(p.Warnings) != 0 {
			return fmt.Errorf("document chunk %d has partial provenance without a document identity", position)
		}
		return nil
	}
	if strings.TrimSpace(p.MediaType) == "" {
		return fmt.Errorf("document chunk %d has an empty media type", position)
	}
	if !isSHA256ContentHash(p.DocumentRevision) {
		return fmt.Errorf("document chunk %d has an invalid document content revision", position)
	}
	if !isSHA256ContentHash(p.ChunkHash) {
		return fmt.Errorf("document chunk %d has an invalid chunk content hash", position)
	}
	if expected := ChunkContentHash(chunk.Text); p.ChunkHash != expected {
		return fmt.Errorf("document chunk %d content hash does not match its text", position)
	}
	if p.BlockTotalChunks == 0 {
		return fmt.Errorf("document chunk %d has no block-local chunk coordinates", position)
	}
	if p.ExtractionMethod != "text" && p.ExtractionMethod != "ocr" {
		return fmt.Errorf("document chunk %d has unsupported extraction method %q", position, p.ExtractionMethod)
	}
	if p.ExtractionMethod != "ocr" && p.OCRConfidence != -1 {
		return fmt.Errorf("document chunk %d has OCR confidence for %q extraction", position, p.ExtractionMethod)
	}
	return nil
}

// ReplaceDocumentChunks atomically upserts a complete imported document and
// removes any stale tail left by an earlier, longer version. The in-memory
// cache changes only after the SQLite transaction commits.
func (s *Store) ReplaceDocumentChunks(sourcePath string, chunks []DocumentChunk) error {
	if sourcePath == "" {
		return fmt.Errorf("document replacement has an empty source path")
	}
	if len(chunks) == 0 {
		return fmt.Errorf("document replacement has no chunks")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	prepared := make([]preparedDocumentChunk, len(chunks))
	expectedDocumentID := chunks[0].Provenance.DocumentID
	expectedDocumentRevision := chunks[0].Provenance.DocumentRevision
	expectedMediaType := chunks[0].Provenance.MediaType
	expectedDimensions := len(chunks[0].Embedding)
	previousBlock := -1
	previousPage := 0
	blockTotal := 0
	for i, chunk := range chunks {
		if err := validateDocumentChunk(chunk, sourcePath, i, len(chunks)); err != nil {
			return err
		}
		if chunk.Provenance.DocumentID != expectedDocumentID {
			return fmt.Errorf("document chunk %d document identity does not match earlier chunks", i)
		}
		if chunk.Provenance.DocumentRevision != expectedDocumentRevision {
			return fmt.Errorf("document chunk %d content revision does not match earlier chunks", i)
		}
		if chunk.Provenance.MediaType != expectedMediaType {
			return fmt.Errorf("document chunk %d media type does not match earlier chunks", i)
		}
		if len(chunk.Embedding) != expectedDimensions {
			return fmt.Errorf("document chunk %d embedding dimensions %d do not match document dimensions %d", i, len(chunk.Embedding), expectedDimensions)
		}
		if expectedDocumentID != "" {
			p := chunk.Provenance
			if p.BlockIndex == previousBlock {
				previous := chunks[i-1].Provenance
				if p.Page != previousPage || p.BlockTotalChunks != blockTotal || p.BlockChunkIndex == 0 ||
					p.BlockMarker != previous.BlockMarker || p.ExtractionMethod != previous.ExtractionMethod ||
					p.OCRConfidence != previous.OCRConfidence {
					return fmt.Errorf("document chunk %d has inconsistent metadata within block %d", i, p.BlockIndex)
				}
				if p.BlockChunkIndex != previous.BlockChunkIndex+1 {
					return fmt.Errorf("document chunk %d has non-contiguous block-local index %d", i, p.BlockChunkIndex)
				}
			} else {
				if previousBlock >= 0 && chunks[i-1].Provenance.BlockChunkIndex+1 != blockTotal {
					return fmt.Errorf("source block %d ended after %d/%d chunks", previousBlock, chunks[i-1].Provenance.BlockChunkIndex+1, blockTotal)
				}
				if p.BlockIndex != previousBlock+1 || p.BlockChunkIndex != 0 {
					return fmt.Errorf("document chunk %d does not start the next contiguous source block", i)
				}
				if p.Page > 0 && previousPage > p.Page {
					return fmt.Errorf("document chunk %d page %d precedes earlier page %d", i, p.Page, previousPage)
				}
				previousBlock, previousPage, blockTotal = p.BlockIndex, p.Page, p.BlockTotalChunks
			}
		}
		tagsCopy := append([]string(nil), chunk.Tags...)
		embeddingCopy := append([]float32(nil), chunk.Embedding...)
		warningsCopy := append([]string{}, chunk.Provenance.Warnings...)
		tagsJSON, err := tagsToJSON(tagsCopy)
		if err != nil {
			return err
		}
		embeddingBLOB, err := floatsToBytes(embeddingCopy)
		if err != nil {
			return err
		}
		warningsBytes, err := json.Marshal(warningsCopy)
		if err != nil {
			return fmt.Errorf("serialize document chunk %d warnings: %w", i, err)
		}
		prepared[i] = preparedDocumentChunk{
			entry: Entry{
				Title: chunk.Title, Text: chunk.Text, Tags: tagsCopy, Created: now,
				Backend: chunk.Backend, Dims: len(embeddingCopy), Embedding: embeddingCopy,
				SourceFile: sourcePath, ChunkLabel: chunk.ChunkLabel,
				ChunkIndex: chunk.ChunkIndex, TotalChunks: chunk.TotalChunks,
				DocumentID:       chunk.Provenance.DocumentID,
				DocumentRevision: chunk.Provenance.DocumentRevision,
				ChunkHash:        chunk.Provenance.ChunkHash, SourcePath: chunk.Provenance.SourcePath,
				MediaType: chunk.Provenance.MediaType, Page: chunk.Provenance.Page,
				BlockIndex: chunk.Provenance.BlockIndex, BlockMarker: chunk.Provenance.BlockMarker,
				BlockChunkIndex:  chunk.Provenance.BlockChunkIndex,
				BlockTotalChunks: chunk.Provenance.BlockTotalChunks,
				ExtractionMethod: chunk.Provenance.ExtractionMethod,
				OCRConfidence:    chunk.Provenance.OCRConfidence, Warnings: warningsCopy,
				Important: chunk.Important,
			},
			tagsJSON: tagsJSON, embeddingBLOB: embeddingBLOB, warningsJSON: string(warningsBytes),
		}
	}
	if expectedDocumentID != "" && chunks[len(chunks)-1].Provenance.BlockChunkIndex+1 != blockTotal {
		return fmt.Errorf("source block %d ended after %d/%d chunks", previousBlock, chunks[len(chunks)-1].Provenance.BlockChunkIndex+1, blockTotal)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin document replacement: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return cause
	}

	for i := range prepared {
		p := &prepared[i]
		e := &p.entry
		_, err = tx.Exec(upsertChunkSQL,
			e.Title, e.Text, p.tagsJSON, e.Created, e.Backend, e.Dims, p.embeddingBLOB,
			e.SourceFile, e.ChunkLabel, e.ChunkIndex, e.TotalChunks,
			e.DocumentID, e.DocumentRevision, e.ChunkHash,
			e.SourcePath, e.MediaType, e.Page, e.BlockIndex, e.BlockMarker,
			e.BlockChunkIndex, e.BlockTotalChunks,
			e.ExtractionMethod, e.OCRConfidence, p.warningsJSON, boolToInt(e.Important))
		if err != nil {
			return rollback(fmt.Errorf("write document chunk %d/%d: %w", i+1, len(prepared), err))
		}
		if err := tx.QueryRow(`SELECT id FROM entries WHERE source_file = ? AND chunk_index = ?`,
			sourcePath, i).Scan(&e.ID); err != nil {
			return rollback(fmt.Errorf("read document chunk %d id: %w", i+1, err))
		}
	}
	if _, err := tx.Exec(`DELETE FROM entries WHERE source_file = ? AND chunk_index >= ?`, sourcePath, len(prepared)); err != nil {
		return rollback(fmt.Errorf("prune stale document chunks: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit document replacement: %w", err)
	}

	entries := s.entries[:0]
	vectors := s.vectors[:0]
	for i := range s.entries {
		if s.entries[i].SourceFile == sourcePath {
			continue
		}
		entries = append(entries, s.entries[i])
		vectors = append(vectors, s.vectors[i])
	}
	for i := range prepared {
		entries = append(entries, prepared[i].entry)
		vectors = append(vectors, prepared[i].entry.Embedding)
	}
	s.entries = entries
	s.vectors = vectors
	s.lexicalDirty = true
	return nil
}

// PruneSourceChunks удаляет устаревшие хвостовые чанки документа после
// успешной повторной индексации более короткой версии. Ручные записи и чанки
// других источников не затрагиваются.
func (s *Store) PruneSourceChunks(sourceFile string, firstStaleIndex int) (int64, error) {
	if sourceFile == "" {
		return 0, fmt.Errorf("удаление хвостовых чанков: пустой source_file")
	}
	if firstStaleIndex < 0 {
		return 0, fmt.Errorf("удаление хвостовых чанков: отрицательный индекс %d", firstStaleIndex)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`DELETE FROM entries WHERE source_file = ? AND chunk_index >= ?`, sourceFile, firstStaleIndex)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("RowsAffected: %w", err)
	}
	if deleted == 0 {
		return 0, nil
	}

	entries := s.entries[:0]
	vectors := s.vectors[:0]
	for i := range s.entries {
		if s.entries[i].SourceFile == sourceFile && s.entries[i].ChunkIndex >= firstStaleIndex {
			continue
		}
		entries = append(entries, s.entries[i])
		vectors = append(vectors, s.vectors[i])
	}
	s.entries = entries
	s.vectors = vectors
	s.lexicalDirty = true
	return deleted, nil
}

// DeleteById удаляет запись по ID (hard delete)
func (s *Store) DeleteById(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`DELETE FROM entries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("RowsAffected: %w", rowsErr)
	}
	if n == 0 {
		return fmt.Errorf("запись #%d не найдена", id)
	}
	s.lexicalDirty = true

	// Удаляем из кэша
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			s.vectors = append(s.vectors[:i], s.vectors[i+1:]...)
			break
		}
	}
	return nil
}

// UpdateById обновляет текст/заголовок/теги/эмбеддинг записи.
// Если записи с таким id нет — возвращает ошибку (ранее молча возвращался nil,
// и пользователь не понимал, почему "правка" не сработала).
func (s *Store) UpdateById(id int64, text string, title string, tags []string, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryIndex := -1
	for i := range s.entries {
		if s.entries[i].ID == id {
			entryIndex = i
			break
		}
	}
	if entryIndex < 0 {
		return fmt.Errorf("запись #%d не найдена", id)
	}
	if s.entries[entryIndex].DocumentID != "" && text != s.entries[entryIndex].Text {
		return fmt.Errorf("запись #%d является source-anchored chunk; измените исходный документ и повторите mem import", id)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Копируем tags — caller может мутировать свой слайс после возврата.
	tagsCopy := append([]string(nil), tags...)
	tagsStr, err := tagsToJSON(tagsCopy)
	if err != nil {
		return fmt.Errorf("сериализация тегов: %w", err)
	}

	if embedding != nil {
		embCopy := append([]float32(nil), embedding...)
		embBytes, err := floatsToBytes(embCopy)
		if err != nil {
			return fmt.Errorf("сериализация embedding: %w", err)
		}
		res, err := s.db.Exec(`UPDATE entries SET text=?, title=?, tags=?, embedding=?, dims=?, created=? WHERE id=?`,
			text, title, tagsStr, embBytes, len(embCopy), now, id)
		if err != nil {
			return err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return fmt.Errorf("RowsAffected: %w", rowsErr)
		} else if n == 0 {
			return fmt.Errorf("запись #%d не найдена", id)
		}
		s.lexicalDirty = true
		for i := range s.entries {
			if s.entries[i].ID == id {
				s.entries[i].Text = text
				s.entries[i].Title = title
				s.entries[i].Tags = tagsCopy
				s.entries[i].Created = now
				s.entries[i].Embedding = embCopy
				s.entries[i].Dims = len(embCopy)
				s.vectors[i] = embCopy
				return nil
			}
		}
		return fmt.Errorf("запись #%d не найдена", id)
	} else {
		res, err := s.db.Exec(`UPDATE entries SET text=?, title=?, tags=?, created=? WHERE id=?`,
			text, title, tagsStr, now, id)
		if err != nil {
			return err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return fmt.Errorf("RowsAffected: %w", rowsErr)
		} else if n == 0 {
			return fmt.Errorf("запись #%d не найдена", id)
		}
		s.lexicalDirty = true
		for i := range s.entries {
			if s.entries[i].ID == id {
				s.entries[i].Text = text
				s.entries[i].Title = title
				s.entries[i].Tags = tagsCopy
				s.entries[i].Created = now
				return nil
			}
		}
		return fmt.Errorf("запись #%d не найдена", id)
	}
}

// ToggleImportant переключает флаг важности.
// Возвращает Entry-копию (не указатель на внутренний кэш) — иначе вызывающий код
// может читать/менять состояние Store конкурентно после снятия локального Lock.
func (s *Store) ToggleImportant(id int64) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var current int
	err := s.db.QueryRow(`SELECT important FROM entries WHERE id = ?`, id).Scan(&current)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("запись #%d не найдена", id)
	}
	if err != nil {
		return nil, err
	}
	newVal := 1 - current
	_, err = s.db.Exec(`UPDATE entries SET important = ? WHERE id = ?`, newVal, id)
	if err != nil {
		return nil, err
	}

	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Important = newVal != 0
			entry := cloneEntry(s.entries[i])
			return &entry, nil
		}
	}
	return nil, fmt.Errorf("запись #%d не найдена", id)
}

// Search ищет ближайшие по смыслу записи (cosine similarity в Go)
func (s *Store) Search(queryVector []float32, backend string, limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	type scored struct {
		entry Entry
		score float64
	}

	var results []scored
	for i := range s.entries {
		entry := s.entries[i]
		if entry.Backend != backend {
			continue
		}
		if entry.Embedding == nil || len(entry.Embedding) != len(queryVector) {
			continue
		}
		score := CosineSimilarity(queryVector, s.vectors[i])
		results = append(results, scored{entry: entry, score: score})
	}

	if len(results) == 0 {
		return nil, nil
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]Entry, limit)
	for i := 0; i < limit; i++ {
		out[i] = cloneEntry(results[i].entry)
		out[i].Score = results[i].score
		out[i].VectorScore = normalizeCosine(results[i].score)
		out[i].FusionScore = out[i].VectorScore
		annotateCitation(&out[i])
	}
	return out, nil
}

// Recent возвращает последние N записей, отсортированных по created DESC.
// Tie-breaker — id DESC (на случай одинаковых таймстампов).
// Раньше возвращался хвост s.entries (отсортирован при loadAll по id ASC)
// и разворачивался — порядок определялся id, а не фактическим временем создания.
func (s *Store) Recent(limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	n := len(s.entries)
	if n == 0 {
		return nil, nil
	}

	// Копируем, чтобы не сортировать внутренний кэш Store (мы держим RLock).
	sorted := make([]Entry, n)
	copy(sorted, s.entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Created != sorted[j].Created {
			return sorted[i].Created > sorted[j].Created
		}
		return sorted[i].ID > sorted[j].ID
	})

	if limit > n {
		limit = n
	}
	out := make([]Entry, limit)
	for i := 0; i < limit; i++ {
		out[i] = cloneEntry(sorted[i])
	}
	return out, nil
}

// GetByID возвращает запись по ID.
// Возвращает указатель на локальную копию Entry с глубокими копиями Tags/Embedding —
// вызывающий код не может мутировать внутренний кэш Store после снятия RLock.
func (s *Store) GetByID(id int64) (*Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.entries {
		if s.entries[i].ID == id {
			entry := cloneEntry(s.entries[i])
			return &entry, nil
		}
	}
	return nil, fmt.Errorf("запись #%d не найдена", id)
}

// GetBySourceFile возвращает все записи (чанки) с указанным SourceFile.
// Возвращает []Entry (не []*Entry) — каждая запись это копия с глубокими копиями
// Tags/Embedding, чтобы вызывающий код не мог мутировать внутренний кэш Store.
func (s *Store) GetBySourceFile(sourceFile string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Entry
	for i := range s.entries {
		if s.entries[i].SourceFile == sourceFile {
			entry := cloneEntry(s.entries[i])
			out = append(out, entry)
		}
	}
	return out
}

// SourceFiles возвращает список уникальных файлов-источников с количеством чанков
func (s *Store) SourceFiles() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]int)
	for _, e := range s.entries {
		if e.SourceFile != "" {
			result[e.SourceFile]++
		}
	}
	return result
}

// Stats возвращает статистику
func (s *Store) Stats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	backendCount := make(map[string]int)
	sourceCount := 0
	for _, e := range s.entries {
		backendCount[e.Backend]++
		if e.SourceFile != "" {
			sourceCount++
		}
	}

	return map[string]interface{}{
		"total_entries":  len(s.entries),
		"by_backend":     backendCount,
		"doc_chunks":     sourceCount,
		"store_location": ".mem/store.db",
	}
}

// cosineSimilarity считает косинусное сходство между двумя векторами
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
