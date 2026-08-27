package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const documentHistorySchema = `
CREATE TABLE IF NOT EXISTS knowledge_graph_snapshots (
    id TEXT PRIMARY KEY,
    digest TEXT NOT NULL UNIQUE,
    graph_json TEXT NOT NULL,
    node_count INTEGER NOT NULL,
    edge_count INTEGER NOT NULL,
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS document_history_snapshots (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    chunk_count INTEGER NOT NULL,
    graph_snapshot_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    created TEXT NOT NULL,
    PRIMARY KEY (document_id, document_revision)
);

CREATE TABLE IF NOT EXISTS document_history_chunks (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '[]',
    created TEXT NOT NULL,
    backend TEXT NOT NULL,
    embedding_model TEXT NOT NULL DEFAULT '',
    embedding_space TEXT NOT NULL DEFAULT '',
    dims INTEGER NOT NULL,
    embedding BLOB NOT NULL,
    source_file TEXT NOT NULL,
    chunk_label TEXT NOT NULL DEFAULT '',
    total_chunks INTEGER NOT NULL,
    chunk_hash TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    page INTEGER NOT NULL DEFAULT 0,
    block_index INTEGER NOT NULL DEFAULT 0,
    block_marker TEXT NOT NULL DEFAULT '',
    block_chunk_index INTEGER NOT NULL DEFAULT 0,
    block_total_chunks INTEGER NOT NULL DEFAULT 0,
    extraction_method TEXT NOT NULL DEFAULT '',
    ocr_confidence REAL NOT NULL DEFAULT -1,
    warnings TEXT NOT NULL DEFAULT '[]',
    important INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (document_id, document_revision, chunk_index)
);

-- The original history tables above used (document_id, document_revision) as
-- their identity. A source revision is a content identity, not a chunk-layout
-- identity, so re-chunking unchanged source text could collide and silently
-- discard an older recoverable state. The version tables keep every distinct
-- document+graph state under a stable snapshot ID. The legacy tables remain
-- read-only so existing databases can be migrated without destructive DDL.
CREATE TABLE IF NOT EXISTS document_history_versions (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id TEXT NOT NULL UNIQUE,
    state_digest TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    chunk_count INTEGER NOT NULL,
    graph_snapshot_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    created TEXT NOT NULL,
    UNIQUE (document_id, document_revision, state_digest)
);

CREATE TABLE IF NOT EXISTS document_history_version_chunks (
    snapshot_id TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '[]',
    created TEXT NOT NULL,
    backend TEXT NOT NULL,
    embedding_model TEXT NOT NULL DEFAULT '',
    embedding_space TEXT NOT NULL DEFAULT '',
    dims INTEGER NOT NULL,
    embedding BLOB NOT NULL,
    source_file TEXT NOT NULL,
    chunk_label TEXT NOT NULL DEFAULT '',
    total_chunks INTEGER NOT NULL,
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    chunk_hash TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    page INTEGER NOT NULL DEFAULT 0,
    block_index INTEGER NOT NULL DEFAULT 0,
    block_marker TEXT NOT NULL DEFAULT '',
    block_chunk_index INTEGER NOT NULL DEFAULT 0,
    block_total_chunks INTEGER NOT NULL DEFAULT 0,
    extraction_method TEXT NOT NULL DEFAULT '',
    ocr_confidence REAL NOT NULL DEFAULT -1,
    warnings TEXT NOT NULL DEFAULT '[]',
    important INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (snapshot_id, chunk_index),
    FOREIGN KEY (snapshot_id) REFERENCES document_history_versions(snapshot_id)
);

CREATE TABLE IF NOT EXISTS document_current_tombstones (
    document_id TEXT PRIMARY KEY,
    source_path TEXT NOT NULL UNIQUE,
    document_revision TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS document_history_legacy_migrations (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    migrated TEXT NOT NULL,
    PRIMARY KEY (document_id, document_revision)
);

CREATE TABLE IF NOT EXISTS knowledge_restore_runs (
    id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL,
    source_path TEXT NOT NULL,
    from_revision TEXT NOT NULL,
    target_revision TEXT NOT NULL,
    before_graph_snapshot_id TEXT NOT NULL,
    target_graph_snapshot_id TEXT NOT NULL,
    plan_digest TEXT NOT NULL,
    rollback_of TEXT NOT NULL DEFAULT '',
    restored_chunks INTEGER NOT NULL,
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_restore_document_snapshots (
    run_id TEXT PRIMARY KEY,
    before_document_snapshot_id TEXT NOT NULL,
    target_document_snapshot_id TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES knowledge_restore_runs(id)
);

CREATE INDEX IF NOT EXISTS idx_document_history_source
    ON document_history_snapshots(source_path, created);
CREATE INDEX IF NOT EXISTS idx_document_history_versions_source
    ON document_history_versions(source_path, sequence DESC);
CREATE INDEX IF NOT EXISTS idx_document_history_versions_revision
    ON document_history_versions(document_id, document_revision, sequence DESC);

CREATE TRIGGER IF NOT EXISTS knowledge_graph_snapshots_no_update
BEFORE UPDATE ON knowledge_graph_snapshots BEGIN
    SELECT RAISE(ABORT, 'knowledge graph snapshots are immutable');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_graph_snapshots_no_delete
BEFORE DELETE ON knowledge_graph_snapshots BEGIN
    SELECT RAISE(ABORT, 'knowledge graph snapshots are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_snapshots_no_update
BEFORE UPDATE ON document_history_snapshots BEGIN
    SELECT RAISE(ABORT, 'document history snapshots are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_snapshots_no_delete
BEFORE DELETE ON document_history_snapshots BEGIN
    SELECT RAISE(ABORT, 'document history snapshots are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_chunks_no_update
BEFORE UPDATE ON document_history_chunks BEGIN
    SELECT RAISE(ABORT, 'document history chunks are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_chunks_no_delete
BEFORE DELETE ON document_history_chunks BEGIN
    SELECT RAISE(ABORT, 'document history chunks are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_versions_no_update
BEFORE UPDATE ON document_history_versions BEGIN
    SELECT RAISE(ABORT, 'document history versions are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_versions_no_delete
BEFORE DELETE ON document_history_versions BEGIN
    SELECT RAISE(ABORT, 'document history versions are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_version_chunks_no_update
BEFORE UPDATE ON document_history_version_chunks BEGIN
    SELECT RAISE(ABORT, 'document history version chunks are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_version_chunks_no_delete
BEFORE DELETE ON document_history_version_chunks BEGIN
    SELECT RAISE(ABORT, 'document history version chunks are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_legacy_migrations_no_update
BEFORE UPDATE ON document_history_legacy_migrations BEGIN
    SELECT RAISE(ABORT, 'document history migration markers are immutable');
END;
CREATE TRIGGER IF NOT EXISTS document_history_legacy_migrations_no_delete
BEFORE DELETE ON document_history_legacy_migrations BEGIN
    SELECT RAISE(ABORT, 'document history migration markers are immutable');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_restore_runs_no_update
BEFORE UPDATE ON knowledge_restore_runs BEGIN
    SELECT RAISE(ABORT, 'knowledge restore history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_restore_runs_no_delete
BEFORE DELETE ON knowledge_restore_runs BEGIN
    SELECT RAISE(ABORT, 'knowledge restore history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_restore_document_snapshots_no_update
BEFORE UPDATE ON knowledge_restore_document_snapshots BEGIN
    SELECT RAISE(ABORT, 'knowledge restore snapshot references are append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_restore_document_snapshots_no_delete
BEFORE DELETE ON knowledge_restore_document_snapshots BEGIN
    SELECT RAISE(ABORT, 'knowledge restore snapshot references are append-only');
END;
`

