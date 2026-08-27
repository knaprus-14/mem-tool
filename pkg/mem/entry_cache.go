package mem

import (
	"database/sql"
	"fmt"
	"sort"
)

type entryGenerationQuerier interface {
	QueryRow(string, ...any) *sql.Row
}

func loadEntryCacheGeneration(q entryGenerationQuerier) (int64, error) {
	var generation int64
	if err := q.QueryRow(`SELECT generation FROM entry_cache_state WHERE singleton = 1`).Scan(&generation); err != nil {
		return 0, fmt.Errorf("read entry cache generation: %w", err)
	}
	return generation, nil
}

func loadEntryFTSGeneration(q entryGenerationQuerier) (int64, error) {
	var generation int64
	if err := q.QueryRow(`SELECT generation FROM entry_fts_state WHERE singleton = 1`).Scan(&generation); err != nil {
		return 0, fmt.Errorf("read entry FTS generation: %w", err)
	}
	return generation, nil
}

// refreshEntryCacheIfStaleUnlocked refreshes read-side state before a plan is
// built from s.entries. The generation is only a cheap gate; the actual cache
// snapshot is still loaded atomically by s.loadAll when needed.
func (s *Store) refreshEntryCacheIfStaleUnlocked(operation string) error {
	databaseGeneration, err := loadEntryCacheGeneration(s.db)
	if err != nil {
		s.invalidateEntryCacheUnlocked()
		return err
	}
	if databaseGeneration == s.entryGeneration {
		return nil
	}
	if err := s.loadAll(); err != nil {
		s.invalidateEntryCacheUnlocked()
		return fmt.Errorf("refresh %s cache after external entry change: %w", operation, err)
	}
	// FTS is a process-local derived cache. A generation mismatch means its
	// rows may describe a different entries snapshot even when the SQLite FTS
	// table itself is shared by another process.
	s.lexicalDirty = true
	return nil
}

// invalidateEntryCacheUnlocked makes APIs without an error return fail closed
// after a generation/read failure. Keeping the previous snapshot would expose
// entries that may already have been deleted or superseded by another process.
// A negative generation guarantees that the next call retries a full reload.
// The caller must hold s.mu.
func (s *Store) invalidateEntryCacheUnlocked() {
	s.entries = nil
	s.vectors = nil
	s.entryGeneration = -1
	s.lexicalDirty = true
}

// beginEntryMutationTx opens a transaction whose entry snapshot matches the
// Store cache. A generation mismatch means another connection changed entries;
// in that case the complete cache is refreshed in a read-only transaction
// before retrying. Consequently a document writer never decodes the full
// corpus while holding SQLite's write lock.
//
// The caller must hold s.mu for the entire call and resulting transaction.
func (s *Store) beginEntryMutationTx(operation string) (*sql.Tx, []Entry, error) {
	const maxSnapshotAttempts = 4
	for attempt := 0; attempt < maxSnapshotAttempts; attempt++ {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, nil, fmt.Errorf("begin %s: %w", operation, err)
		}
		// Reserve SQLite's single writer before comparing generations. Once this
		// no-op UPDATE succeeds, another process cannot commit an entries change
		// between the comparison and the caller's document writes.
		if _, err := tx.Exec(`UPDATE entry_cache_state SET generation = generation WHERE singleton = 1`); err != nil {
			_ = tx.Rollback()
			return nil, nil, fmt.Errorf("reserve %s writer: %w", operation, err)
		}
		databaseGeneration, err := loadEntryCacheGeneration(tx)
		if err != nil {
			_ = tx.Rollback()
			return nil, nil, err
		}
		if databaseGeneration == s.entryGeneration {
			return tx, append([]Entry(nil), s.entries...), nil
		}
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			return nil, nil, fmt.Errorf("refresh %s cache: rollback stale snapshot: %w", operation, err)
		}
		if err := s.loadAll(); err != nil {
			return nil, nil, fmt.Errorf("refresh %s cache after external entry change: %w", operation, err)
		}
		// A refreshed entry snapshot may no longer match the shared FTS table.
		// Callers which do not mutate indexed fields can leave it dirty; the next
		// lexical read will rebuild it under the same writer serialization.
		s.lexicalDirty = true
	}
	return nil, nil, fmt.Errorf("begin %s: entries kept changing in another process", operation)
}

func replaceSourceInEntryCache(cached []Entry, sourcePath string, replacement []Entry) ([]Entry, [][]float32) {
	result := make([]Entry, 0, len(cached)+len(replacement))
	for _, entry := range cached {
		if !coveragePathsEqual(entry.SourceFile, sourcePath) {
			result = append(result, entry)
		}
	}
	result = append(result, replacement...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	vectors := make([][]float32, len(result))
	for i := range result {
		vectors[i] = result[i].Embedding
	}
	return result, vectors
}
