package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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

CREATE INDEX IF NOT EXISTS idx_document_history_source
    ON document_history_snapshots(source_path, created);

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
CREATE TRIGGER IF NOT EXISTS knowledge_restore_runs_no_update
BEFORE UPDATE ON knowledge_restore_runs BEGIN
    SELECT RAISE(ABORT, 'knowledge restore history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS knowledge_restore_runs_no_delete
BEFORE DELETE ON knowledge_restore_runs BEGIN
    SELECT RAISE(ABORT, 'knowledge restore history is append-only');
END;
`

var ErrDocumentHistoryUnavailable = errors.New("document history snapshot is unavailable")

type DocumentHistorySnapshot struct {
	DocumentID       string `json:"document_id"`
	DocumentRevision string `json:"document_revision"`
	SourcePath       string `json:"source_path"`
	MediaType        string `json:"media_type,omitempty"`
	ChunkCount       int    `json:"chunk_count"`
	GraphSnapshotID  string `json:"graph_snapshot_id"`
	Reason           string `json:"reason"`
	Created          string `json:"created"`
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
	ToRevision      string            `json:"to_revision"`
	FromSnapshotAt  string            `json:"from_snapshot_at,omitempty"`
	AddedChunks     int               `json:"added_chunks"`
	ChangedChunks   int               `json:"changed_chunks"`
	RemovedChunks   int               `json:"removed_chunks"`
	UnchangedChunks int               `json:"unchanged_chunks"`
	Changes         []CorpusChunkDiff `json:"changes,omitempty"`
	ComparisonBasis string            `json:"comparison_basis"`
}

func archiveDocumentHistoryTx(tx *sql.Tx, entries []Entry, graph KnowledgeGraph, created, reason string) error {
	if len(entries) == 0 {
		return nil
	}
	first := entries[0]
	if first.DocumentID == "" || first.DocumentRevision == "" || first.SourcePath == "" {
		return fmt.Errorf("archive document history: current document has incomplete provenance")
	}
	for i := range entries {
		entry := entries[i]
		if entry.DocumentID != first.DocumentID || entry.DocumentRevision != first.DocumentRevision || entry.SourcePath != first.SourcePath {
			return fmt.Errorf("archive document history: chunk %d has inconsistent document identity", i)
		}
	}
	graphID, _, err := writeKnowledgeGraphSnapshotTx(tx, graph, created)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO document_history_snapshots
(document_id, document_revision, source_path, media_type, chunk_count, graph_snapshot_id, reason, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, first.DocumentID, first.DocumentRevision, first.SourcePath,
		first.MediaType, len(entries), graphID, reason, created)
	if err != nil {
		return fmt.Errorf("archive document snapshot: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("archive document snapshot result: %w", err)
	}
	if inserted == 0 {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ChunkIndex < entries[j].ChunkIndex })
	for _, entry := range entries {
		tagsJSON, err := tagsToJSON(entry.Tags)
		if err != nil {
			return err
		}
		warningsJSON, err := json.Marshal(entry.Warnings)
		if err != nil {
			return fmt.Errorf("archive chunk %d warnings: %w", entry.ChunkIndex, err)
		}
		embedding, err := floatsToBytes(entry.Embedding)
		if err != nil {
			return fmt.Errorf("archive chunk %d embedding: %w", entry.ChunkIndex, err)
		}
		_, err = tx.Exec(`INSERT INTO document_history_chunks
(document_id, document_revision, chunk_index, title, text, tags, created, backend,
 embedding_model, embedding_space, dims, embedding, source_file, chunk_label, total_chunks,
 chunk_hash, source_path, media_type, page, block_index, block_marker, block_chunk_index,
 block_total_chunks, extraction_method, ocr_confidence, warnings, important)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.DocumentID, entry.DocumentRevision, entry.ChunkIndex, entry.Title, entry.Text,
			tagsJSON, entry.Created, entry.Backend, entry.EmbeddingModel, entry.EmbeddingSpace,
			entry.Dims, embedding, entry.SourceFile, entry.ChunkLabel, entry.TotalChunks,
			entry.ChunkHash, entry.SourcePath, entry.MediaType, entry.Page, entry.BlockIndex,
			entry.BlockMarker, entry.BlockChunkIndex, entry.BlockTotalChunks, entry.ExtractionMethod,
			entry.OCRConfidence, string(warningsJSON), boolToInt(entry.Important))
		if err != nil {
			return fmt.Errorf("archive document chunk %d: %w", entry.ChunkIndex, err)
		}
	}
	return nil
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
	query := `SELECT document_id, document_revision, source_path, media_type, chunk_count,
graph_snapshot_id, reason, created FROM document_history_snapshots`
	args := []any{}
	if document != "" {
		query += ` WHERE document_id = ? OR LOWER(source_path) = LOWER(?)`
		args = append(args, document, document)
	}
	query += ` ORDER BY created DESC, document_id, document_revision`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list document history snapshots: %w", err)
	}
	defer rows.Close()
	var snapshots []DocumentHistorySnapshot
	for rows.Next() {
		var snapshot DocumentHistorySnapshot
		if err := rows.Scan(&snapshot.DocumentID, &snapshot.DocumentRevision, &snapshot.SourcePath,
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
	s.mu.RLock()
	current := make([]Entry, 0)
	for i := range s.entries {
		entry := s.entries[i]
		if entry.DocumentID == document || coveragePathsEqual(entry.SourcePath, document) {
			current = append(current, cloneEntry(entry))
		}
	}
	s.mu.RUnlock()
	snapshots, err := s.ListDocumentHistorySnapshots(document)
	if err != nil {
		return CorpusRevisionDiff{}, err
	}
	if len(current) == 0 && len(snapshots) == 0 {
		return CorpusRevisionDiff{}, fmt.Errorf("corpus diff document %q was not found", document)
	}
	documentID, sourcePath := "", ""
	if len(current) > 0 {
		documentID, sourcePath = current[0].DocumentID, current[0].SourcePath
	} else {
		documentID, sourcePath = snapshots[0].DocumentID, snapshots[0].SourcePath
	}
	if fromRevision == "" {
		if len(snapshots) == 0 {
			return CorpusRevisionDiff{}, fmt.Errorf("%w for %q; re-import a changed revision first", ErrDocumentHistoryUnavailable, sourcePath)
		}
		fromRevision = snapshots[0].DocumentRevision
	}
	before, snapshot, err := s.loadHistoricalDocumentEntries(documentID, fromRevision)
	if err != nil {
		return CorpusRevisionDiff{}, err
	}
	var after []Entry
	if toRevision == "" || strings.EqualFold(toRevision, "current") {
		if len(current) == 0 {
			return CorpusRevisionDiff{}, fmt.Errorf("current revision of %q is unavailable", sourcePath)
		}
		after = current
		toRevision = current[0].DocumentRevision
	} else if len(current) > 0 && current[0].DocumentRevision == toRevision {
		after = current
	} else {
		after, _, err = s.loadHistoricalDocumentEntries(documentID, toRevision)
		if err != nil {
			return CorpusRevisionDiff{}, err
		}
	}
	return compareCorpusEntries(documentID, sourcePath, fromRevision, toRevision, snapshot.Created, before, after), nil
}

func (s *Store) loadHistoricalDocumentEntries(documentID, revision string) ([]Entry, DocumentHistorySnapshot, error) {
	return s.loadHistoricalDocumentEntriesFull(documentID, revision)
}

func (s *Store) loadHistoricalDocumentEntriesFull(documentID, revision string) ([]Entry, DocumentHistorySnapshot, error) {
	var snapshot DocumentHistorySnapshot
	err := s.db.QueryRow(`SELECT document_id, document_revision, source_path, media_type, chunk_count,
graph_snapshot_id, reason, created FROM document_history_snapshots
WHERE document_id = ? AND document_revision = ?`, documentID, revision).Scan(
		&snapshot.DocumentID, &snapshot.DocumentRevision, &snapshot.SourcePath, &snapshot.MediaType,
		&snapshot.ChunkCount, &snapshot.GraphSnapshotID, &snapshot.Reason, &snapshot.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, snapshot, fmt.Errorf("%w: revision %s", ErrDocumentHistoryUnavailable, revision)
	}
	if err != nil {
		return nil, snapshot, err
	}
	rows, err := s.db.Query(`SELECT chunk_index, title, text, tags, created, backend,
embedding_model, embedding_space, dims, embedding, source_file, chunk_label, total_chunks,
chunk_hash, source_path, media_type, page, block_index, block_marker, block_chunk_index,
block_total_chunks, extraction_method, ocr_confidence, warnings, important
FROM document_history_chunks WHERE document_id = ? AND document_revision = ? ORDER BY chunk_index`, documentID, revision)
	if err != nil {
		return nil, snapshot, err
	}
	defer rows.Close()
	entries := make([]Entry, 0, snapshot.ChunkCount)
	for rows.Next() {
		var entry Entry
		var tagsJSON, warningsJSON string
		var embedding []byte
		var important int
		entry.DocumentID, entry.DocumentRevision = documentID, revision
		if err := rows.Scan(&entry.ChunkIndex, &entry.Title, &entry.Text, &tagsJSON, &entry.Created,
			&entry.Backend, &entry.EmbeddingModel, &entry.EmbeddingSpace, &entry.Dims, &embedding,
			&entry.SourceFile, &entry.ChunkLabel, &entry.TotalChunks, &entry.ChunkHash, &entry.SourcePath,
			&entry.MediaType, &entry.Page, &entry.BlockIndex, &entry.BlockMarker, &entry.BlockChunkIndex,
			&entry.BlockTotalChunks, &entry.ExtractionMethod, &entry.OCRConfidence, &warningsJSON,
			&important); err != nil {
			return nil, snapshot, err
		}
		entry.Tags, err = tagsFromJSON(tagsJSON)
		if err != nil {
			return nil, snapshot, fmt.Errorf("decode historical chunk %d tags: %w", entry.ChunkIndex, err)
		}
		entry.Embedding, err = bytesToFloats(embedding)
		if err != nil {
			return nil, snapshot, fmt.Errorf("decode historical chunk %d embedding: %w", entry.ChunkIndex, err)
		}
		if len(entry.Embedding) != entry.Dims {
			return nil, snapshot, fmt.Errorf("decode historical chunk %d embedding: dimensions %d != %d", entry.ChunkIndex, len(entry.Embedding), entry.Dims)
		}
		if err := json.Unmarshal([]byte(warningsJSON), &entry.Warnings); err != nil {
			return nil, snapshot, fmt.Errorf("decode historical chunk %d warnings: %w", entry.ChunkIndex, err)
		}
		entry.Important = important != 0
		if ChunkContentHash(entry.Text) != entry.ChunkHash {
			return nil, snapshot, fmt.Errorf("historical chunk %d content hash mismatch", entry.ChunkIndex)
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
