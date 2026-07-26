package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry — одна запись в базе памяти
type Entry struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title,omitempty"` // заголовок записи
	Text      string    `json:"text"`
	Tags      []string  `json:"tags,omitempty"`
	Created   string    `json:"created"`
	Backend   string    `json:"backend"`
	Dims      int       `json:"dims"`
	Embedding []float32 `json:"-"`
	Score     float64   `json:"-"` // для результатов поиска
	Deleted   bool      `json:"deleted,omitempty"` // помечена на удаление
	Important bool      `json:"important,omitempty"` // флаг важности

	// Document tracking
	SourceFile  string `json:"source_file,omitempty"`  // путь к исходному файлу
	ChunkLabel  string `json:"chunk_label,omitempty"`  // описание чанка (раздел, страница)
	ChunkIndex  int    `json:"chunk_index,omitempty"`  // номер чанка в документе
	TotalChunks int    `json:"total_chunks,omitempty"` // всего чанков в документе
}

// Store — потокобезопасное хранилище векторов
type Store struct {
	mu      sync.RWMutex
	entries []Entry
	vectors [][]float32

	dir  string
	path string
	next int64
}

func newStore(path string) (*Store, error) {
	// Создаём родительскую директорию, если её нет
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	s := &Store{
		dir:  filepath.Dir(path),
		path: path,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			s.vectors = nil
			s.next = 1
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var maxID int64
	// Карта для отслеживания последней версии каждой записи (ID → индекс в entries)
	seenIDs := make(map[int64]int)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return fmt.Errorf("ошибка парсинга store.jsonl: %w", err)
		}

		// Пропускаем удалённые записи
		if entry.Deleted {
			if entry.ID > maxID {
				maxID = entry.ID
			}
			continue
		}

		// Читаем эмбеддинг
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err == nil {
			if emb, ok := raw["embedding"]; ok {
				var vec []float32
				if err := json.Unmarshal(emb, &vec); err == nil {
					entry.Embedding = vec
					entry.Dims = len(vec)
				}
			}
		}

		if entry.ID > maxID {
			maxID = entry.ID
		}

		// Если ID уже встречался — заменяем старую запись (редактирование)
		if idx, exists := seenIDs[entry.ID]; exists {
			s.entries[idx] = entry
		} else {
			seenIDs[entry.ID] = len(s.entries)
			s.entries = append(s.entries, entry)
		}
	}

	// Перестраиваем векторы после загрузки всех записей
	s.rebuildVectors()

	s.next = maxID + 1
	return scanner.Err()
}

// rebuildVectors перестраивает слайс vectors из entries
func (s *Store) rebuildVectors() {
	s.vectors = nil
	for _, e := range s.entries {
		if e.Embedding != nil {
			s.vectors = append(s.vectors, e.Embedding)
		}
	}
}

func (s *Store) append(entry Entry) error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	data := entryToJSON(entry)
	line, err := json.Marshal(data)
	if err != nil {
		return err
	}

	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// rewriteFile перезаписывает весь JSONL-файл из памяти
