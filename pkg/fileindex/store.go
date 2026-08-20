package fileindex

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

// FileEntry — одна запись в каталоге файлов.
// Path хранится ОТНОСИТЕЛЬНО scan-root (для портабельности БД).
type FileEntry struct {
	ID         int64
	Path       string // relative to scan-root
	Name       string // basename
	Ext        string // ".fb2", ".pdf", ...
	Size       int64
	Mtime      int64  // unix seconds
	ParentDir  string // последние 2 уровня parent_dir_chain
	Hash       string // xxhash первых 64 KB (опционально)
	Annotation string // извлечённый текст аннотации
	Backend    string // ollama / polza
	Embedding  []float32
	Dims       int
	Tags       []string
	Stale      bool
	CreatedAt  string
	UpdatedAt  string
	LastSeenAt string
}

// Scored — обёртка для результатов Search с score.
type Scored struct {
	Entry FileEntry
	Score float64
}

// Stats — статистика базы fileindex.
type Stats struct {
	Total     int
	NotStale  int
	Stale     int
	ByExt     map[string]int
	StorePath string
}

// Store — потокобезопасное хранилище файлов на базе SQLite + in-memory кэш.
type Store struct {
	mu      sync.RWMutex
	db      *sql.DB
	files   []FileEntry
	vectors [][]float32
}

// Схема БД. UNIQUE INDEX на path гарантирует UPSERT через ON CONFLICT.
const storeSchema = `
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL,
    name TEXT NOT NULL,
    ext TEXT,
    size INTEGER,
    mtime INTEGER,
    parent_dir TEXT,
    hash TEXT,
    annotation TEXT,
    backend TEXT NOT NULL,
    dims INTEGER NOT NULL,
    embedding BLOB NOT NULL,
    tags TEXT NOT NULL DEFAULT '[]',
    stale INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_path ON files(path);
CREATE INDEX IF NOT EXISTS idx_files_stale ON files(stale);
CREATE INDEX IF NOT EXISTS idx_files_name ON files(name);
`

