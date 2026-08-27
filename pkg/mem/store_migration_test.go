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
		"knowledge_graph_snapshots": false, "document_history_snapshots": false,
		"document_history_chunks": false, "knowledge_restore_runs": false,
		"mind_maps": false, "mind_map_nodes": false, "mind_map_node_sources": false,
		"mind_map_changes": false, "mind_map_snapshots": false, "mind_map_generation_runs": false,
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

func TestNewStoreMigratesLegacyDocumentHistoryExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	seed, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	chunks := validStructuredChunks()
	created := "2026-01-02T03:04:05Z"
	tx, err := seed.db.Begin()
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	graphID, _, err := writeKnowledgeGraphSnapshotTx(tx, KnowledgeGraph{}, created)
	if err != nil {
		_ = tx.Rollback()
		seed.Close()
		t.Fatal(err)
	}
	first := chunks[0]
	if _, err := tx.Exec(`INSERT INTO document_history_snapshots
(document_id, document_revision, source_path, media_type, chunk_count,
 graph_snapshot_id, reason, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		first.Provenance.DocumentID, first.Provenance.DocumentRevision,
		first.Provenance.SourcePath, first.Provenance.MediaType, len(chunks),
		graphID, "legacy_fixture", created); err != nil {
		_ = tx.Rollback()
		seed.Close()
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		embedding, err := floatsToBytes(chunk.Embedding)
		if err != nil {
			_ = tx.Rollback()
			seed.Close()
			t.Fatal(err)
		}
		p := chunk.Provenance
		if _, err := tx.Exec(`INSERT INTO document_history_chunks
(document_id, document_revision, chunk_index, title, text, tags, created, backend,
 embedding_model, embedding_space, dims, embedding, source_file, chunk_label,
 total_chunks, chunk_hash, source_path, media_type, page, block_index, block_marker,
 block_chunk_index, block_total_chunks, extraction_method, ocr_confidence, warnings, important)
VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', ?)`,
			p.DocumentID, p.DocumentRevision, chunk.ChunkIndex, chunk.Title, chunk.Text,
			created, chunk.Backend, chunk.EmbeddingModel, chunk.EmbeddingSpace,
			len(chunk.Embedding), embedding, p.SourcePath, chunk.ChunkLabel,
			chunk.TotalChunks, p.ChunkHash, p.SourcePath, p.MediaType, p.Page,
			p.BlockIndex, p.BlockMarker, p.BlockChunkIndex, p.BlockTotalChunks,
			p.ExtractionMethod, p.OCRConfidence, boolToInt(chunk.Important)); err != nil {
			_ = tx.Rollback()
			seed.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	firstOpen, err := NewStore(dir)
	if err != nil {
		t.Fatalf("first legacy history migration: %v", err)
	}
	snapshots, err := firstOpen.ListDocumentHistorySnapshots(first.Provenance.SourcePath)
	if err != nil {
		firstOpen.Close()
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ChunkCount != len(chunks) || snapshots[0].SnapshotID == "" {
		firstOpen.Close()
		t.Fatalf("legacy history was not migrated exactly once: %#v", snapshots)
	}
	wantSnapshotID := snapshots[0].SnapshotID
	var wantMigrated string
	if err := firstOpen.db.QueryRow(`SELECT migrated FROM document_history_legacy_migrations
WHERE document_id = ? AND document_revision = ?`, first.Provenance.DocumentID,
		first.Provenance.DocumentRevision).Scan(&wantMigrated); err != nil {
		firstOpen.Close()
		t.Fatalf("read legacy migration marker: %v", err)
	}
	if wantMigrated == "" {
		firstOpen.Close()
		t.Fatal("legacy migration marker has an empty timestamp")
	}

	// If a subsequent NewStore attempted to decode this already-migrated row,
	// the malformed float BLOB would fail startup. The immutable legacy trigger
	// is dropped only to construct this regression fixture; initSchema recreates
	// it before the second migration pass.
	if _, err := firstOpen.db.Exec(`DROP TRIGGER document_history_chunks_no_update`); err != nil {
		firstOpen.Close()
		t.Fatal(err)
	}
	if _, err := firstOpen.db.Exec(`UPDATE document_history_chunks SET embedding = x'00'
WHERE document_id = ? AND document_revision = ?`, first.Provenance.DocumentID,
		first.Provenance.DocumentRevision); err != nil {
		firstOpen.Close()
		t.Fatal(err)
	}
	if err := firstOpen.Close(); err != nil {
		t.Fatal(err)
	}

	secondOpen, err := NewStore(dir)
	if err != nil {
		t.Fatalf("repeat NewStore re-decoded migrated legacy chunks: %v", err)
	}
	defer secondOpen.Close()

	var versionCount, versionChunkCount, markerCount int
	if err := secondOpen.db.QueryRow(`SELECT COUNT(*) FROM document_history_versions`).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := secondOpen.db.QueryRow(`SELECT COUNT(*) FROM document_history_version_chunks`).Scan(&versionChunkCount); err != nil {
		t.Fatal(err)
	}
	if err := secondOpen.db.QueryRow(`SELECT COUNT(*) FROM document_history_legacy_migrations`).Scan(&markerCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 || versionChunkCount != len(chunks) || markerCount != 1 {
		t.Fatalf("repeat NewStore duplicated legacy history: versions=%d chunks=%d markers=%d",
			versionCount, versionChunkCount, markerCount)
	}
	var gotSnapshotID, gotMigrated string
	if err := secondOpen.db.QueryRow(`SELECT snapshot_id, migrated
FROM document_history_legacy_migrations WHERE document_id = ? AND document_revision = ?`,
		first.Provenance.DocumentID, first.Provenance.DocumentRevision).
		Scan(&gotSnapshotID, &gotMigrated); err != nil {
		t.Fatal(err)
	}
	if gotSnapshotID != wantSnapshotID || gotMigrated != wantMigrated {
		t.Fatalf("repeat NewStore rewrote migration marker: id=%q/%q migrated=%q/%q",
			gotSnapshotID, wantSnapshotID, gotMigrated, wantMigrated)
	}
}