var ErrDocumentHistoryUnavailable = errors.New("document history snapshot is unavailable")

type DocumentHistorySnapshot struct {
	Sequence         int64  `json:"sequence"`
	SnapshotID       string `json:"snapshot_id"`
	StateDigest      string `json:"state_digest"`
	DocumentID       string `json:"document_id"`
	DocumentRevision string `json:"document_revision"`
	SourcePath       string `json:"source_path"`
	MediaType        string `json:"media_type,omitempty"`
	ChunkCount       int    `json:"chunk_count"`
	GraphSnapshotID  string `json:"graph_snapshot_id"`
	Reason           string `json:"reason"`
	Created          string `json:"created"`
}

type documentTombstone struct {
	DocumentID       string
	SourcePath       string
	DocumentRevision string
	MediaType        string
	Created          string
}

type CorpusChunkDiffState string

const (
	CorpusChunkAdded     CorpusChunkDiffState = "added"
	CorpusChunkChanged   CorpusChunkDiffState = "changed"
	CorpusChunkRemoved   CorpusChunkDiffState = "removed"
	CorpusChunkUnchanged CorpusChunkDiffState = "unchanged"
)

type CorpusChunkDiff struct {
	State           CorpusChunkDiffState `json:"state"`
	Page            int                  `json:"page"`
	BlockIndex      int                  `json:"block_index"`
	BlockChunkIndex int                  `json:"block_chunk_index"`
	BeforeIndex     int                  `json:"before_index,omitempty"`
	AfterIndex      int                  `json:"after_index,omitempty"`
	BeforeHash      string               `json:"before_hash,omitempty"`
	AfterHash       string               `json:"after_hash,omitempty"`
	BeforeText      string               `json:"before_text,omitempty"`
	AfterText       string               `json:"after_text,omitempty"`
}

type CorpusRevisionDiff struct {
	Available       bool              `json:"available"`
	DocumentID      string            `json:"document_id"`
	SourcePath      string            `json:"source_path"`
	FromRevision    string            `json:"from_revision"`
	FromSnapshotID  string            `json:"from_snapshot_id,omitempty"`
	ToRevision      string            `json:"to_revision"`
	ToSnapshotID    string            `json:"to_snapshot_id,omitempty"`
	FromSnapshotAt  string            `json:"from_snapshot_at,omitempty"`
	AddedChunks     int               `json:"added_chunks"`
	ChangedChunks   int               `json:"changed_chunks"`
	RemovedChunks   int               `json:"removed_chunks"`
	UnchangedChunks int               `json:"unchanged_chunks"`
	Changes         []CorpusChunkDiff `json:"changes,omitempty"`
	ComparisonBasis string            `json:"comparison_basis"`
}

