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
	"time"
)

var ErrDocumentRestoreStateChanged = errors.New("document restore state changed after preview")

type DocumentRestorePlan struct {
	PlanDigest                   string             `json:"plan_digest"`
	DocumentID                   string             `json:"document_id"`
	SourcePath                   string             `json:"source_path"`
	CurrentRevision              string             `json:"current_revision"`
	TargetRevision               string             `json:"target_revision"`
	CurrentChunks                int                `json:"current_chunks"`
	TargetChunks                 int                `json:"target_chunks"`
	CurrentGraphSnapshotID       string             `json:"current_graph_snapshot_id"`
	CurrentGraphDigest           string             `json:"current_graph_digest"`
	CurrentStateDigest           string             `json:"current_state_digest"`
	CurrentGraphNodes            int                `json:"current_graph_nodes"`
	CurrentGraphEdges            int                `json:"current_graph_edges"`
	TargetGraphSnapshotID        string             `json:"target_graph_snapshot_id"`
	TargetGraphDigest            string             `json:"target_graph_digest"`
	TargetGraphNodes             int                `json:"target_graph_nodes"`
	TargetGraphEdges             int                `json:"target_graph_edges"`
	RollbackOf                   string             `json:"rollback_of,omitempty"`
	ChunkDiff                    CorpusRevisionDiff `json:"chunk_diff"`
	RequiresExplicitConfirmation bool               `json:"requires_explicit_confirmation"`
	Warning                      string             `json:"warning"`
}

type DocumentRestoreRun struct {
	ID                    string `json:"id"`
	DocumentID            string `json:"document_id"`
	SourcePath            string `json:"source_path"`
	FromRevision          string `json:"from_revision"`
	TargetRevision        string `json:"target_revision"`
	BeforeGraphSnapshotID string `json:"before_graph_snapshot_id"`
	TargetGraphSnapshotID string `json:"target_graph_snapshot_id"`
	PlanDigest            string `json:"plan_digest"`
	RollbackOf            string `json:"rollback_of,omitempty"`
	RestoredChunks        int    `json:"restored_chunks"`
	Created               string `json:"created"`
}

type knowledgeGraphSnapshot struct {
	ID        string
	Digest    string
	Graph     KnowledgeGraph
	NodeCount int
	EdgeCount int
}

func (s *Store) BuildDocumentRestorePlan(document, revision string) (DocumentRestorePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buildDocumentRestorePlanUnlocked(document, revision, "", "")
}

