package fileindex

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestNewStoreMigratesLegacyEmbeddingSchemaWithoutInventingIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".fileindex")
	dbPath := filepath.Join(dir, "store.db")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `CREATE TABLE files (
id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL, name TEXT NOT NULL,
ext TEXT, size INTEGER, mtime INTEGER, parent_dir TEXT, hash TEXT, annotation TEXT,
backend TEXT NOT NULL, dims INTEGER NOT NULL, embedding BLOB NOT NULL,
tags TEXT NOT NULL DEFAULT '[]', stale INTEGER NOT NULL DEFAULT 0,
created_at TEXT NOT NULL, updated_at TEXT NOT NULL, last_seen_at TEXT NOT NULL);`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	embedding, err := mem.FloatsToBytes([]float32{1, 0})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO files
(path, name, ext, size, mtime, parent_dir, hash, annotation, backend, dims, embedding,
 tags, stale, created_at, updated_at, last_seen_at)
VALUES (?, ?, '.txt', 6, 1, '', '', '', 'ollama', 2, ?, '[]', 0, ?, ?, ?)`,
		"legacy.txt", "legacy.txt", embedding,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	wantColumns := map[string]bool{"embedding_model": false, "embedding_space": false}
	rows, err := store.db.Query(`PRAGMA table_info(files)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if _, ok := wantColumns[name]; ok {
			wantColumns[name] = true
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for name, found := range wantColumns {
		if !found {
			t.Errorf("migration did not add %s", name)
		}
	}

	legacy, ok := store.GetByPath("legacy.txt")
	if !ok {
		t.Fatal("legacy row did not survive migration")
	}
	if legacy.EmbeddingModel != "" || legacy.EmbeddingSpace != "" {
		t.Fatalf("legacy row received invented embedding identity: %+v", legacy)
	}
	results, err := store.Search([]float32{1, 0}, "current-space", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("legacy unknown vector participated in semantic search: %+v", results)
	}
}

func TestStorePersistsEmbeddingIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".fileindex")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := &FileEntry{
		Path: "known.txt", Name: "known.txt", Backend: "ollama",
		EmbeddingModel: "known-model", EmbeddingSpace: "known-space",
		Embedding: []float32{0.25, 0.75},
	}
	if err := store.Upsert(entry); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.GetByPath(entry.Path)
	if !ok || got.EmbeddingModel != entry.EmbeddingModel || got.EmbeddingSpace != entry.EmbeddingSpace {
		t.Fatalf("embedding identity did not survive reopen: %+v", got)
	}
	if len(got.Embedding) != 2 || got.Embedding[0] != 0.25 || got.Embedding[1] != 0.75 {
		t.Fatalf("embedding did not survive reopen: %v", got.Embedding)
	}
}