// NewStore открывает (или создаёт) SQLite-базу в указанной директории (.fileindex/).
// Загружает все записи в in-memory кэш для быстрого Search.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "store.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("открытие БД: %w", err)
	}
	if _, err := db.Exec(storeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("создание схемы: %w", err)
	}
	s := &Store{db: db}
	if err := s.loadAll(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close закрывает соединение с БД.
func (s *Store) Close() error {
	return s.db.Close()
}

// loadAll загружает все записи в in-memory кэш.
func (s *Store) loadAll() error {
	rows, err := s.db.Query(`SELECT id, path, name, ext, size, mtime, parent_dir, hash,
		annotation, backend, dims, embedding, tags, stale, created_at, updated_at, last_seen_at
		FROM files ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e FileEntry
		var tagsJSON string
		var embBytes []byte
		var stale int

		if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Ext, &e.Size, &e.Mtime,
			&e.ParentDir, &e.Hash, &e.Annotation, &e.Backend, &e.Dims, &embBytes,
			&tagsJSON, &stale, &e.CreatedAt, &e.UpdatedAt, &e.LastSeenAt); err != nil {
			return err
		}

		tags, err := tagsFromJSON(tagsJSON)
		if err != nil {
			return fmt.Errorf("запись #%d (tags): %w", e.ID, err)
		}
		embedding, err := mem.BytesToFloats(embBytes)
		if err != nil {
			return fmt.Errorf("запись #%d (embedding): %w", e.ID, err)
		}

		e.Tags = tags
		e.Embedding = embedding
		e.Stale = stale != 0

		s.files = append(s.files, e)
		s.vectors = append(s.vectors, e.Embedding)
	}
	return rows.Err()
}

// Upsert вставляет или обновляет запись по path.
// Если path уже существует — обновляет все поля и сохраняет старый created_at.
// Также обновляет in-memory кэш.
func (s *Store) Upsert(entry *FileEntry) error {
	if entry.Path == "" {
		return fmt.Errorf("Upsert: пустой path")
	}
	if entry.Backend == "" {
		return fmt.Errorf("Upsert: пустой Backend")
	}
	if entry.LastSeenAt == "" {
		entry.LastSeenAt = time.Now().UTC().Format(time.RFC3339)
	}

	tagsJSON, err := tagsToJSON(entry.Tags)
	if err != nil {
		return err
	}
	embBytes := []byte{}
	if len(entry.Embedding) > 0 {
		embBytes, err = mem.FloatsToBytes(entry.Embedding)
		if err != nil {
			return err
		}
	}
	entry.Dims = len(entry.Embedding)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Проверяем, существует ли запись с таким path.
	var existingID int64
	var existingCreated string
	err = s.db.QueryRow(`SELECT id, created_at FROM files WHERE path = ?`, entry.Path).
		Scan(&existingID, &existingCreated)

	if err == sql.ErrNoRows {
		// INSERT.
		entry.CreatedAt = entry.LastSeenAt
		entry.UpdatedAt = entry.LastSeenAt
		res, err := s.db.Exec(`INSERT INTO files
			(path, name, ext, size, mtime, parent_dir, hash, annotation, backend, dims,
			 embedding, tags, stale, created_at, updated_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			entry.Path, entry.Name, entry.Ext, entry.Size, entry.Mtime,
			entry.ParentDir, entry.Hash, entry.Annotation, entry.Backend, entry.Dims,
			embBytes, tagsJSON, entry.CreatedAt, entry.UpdatedAt, entry.LastSeenAt)
		if err != nil {
			return fmt.Errorf("INSERT: %w", err)
		}
		id, _ := res.LastInsertId()
		entry.ID = id

		// Cache.
		cp := cloneFileEntry(*entry)
		s.files = append(s.files, cp)
		s.vectors = append(s.vectors, cp.Embedding)
		return nil
	}
	if err != nil {
		return fmt.Errorf("Upsert SELECT: %w", err)
	}

	// UPDATE — сохраняем старый created_at.
	entry.CreatedAt = existingCreated
	entry.UpdatedAt = entry.LastSeenAt
	entry.ID = existingID
	_, err = s.db.Exec(`UPDATE files SET
		name = ?, ext = ?, size = ?, mtime = ?, parent_dir = ?, hash = ?, annotation = ?,
		backend = ?, dims = ?, embedding = ?, tags = ?, stale = 0,
		updated_at = ?, last_seen_at = ?
		WHERE id = ?`,
		entry.Name, entry.Ext, entry.Size, entry.Mtime, entry.ParentDir, entry.Hash,
		entry.Annotation, entry.Backend, entry.Dims, embBytes, tagsJSON,
		entry.UpdatedAt, entry.LastSeenAt, existingID)
	if err != nil {
		return fmt.Errorf("UPDATE: %w", err)
	}

	// Cache: обновляем на месте.
	for i := range s.files {
		if s.files[i].ID == existingID {
			cp := cloneFileEntry(*entry)
			s.files[i] = cp
			s.vectors[i] = cp.Embedding
			return nil
		}
	}
	// Если не нашли в кэше (странно) — добавляем.
	cp := cloneFileEntry(*entry)
	s.files = append(s.files, cp)
	s.vectors = append(s.vectors, cp.Embedding)
	return nil
}

// Get возвращает запись по ID (глубокая копия Embedding/Tags).
func (s *Store) Get(id int64) (*FileEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.files {
		if s.files[i].ID == id {
			cp := cloneFileEntry(s.files[i])
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("запись #%d не найдена", id)
}

// GetByPath возвращает запись по относительному path. ok=false если нет.
func (s *Store) GetByPath(relPath string) (FileEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	norm := filepath.ToSlash(relPath)
	for i := range s.files {
		if filepath.ToSlash(s.files[i].Path) == norm {
			return cloneFileEntry(s.files[i]), true
		}
	}
	return FileEntry{}, false
}

// List возвращает последние N записей (по last_seen_at DESC, id DESC как tie-breaker).
func (s *Store) List(limit int, includeStale bool) ([]FileEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	all := make([]FileEntry, 0, len(s.files))
	for i := range s.files {
		if s.files[i].Stale && !includeStale {
			continue
		}
		cp := cloneFileEntry(s.files[i])
		all = append(all, cp)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].LastSeenAt != all[j].LastSeenAt {
			return all[i].LastSeenAt > all[j].LastSeenAt
		}
		return all[i].ID > all[j].ID
	})

	if limit > len(all) {
		limit = len(all)
	}
	return all[:limit], nil
}

// Search ищет top-K ближайших к queryVec записей (cosine similarity in-Go).
// Исключает stale, metadata-only записи и векторы от другого backend.
// Применяет гибридный boost: +0.05 если query substring встречается в Name.
func (s *Store) Search(queryVec []float32, backend string, k int, query string) ([]Scored, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if strings.TrimSpace(backend) == "" {
		return nil, fmt.Errorf("Search: пустой backend")
	}
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("Search: пустой query vector")
	}
	if k <= 0 {
		k = 10
	}

	q := strings.ToLower(query)
	var results []Scored
	for i := range s.files {
		e := s.files[i]
		if e.Stale || e.Backend != backend {
			continue
		}
		if len(e.Embedding) == 0 || len(e.Embedding) != len(queryVec) {
			continue
		}
		score := mem.CosineSimilarity(queryVec, s.vectors[i])
		// Гибридный boost: точное/частичное совпадение в имени файла.
		if q != "" && strings.Contains(strings.ToLower(e.Name), q) {
			score += 0.05
			if score > 1.0 {
				score = 1.0
			}
		}
		results = append(results, Scored{Entry: cloneFileEntry(e), Score: score})
	}

	if len(results) == 0 {
		return nil, nil
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if k > len(results) {
		k = len(results)
	}
	out := make([]Scored, k)
	for i := 0; i < k; i++ {
		out[i] = results[i]
	}
	return out, nil
}

// Stats возвращает статистику базы.
func (s *Store) Stats() (Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := Stats{
		ByExt: make(map[string]int),
	}
	for i := range s.files {
		stats.Total++
		if s.files[i].Stale {
			stats.Stale++
		} else {
			stats.NotStale++
		}
		stats.ByExt[s.files[i].Ext]++
	}
	return stats, nil
}

// Delete удаляет запись по ID (из БД и кэша).
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DELETE: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("запись #%d не найдена", id)
	}
	for i := range s.files {
		if s.files[i].ID == id {
			s.files = append(s.files[:i], s.files[i+1:]...)
			s.vectors = append(s.vectors[:i], s.vectors[i+1:]...)
			return nil
		}
	}
	return nil
}