func (s *Store) BuildDocumentRestoreRollbackPlan(runID string) (DocumentRestorePlan, error) {
	run, err := s.GetDocumentRestoreRun(runID)
	if err != nil {
		return DocumentRestorePlan{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buildDocumentRestorePlanUnlocked(run.SourcePath, run.FromRevision, run.BeforeGraphSnapshotID, run.ID)
}

func (s *Store) buildDocumentRestorePlanUnlocked(document, revision, targetGraphOverride, rollbackOf string) (DocumentRestorePlan, error) {
	document, revision = strings.TrimSpace(document), strings.TrimSpace(revision)
	if document == "" || revision == "" {
		return DocumentRestorePlan{}, fmt.Errorf("restore preview requires document and target revision")
	}
	current := make([]Entry, 0)
	for i := range s.entries {
		entry := s.entries[i]
		if entry.DocumentID == document || coveragePathsEqual(entry.SourcePath, document) {
			current = append(current, cloneEntry(entry))
		}
	}
	if len(current) == 0 {
		return DocumentRestorePlan{}, fmt.Errorf("current document %q was not found", document)
	}
	sort.Slice(current, func(i, j int) bool { return current[i].ChunkIndex < current[j].ChunkIndex })
	if current[0].DocumentRevision == revision {
		return DocumentRestorePlan{}, fmt.Errorf("target revision %s is already current", revision)
	}
	targetEntries, targetSnapshot, err := s.loadHistoricalDocumentEntriesFull(current[0].DocumentID, revision)
	if err != nil {
		return DocumentRestorePlan{}, err
	}
	targetGraphID := targetSnapshot.GraphSnapshotID
	if targetGraphOverride != "" {
		targetGraphID = targetGraphOverride
	}
	targetGraph, err := s.loadKnowledgeGraphSnapshot(targetGraphID)
	if err != nil {
		return DocumentRestorePlan{}, fmt.Errorf("load target graph snapshot: %w", err)
	}
	currentGraph, err := loadKnowledgeGraphFromQuerier(s.db)
	if err != nil {
		return DocumentRestorePlan{}, fmt.Errorf("load current graph for restore preview: %w", err)
	}
	currentGraphJSON, err := json.Marshal(currentGraph)
	if err != nil {
		return DocumentRestorePlan{}, err
	}
	currentGraphDigest := prefixedSHA256(currentGraphJSON)
	currentGraphID := graphSnapshotIDFromDigest(currentGraphDigest)
	currentStateDigest, err := documentRestoreCurrentStateDigest(current, currentGraphDigest)
	if err != nil {
		return DocumentRestorePlan{}, err
	}
	diff := compareCorpusEntries(current[0].DocumentID, current[0].SourcePath,
		current[0].DocumentRevision, revision, targetSnapshot.Created, current, targetEntries)
	plan := DocumentRestorePlan{
		DocumentID: current[0].DocumentID, SourcePath: current[0].SourcePath,
		CurrentRevision: current[0].DocumentRevision, TargetRevision: revision,
		CurrentChunks: len(current), TargetChunks: len(targetEntries),
		CurrentGraphSnapshotID: currentGraphID, CurrentGraphDigest: currentGraphDigest,
		CurrentStateDigest: currentStateDigest,
		CurrentGraphNodes:  len(currentGraph.Nodes), CurrentGraphEdges: len(currentGraph.Edges),
		TargetGraphSnapshotID: targetGraph.ID, TargetGraphDigest: targetGraph.Digest,
		TargetGraphNodes: targetGraph.NodeCount, TargetGraphEdges: targetGraph.EdgeCount,
		RollbackOf: rollbackOf, ChunkDiff: diff, RequiresExplicitConfirmation: true,
		Warning: "Восстановление заменит выбранный документ и весь knowledge graph снимком указанного момента; журналы review/edit/learning и именованные визуальные виды останутся append-only и не удаляются.",
	}
	pin := struct {
		DocumentID, SourcePath, CurrentRevision, TargetRevision string
		CurrentGraphDigest, TargetGraphDigest                   string
		CurrentChunkPins                                        []string
		TargetChunkPins                                         []string
		RollbackOf                                              string
	}{
		DocumentID: plan.DocumentID, SourcePath: plan.SourcePath,
		CurrentRevision: plan.CurrentRevision, TargetRevision: plan.TargetRevision,
		CurrentGraphDigest: plan.CurrentGraphDigest, TargetGraphDigest: plan.TargetGraphDigest,
		RollbackOf: rollbackOf,
	}
	for _, entry := range current {
		entryDigest, err := documentRestoreEntryDigest(entry)
		if err != nil {
			return DocumentRestorePlan{}, err
		}
		pin.CurrentChunkPins = append(pin.CurrentChunkPins, entryDigest)
	}
	for _, entry := range targetEntries {
		entryDigest, err := documentRestoreEntryDigest(entry)
		if err != nil {
			return DocumentRestorePlan{}, err
		}
		pin.TargetChunkPins = append(pin.TargetChunkPins, entryDigest)
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		return DocumentRestorePlan{}, err
	}
	plan.PlanDigest = prefixedSHA256(encoded)
	return plan, nil
}

func documentRestoreEntryDigest(entry Entry) (string, error) {
	embedding, err := floatsToBytes(entry.Embedding)
	if err != nil {
		return "", err
	}
	pin := struct {
		Title, Text, Created, Backend, EmbeddingModel, EmbeddingSpace string
		SourceFile, ChunkLabel, DocumentID, DocumentRevision          string
		ChunkHash, SourcePath, MediaType, BlockMarker                 string
		ExtractionMethod                                              string
		Tags, Warnings                                                []string
		ChunkIndex, TotalChunks, Dims, Page, BlockIndex               int
		BlockChunkIndex, BlockTotalChunks                             int
		OCRConfidence                                                 float64
		Important                                                     bool
		Embedding                                                     []byte
	}{
		Title: entry.Title, Text: entry.Text, Created: entry.Created, Backend: entry.Backend,
		EmbeddingModel: entry.EmbeddingModel, EmbeddingSpace: entry.EmbeddingSpace,
		SourceFile: entry.SourceFile, ChunkLabel: entry.ChunkLabel, DocumentID: entry.DocumentID,
		DocumentRevision: entry.DocumentRevision, ChunkHash: entry.ChunkHash,
		SourcePath: entry.SourcePath, MediaType: entry.MediaType, BlockMarker: entry.BlockMarker,
		ExtractionMethod: entry.ExtractionMethod,
		Tags:             append([]string(nil), entry.Tags...),
		Warnings:         append([]string(nil), entry.Warnings...),
		ChunkIndex:       entry.ChunkIndex, TotalChunks: entry.TotalChunks, Dims: entry.Dims,
		Page: entry.Page, BlockIndex: entry.BlockIndex, BlockChunkIndex: entry.BlockChunkIndex,
		BlockTotalChunks: entry.BlockTotalChunks, OCRConfidence: entry.OCRConfidence,
		Important: entry.Important, Embedding: embedding,
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		return "", err
	}
	return prefixedSHA256(encoded), nil
}

func documentRestoreCurrentStateDigest(entries []Entry, graphDigest string) (string, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ChunkIndex < entries[j].ChunkIndex })
	pins := make([]string, 0, len(entries))
	for _, entry := range entries {
		digest, err := documentRestoreEntryDigest(entry)
		if err != nil {
			return "", err
		}
		pins = append(pins, digest)
	}
	encoded, err := json.Marshal(struct {
		GraphDigest string
		EntryPins   []string
	}{GraphDigest: graphDigest, EntryPins: pins})
	if err != nil {
		return "", err
	}
	return prefixedSHA256(encoded), nil
}