func archiveDocumentHistoryTx(tx *sql.Tx, entries []Entry, graph KnowledgeGraph, created, reason string) (DocumentHistorySnapshot, error) {
	if len(entries) == 0 {
		return DocumentHistorySnapshot{}, nil
	}
	first := entries[0]
	if first.DocumentID == "" || first.DocumentRevision == "" || first.SourcePath == "" {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive document history: current document has incomplete provenance")
	}
	for i := range entries {
		entry := entries[i]
		if entry.DocumentID != first.DocumentID || entry.DocumentRevision != first.DocumentRevision || entry.SourcePath != first.SourcePath {
			return DocumentHistorySnapshot{}, fmt.Errorf("archive document history: chunk %d has inconsistent document identity", i)
		}
	}
	graphID, graphDigest, err := writeKnowledgeGraphSnapshotTx(tx, graph, created)
	if err != nil {
		return DocumentHistorySnapshot{}, err
	}
	return writeDocumentHistoryVersionTx(tx, entries, graphID, graphDigest, created, reason)
}

func writeDocumentHistoryVersionTx(tx *sql.Tx, entries []Entry, graphID, graphDigest, created, reason string) (DocumentHistorySnapshot, error) {
	if len(entries) == 0 {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive document history: no document chunks")
	}
	ordered := make([]Entry, len(entries))
	for i := range entries {
		ordered[i] = cloneEntry(entries[i])
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ChunkIndex < ordered[j].ChunkIndex })
	first := ordered[0]
	stateDigest, err := documentRestoreCurrentStateDigest(ordered, graphDigest)
	if err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive document state digest: %w", err)
	}
	snapshotID := documentHistorySnapshotID(stateDigest)
	result, err := tx.Exec(`INSERT OR IGNORE INTO document_history_versions
(snapshot_id, state_digest, document_id, document_revision, source_path, media_type,
 chunk_count, graph_snapshot_id, reason, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotID, stateDigest, first.DocumentID,
		first.DocumentRevision, first.SourcePath, first.MediaType, len(ordered), graphID, reason, created)
	if err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive document snapshot: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive document snapshot result: %w", err)
	}
	if inserted != 0 {
		for _, entry := range ordered {
			tagsJSON, err := tagsToJSON(entry.Tags)
			if err != nil {
				return DocumentHistorySnapshot{}, err
			}
			warningsJSON, err := json.Marshal(entry.Warnings)
			if err != nil {
				return DocumentHistorySnapshot{}, fmt.Errorf("archive chunk %d warnings: %w", entry.ChunkIndex, err)
			}
			embedding, err := floatsToBytes(entry.Embedding)
			if err != nil {
				return DocumentHistorySnapshot{}, fmt.Errorf("archive chunk %d embedding: %w", entry.ChunkIndex, err)
			}
			_, err = tx.Exec(`INSERT INTO document_history_version_chunks
(snapshot_id, chunk_index, title, text, tags, created, backend, embedding_model,
 embedding_space, dims, embedding, source_file, chunk_label, total_chunks, document_id,
 document_revision, chunk_hash, source_path, media_type, page, block_index, block_marker,
 block_chunk_index, block_total_chunks, extraction_method, ocr_confidence, warnings, important)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				snapshotID, entry.ChunkIndex, entry.Title, entry.Text, tagsJSON, entry.Created,
				entry.Backend, entry.EmbeddingModel, entry.EmbeddingSpace, entry.Dims, embedding,
				entry.SourceFile, entry.ChunkLabel, entry.TotalChunks, entry.DocumentID,
				entry.DocumentRevision, entry.ChunkHash, entry.SourcePath, entry.MediaType,
				entry.Page, entry.BlockIndex, entry.BlockMarker, entry.BlockChunkIndex,
				entry.BlockTotalChunks, entry.ExtractionMethod, entry.OCRConfidence,
				string(warningsJSON), boolToInt(entry.Important))
			if err != nil {
				return DocumentHistorySnapshot{}, fmt.Errorf("archive document chunk %d: %w", entry.ChunkIndex, err)
			}
		}
	}
	var snapshot DocumentHistorySnapshot
	if err := tx.QueryRow(`SELECT sequence, snapshot_id, state_digest, document_id,
document_revision, source_path, media_type, chunk_count, graph_snapshot_id, reason, created
FROM document_history_versions WHERE snapshot_id = ?`, snapshotID).Scan(
		&snapshot.Sequence, &snapshot.SnapshotID, &snapshot.StateDigest, &snapshot.DocumentID,
		&snapshot.DocumentRevision, &snapshot.SourcePath, &snapshot.MediaType,
		&snapshot.ChunkCount, &snapshot.GraphSnapshotID, &snapshot.Reason, &snapshot.Created); err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("read archived document snapshot: %w", err)
	}
	return snapshot, nil
}

