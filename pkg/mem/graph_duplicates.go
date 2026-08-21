package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	DefaultKnowledgeDuplicateThreshold = 0.92
	MaxKnowledgeDuplicateCandidates    = 1000
)

type KnowledgeNodeVector struct {
	NodeID     string    `json:"node_id"`
	NodeDigest string    `json:"node_digest"`
	Embedding  []float32 `json:"-"`
}

type KnowledgeDuplicateNode struct {
	ID             string            `json:"id"`
	Kind           KnowledgeNodeKind `json:"kind"`
	Label          string            `json:"label"`
	Status         KnowledgeStatus   `json:"status"`
	Origin         KnowledgeOrigin   `json:"origin"`
	NodeDigest     string            `json:"node_digest"`
	EvidenceDigest string            `json:"evidence_digest"`
}

type KnowledgeDuplicateCandidate struct {
	Left            KnowledgeDuplicateNode `json:"left"`
	Right           KnowledgeDuplicateNode `json:"right"`
	Similarity      float64                `json:"similarity"`
	EmbeddingSpace  string                 `json:"embedding_space"`
	SuggestedSource string                 `json:"suggested_source,omitempty"`
	SuggestedTarget string                 `json:"suggested_target,omitempty"`
}

type KnowledgeDuplicateReport struct {
	Threshold         float64                       `json:"threshold"`
	EmbeddingSpace    string                        `json:"embedding_space"`
	ScannedNodes      int                           `json:"scanned_nodes"`
	EligibleNodes     int                           `json:"eligible_nodes"`
	SkippedResolved   int                           `json:"skipped_resolved"`
	SkippedNonCurrent int                           `json:"skipped_non_current"`
	SkippedChanged    int                           `json:"skipped_changed"`
	SkippedNoVector   int                           `json:"skipped_no_vector"`
	Candidates        []KnowledgeDuplicateCandidate `json:"candidates"`
}

type KnowledgeNodeMergeRequest struct {
	SourceID                     string  `json:"source_id"`
	TargetID                     string  `json:"target_id"`
	Reviewer                     string  `json:"reviewer"`
	Comment                      string  `json:"comment,omitempty"`
	ExpectedSourceNodeDigest     string  `json:"expected_source_node_digest"`
	ExpectedTargetNodeDigest     string  `json:"expected_target_node_digest"`
	ExpectedSourceEvidenceDigest string  `json:"expected_source_evidence_digest"`
	ExpectedTargetEvidenceDigest string  `json:"expected_target_evidence_digest"`
	Similarity                   float64 `json:"similarity"`
	EmbeddingSpace               string  `json:"embedding_space"`
}

type KnowledgeNodeMergeRecord struct {
	ID                   int64             `json:"id"`
	SourceID             string            `json:"source_id"`
	TargetID             string            `json:"target_id"`
	Kind                 KnowledgeNodeKind `json:"kind"`
	Similarity           float64           `json:"similarity"`
	EmbeddingSpace       string            `json:"embedding_space"`
	SourceNodeDigest     string            `json:"source_node_digest"`
	TargetNodeDigest     string            `json:"target_node_digest"`
	SourceEvidenceDigest string            `json:"source_evidence_digest"`
	TargetEvidenceDigest string            `json:"target_evidence_digest"`
	Reviewer             string            `json:"reviewer"`
	Comment              string            `json:"comment,omitempty"`
	Created              string            `json:"created"`
	Current              bool              `json:"current"`
	StateReason          string            `json:"state_reason,omitempty"`
}

type KnowledgeNodeMergeResult struct {
	Merge         KnowledgeNodeMergeRecord `json:"merge"`
	ResolvedEdges int                      `json:"resolved_edges"`
	Review        KnowledgeReviewRecord    `json:"review"`
}