func (s *Store) ApplyDocumentRestore(document, revision, expectedPlanDigest string) (DocumentRestoreRun, error) {
	return s.applyDocumentRestore(document, revision, "", "", expectedPlanDigest)
}

func (s *Store) ApplyDocumentRestoreRollback(runID, expectedPlanDigest string) (DocumentRestoreRun, error) {
	previous, err := s.GetDocumentRestoreRun(runID)
	if err != nil {
		return DocumentRestoreRun{}, err
	}
	return s.applyDocumentRestore(previous.SourcePath, previous.FromRevision, previous.BeforeGraphSnapshotID, previous.ID, expectedPlanDigest)
}

func (s *Store) applyDocumentRestore(document, revision, targetGraphOverride, rollbackOf, expectedPlanDigest string) (DocumentRestoreRun, error) {
	expectedPlanDigest = strings.TrimSpace(expectedPlanDigest)
	if !validSHA256Digest(expectedPlanDigest) {
		return DocumentRestoreRun{}, fmt.Errorf("restore requires the exact sha256 plan digest from preview")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := s.buildDocumentRestorePlanUnlocked(document, revision, targetGraphOverride, rollbackOf)
	if err != nil {
		return DocumentRestoreRun{}, err
	}
	if plan.PlanDigest != expectedPlanDigest {
		return DocumentRestoreRun{}, fmt.Errorf("%w: expected %s, current %s", ErrDocumentRestoreStateChanged, expectedPlanDigest, plan.PlanDigest)
	}
	targetEntries, _, err := s.loadHistoricalDocumentEntriesFull(plan.DocumentID, plan.TargetRevision)
	if err != nil {
		return DocumentRestoreRun{}, err
	}
	targetGraph, err := s.loadKnowledgeGraphSnapshot(plan.TargetGraphSnapshotID)
	if err != nil {
		return DocumentRestoreRun{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	runID, err := newKnowledgeLearningID("restore-run-")
	if err != nil {
		return DocumentRestoreRun{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DocumentRestoreRun{}, fmt.Errorf("begin document restore: %w", err)
	}
	rollback := func(cause error) (DocumentRestoreRun, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return DocumentRestoreRun{}, fmt.Errorf("%v; document restore rollback failed: %w", cause, rollbackErr)
		}
		return DocumentRestoreRun{}, cause
	}
	currentEntries, err := loadDocumentEntriesForRestore(tx, plan.SourcePath)
	if err != nil {
		return rollback(err)
	}
	currentGraph, err := loadKnowledgeGraphFromQuerier(tx)
	if err != nil {
		return rollback(err)
	}
	currentGraphJSON, err := json.Marshal(currentGraph)
	if err != nil {
		return rollback(err)
	}
	transactionStateDigest, err := documentRestoreCurrentStateDigest(currentEntries, prefixedSHA256(currentGraphJSON))
	if err != nil {
		return rollback(err)
	}
	if transactionStateDigest != plan.CurrentStateDigest {
		return rollback(fmt.Errorf("%w inside SQLite transaction: expected %s, current %s",
			ErrDocumentRestoreStateChanged, plan.CurrentStateDigest, transactionStateDigest))
	}
	if err := archiveDocumentHistoryTx(tx, currentEntries, currentGraph, now, "before_document_restore"); err != nil {
		return rollback(err)
	}
	beforeGraphID, _, err := writeKnowledgeGraphSnapshotTx(tx, currentGraph, now)
	if err != nil {
		return rollback(err)
	}
	restored, err := restoreDocumentEntriesTx(tx, plan.SourcePath, targetEntries)
	if err != nil {
		return rollback(err)
	}
	if err := replaceKnowledgeGraphTx(tx, targetGraph.Graph); err != nil {
		return rollback(err)
	}
	run := DocumentRestoreRun{
		ID: runID, DocumentID: plan.DocumentID, SourcePath: plan.SourcePath,
		FromRevision: plan.CurrentRevision, TargetRevision: plan.TargetRevision,
		BeforeGraphSnapshotID: beforeGraphID, TargetGraphSnapshotID: plan.TargetGraphSnapshotID,
		PlanDigest: plan.PlanDigest, RollbackOf: rollbackOf, RestoredChunks: len(restored), Created: now,
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_restore_runs
(id, document_id, source_path, from_revision, target_revision, before_graph_snapshot_id,
 target_graph_snapshot_id, plan_digest, rollback_of, restored_chunks, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, run.DocumentID, run.SourcePath,
		run.FromRevision, run.TargetRevision, run.BeforeGraphSnapshotID, run.TargetGraphSnapshotID,
		run.PlanDigest, run.RollbackOf, run.RestoredChunks, run.Created); err != nil {
		return rollback(fmt.Errorf("record document restore: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return DocumentRestoreRun{}, fmt.Errorf("commit document restore: %w", err)
	}
	entries := s.entries[:0]
	vectors := s.vectors[:0]
	for i := range s.entries {
		if coveragePathsEqual(s.entries[i].SourcePath, plan.SourcePath) {
			continue
		}
		entries = append(entries, s.entries[i])
		vectors = append(vectors, s.vectors[i])
	}
	for i := range restored {
		entries = append(entries, restored[i])
		vectors = append(vectors, restored[i].Embedding)
	}
	s.entries, s.vectors, s.lexicalDirty = entries, vectors, true
	return run, nil
}

type documentRestoreQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func loadDocumentEntriesForRestore(q documentRestoreQuerier, sourcePath string) ([]Entry, error) {
	rows, err := q.Query(`SELECT id, title, text, tags, created, backend, embedding_model, embedding_space, dims, embedding,
source_file, chunk_label, chunk_index, total_chunks, document_id, document_revision, chunk_hash,
source_path, media_type, page, block_index, block_marker, block_chunk_index, block_total_chunks,
extraction_method, ocr_confidence, warnings, important
FROM entries WHERE source_file = ? ORDER BY chunk_index`, sourcePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var entry Entry
		var tagsJSON, warningsJSON string
		var embedding []byte
		var important int
		if err := rows.Scan(&entry.ID, &entry.Title, &entry.Text, &tagsJSON, &entry.Created,
			&entry.Backend, &entry.EmbeddingModel, &entry.EmbeddingSpace, &entry.Dims, &embedding,
			&entry.SourceFile, &entry.ChunkLabel, &entry.ChunkIndex, &entry.TotalChunks,
			&entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash, &entry.SourcePath,
			&entry.MediaType, &entry.Page, &entry.BlockIndex, &entry.BlockMarker,
			&entry.BlockChunkIndex, &entry.BlockTotalChunks, &entry.ExtractionMethod,
			&entry.OCRConfidence, &warningsJSON, &important); err != nil {
			return nil, err
		}
		entry.Tags, err = tagsFromJSON(tagsJSON)
		if err != nil {
			return nil, err
		}
		entry.Embedding, err = bytesToFloats(embedding)
		if err != nil || len(entry.Embedding) != entry.Dims {
			return nil, fmt.Errorf("current restore chunk %d has invalid embedding dimensions", entry.ChunkIndex)
		}
		if err := json.Unmarshal([]byte(warningsJSON), &entry.Warnings); err != nil {
			return nil, err
		}
		entry.Important = important != 0
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("current document %q disappeared before restore", sourcePath)
	}
	return entries, nil
}

func restoreDocumentEntriesTx(tx *sql.Tx, sourcePath string, entries []Entry) ([]Entry, error) {
	restored := make([]Entry, len(entries))
	for i := range entries {
		entry := cloneEntry(entries[i])
		entry.SourceFile, entry.SourcePath = sourcePath, sourcePath
		tagsJSON, err := tagsToJSON(entry.Tags)
		if err != nil {
			return nil, err
		}
		embedding, err := floatsToBytes(entry.Embedding)
		if err != nil {
			return nil, err
		}
		warnings, err := json.Marshal(entry.Warnings)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(upsertChunkSQL,
			entry.Title, entry.Text, tagsJSON, entry.Created, entry.Backend, entry.EmbeddingModel,
			entry.EmbeddingSpace, entry.Dims, embedding, entry.SourceFile, entry.ChunkLabel,
			entry.ChunkIndex, entry.TotalChunks, entry.DocumentID, entry.DocumentRevision,
			entry.ChunkHash, entry.SourcePath, entry.MediaType, entry.Page, entry.BlockIndex,
			entry.BlockMarker, entry.BlockChunkIndex, entry.BlockTotalChunks, entry.ExtractionMethod,
			entry.OCRConfidence, string(warnings), boolToInt(entry.Important)); err != nil {
			return nil, fmt.Errorf("restore document chunk %d: %w", entry.ChunkIndex, err)
		}
		if err := tx.QueryRow(`SELECT id FROM entries WHERE source_file = ? AND chunk_index = ?`,
			sourcePath, entry.ChunkIndex).Scan(&entry.ID); err != nil {
			return nil, err
		}
		restored[i] = entry
	}
	if _, err := tx.Exec(`DELETE FROM entries WHERE source_file = ? AND chunk_index >= ?`, sourcePath, len(entries)); err != nil {
		return nil, fmt.Errorf("prune restored document tail: %w", err)
	}
	return restored, nil
}

func replaceKnowledgeGraphTx(tx *sql.Tx, graph KnowledgeGraph) error {
	graph = normalizeKnowledgeGraph(graph)
	if err := ValidateKnowledgeGraph(graph); err != nil {
		return fmt.Errorf("restore knowledge graph validation: %w", err)
	}
	for _, statement := range []string{
		`DELETE FROM knowledge_edge_evidence`, `DELETE FROM knowledge_edges`,
		`DELETE FROM knowledge_node_evidence`, `DELETE FROM knowledge_nodes`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("clear current knowledge graph: %w", err)
		}
	}
	for _, node := range graph.Nodes {
		if _, err := tx.Exec(`INSERT INTO knowledge_nodes
(id, kind, label, body, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, node.ID, node.Kind, node.Label, node.Body,
			node.Status, node.Origin, node.Confidence, node.Created, node.Updated); err != nil {
			return fmt.Errorf("restore knowledge node %q: %w", node.ID, err)
		}
		for ordinal, anchor := range node.Evidence {
			if err := insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", node.ID, ordinal, anchor); err != nil {
				return err
			}
		}
	}
	for _, edge := range graph.Edges {
		if _, err := tx.Exec(`INSERT INTO knowledge_edges
(id, from_node, to_node, kind, label, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, edge.ID, edge.From, edge.To, edge.Kind,
			edge.Label, edge.Status, edge.Origin, edge.Confidence, edge.Created, edge.Updated); err != nil {
			return fmt.Errorf("restore knowledge edge %q: %w", edge.ID, err)
		}
		for ordinal, anchor := range edge.Evidence {
			if err := insertKnowledgeEvidence(tx, "knowledge_edge_evidence", "edge_id", edge.ID, ordinal, anchor); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) loadKnowledgeGraphSnapshot(id string) (knowledgeGraphSnapshot, error) {
	var snapshot knowledgeGraphSnapshot
	var graphJSON string
	err := s.db.QueryRow(`SELECT id, digest, graph_json, node_count, edge_count
FROM knowledge_graph_snapshots WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&snapshot.ID, &snapshot.Digest, &graphJSON, &snapshot.NodeCount, &snapshot.EdgeCount)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, fmt.Errorf("knowledge graph snapshot %q was not found", id)
	}
	if err != nil {
		return snapshot, err
	}
	if prefixedSHA256([]byte(graphJSON)) != snapshot.Digest {
		return snapshot, fmt.Errorf("knowledge graph snapshot %q digest mismatch", id)
	}
	if graphSnapshotIDFromDigest(snapshot.Digest) != snapshot.ID {
		return snapshot, fmt.Errorf("knowledge graph snapshot %q identity mismatch", id)
	}
	if err := json.Unmarshal([]byte(graphJSON), &snapshot.Graph); err != nil {
		return snapshot, fmt.Errorf("decode knowledge graph snapshot %q: %w", id, err)
	}
	if err := ValidateKnowledgeGraph(snapshot.Graph); err != nil || len(snapshot.Graph.Nodes) != snapshot.NodeCount || len(snapshot.Graph.Edges) != snapshot.EdgeCount {
		return snapshot, fmt.Errorf("knowledge graph snapshot %q is invalid or incomplete: %w", id, err)
	}
	return snapshot, nil
}

func prefixedSHA256(data []byte) string {
	digest := sha256Bytes(data)
	return "sha256:" + hex.EncodeToString(digest)
}

func sha256Bytes(data []byte) []byte {
	digest := sha256.Sum256(data)
	return digest[:]
}

func graphSnapshotIDFromDigest(digest string) string {
	value := strings.TrimPrefix(digest, "sha256:")
	if len(value) < 32 {
		return ""
	}
	return "kgs-" + value[:32]
}

func (s *Store) ListDocumentRestoreRuns(limit int) ([]DocumentRestoreRun, error) {
	if limit <= 0 || limit > 10000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, document_id, source_path, from_revision, target_revision,
before_graph_snapshot_id, target_graph_snapshot_id, plan_digest, rollback_of, restored_chunks, created
FROM knowledge_restore_runs ORDER BY created DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DocumentRestoreRun
	for rows.Next() {
		var run DocumentRestoreRun
		if err := rows.Scan(&run.ID, &run.DocumentID, &run.SourcePath, &run.FromRevision,
			&run.TargetRevision, &run.BeforeGraphSnapshotID, &run.TargetGraphSnapshotID,
			&run.PlanDigest, &run.RollbackOf, &run.RestoredChunks, &run.Created); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) GetDocumentRestoreRun(id string) (DocumentRestoreRun, error) {
	var run DocumentRestoreRun
	err := s.db.QueryRow(`SELECT id, document_id, source_path, from_revision, target_revision,
before_graph_snapshot_id, target_graph_snapshot_id, plan_digest, rollback_of, restored_chunks, created
FROM knowledge_restore_runs WHERE id = ?`, strings.TrimSpace(id)).Scan(&run.ID, &run.DocumentID,
		&run.SourcePath, &run.FromRevision, &run.TargetRevision, &run.BeforeGraphSnapshotID,
		&run.TargetGraphSnapshotID, &run.PlanDigest, &run.RollbackOf, &run.RestoredChunks, &run.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return run, fmt.Errorf("document restore run %q was not found", id)
	}
	return run, err
}