func documentHistorySnapshotID(stateDigest string) string {
	value := strings.TrimPrefix(stateDigest, "sha256:")
	if len(value) < 32 {
		return ""
	}
	return "dhs-" + value[:32]
}

func emptyDocumentStateDigest(tombstone documentTombstone, graphDigest string) (string, error) {
	encoded, err := json.Marshal(struct {
		DocumentID, SourcePath, DocumentRevision, MediaType, GraphDigest string
	}{
		DocumentID: tombstone.DocumentID, SourcePath: tombstone.SourcePath,
		DocumentRevision: tombstone.DocumentRevision, MediaType: tombstone.MediaType,
		GraphDigest: graphDigest,
	})
	if err != nil {
		return "", err
	}
	return prefixedSHA256(encoded), nil
}

func writeEmptyDocumentHistoryVersionTx(tx *sql.Tx, tombstone documentTombstone, graph KnowledgeGraph,
	created, reason string) (DocumentHistorySnapshot, error) {
	graphID, graphDigest, err := writeKnowledgeGraphSnapshotTx(tx, graph, created)
	if err != nil {
		return DocumentHistorySnapshot{}, err
	}
	stateDigest, err := emptyDocumentStateDigest(tombstone, graphDigest)
	if err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive empty document state digest: %w", err)
	}
	snapshotID := documentHistorySnapshotID(stateDigest)
	if _, err := tx.Exec(`INSERT OR IGNORE INTO document_history_versions
(snapshot_id, state_digest, document_id, document_revision, source_path, media_type,
 chunk_count, graph_snapshot_id, reason, created)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`, snapshotID, stateDigest, tombstone.DocumentID,
		tombstone.DocumentRevision, tombstone.SourcePath, tombstone.MediaType, graphID, reason, created); err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("archive empty document snapshot: %w", err)
	}
	var snapshot DocumentHistorySnapshot
	if err := tx.QueryRow(`SELECT sequence, snapshot_id, state_digest, document_id,
document_revision, source_path, media_type, chunk_count, graph_snapshot_id, reason, created
FROM document_history_versions WHERE snapshot_id = ?`, snapshotID).Scan(
		&snapshot.Sequence, &snapshot.SnapshotID, &snapshot.StateDigest, &snapshot.DocumentID,
		&snapshot.DocumentRevision, &snapshot.SourcePath, &snapshot.MediaType,
		&snapshot.ChunkCount, &snapshot.GraphSnapshotID, &snapshot.Reason, &snapshot.Created); err != nil {
		return DocumentHistorySnapshot{}, fmt.Errorf("read archived empty document snapshot: %w", err)
	}
	return snapshot, nil
}

type documentTombstoneQuerier interface {
	QueryRow(string, ...any) *sql.Row
}

func loadDocumentTombstone(q documentTombstoneQuerier, document string) (documentTombstone, error) {
	var tombstone documentTombstone
	err := q.QueryRow(`SELECT document_id, source_path, document_revision, media_type, created
FROM document_current_tombstones WHERE document_id = ? OR `+sourcePathSQLPredicate("source_path"),
		document, document).Scan(&tombstone.DocumentID, &tombstone.SourcePath,
		&tombstone.DocumentRevision, &tombstone.MediaType, &tombstone.Created)
	return tombstone, err
}

func upsertDocumentTombstoneTx(tx *sql.Tx, tombstone documentTombstone) error {
	_, err := tx.Exec(`INSERT INTO document_current_tombstones
(document_id, source_path, document_revision, media_type, created) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(document_id) DO UPDATE SET source_path=excluded.source_path,
document_revision=excluded.document_revision, media_type=excluded.media_type, created=excluded.created`,
		tombstone.DocumentID, tombstone.SourcePath, tombstone.DocumentRevision,
		tombstone.MediaType, tombstone.Created)
	return err
}