func (s *Store) rewriteFile() error {
	f, err := os.Create(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, entry := range s.entries {
		data := entryToJSON(entry)
		line, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// entryToJSON собирает JSON-объект из Entry
func entryToJSON(entry Entry) map[string]interface{} {
	m := map[string]interface{}{
		"id":        entry.ID,
		"text":      entry.Text,
		"created":   entry.Created,
		"backend":   entry.Backend,
		"dims":      entry.Dims,
		"embedding": entry.Embedding,
	}
	if entry.Title != "" {
		m["title"] = entry.Title
	}
	if len(entry.Tags) > 0 {
		m["tags"] = entry.Tags
	}
	if entry.Deleted {
		m["deleted"] = true
	}
	if entry.Important {
		m["important"] = true
	}
	if entry.SourceFile != "" {
		m["source_file"] = entry.SourceFile
	}
	if entry.ChunkLabel != "" {
		m["chunk_label"] = entry.ChunkLabel
	}
	if entry.TotalChunks > 0 {
		m["chunk_index"] = entry.ChunkIndex
		m["total_chunks"] = entry.TotalChunks
	}
	return m
}

// Add добавляет запись в хранилище
func (s *Store) Add(text string, title string, tags []string, backend string, embedding []float32, important bool) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := Entry{
		ID:        s.next,
		Text:      text,
		Title:     title,
		Tags:      tags,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Backend:   backend,
		Dims:      len(embedding),
		Embedding: embedding,
		Important: important,
	}
	s.next++

	if err := s.append(entry); err != nil {
		return nil, err
	}

	s.entries = append(s.entries, entry)
	s.vectors = append(s.vectors, embedding)

	return &entry, nil
}

// AddChunk добавляет чанк документа с источником
func (s *Store) AddChunk(text string, title string, tags []string, backend string, embedding []float32,
	sourceFile, chunkLabel string, chunkIndex, totalChunks int, important bool) (*Entry, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := Entry{
		ID:          s.next,
		Text:        text,
		Title:       title,
		Tags:        tags,
		Created:     time.Now().UTC().Format(time.RFC3339),
		Backend:     backend,
		Dims:        len(embedding),
		Embedding:   embedding,
		SourceFile:  sourceFile,
		ChunkLabel:  chunkLabel,
		ChunkIndex:  chunkIndex,
		TotalChunks: totalChunks,
		Important:   important,
	}
	s.next++

	if err := s.append(entry); err != nil {
		return nil, err
	}

	s.entries = append(s.entries, entry)
	s.vectors = append(s.vectors, embedding)

	return &entry, nil
}

// DeleteById удаляет запись по ID (помечает deleted и перезаписывает файл)
func (s *Store) DeleteById(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Deleted = true
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("запись #%d не найдена", id)
	}

	// Перестраиваем векторы (убираем embedding удалённой)
	s.rebuildVectors()

	// Удаляем из слайса entries
	var filtered []Entry
	for _, e := range s.entries {
		if !e.Deleted {
			filtered = append(filtered, e)
		}
	}
	s.entries = filtered

	return s.rewriteFile()
}

// UpdateById обновляет текст/заголовок/теги/эмбеддинг записи
func (s *Store) UpdateById(id int64, text string, title string, tags []string, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Text = text
			s.entries[i].Title = title
			s.entries[i].Tags = tags
			if embedding != nil {
				s.entries[i].Embedding = embedding
				s.entries[i].Dims = len(embedding)
			}
			s.entries[i].Created = time.Now().UTC().Format(time.RFC3339)

			s.rebuildVectors()
			return s.rewriteFile()
		}
	}

	return fmt.Errorf("запись #%d не найдена", id)
}

// ToggleImportant переключает флаг важности записи
func (s *Store) ToggleImportant(id int64) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Important = !s.entries[i].Important
			if err := s.rewriteFile(); err != nil {
				// Откатываем
				s.entries[i].Important = !s.entries[i].Important
				return nil, err
			}
			return &s.entries[i], nil
		}
	}

	return nil, fmt.Errorf("запись #%d не найдена", id)
}

// Search ищет ближайшие по смыслу записи
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
	for _, entry := range s.entries {
		if entry.Backend != backend {
			continue
		}
		if entry.Embedding == nil || len(entry.Embedding) != len(queryVector) {
			continue
		}

		score := cosineSimilarity(queryVector, entry.Embedding)
		results = append(results, scored{
			entry: entry,
			score: score,
		})
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

// Recent возвращает последние N записей
func (s *Store) Recent(limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	start := len(s.entries) - limit
	if start < 0 {
		start = 0
	}

	out := make([]Entry, len(s.entries)-start)
	for i := start; i < len(s.entries); i++ {
		out[i-start] = s.entries[i]
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	return out, nil
}

// GetByID возвращает запись по ID
func (s *Store) GetByID(id int64) (*Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i, entry := range s.entries {
		if entry.ID == id {
			return &s.entries[i], nil
		}
	}
	return nil, fmt.Errorf("запись #%d не найдена", id)
}

// SourceFiles возвращает список уникальных файлов-источников
func (s *Store) SourceFiles() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]int)
	for _, entry := range s.entries {
		if entry.SourceFile != "" {
			// Нормализуем путь для красивого отображения
			path := entry.SourceFile
			result[path]++
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
		"store_location": s.path,
	}
}

// cosineSimilarity считает косинусное сходство между двумя векторами
func cosineSimilarity(a, b []float32) float64 {
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
