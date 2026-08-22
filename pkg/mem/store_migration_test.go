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
total_chunks INTEGER NOT NULL DEFAULT 0, important INTEGER NOT NULL DEFAULT 0);
CREATE TABLE knowledge_reviews (
id INTEGER PRIMARY KEY AUTOINCREMENT, object_type TEXT NOT NULL, object_id TEXT NOT NULL,
action TEXT NOT NULL, previous_status TEXT NOT NULL, new_status TEXT NOT NULL,
reviewer TEXT NOT NULL, comment TEXT NOT NULL DEFAULT '', evidence_digest TEXT NOT NULL,
created TEXT NOT NULL);`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entries
(title, text, tags, created, backend, dims, embedding, source_file, chunk_label, chunk_index, total_chunks, important)
VALUES ('Legacy', 'legacy text', '[]', '2026-01-01T00:00:00Z', 'test', 1, x'0000803f', 'legacy.md', '', 0, 1, 0)`); err != nil {
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
		"embedding_model": false, "embedding_space": false,
		"document_id": false, "document_revision": false, "chunk_hash": false,
		"source_path": false, "media_type": false,
		"page": false, "block_index": false, "block_marker": false,
		"block_chunk_index": false, "block_total_chunks": false,
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
	legacy := store.GetBySourceFile("legacy.md")
	if len(legacy) != 1 || legacy[0].Text != "legacy text" {
		t.Fatalf("legacy row did not survive migration: %#v", legacy)
	}
	if legacy[0].DocumentRevision != "" || legacy[0].ChunkHash != "" {
		t.Fatalf("legacy row received invented content hashes: %#v", legacy[0])
	}
	if legacy[0].EmbeddingModel != "" || legacy[0].EmbeddingSpace != "" {
		t.Fatalf("legacy row received invented embedding provenance: %#v", legacy[0])
	}
	wantTables := map[string]bool{
		"knowledge_nodes": false, "knowledge_edges": false,
		"knowledge_node_evidence": false, "knowledge_edge_evidence": false,
		"knowledge_reviews": false, "knowledge_edits": false, "knowledge_node_merges": false, "knowledge_analysis_runs": false,
		"knowledge_analysis_batches": false, "knowledge_extraction_runs": false,
		"knowledge_extraction_batches": false, "knowledge_extraction_coverage": false,
		"document_import_manifests": false, "document_import_pages": false,
		"document_import_runs": false, "document_import_run_pages": false,
	}
	tableRows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer tableRows.Close()
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, ok := wantTables[name]; ok {
			wantTables[name] = true
		}
	}
	for name, found := range wantTables {
		if !found {
			t.Errorf("migration did not create %s", name)
		}
	}
	reviewColumns, err := store.db.Query(`PRAGMA table_info(knowledge_reviews)`)
	if err != nil {
		t.Fatal(err)
	}
	defer reviewColumns.Close()
	hasRevertsReviewID := false
	for reviewColumns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := reviewColumns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		hasRevertsReviewID = hasRevertsReviewID || name == "reverts_review_id"
	}
	if !hasRevertsReviewID {
		t.Fatal("migration did not add knowledge_reviews.reverts_review_id")
	}
}