// ReplaceDocumentWithEmpty makes an empty source immediately disappear from
// search while preserving its previous chunks and graph as an addressable
// history snapshot. The tombstone keeps the empty state restorable and makes a
// later rollback distinguishable from a document that never existed.
func (s *Store) ReplaceDocumentWithEmpty(sourcePath string) (int64, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return 0, fmt.Errorf("empty document replacement has an empty source path")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, cacheEntries, err := s.beginEntryMutationTx("empty document replacement")
	if err != nil {
		return 0, err
	}
	rollback := func(cause error) (int64, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return 0, fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return 0, cause
	}
	databaseGeneration := s.entryGeneration
	oldEntries, err := loadEntriesBySource(tx, sourcePath)
	if err != nil {
		return rollback(fmt.Errorf("read current document before empty replacement: %w", err))
	}
	if len(oldEntries) == 0 {
		if _, err := loadDocumentTombstone(tx, sourcePath); err == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return 0, fmt.Errorf("finish existing empty document refresh: %w", commitErr)
			}
			freshEntries, freshVectors := replaceSourceInEntryCache(cacheEntries, sourcePath, nil)
			s.entries, s.vectors, s.entryGeneration, s.lexicalDirty = freshEntries, freshVectors, databaseGeneration, true
			return 0, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return rollback(fmt.Errorf("read current empty document state: %w", err))
		}
	}

	archivable := normalizeLegacyDocumentEntries(oldEntries, sourcePath)
	documentID := documentIDForSourcePath(sourcePath)
	mediaType := mediaTypeForSourcePath(sourcePath)
	if len(archivable) > 0 {
		documentID, mediaType = archivable[0].DocumentID, archivable[0].MediaType
		graph, graphErr := loadKnowledgeGraphFromQuerier(tx)
		if graphErr != nil {
			return rollback(fmt.Errorf("snapshot current knowledge graph: %w", graphErr))
		}
		if _, err := archiveDocumentHistoryTx(tx, archivable, graph, now, "before_document_became_empty"); err != nil {
			return rollback(err)
		}
	}
	result, err := tx.Exec(`DELETE FROM entries WHERE `+sourcePathSQLPredicate("source_file"), sourcePath)
	if err != nil {
		return rollback(fmt.Errorf("remove chunks for empty document: %w", err))
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return rollback(fmt.Errorf("read empty document removal count: %w", err))
	}
	if _, err := tx.Exec(`DELETE FROM document_current_tombstones
WHERE document_id = ? OR `+sourcePathSQLPredicate("source_path"), documentID, sourcePath); err != nil {
		return rollback(fmt.Errorf("replace current empty document marker: %w", err))
	}
	tombstone := documentTombstone{
		DocumentID: documentID, SourcePath: sourcePath, DocumentRevision: ChunkContentHash(""),
		MediaType: mediaType, Created: now,
	}
	if err := upsertDocumentTombstoneTx(tx, tombstone); err != nil {
		return rollback(fmt.Errorf("record current empty document state: %w", err))
	}
	finalGeneration, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return rollback(err)
	}
	freshEntries, freshVectors := replaceSourceInEntryCache(cacheEntries, sourcePath, nil)
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit empty document replacement: %w", err)
	}
	s.entries, s.vectors, s.entryGeneration, s.lexicalDirty = freshEntries, freshVectors, finalGeneration, true
	return deleted, nil
}

func normalizeLegacyDocumentEntries(entries []Entry, sourcePath string) []Entry {
	if len(entries) == 0 {
		return nil
	}
	result := make([]Entry, len(entries))
	for i := range entries {
		result[i] = cloneEntry(entries[i])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ChunkIndex < result[j].ChunkIndex })
	if result[0].DocumentID != "" && result[0].DocumentRevision != "" {
		return result
	}
	type revisionChunk struct {
		Index int
		Text  string
	}
	revisionInput := make([]revisionChunk, len(result))
	for i := range result {
		revisionInput[i] = revisionChunk{Index: result[i].ChunkIndex, Text: result[i].Text}
	}
	encoded, _ := json.Marshal(revisionInput)
	documentID := documentIDForSourcePath(sourcePath)
	revision := prefixedSHA256(encoded)
	mediaType := mediaTypeForSourcePath(sourcePath)
	for i := range result {
		result[i].DocumentID, result[i].DocumentRevision = documentID, revision
		result[i].ChunkHash, result[i].SourcePath = ChunkContentHash(result[i].Text), sourcePath
		result[i].MediaType, result[i].SourceFile = mediaType, sourcePath
		result[i].Page, result[i].BlockIndex, result[i].BlockMarker = 0, 0, ""
		result[i].BlockChunkIndex, result[i].BlockTotalChunks = i, len(result)
		result[i].ExtractionMethod, result[i].OCRConfidence = "text", -1
	}
	return result
}

func documentIDForSourcePath(sourcePath string) string {
	identity := filepath.Clean(sourcePath)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := sha256.Sum256([]byte(identity))
	return "doc-" + hex.EncodeToString(digest[:12])
}

func mediaTypeForSourcePath(sourcePath string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(sourcePath))); value != "" {
		if separator := strings.IndexByte(value, ';'); separator >= 0 {
			value = value[:separator]
		}
		return value
	}
	return "text/plain"
}

