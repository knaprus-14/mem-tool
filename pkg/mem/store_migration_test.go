package mem

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestNewStoreMigratesStageOneSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `CREATE TABLE entries (
id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL DEFAULT '', text TEXT NOT NULL,
tags TEXT NOT NULL DEFAULT '[]', created TEXT NOT NULL, backend TEXT NOT NULL,
dims INTEGER NOT NULL, embedding BLOB NOT NULL, source_file TEXT NOT NULL DEFAULT '',
chunk_label TEXT NOT NULL DEFAULT '', chunk_index INTEGER NOT NULL DEFAULT 0,
total_chunks INTEGER NOT NULL DEFAULT 0, important INTEGER NOT NULL DEFAULT 0);`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("open Stage 1 store: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`PRAGMA table_info(entries)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{
		"document_id": false, "source_path": false, "media_type": false,
		"page": false, "block_index": false, "block_marker": false,
		"extraction_method": false, "ocr_confidence": false, "warnings": false,
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for column, found := range want {
		if !found {
			t.Errorf("migration did not add %s", column)
		}
	}
}