// MarkStale помечает записи по path как stale=1.
func (s *Store) MarkStale(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE files SET stale = 1 WHERE path = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, p := range paths {
		if _, err := stmt.Exec(p); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Обновляем кэш.
	for _, p := range paths {
		norm := filepath.ToSlash(p)
		for i := range s.files {
			if filepath.ToSlash(s.files[i].Path) == norm {
				s.files[i].Stale = true
				break
			}
		}
	}
	return nil
}

// UnmarkStale снимает пометку stale (используется при scan, когда ранее
// отсутствующий файл снова найден).
func (s *Store) UnmarkStale(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE files SET stale = 0 WHERE path = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, p := range paths {
		if _, err := stmt.Exec(p); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for _, p := range paths {
		norm := filepath.ToSlash(p)
		for i := range s.files {
			if filepath.ToSlash(s.files[i].Path) == norm {
				s.files[i].Stale = false
				break
			}
		}
	}
	return nil
}

// AllPaths возвращает все path из БД (для reconciling при scan).
func (s *Store) AllPaths() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	paths := make([]string, 0, len(s.files))
	for i := range s.files {
		paths = append(paths, s.files[i].Path)
	}
	return paths, nil
}

// === Теги: JSON-кодирование (как в pkg/mem/store.go) ===

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

func cloneFileEntry(entry FileEntry) FileEntry {
	entry.Tags = append([]string(nil), entry.Tags...)
	entry.Embedding = append([]float32(nil), entry.Embedding...)
	return entry
}