// migrateLegacyDocumentHistoryTx copies the collision-prone v1 rows into the
// snapshot-addressed store. The old tables are deliberately retained and
// protected by their immutable triggers; migration is deterministic and can
// safely run on every startup.
func migrateLegacyDocumentHistoryTx(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT h.document_id, h.document_revision, h.source_path, h.media_type,
h.chunk_count, h.graph_snapshot_id, h.reason, h.created
FROM document_history_snapshots h
LEFT JOIN document_history_legacy_migrations m
  ON m.document_id=h.document_id AND m.document_revision=h.document_revision
WHERE m.document_id IS NULL
ORDER BY h.created, h.document_id, h.document_revision`)
	if err != nil {
		return fmt.Errorf("read legacy document history: %w", err)
	}
	type legacySnapshot struct {
		documentID, revision, sourcePath, mediaType string
		chunkCount                                  int
		graphID, reason, created                    string
	}
	var legacy []legacySnapshot
	for rows.Next() {
		var item legacySnapshot
		if err := rows.Scan(&item.documentID, &item.revision, &item.sourcePath,
			&item.mediaType, &item.chunkCount, &item.graphID, &item.reason, &item.created); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy document history: %w", err)
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read legacy document history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy document history: %w", err)
	}

	for _, item := range legacy {
		entries, err := loadLegacyDocumentHistoryEntriesTx(tx, item.documentID, item.revision, item.chunkCount)
		if err != nil {
			return err
		}
		var graphDigest string
		if err := tx.QueryRow(`SELECT digest FROM knowledge_graph_snapshots WHERE id = ?`, item.graphID).Scan(&graphDigest); err != nil {
			return fmt.Errorf("read legacy graph snapshot %q: %w", item.graphID, err)
		}
		snapshot, err := writeDocumentHistoryVersionTx(tx, entries, item.graphID, graphDigest, item.created, item.reason)
		if err != nil {
			return fmt.Errorf("migrate legacy document %s revision %s: %w", item.documentID, item.revision, err)
		}
		if _, err := tx.Exec(`INSERT INTO document_history_legacy_migrations
(document_id, document_revision, snapshot_id, migrated) VALUES (?, ?, ?, ?)`, item.documentID,
			item.revision, snapshot.SnapshotID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record legacy document history migration: %w", err)
		}
	}
	return nil
}

func loadLegacyDocumentHistoryEntriesTx(tx *sql.Tx, documentID, revision string, expected int) ([]Entry, error) {
	rows, err := tx.Query(`SELECT chunk_index, title, text, tags, created, backend,
embedding_model, embedding_space, dims, embedding, source_file, chunk_label, total_chunks,
? AS document_id, ? AS document_revision, chunk_hash, source_path, media_type,
page, block_index, block_marker, block_chunk_index,
block_total_chunks, extraction_method, ocr_confidence, warnings, important
FROM document_history_chunks WHERE document_id = ? AND document_revision = ? ORDER BY chunk_index`,
		documentID, revision, documentID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]Entry, 0, expected)
	for rows.Next() {
		entry, err := scanHistoricalEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) != expected {
		return nil, fmt.Errorf("legacy document history %s/%s is incomplete: %d/%d chunks",
			documentID, revision, len(entries), expected)
	}
	return entries, nil
}

func writeKnowledgeGraphSnapshotTx(tx *sql.Tx, graph KnowledgeGraph, created string) (string, string, error) {
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return "", "", fmt.Errorf("archive knowledge graph snapshot: encode: %w", err)
	}
	digestBytes := sha256.Sum256(graphJSON)
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	graphID := "kgs-" + hex.EncodeToString(digestBytes[:16])
	if _, err := tx.Exec(`INSERT OR IGNORE INTO knowledge_graph_snapshots
(id, digest, graph_json, node_count, edge_count, created) VALUES (?, ?, ?, ?, ?, ?)`,
		graphID, digest, string(graphJSON), len(graph.Nodes), len(graph.Edges), created); err != nil {
		return "", "", fmt.Errorf("archive knowledge graph snapshot: %w", err)
	}
	return graphID, digest, nil
}

func (s *Store) ListDocumentHistorySnapshots(document string) ([]DocumentHistorySnapshot, error) {
	document = strings.TrimSpace(document)
	query := `SELECT sequence, snapshot_id, state_digest, document_id, document_revision,
source_path, media_type, chunk_count, graph_snapshot_id, reason, created
FROM document_history_versions`
	args := []any{}
	if document != "" {
		query += ` WHERE document_id = ? OR ` + sourcePathSQLPredicate("source_path")
		args = append(args, document, document)
	}
	query += ` ORDER BY sequence DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list document history snapshots: %w", err)
	}
	defer rows.Close()
	var snapshots []DocumentHistorySnapshot
	for rows.Next() {
		var snapshot DocumentHistorySnapshot
		if err := rows.Scan(&snapshot.Sequence, &snapshot.SnapshotID, &snapshot.StateDigest,
			&snapshot.DocumentID, &snapshot.DocumentRevision, &snapshot.SourcePath,
			&snapshot.MediaType, &snapshot.ChunkCount, &snapshot.GraphSnapshotID,
			&snapshot.Reason, &snapshot.Created); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) BuildCorpusRevisionDiff(document, fromRevision, toRevision string) (CorpusRevisionDiff, error) {
	document, fromRevision, toRevision = strings.TrimSpace(document), strings.TrimSpace(fromRevision), strings.TrimSpace(toRevision)
	if document == "" {
		return CorpusRevisionDiff{}, fmt.Errorf("corpus diff requires a document path or document ID")
	}
	s.mu.Lock()
	if err := s.refreshEntryCacheIfStaleUnlocked("corpus revision diff"); err != nil {
		s.mu.Unlock()
		return CorpusRevisionDiff{}, err
	}
	current := make([]Entry, 0)
	for i := range s.entries {
		entry := s.entries[i]
		if entry.DocumentID == document || coveragePathsEqual(entry.SourcePath, document) {
			current = append(current, cloneEntry(entry))
		}
	}
	s.mu.Unlock()
	snapshots, err := s.ListDocumentHistorySnapshots(document)
	if err != nil {
		return CorpusRevisionDiff{}, err
	}
	tombstone, tombstoneErr := loadDocumentTombstone(s.db, document)
	hasTombstone := tombstoneErr == nil
	if tombstoneErr != nil && !errors.Is(tombstoneErr, sql.ErrNoRows) {
		return CorpusRevisionDiff{}, fmt.Errorf("read current empty document state: %w", tombstoneErr)
	}
	if len(current) == 0 && len(snapshots) == 0 && !hasTombstone {
		return CorpusRevisionDiff{}, fmt.Errorf("corpus diff document %q was not found", document)
	}
	documentID, sourcePath := "", ""
	if len(current) > 0 {
		documentID, sourcePath = current[0].DocumentID, current[0].SourcePath
	} else if hasTombstone {
		documentID, sourcePath = tombstone.DocumentID, tombstone.SourcePath
	} else {
		documentID, sourcePath = snapshots[0].DocumentID, snapshots[0].SourcePath
	}
	if fromRevision == "" {
		if len(snapshots) == 0 {
			return CorpusRevisionDiff{}, fmt.Errorf("%w for %q; re-import a changed revision first", ErrDocumentHistoryUnavailable, sourcePath)
		}
		fromRevision = snapshots[0].SnapshotID
	}
	before, snapshot, err := s.loadHistoricalDocumentEntries(documentID, fromRevision)
	if err != nil {
		return CorpusRevisionDiff{}, err
	}
	var after []Entry
	var afterSnapshot DocumentHistorySnapshot
	if toRevision == "" || strings.EqualFold(toRevision, "current") {
		if len(current) == 0 && !hasTombstone {
			return CorpusRevisionDiff{}, fmt.Errorf("current revision of %q is unavailable", sourcePath)
		}
		after = current
		if hasTombstone {
			toRevision = tombstone.DocumentRevision
		} else {
			toRevision = current[0].DocumentRevision
		}
	} else if len(current) > 0 && current[0].DocumentRevision == toRevision {
		after = current
	} else {
		after, afterSnapshot, err = s.loadHistoricalDocumentEntries(documentID, toRevision)
		if err != nil {
			return CorpusRevisionDiff{}, err
		}
		toRevision = afterSnapshot.DocumentRevision
	}
	report := compareCorpusEntries(documentID, sourcePath, snapshot.DocumentRevision, toRevision, snapshot.Created, before, after)
	report.FromSnapshotID = snapshot.SnapshotID
	report.ToSnapshotID = afterSnapshot.SnapshotID
	return report, nil
}

