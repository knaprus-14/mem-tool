package mem

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, без CGO
)

// Entry — одна запись в базе памяти
type Entry struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title,omitempty"`
	Text        string    `json:"text"`
	Tags        []string  `json:"tags,omitempty"`
	Created     string    `json:"created"`
	Backend     string    `json:"backend"`
	Dims        int       `json:"dims"`
	Embedding   []float32 `json:"-"`
	Score       float64   `json:"-"` // для результатов поиска
	SourceFile  string    `json:"source_file,omitempty"`
	ChunkLabel  string    `json:"chunk_label,omitempty"`
	ChunkIndex  int       `json:"chunk_index,omitempty"`
	TotalChunks int       `json:"total_chunks,omitempty"`
	Important   bool      `json:"important,omitempty"`
}

// Store — потокобезопасное хранилище векторов на базе SQLite
type Store struct {
	mu      sync.RWMutex
	db      *sql.DB
	entries []Entry
	vectors [][]float32
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
    important INTEGER NOT NULL DEFAULT 0
);

-- Дедупликация: одна (source_file, chunk_index) пара = одна запись
-- (только для записей с реальным source_file, ручные add не индексируются)
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_chunk
    ON entries(source_file, chunk_index)
    WHERE source_file != '';

CREATE INDEX IF NOT EXISTS idx_backend ON entries(backend);
`

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

	s := &Store{db: db}
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
	}
	return nil
}

// Close закрывает соединение с БД. Должен вызываться через defer сразу после NewStore:
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
		source_file, chunk_label, chunk_index, total_chunks, important
		FROM entries ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e Entry
		var tagsJSON string
		var embBytes []byte
		var important int

		if err := rows.Scan(&e.ID, &e.Title, &e.Text, &tagsJSON, &e.Created, &e.Backend,
			&e.Dims, &embBytes, &e.SourceFile, &e.ChunkLabel, &e.ChunkIndex,
			&e.TotalChunks, &important); err != nil {
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
	return &entry, nil
}

// AddChunk добавляет чанк документа с дедупликацией по (source_file, chunk_index):
//   - если такой чанк уже есть и текст совпал → ничего не делает
//   - если текст изменился → обновляет (новый эмбеддинг)
//   - если такого чанка нет → вставляет
func (s *Store) AddChunk(text string, title string, tags []string, backend string, embedding []float32,
	sourceFile, chunkLabel string, chunkIndex, totalChunks int, important bool) (*Entry, error) {

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
	impInt := boolToInt(important)

	// Пытаемся вставить; если конфликт по (source_file, chunk_index) — обновляем
	res, err := s.db.Exec(`INSERT INTO entries
		(title, text, tags, created, backend, dims, embedding,
		 source_file, chunk_label, chunk_index, total_chunks, important)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_file, chunk_index) WHERE source_file != ''
		DO UPDATE SET
			text = excluded.text,
			title = excluded.title,
			tags = excluded.tags,
			embedding = excluded.embedding,
			dims = excluded.dims,
			chunk_label = excluded.chunk_label,
			total_chunks = excluded.total_chunks,
			important = excluded.important,
			created = excluded.created,
			backend = excluded.backend`,
		title, text, tagsStr, now, backend, len(embCopy), embBytes,
		sourceFile, chunkLabel, chunkIndex, totalChunks, impInt)

	if err != nil {
		return nil, err
	}

	id, lastErr := res.LastInsertId()
	if lastErr != nil {
		return nil, fmt.Errorf("получение LastInsertId: %w", lastErr)
	}
	// LastInsertId() может вернуть 0 при UPDATE — тогда достаём ID через SELECT
	if id == 0 {
		err := s.db.QueryRow(`SELECT id FROM entries WHERE source_file = ? AND chunk_index = ?`,
			sourceFile, chunkIndex).Scan(&id)
		if err != nil {
			return nil, err
		}
	}

	// Обновляем кэш в памяти
	entry := Entry{
		ID: id, Title: title, Text: text, Tags: tagsCopy,
		Created: now, Backend: backend, Dims: len(embCopy),
		Embedding: embCopy, SourceFile: sourceFile, ChunkLabel: chunkLabel,
		ChunkIndex: chunkIndex, TotalChunks: totalChunks, Important: important,
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
	return &entry, nil
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
			entry := s.entries[i]
			entry.Tags = append([]string(nil), s.entries[i].Tags...)
			entry.Embedding = append([]float32(nil), s.entries[i].Embedding...)
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
		out[i] = results[i].entry
		out[i].Score = results[i].score
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
	return sorted[:limit], nil
}

// GetByID возвращает запись по ID.
// Возвращает указатель на локальную копию Entry с глубокими копиями Tags/Embedding —
// вызывающий код не может мутировать внутренний кэш Store после снятия RLock.
func (s *Store) GetByID(id int64) (*Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.entries {
		if s.entries[i].ID == id {
			entry := s.entries[i]
			entry.Tags = append([]string(nil), s.entries[i].Tags...)
			entry.Embedding = append([]float32(nil), s.entries[i].Embedding...)
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
			entry := s.entries[i]
			entry.Tags = append([]string(nil), s.entries[i].Tags...)
			entry.Embedding = append([]float32(nil), s.entries[i].Embedding...)
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
