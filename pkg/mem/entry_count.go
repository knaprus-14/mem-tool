package mem

import "fmt"

// EntryCount returns the authoritative number of stored entries. Unlike
// Stats, it preserves SQLite errors so admission and quota callers can fail
// closed instead of mistaking an unreadable database for an empty one.
func (s *Store) EntryCount() (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("count entries: store is not open")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count entries: %w", err)
	}
	return count, nil
}