func (s *Store) loadHistoricalDocumentEntries(documentID, revision string) ([]Entry, DocumentHistorySnapshot, error) {
	return s.loadHistoricalDocumentEntriesFull(documentID, revision)
}

func (s *Store) loadHistoricalDocumentEntriesFull(documentID, revision string) ([]Entry, DocumentHistorySnapshot, error) {
	var snapshot DocumentHistorySnapshot
	err := s.db.QueryRow(`SELECT sequence, snapshot_id, state_digest, document_id,
document_revision, source_path, media_type, chunk_count, graph_snapshot_id, reason, created
FROM document_history_versions
WHERE document_id = ? AND (snapshot_id = ? OR document_revision = ?)
ORDER BY CASE WHEN snapshot_id = ? THEN 0 ELSE 1 END, sequence DESC LIMIT 1`,
		documentID, revision, revision, revision).Scan(
		&snapshot.Sequence, &snapshot.SnapshotID, &snapshot.StateDigest, &snapshot.DocumentID,
		&snapshot.DocumentRevision, &snapshot.SourcePath, &snapshot.MediaType,
		&snapshot.ChunkCount, &snapshot.GraphSnapshotID, &snapshot.Reason, &snapshot.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, snapshot, fmt.Errorf("%w: revision %s", ErrDocumentHistoryUnavailable, revision)
	}
	if err != nil {
		return nil, snapshot, err
	}
	rows, err := s.db.Query(`SELECT chunk_index, title, text, tags, created, backend,
embedding_model, embedding_space, dims, embedding, source_file, chunk_label, total_chunks,
document_id, document_revision, chunk_hash, source_path, media_type, page, block_index, block_marker, block_chunk_index,
block_total_chunks, extraction_method, ocr_confidence, warnings, important
FROM document_history_version_chunks WHERE snapshot_id = ? ORDER BY chunk_index`, snapshot.SnapshotID)
	if err != nil {
		return nil, snapshot, err
	}
	defer rows.Close()
	entries := make([]Entry, 0, snapshot.ChunkCount)
	for rows.Next() {
		entry, err := scanHistoricalEntry(rows)
		if err != nil {
			return nil, snapshot, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, snapshot, err
	}
	if len(entries) != snapshot.ChunkCount {
		return nil, snapshot, fmt.Errorf("document history snapshot %s is incomplete: %d/%d chunks", revision, len(entries), snapshot.ChunkCount)
	}
	return entries, snapshot, nil
}

type historicalEntryScanner interface {
	Scan(dest ...any) error
}

func scanHistoricalEntry(scanner historicalEntryScanner) (Entry, error) {
	var entry Entry
	var tagsJSON, warningsJSON string
	var embedding []byte
	var important int
	if err := scanner.Scan(&entry.ChunkIndex, &entry.Title, &entry.Text, &tagsJSON, &entry.Created,
		&entry.Backend, &entry.EmbeddingModel, &entry.EmbeddingSpace, &entry.Dims, &embedding,
		&entry.SourceFile, &entry.ChunkLabel, &entry.TotalChunks, &entry.DocumentID,
		&entry.DocumentRevision, &entry.ChunkHash, &entry.SourcePath, &entry.MediaType,
		&entry.Page, &entry.BlockIndex, &entry.BlockMarker, &entry.BlockChunkIndex,
		&entry.BlockTotalChunks, &entry.ExtractionMethod, &entry.OCRConfidence, &warningsJSON,
		&important); err != nil {
		return entry, err
	}
	var err error
	entry.Tags, err = tagsFromJSON(tagsJSON)
	if err != nil {
		return entry, fmt.Errorf("decode historical chunk %d tags: %w", entry.ChunkIndex, err)
	}
	entry.Embedding, err = bytesToFloats(embedding)
	if err != nil {
		return entry, fmt.Errorf("decode historical chunk %d embedding: %w", entry.ChunkIndex, err)
	}
	if len(entry.Embedding) != entry.Dims {
		return entry, fmt.Errorf("decode historical chunk %d embedding: dimensions %d != %d",
			entry.ChunkIndex, len(entry.Embedding), entry.Dims)
	}
	if err := json.Unmarshal([]byte(warningsJSON), &entry.Warnings); err != nil {
		return entry, fmt.Errorf("decode historical chunk %d warnings: %w", entry.ChunkIndex, err)
	}
	entry.Important = important != 0
	if ChunkContentHash(entry.Text) != entry.ChunkHash {
		return entry, fmt.Errorf("historical chunk %d content hash mismatch", entry.ChunkIndex)
	}
	return entry, nil
}

func compareCorpusEntries(documentID, sourcePath, fromRevision, toRevision, snapshotAt string, before, after []Entry) CorpusRevisionDiff {
	report := CorpusRevisionDiff{Available: true, DocumentID: documentID, SourcePath: sourcePath,
		FromRevision: fromRevision, ToRevision: toRevision, FromSnapshotAt: snapshotAt,
		ComparisonBasis: "physical page + source block + block-local chunk; legacy fallback uses global chunk index"}
	type pair struct{ before, after *Entry }
	pairs := make(map[string]pair, len(before)+len(after))
	key := func(entry Entry) string {
		if entry.Page > 0 {
			return fmt.Sprintf("p:%09d:b:%09d:c:%09d", entry.Page, entry.BlockIndex, entry.BlockChunkIndex)
		}
		return fmt.Sprintf("i:%09d", entry.ChunkIndex)
	}
	for i := range before {
		item := pairs[key(before[i])]
		item.before = &before[i]
		pairs[key(before[i])] = item
	}
	for i := range after {
		item := pairs[key(after[i])]
		item.after = &after[i]
		pairs[key(after[i])] = item
	}
	keys := make([]string, 0, len(pairs))
	for value := range pairs {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	for _, value := range keys {
		item := pairs[value]
		change := CorpusChunkDiff{}
		if item.before != nil {
			change.Page, change.BlockIndex, change.BlockChunkIndex = item.before.Page, item.before.BlockIndex, item.before.BlockChunkIndex
			change.BeforeIndex, change.BeforeHash, change.BeforeText = item.before.ChunkIndex, item.before.ChunkHash, item.before.Text
		}
		if item.after != nil {
			change.Page, change.BlockIndex, change.BlockChunkIndex = item.after.Page, item.after.BlockIndex, item.after.BlockChunkIndex
			change.AfterIndex, change.AfterHash, change.AfterText = item.after.ChunkIndex, item.after.ChunkHash, item.after.Text
		}
		switch {
		case item.before == nil:
			change.State = CorpusChunkAdded
			report.AddedChunks++
		case item.after == nil:
			change.State = CorpusChunkRemoved
			report.RemovedChunks++
		case item.before.ChunkHash != item.after.ChunkHash || item.before.Text != item.after.Text:
			change.State = CorpusChunkChanged
			report.ChangedChunks++
		default:
			change.State = CorpusChunkUnchanged
			report.UnchangedChunks++
			continue
		}
		report.Changes = append(report.Changes, change)
	}
	return report
}