// KnowledgeNodeContentDigest pins the semantic object that a reviewer saw.
// Status and timestamps are excluded so approval/merge transitions do not
// invalidate the digest by themselves.
func KnowledgeNodeContentDigest(node KnowledgeNode) (string, error) {
	if err := validateKnowledgeID(node.ID); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		ID         string            `json:"id"`
		Kind       KnowledgeNodeKind `json:"kind"`
		Label      string            `json:"label"`
		Body       string            `json:"body"`
		Origin     KnowledgeOrigin   `json:"origin"`
		Confidence float64           `json:"confidence"`
	}{node.ID, node.Kind, node.Label, node.Body, node.Origin, node.Confidence})
	if err != nil {
		return "", fmt.Errorf("encode knowledge node digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DetectKnowledgeNodeDuplicates compares embeddings of exact node label/body
// text. The vectors are accepted only when pinned to the current node digest;
// stored chunk embeddings are intentionally not reused for this purpose.
func (s *Store) DetectKnowledgeNodeDuplicates(vectors []KnowledgeNodeVector, embeddingSpace string, threshold float64, limit int, kind KnowledgeNodeKind) (KnowledgeDuplicateReport, error) {
	embeddingSpace = strings.TrimSpace(embeddingSpace)
	if embeddingSpace == "" {
		return KnowledgeDuplicateReport{}, errors.New("knowledge duplicate embedding space is empty")
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
		return KnowledgeDuplicateReport{}, errors.New("knowledge duplicate threshold must be between 0 and 1")
	}
	if limit < 1 || limit > MaxKnowledgeDuplicateCandidates {
		return KnowledgeDuplicateReport{}, fmt.Errorf("knowledge duplicate limit must be between 1 and %d", MaxKnowledgeDuplicateCandidates)
	}
	if kind != "" && !validKnowledgeNodeKind(kind) {
		return KnowledgeDuplicateReport{}, fmt.Errorf("unsupported knowledge node kind %q", kind)
	}
	vectorByID := make(map[string]KnowledgeNodeVector, len(vectors))
	for _, vector := range vectors {
		if vector.NodeID == "" || vectorByID[vector.NodeID].NodeID != "" {
			return KnowledgeDuplicateReport{}, fmt.Errorf("duplicate or empty knowledge node vector ID %q", vector.NodeID)
		}
		vector.Embedding = append([]float32(nil), vector.Embedding...)
		vectorByID[vector.NodeID] = vector
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeDuplicateReport{}, err
	}
	report := KnowledgeDuplicateReport{
		Threshold: threshold, EmbeddingSpace: embeddingSpace,
		Candidates: make([]KnowledgeDuplicateCandidate, 0),
	}
	type candidateNode struct {
		node      KnowledgeNode
		summary   KnowledgeDuplicateNode
		embedding []float32
	}
	eligible := make([]candidateNode, 0)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, node := range graph.Nodes {
		if kind != "" && node.Kind != kind {
			continue
		}
		report.ScannedNodes++
		if node.Status == KnowledgeStatusResolved {
			report.SkippedResolved++
			continue
		}
		current := true
		for _, anchor := range node.Evidence {
			if resolveEvidenceAnchorFromEntries(anchor, s.entries).State != EvidenceCurrent {
				current = false
				break
			}
		}
		if !current {
			report.SkippedNonCurrent++
			continue
		}
		nodeDigest, err := KnowledgeNodeContentDigest(node)
		if err != nil {
			return KnowledgeDuplicateReport{}, err
		}
		vector, ok := vectorByID[node.ID]
		if !ok || !validKnowledgeDuplicateVector(vector.Embedding) {
			report.SkippedNoVector++
			continue
		}
		if vector.NodeDigest != nodeDigest {
			report.SkippedChanged++
			continue
		}
		evidenceDigest, err := KnowledgeEvidenceDigest(node.Evidence)
		if err != nil {
			return KnowledgeDuplicateReport{}, err
		}
		eligible = append(eligible, candidateNode{node: node, embedding: vector.Embedding, summary: KnowledgeDuplicateNode{
			ID: node.ID, Kind: node.Kind, Label: node.Label, Status: node.Status, Origin: node.Origin,
			NodeDigest: nodeDigest, EvidenceDigest: evidenceDigest,
		}})
	}
	report.EligibleNodes = len(eligible)
	for i := 0; i < len(eligible); i++ {
		for j := i + 1; j < len(eligible); j++ {
			if eligible[i].node.Kind != eligible[j].node.Kind || len(eligible[i].embedding) != len(eligible[j].embedding) {
				continue
			}
			similarity := CosineSimilarity(eligible[i].embedding, eligible[j].embedding)
			if math.IsNaN(similarity) || math.IsInf(similarity, 0) || similarity < threshold {
				continue
			}
			item := KnowledgeDuplicateCandidate{
				Left: eligible[i].summary, Right: eligible[j].summary,
				Similarity: similarity, EmbeddingSpace: embeddingSpace,
			}
			if eligible[i].node.Status == KnowledgeStatusDraft && eligible[i].node.Origin == KnowledgeOriginGenerated && eligible[j].node.Status == KnowledgeStatusActive {
				item.SuggestedSource, item.SuggestedTarget = eligible[i].node.ID, eligible[j].node.ID
			} else if eligible[j].node.Status == KnowledgeStatusDraft && eligible[j].node.Origin == KnowledgeOriginGenerated && eligible[i].node.Status == KnowledgeStatusActive {
				item.SuggestedSource, item.SuggestedTarget = eligible[j].node.ID, eligible[i].node.ID
			}
			report.Candidates = append(report.Candidates, item)
		}
	}
	sort.Slice(report.Candidates, func(i, j int) bool {
		if report.Candidates[i].Similarity != report.Candidates[j].Similarity {
			return report.Candidates[i].Similarity > report.Candidates[j].Similarity
		}
		leftI := report.Candidates[i].Left.ID + "\x00" + report.Candidates[i].Right.ID
		leftJ := report.Candidates[j].Left.ID + "\x00" + report.Candidates[j].Right.ID
		return leftI < leftJ
	})
	if len(report.Candidates) > limit {
		report.Candidates = report.Candidates[:limit]
	}
	return report, nil
}

func validKnowledgeDuplicateVector(vector []float32) bool {
	if len(vector) == 0 {
		return false
	}
	norm := 0.0
	for _, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		norm += f * f
	}
	return norm > 0 && !math.IsNaN(norm) && !math.IsInf(norm, 0)
}

func (s *Store) MergeKnowledgeDuplicate(request KnowledgeNodeMergeRequest) (KnowledgeNodeMergeResult, error) {
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.TargetID = strings.TrimSpace(request.TargetID)
	request.Reviewer = strings.TrimSpace(request.Reviewer)
	request.Comment = strings.TrimSpace(request.Comment)
	request.EmbeddingSpace = strings.TrimSpace(request.EmbeddingSpace)
	if request.SourceID == request.TargetID {
		return KnowledgeNodeMergeResult{}, errors.New("knowledge duplicate source and target are identical")
	}
	if request.EmbeddingSpace == "" {
		return KnowledgeNodeMergeResult{}, errors.New("knowledge duplicate embedding space is empty")
	}
	if math.IsNaN(request.Similarity) || math.IsInf(request.Similarity, 0) || request.Similarity < 0 || request.Similarity > 1 {
		return KnowledgeNodeMergeResult{}, errors.New("knowledge duplicate similarity must be between 0 and 1")
	}
	for _, item := range []KnowledgeApprovalRequest{
		{ObjectType: KnowledgeObjectNode, ID: request.SourceID, Reviewer: request.Reviewer, Comment: request.Comment, ExpectedEvidenceDigest: request.ExpectedSourceEvidenceDigest},
		{ObjectType: KnowledgeObjectNode, ID: request.TargetID, Reviewer: request.Reviewer, Comment: request.Comment, ExpectedEvidenceDigest: request.ExpectedTargetEvidenceDigest},
	} {
		if _, err := normalizeKnowledgeApprovalRequest(item, true); err != nil {
			return KnowledgeNodeMergeResult{}, err
		}
	}
	if !validSHA256Digest(request.ExpectedSourceNodeDigest) || !validSHA256Digest(request.ExpectedTargetNodeDigest) {
		return KnowledgeNodeMergeResult{}, errors.New("knowledge duplicate node digests are invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeNodeMergeResult{}, fmt.Errorf("begin knowledge duplicate merge: %w", err)
	}
	rollback := func(cause error) (KnowledgeNodeMergeResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeNodeMergeResult{}, fmt.Errorf("%v; knowledge duplicate merge rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeNodeMergeResult{}, cause
	}
	source, err := loadKnowledgeNode(tx, request.SourceID)
	if err != nil {
		return rollback(err)
	}
	target, err := loadKnowledgeNode(tx, request.TargetID)
	if err != nil {
		return rollback(err)
	}
	if source.Status != KnowledgeStatusDraft || source.Origin != KnowledgeOriginGenerated {
		return rollback(fmt.Errorf("knowledge duplicate source %q must be a generated draft", source.ID))
	}
	if target.Status != KnowledgeStatusActive {
		return rollback(fmt.Errorf("knowledge duplicate target %q must be active", target.ID))
	}
	if source.Kind != target.Kind {
		return rollback(fmt.Errorf("knowledge duplicate kinds differ: %s and %s", source.Kind, target.Kind))
	}
	sourceNodeDigest, sourceEvidenceDigest, err := knowledgeNodeDigests(tx, source)
	if err != nil {
		return rollback(err)
	}
	targetNodeDigest, targetEvidenceDigest, err := knowledgeNodeDigests(tx, target)
	if err != nil {
		return rollback(err)
	}
	if sourceNodeDigest != request.ExpectedSourceNodeDigest || targetNodeDigest != request.ExpectedTargetNodeDigest ||
		sourceEvidenceDigest != request.ExpectedSourceEvidenceDigest || targetEvidenceDigest != request.ExpectedTargetEvidenceDigest {
		return rollback(ErrKnowledgeEvidenceChanged)
	}
	for _, node := range []KnowledgeNode{source, target} {
		for _, anchor := range node.Evidence {
			if resolution := resolveEvidenceAnchorFromEntries(anchor, s.entries); resolution.State != EvidenceCurrent {
				return rollback(fmt.Errorf("%w: %s is %s", ErrKnowledgeEvidenceNotCurrent, anchor.CitationID, resolution.State))
			}
		}
	}
	var unsafeEdges int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge_edges
		WHERE (from_node = ? OR to_node = ?) AND status != ?
		AND NOT (origin = ? AND status = ?)`, source.ID, source.ID,
		KnowledgeStatusResolved, KnowledgeOriginGenerated, KnowledgeStatusDraft).Scan(&unsafeEdges); err != nil {
		return rollback(fmt.Errorf("inspect duplicate node %q edges: %w", source.ID, err))
	}
	if unsafeEdges != 0 {
		return rollback(fmt.Errorf("knowledge duplicate source %q has %d active or non-generated incident edges", source.ID, unsafeEdges))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.Exec(`UPDATE knowledge_nodes SET status = ?, updated = ? WHERE id = ? AND status = ? AND origin = ?`,
		KnowledgeStatusResolved, now, source.ID, KnowledgeStatusDraft, KnowledgeOriginGenerated)
	if err != nil {
		return rollback(fmt.Errorf("resolve duplicate node %q: %w", source.ID, err))
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return rollback(fmt.Errorf("knowledge duplicate source %q changed concurrently", source.ID))
	}
	edges, err := tx.Exec(`UPDATE knowledge_edges SET status = ?, updated = ?
		WHERE (from_node = ? OR to_node = ?) AND origin = ? AND status = ?`,
		KnowledgeStatusResolved, now, source.ID, source.ID, KnowledgeOriginGenerated, KnowledgeStatusDraft)
	if err != nil {
		return rollback(fmt.Errorf("resolve duplicate node %q edges: %w", source.ID, err))
	}
	resolvedEdges, err := edges.RowsAffected()
	if err != nil {
		return rollback(fmt.Errorf("count resolved duplicate edges: %w", err))
	}
	record := KnowledgeNodeMergeRecord{
		SourceID: source.ID, TargetID: target.ID, Kind: source.Kind,
		Similarity: request.Similarity, EmbeddingSpace: request.EmbeddingSpace,
		SourceNodeDigest: sourceNodeDigest, TargetNodeDigest: targetNodeDigest,
		SourceEvidenceDigest: sourceEvidenceDigest, TargetEvidenceDigest: targetEvidenceDigest,
		Reviewer: request.Reviewer, Comment: request.Comment, Created: now, Current: true,
	}
	inserted, err := tx.Exec(`INSERT INTO knowledge_node_merges
		(source_node, target_node, kind, similarity, embedding_space, source_node_digest, target_node_digest,
		 source_evidence_digest, target_evidence_digest, reviewer, comment, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SourceID, record.TargetID, record.Kind, record.Similarity, record.EmbeddingSpace,
		record.SourceNodeDigest, record.TargetNodeDigest, record.SourceEvidenceDigest, record.TargetEvidenceDigest,
		record.Reviewer, record.Comment, record.Created)
	if err != nil {
		return rollback(fmt.Errorf("record knowledge node merge: %w", err))
	}
	record.ID, err = inserted.LastInsertId()
	if err != nil {
		return rollback(fmt.Errorf("read knowledge node merge ID: %w", err))
	}
	review := KnowledgeReviewRecord{
		ObjectType: KnowledgeObjectNode, ObjectID: source.ID, Action: "merge",
		PreviousStatus: KnowledgeStatusDraft, NewStatus: KnowledgeStatusResolved,
		Reviewer: request.Reviewer, Comment: request.Comment,
		EvidenceDigest: sourceEvidenceDigest, Created: now,
	}
	reviewResult, err := tx.Exec(`INSERT INTO knowledge_reviews
		(object_type, object_id, action, previous_status, new_status, reviewer, comment, evidence_digest, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, review.ObjectType, review.ObjectID, review.Action,
		review.PreviousStatus, review.NewStatus, review.Reviewer, review.Comment, review.EvidenceDigest, review.Created)
	if err != nil {
		return rollback(fmt.Errorf("append knowledge merge review: %w", err))
	}
	review.ID, err = reviewResult.LastInsertId()
	if err != nil {
		return rollback(fmt.Errorf("read knowledge merge review ID: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeNodeMergeResult{}, fmt.Errorf("commit knowledge duplicate merge: %w", err)
	}
	return KnowledgeNodeMergeResult{Merge: record, ResolvedEdges: int(resolvedEdges), Review: review}, nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func loadKnowledgeNode(q interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}, id string) (KnowledgeNode, error) {
	var node KnowledgeNode
	err := q.QueryRow(`SELECT id, kind, label, body, status, origin, confidence, created, updated
		FROM knowledge_nodes WHERE id = ?`, id).Scan(&node.ID, &node.Kind, &node.Label, &node.Body,
		&node.Status, &node.Origin, &node.Confidence, &node.Created, &node.Updated)
	if err == sql.ErrNoRows {
		return KnowledgeNode{}, fmt.Errorf("knowledge node %q not found", id)
	}
	if err != nil {
		return KnowledgeNode{}, fmt.Errorf("load knowledge node %q: %w", id, err)
	}
	node.Evidence, err = loadKnowledgeEvidence(q, "knowledge_node_evidence", "node_id", id)
	if err != nil {
		return KnowledgeNode{}, fmt.Errorf("load knowledge node %q evidence: %w", id, err)
	}
	return node, nil
}

func knowledgeNodeDigests(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, node KnowledgeNode) (string, string, error) {
	if node.Evidence == nil {
		var err error
		node.Evidence, err = loadKnowledgeEvidence(q, "knowledge_node_evidence", "node_id", node.ID)
		if err != nil {
			return "", "", err
		}
	}
	nodeDigest, err := KnowledgeNodeContentDigest(node)
	if err != nil {
		return "", "", err
	}
	evidenceDigest, err := KnowledgeEvidenceDigest(node.Evidence)
	if err != nil {
		return "", "", err
	}
	return nodeDigest, evidenceDigest, nil
}

func (s *Store) ListKnowledgeNodeMerges(limit int) ([]KnowledgeNodeMergeRecord, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("knowledge node merge limit must be between 1 and 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, source_node, target_node, kind, similarity, embedding_space,
		source_node_digest, target_node_digest, source_evidence_digest, target_evidence_digest,
		reviewer, comment, created FROM knowledge_node_merges ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge node merges: %w", err)
	}
	records := make([]KnowledgeNodeMergeRecord, 0)
	for rows.Next() {
		var record KnowledgeNodeMergeRecord
		if err := rows.Scan(&record.ID, &record.SourceID, &record.TargetID, &record.Kind,
			&record.Similarity, &record.EmbeddingSpace, &record.SourceNodeDigest, &record.TargetNodeDigest,
			&record.SourceEvidenceDigest, &record.TargetEvidenceDigest, &record.Reviewer,
			&record.Comment, &record.Created); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan knowledge node merge: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	seenSource := make(map[string]bool)
	for i := range records {
		if seenSource[records[i].SourceID] {
			records[i].StateReason = "superseded by newer merge"
			continue
		}
		seenSource[records[i].SourceID] = true
		records[i].Current, records[i].StateReason = knowledgeNodeMergeState(s.db, s.entries, records[i])
	}
	return records, nil
}

func knowledgeNodeMergeState(q interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}, entries []Entry, record KnowledgeNodeMergeRecord) (bool, string) {
	source, err := loadKnowledgeNode(q, record.SourceID)
	if err != nil {
		return false, "source node missing"
	}
	target, err := loadKnowledgeNode(q, record.TargetID)
	if err != nil {
		return false, "target node missing"
	}
	if source.Status != KnowledgeStatusResolved {
		return false, "source node is no longer resolved"
	}
	if target.Status != KnowledgeStatusActive {
		return false, "target node is no longer active"
	}
	sourceNodeDigest, sourceEvidenceDigest, err := knowledgeNodeDigests(q, source)
	if err != nil || sourceNodeDigest != record.SourceNodeDigest || sourceEvidenceDigest != record.SourceEvidenceDigest {
		return false, "source node or evidence changed"
	}
	targetNodeDigest, targetEvidenceDigest, err := knowledgeNodeDigests(q, target)
	if err != nil || targetNodeDigest != record.TargetNodeDigest || targetEvidenceDigest != record.TargetEvidenceDigest {
		return false, "target node or evidence changed"
	}
	for _, node := range []KnowledgeNode{source, target} {
		for _, anchor := range node.Evidence {
			if resolveEvidenceAnchorFromEntries(anchor, entries).State != EvidenceCurrent {
				return false, "source evidence is not current"
			}
		}
	}
	return true, ""
}

// invalidateChangedKnowledgeNodeMerges reopens generated objects when the
// semantic content or evidence pinned by their latest merge changes.
func invalidateChangedKnowledgeNodeMerges(tx *sql.Tx, entries []Entry) error {
	rows, err := tx.Query(`SELECT id, source_node, target_node, kind, similarity, embedding_space,
		source_node_digest, target_node_digest, source_evidence_digest, target_evidence_digest,
		reviewer, comment, created FROM knowledge_node_merges
		WHERE id IN (SELECT MAX(id) FROM knowledge_node_merges GROUP BY source_node)`)
	if err != nil {
		return fmt.Errorf("inspect knowledge node merges: %w", err)
	}
	records := make([]KnowledgeNodeMergeRecord, 0)
	for rows.Next() {
		var record KnowledgeNodeMergeRecord
		if err := rows.Scan(&record.ID, &record.SourceID, &record.TargetID, &record.Kind,
			&record.Similarity, &record.EmbeddingSpace, &record.SourceNodeDigest, &record.TargetNodeDigest,
			&record.SourceEvidenceDigest, &record.TargetEvidenceDigest, &record.Reviewer,
			&record.Comment, &record.Created); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, record := range records {
		current, _ := knowledgeNodeMergeState(tx, entries, record)
		if current {
			continue
		}
		if _, err := tx.Exec(`UPDATE knowledge_nodes SET status = ?, updated = ?
			WHERE id = ? AND status = ? AND origin = ?`, KnowledgeStatusDraft, now,
			record.SourceID, KnowledgeStatusResolved, KnowledgeOriginGenerated); err != nil {
			return fmt.Errorf("reopen changed merged node %q: %w", record.SourceID, err)
		}
		if _, err := tx.Exec(`UPDATE knowledge_edges SET status = ?, updated = ?
			WHERE (from_node = ? OR to_node = ?) AND status = ? AND origin = ?`,
			KnowledgeStatusDraft, now, record.SourceID, record.SourceID,
			KnowledgeStatusResolved, KnowledgeOriginGenerated); err != nil {
			return fmt.Errorf("reopen changed merged node %q edges: %w", record.SourceID, err)
		}
	}
	return nil
}
