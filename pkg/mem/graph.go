package mem

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type KnowledgeNodeKind string

const (
	KnowledgeNodeDocument      KnowledgeNodeKind = "document"
	KnowledgeNodeSection       KnowledgeNodeKind = "section"
	KnowledgeNodeTopic         KnowledgeNodeKind = "topic"
	KnowledgeNodeDefinition    KnowledgeNodeKind = "definition"
	KnowledgeNodeClaim         KnowledgeNodeKind = "claim"
	KnowledgeNodeFormula       KnowledgeNodeKind = "formula"
	KnowledgeNodeExample       KnowledgeNodeKind = "example"
	KnowledgeNodeProcedure     KnowledgeNodeKind = "procedure"
	KnowledgeNodeComparison    KnowledgeNodeKind = "comparison"
	KnowledgeNodeNote          KnowledgeNodeKind = "note"
	KnowledgeNodeQuestion      KnowledgeNodeKind = "question"
	KnowledgeNodeCard          KnowledgeNodeKind = "card"
	KnowledgeNodeHypothesis    KnowledgeNodeKind = "hypothesis"
	KnowledgeNodeDecision      KnowledgeNodeKind = "decision"
	KnowledgeNodeTask          KnowledgeNodeKind = "task"
	KnowledgeNodeContradiction KnowledgeNodeKind = "contradiction"
	KnowledgeNodeGap           KnowledgeNodeKind = "gap"
	KnowledgeNodeDependency    KnowledgeNodeKind = "dependency"
	KnowledgeNodeCause         KnowledgeNodeKind = "cause"
	KnowledgeNodeEffect        KnowledgeNodeKind = "effect"
	KnowledgeNodeRisk          KnowledgeNodeKind = "risk"
	KnowledgeNodeConstraint    KnowledgeNodeKind = "constraint"
)

type KnowledgeNodeLayer string

const (
	KnowledgeLayerSource    KnowledgeNodeLayer = "source"
	KnowledgeLayerAnalytics KnowledgeNodeLayer = "analytics"
	KnowledgeLayerWorkspace KnowledgeNodeLayer = "workspace"
)

type KnowledgeRelationKind string

const (
	KnowledgeRelationContains     KnowledgeRelationKind = "contains"
	KnowledgeRelationAbout        KnowledgeRelationKind = "about"
	KnowledgeRelationSupports     KnowledgeRelationKind = "supports"
	KnowledgeRelationContradicts  KnowledgeRelationKind = "contradicts"
	KnowledgeRelationDerivedFrom  KnowledgeRelationKind = "derived_from"
	KnowledgeRelationAsks         KnowledgeRelationKind = "asks"
	KnowledgeRelationAnswers      KnowledgeRelationKind = "answers"
	KnowledgeRelationPrerequisite KnowledgeRelationKind = "prerequisite"
	KnowledgeRelationRelated      KnowledgeRelationKind = "related"
	KnowledgeRelationCompares     KnowledgeRelationKind = "compares"
	KnowledgeRelationRevealsGap   KnowledgeRelationKind = "reveals_gap"
	KnowledgeRelationResolves     KnowledgeRelationKind = "resolves"
	KnowledgeRelationDefines      KnowledgeRelationKind = "defines"
	KnowledgeRelationExemplifies  KnowledgeRelationKind = "exemplifies"
	KnowledgeRelationDependsOn    KnowledgeRelationKind = "depends_on"
	KnowledgeRelationCauses       KnowledgeRelationKind = "causes"
	KnowledgeRelationMitigates    KnowledgeRelationKind = "mitigates"
	KnowledgeRelationConstrains   KnowledgeRelationKind = "constrains"
	KnowledgeRelationPrecedes     KnowledgeRelationKind = "precedes"
	KnowledgeRelationHypothesizes KnowledgeRelationKind = "hypothesizes_about"
	KnowledgeRelationBasedOn      KnowledgeRelationKind = "based_on"
	KnowledgeRelationActsOn       KnowledgeRelationKind = "acts_on"
)

type KnowledgeStatus string

const (
	KnowledgeStatusActive   KnowledgeStatus = "active"
	KnowledgeStatusDraft    KnowledgeStatus = "draft"
	KnowledgeStatusRejected KnowledgeStatus = "rejected"
	KnowledgeStatusResolved KnowledgeStatus = "resolved"
)

type KnowledgeOrigin string

const (
	KnowledgeOriginSource    KnowledgeOrigin = "source"
	KnowledgeOriginGenerated KnowledgeOrigin = "generated"
	KnowledgeOriginManual    KnowledgeOrigin = "manual"
)

const (
	MaxKnowledgeIDBytes      = 128
	MaxKnowledgeLabelRunes   = 512
	MaxKnowledgeBodyRunes    = 65536
	MaxKnowledgeExcerptRunes = 16384
)

// EvidenceAnchor snapshots both the stable source location and the exact
// document/chunk revision from which a graph assertion was derived.
type EvidenceAnchor struct {
	CitationID       string `json:"citation_id"`
	DocumentID       string `json:"document_id"`
	DocumentRevision string `json:"document_revision"`
	ChunkHash        string `json:"chunk_hash"`
	EvidenceHash     string `json:"evidence_hash"`
	SourcePath       string `json:"source_path"`
	Page             int    `json:"page"`
	BlockIndex       int    `json:"block_index"`
	BlockChunkIndex  int    `json:"block_chunk_index"`
	Excerpt          string `json:"excerpt"`
}

type KnowledgeNode struct {
	ID         string            `json:"id"`
	Kind       KnowledgeNodeKind `json:"kind"`
	Label      string            `json:"label"`
	Body       string            `json:"body,omitempty"`
	Status     KnowledgeStatus   `json:"status"`
	Origin     KnowledgeOrigin   `json:"origin"`
	Confidence float64           `json:"confidence,omitempty"`
	Created    string            `json:"created"`
	Updated    string            `json:"updated"`
	Evidence   []EvidenceAnchor  `json:"evidence"`
}

type KnowledgeEdge struct {
	ID         string                `json:"id"`
	From       string                `json:"from"`
	To         string                `json:"to"`
	Kind       KnowledgeRelationKind `json:"kind"`
	Label      string                `json:"label,omitempty"`
	Status     KnowledgeStatus       `json:"status"`
	Origin     KnowledgeOrigin       `json:"origin"`
	Confidence float64               `json:"confidence,omitempty"`
	Created    string                `json:"created"`
	Updated    string                `json:"updated"`
	Evidence   []EvidenceAnchor      `json:"evidence"`
}

type KnowledgeGraph struct {
	Nodes []KnowledgeNode `json:"nodes"`
	Edges []KnowledgeEdge `json:"edges"`
}

type EvidenceState string

const (
	EvidenceCurrent EvidenceState = "current"
	EvidenceStale   EvidenceState = "stale"
	EvidenceMissing EvidenceState = "missing"
)

type EvidenceResolution struct {
	Anchor                  EvidenceAnchor `json:"anchor"`
	State                   EvidenceState  `json:"state"`
	CurrentDocumentRevision string         `json:"current_document_revision,omitempty"`
	CurrentChunkHash        string         `json:"current_chunk_hash,omitempty"`
}

const knowledgeGraphSchema = `
CREATE TABLE IF NOT EXISTS knowledge_nodes (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    label TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    origin TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0,
    created TEXT NOT NULL,
    updated TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_edges (
    id TEXT PRIMARY KEY,
    from_node TEXT NOT NULL,
    to_node TEXT NOT NULL,
    kind TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    origin TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0,
    created TEXT NOT NULL,
    updated TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_node_evidence (
    node_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    citation_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    chunk_hash TEXT NOT NULL,
    evidence_hash TEXT NOT NULL,
    source_path TEXT NOT NULL,
    page INTEGER NOT NULL,
    block_index INTEGER NOT NULL,
    block_chunk_index INTEGER NOT NULL,
    excerpt TEXT NOT NULL,
    PRIMARY KEY (node_id, ordinal)
);

CREATE TABLE IF NOT EXISTS knowledge_edge_evidence (
    edge_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    citation_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    chunk_hash TEXT NOT NULL,
    evidence_hash TEXT NOT NULL,
    source_path TEXT NOT NULL,
    page INTEGER NOT NULL,
    block_index INTEGER NOT NULL,
    block_chunk_index INTEGER NOT NULL,
    excerpt TEXT NOT NULL,
    PRIMARY KEY (edge_id, ordinal)
);

CREATE TABLE IF NOT EXISTS knowledge_reviews (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    previous_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    reviewer TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    evidence_digest TEXT NOT NULL,
    reverts_review_id INTEGER NOT NULL DEFAULT 0,
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_edits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    action TEXT NOT NULL,
    previous_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    previous_label TEXT NOT NULL,
    previous_body TEXT NOT NULL DEFAULT '',
    new_label TEXT NOT NULL,
    new_body TEXT NOT NULL DEFAULT '',
    previous_content_digest TEXT NOT NULL,
    new_content_digest TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    reverts_edit_id INTEGER NOT NULL DEFAULT 0,
    editor TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_node_merges (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_node TEXT NOT NULL,
    target_node TEXT NOT NULL,
    kind TEXT NOT NULL,
    similarity REAL NOT NULL,
    embedding_space TEXT NOT NULL,
    source_node_digest TEXT NOT NULL,
    target_node_digest TEXT NOT NULL,
    source_evidence_digest TEXT NOT NULL,
    target_evidence_digest TEXT NOT NULL,
    reviewer TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_map_views (
    name TEXT PRIMARY KEY,
    layout_json TEXT NOT NULL,
    updated TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_workspace_creations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL UNIQUE,
    edge_id TEXT NOT NULL UNIQUE,
    parent_node_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    relation_kind TEXT NOT NULL,
    author TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    content_digest TEXT NOT NULL,
    parent_content_digest TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    created TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_knowledge_edges_from ON knowledge_edges(from_node);
CREATE INDEX IF NOT EXISTS idx_knowledge_edges_to ON knowledge_edges(to_node);
CREATE INDEX IF NOT EXISTS idx_knowledge_node_evidence_citation ON knowledge_node_evidence(citation_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_edge_evidence_citation ON knowledge_edge_evidence(citation_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_reviews_object ON knowledge_reviews(object_type, object_id, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_edits_object ON knowledge_edits(object_type, object_id, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_node_merges_source ON knowledge_node_merges(source_node, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_node_merges_target ON knowledge_node_merges(target_node, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_workspace_creations_parent ON knowledge_workspace_creations(parent_node_id, id);

CREATE TRIGGER IF NOT EXISTS knowledge_reviews_no_update
BEFORE UPDATE ON knowledge_reviews
BEGIN
    SELECT RAISE(ABORT, 'knowledge review history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_reviews_no_delete
BEFORE DELETE ON knowledge_reviews
BEGIN
    SELECT RAISE(ABORT, 'knowledge review history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_edits_no_update
BEFORE UPDATE ON knowledge_edits
BEGIN
    SELECT RAISE(ABORT, 'knowledge edit history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_edits_no_delete
BEFORE DELETE ON knowledge_edits
BEGIN
    SELECT RAISE(ABORT, 'knowledge edit history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_node_merges_no_update
BEFORE UPDATE ON knowledge_node_merges
BEGIN
    SELECT RAISE(ABORT, 'knowledge node merge history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_node_merges_no_delete
BEFORE DELETE ON knowledge_node_merges
BEGIN
    SELECT RAISE(ABORT, 'knowledge node merge history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_workspace_creations_no_update
BEFORE UPDATE ON knowledge_workspace_creations
BEGIN
    SELECT RAISE(ABORT, 'knowledge workspace creation history is append-only');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_workspace_creations_no_delete
BEFORE DELETE ON knowledge_workspace_creations
BEGIN
    SELECT RAISE(ABORT, 'knowledge workspace creation history is append-only');
END;
`

func EvidenceAnchorForEntry(entry Entry, excerpt string) (EvidenceAnchor, error) {
	if excerpt == "" {
		excerpt = entry.Text
	}
	if strings.TrimSpace(excerpt) == "" {
		return EvidenceAnchor{}, fmt.Errorf("knowledge evidence excerpt is empty")
	}
	if !strings.Contains(entry.Text, excerpt) {
		return EvidenceAnchor{}, fmt.Errorf("knowledge evidence excerpt is not contained in entry %d", entry.ID)
	}
	if entry.DocumentID == "" || entry.DocumentRevision == "" || entry.ChunkHash == "" || entry.SourcePath == "" {
		return EvidenceAnchor{}, fmt.Errorf("entry %d has no versioned document provenance", entry.ID)
	}
	if entry.ChunkHash != ChunkContentHash(entry.Text) {
		return EvidenceAnchor{}, fmt.Errorf("entry %d chunk hash does not match text", entry.ID)
	}
	citationID, _ := CitationForEntry(entry)
	anchor := EvidenceAnchor{
		CitationID: citationID, DocumentID: entry.DocumentID,
		DocumentRevision: entry.DocumentRevision, ChunkHash: entry.ChunkHash,
		EvidenceHash: ChunkContentHash(excerpt), SourcePath: entry.SourcePath,
		Page: entry.Page, BlockIndex: entry.BlockIndex,
		BlockChunkIndex: entry.BlockChunkIndex, Excerpt: excerpt,
	}
	if err := validateEvidenceAnchor(anchor); err != nil {
		return EvidenceAnchor{}, err
	}
	return anchor, nil
}

func ValidateKnowledgeGraph(graph KnowledgeGraph) error {
	nodeIDs := make(map[string]bool, len(graph.Nodes))
	for i := range graph.Nodes {
		if err := validateKnowledgeNode(graph.Nodes[i]); err != nil {
			return fmt.Errorf("knowledge node %d: %w", i, err)
		}
		if nodeIDs[graph.Nodes[i].ID] {
			return fmt.Errorf("duplicate knowledge node ID %q", graph.Nodes[i].ID)
		}
		nodeIDs[graph.Nodes[i].ID] = true
	}
	edgeIDs := make(map[string]bool, len(graph.Edges))
	for i := range graph.Edges {
		if err := validateKnowledgeEdge(graph.Edges[i]); err != nil {
			return fmt.Errorf("knowledge edge %d: %w", i, err)
		}
		if edgeIDs[graph.Edges[i].ID] {
			return fmt.Errorf("duplicate knowledge edge ID %q", graph.Edges[i].ID)
		}
		edgeIDs[graph.Edges[i].ID] = true
	}
	return nil
}

func validateKnowledgeNode(node KnowledgeNode) error {
	if err := validateKnowledgeID(node.ID); err != nil {
		return err
	}
	if !validKnowledgeNodeKind(node.Kind) {
		return fmt.Errorf("unsupported node kind %q", node.Kind)
	}
	if strings.TrimSpace(node.Label) == "" || utf8.RuneCountInString(node.Label) > MaxKnowledgeLabelRunes {
		return fmt.Errorf("node label must contain 1..%d runes", MaxKnowledgeLabelRunes)
	}
	if utf8.RuneCountInString(node.Body) > MaxKnowledgeBodyRunes {
		return fmt.Errorf("node body exceeds %d runes", MaxKnowledgeBodyRunes)
	}
	if !validKnowledgeStatus(node.Status) || !validKnowledgeOrigin(node.Origin) {
		return fmt.Errorf("invalid node status/origin %q/%q", node.Status, node.Origin)
	}
	if !validKnowledgeConfidence(node.Confidence) {
		return fmt.Errorf("node confidence must be finite and between 0 and 1")
	}
	if err := validateKnowledgeTimes(node.Created, node.Updated); err != nil {
		return err
	}
	if len(node.Evidence) == 0 {
		return fmt.Errorf("node has no source evidence")
	}
	return validateEvidenceList(node.Evidence)
}

func validateKnowledgeEdge(edge KnowledgeEdge) error {
	if err := validateKnowledgeID(edge.ID); err != nil {
		return err
	}
	if err := validateKnowledgeID(edge.From); err != nil {
		return fmt.Errorf("invalid from node: %w", err)
	}
	if err := validateKnowledgeID(edge.To); err != nil {
		return fmt.Errorf("invalid to node: %w", err)
	}
	if edge.From == edge.To {
		return fmt.Errorf("self-referential knowledge edge")
	}
	if !validKnowledgeRelationKind(edge.Kind) {
		return fmt.Errorf("unsupported relation kind %q", edge.Kind)
	}
	if utf8.RuneCountInString(edge.Label) > MaxKnowledgeLabelRunes {
		return fmt.Errorf("edge label exceeds %d runes", MaxKnowledgeLabelRunes)
	}
	if !validKnowledgeStatus(edge.Status) || !validKnowledgeOrigin(edge.Origin) {
		return fmt.Errorf("invalid edge status/origin %q/%q", edge.Status, edge.Origin)
	}
	if !validKnowledgeConfidence(edge.Confidence) {
		return fmt.Errorf("edge confidence must be finite and between 0 and 1")
	}
	if err := validateKnowledgeTimes(edge.Created, edge.Updated); err != nil {
		return err
	}
	if len(edge.Evidence) == 0 {
		return fmt.Errorf("edge has no source evidence")
	}
	return validateEvidenceList(edge.Evidence)
}

func validateEvidenceList(evidence []EvidenceAnchor) error {
	seen := make(map[string]bool, len(evidence))
	for i, anchor := range evidence {
		if err := validateEvidenceAnchor(anchor); err != nil {
			return fmt.Errorf("evidence %d: %w", i, err)
		}
		key := anchor.CitationID + "\x00" + anchor.EvidenceHash
		if seen[key] {
			return fmt.Errorf("duplicate evidence %q", anchor.CitationID)
		}
		seen[key] = true
	}
	return nil
}

func validateEvidenceAnchor(anchor EvidenceAnchor) error {
	if !strings.HasPrefix(anchor.CitationID, "cite-") || !citationIDPattern.MatchString(anchor.CitationID) {
		return fmt.Errorf("invalid document citation ID %q", anchor.CitationID)
	}
	if strings.TrimSpace(anchor.DocumentID) == "" || strings.TrimSpace(anchor.SourcePath) == "" {
		return fmt.Errorf("missing document identity or source path")
	}
	if !isSHA256ContentHash(anchor.DocumentRevision) || !isSHA256ContentHash(anchor.ChunkHash) || !isSHA256ContentHash(anchor.EvidenceHash) {
		return fmt.Errorf("invalid revision or evidence hash")
	}
	if anchor.Page < 0 || anchor.BlockIndex < 0 || anchor.BlockChunkIndex < 0 {
		return fmt.Errorf("negative source coordinates")
	}
	if strings.TrimSpace(anchor.Excerpt) == "" || utf8.RuneCountInString(anchor.Excerpt) > MaxKnowledgeExcerptRunes {
		return fmt.Errorf("evidence excerpt must contain 1..%d runes", MaxKnowledgeExcerptRunes)
	}
	if anchor.EvidenceHash != ChunkContentHash(anchor.Excerpt) {
		return fmt.Errorf("evidence hash does not match excerpt")
	}
	expected, _ := CitationForEntry(Entry{
		DocumentID: anchor.DocumentID, SourcePath: anchor.SourcePath,
		Page: anchor.Page, BlockIndex: anchor.BlockIndex,
		BlockChunkIndex: anchor.BlockChunkIndex, BlockTotalChunks: anchor.BlockChunkIndex + 1,
	})
	if anchor.CitationID != expected {
		return fmt.Errorf("citation ID does not match source coordinates")
	}
	return nil
}

func validateKnowledgeID(id string) error {
	if len(id) == 0 || len(id) > MaxKnowledgeIDBytes {
		return fmt.Errorf("knowledge ID must contain 1..%d bytes", MaxKnowledgeIDBytes)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("knowledge ID %q contains unsupported characters", id)
	}
	return nil
}

func validKnowledgeNodeKind(kind KnowledgeNodeKind) bool {
	return KnowledgeNodeLayerForKind(kind) != ""
}

// KnowledgeNodeLayerForKind classifies every supported kind without changing
// the persisted graph format. The layer is semantic metadata, not an authority
// decision: generated source-layer objects remain drafts until review.
func KnowledgeNodeLayerForKind(kind KnowledgeNodeKind) KnowledgeNodeLayer {
	switch kind {
	case KnowledgeNodeDocument, KnowledgeNodeSection, KnowledgeNodeTopic,
		KnowledgeNodeDefinition, KnowledgeNodeClaim, KnowledgeNodeFormula,
		KnowledgeNodeExample, KnowledgeNodeProcedure:
		return KnowledgeLayerSource
	case KnowledgeNodeComparison, KnowledgeNodeContradiction, KnowledgeNodeGap,
		KnowledgeNodeDependency, KnowledgeNodeCause, KnowledgeNodeEffect,
		KnowledgeNodeRisk, KnowledgeNodeConstraint:
		return KnowledgeLayerAnalytics
	case KnowledgeNodeNote, KnowledgeNodeQuestion, KnowledgeNodeCard,
		KnowledgeNodeHypothesis, KnowledgeNodeDecision, KnowledgeNodeTask:
		return KnowledgeLayerWorkspace
	default:
		return ""
	}
}

func validKnowledgeExtractionNodeKind(kind KnowledgeNodeKind) bool {
	layer := KnowledgeNodeLayerForKind(kind)
	return layer == KnowledgeLayerSource || layer == KnowledgeLayerAnalytics
}

func validKnowledgeRelationKind(kind KnowledgeRelationKind) bool {
	switch kind {
	case KnowledgeRelationContains, KnowledgeRelationAbout, KnowledgeRelationSupports,
		KnowledgeRelationContradicts, KnowledgeRelationDerivedFrom, KnowledgeRelationAsks,
		KnowledgeRelationAnswers, KnowledgeRelationPrerequisite, KnowledgeRelationRelated,
		KnowledgeRelationCompares, KnowledgeRelationRevealsGap, KnowledgeRelationResolves,
		KnowledgeRelationDefines, KnowledgeRelationExemplifies, KnowledgeRelationDependsOn,
		KnowledgeRelationCauses, KnowledgeRelationMitigates, KnowledgeRelationConstrains,
		KnowledgeRelationPrecedes, KnowledgeRelationHypothesizes, KnowledgeRelationBasedOn,
		KnowledgeRelationActsOn:
		return true
	default:
		return false
	}
}

func validKnowledgeExtractionRelationKind(kind KnowledgeRelationKind) bool {
	if !validKnowledgeRelationKind(kind) {
		return false
	}
	// These relations belong to explicit user workspace actions. Model
	// extraction must not manufacture questions, hypotheses, decisions, tasks,
	// or present generated answers as reviewed user work.
	switch kind {
	case KnowledgeRelationAsks, KnowledgeRelationAnswers, KnowledgeRelationHypothesizes,
		KnowledgeRelationBasedOn, KnowledgeRelationActsOn:
		return false
	default:
		return true
	}
}

func validKnowledgeStatus(status KnowledgeStatus) bool {
	return status == KnowledgeStatusActive || status == KnowledgeStatusDraft ||
		status == KnowledgeStatusRejected || status == KnowledgeStatusResolved
}

func validKnowledgeOrigin(origin KnowledgeOrigin) bool {
	return origin == KnowledgeOriginSource || origin == KnowledgeOriginGenerated || origin == KnowledgeOriginManual
}

func validKnowledgeConfidence(confidence float64) bool {
	return !math.IsNaN(confidence) && !math.IsInf(confidence, 0) && confidence >= 0 && confidence <= 1
}

func validateKnowledgeTimes(created, updated string) error {
	var createdTime, updatedTime time.Time
	var err error
	if created != "" {
		createdTime, err = time.Parse(time.RFC3339, created)
		if err != nil {
			return fmt.Errorf("invalid created timestamp")
		}
	}
	if updated != "" {
		updatedTime, err = time.Parse(time.RFC3339, updated)
		if err != nil {
			return fmt.Errorf("invalid updated timestamp")
		}
	}
	if !createdTime.IsZero() && !updatedTime.IsZero() && updatedTime.Before(createdTime) {
		return fmt.Errorf("updated timestamp precedes created timestamp")
	}
	return nil
}

// UpsertKnowledgeGraph atomically upserts a graph fragment and replaces the
// evidence lists only for the supplied nodes and edges. Other graph branches
// are left intact.
func (s *Store) UpsertKnowledgeGraph(graph KnowledgeGraph) error {
	return s.upsertKnowledgeGraph(graph, false, false)
}

// UpsertCurrentKnowledgeGraph additionally verifies every supplied anchor
// against the current entry snapshot while holding the same write lock used by
// persistence. It closes the evidence-change window during model generation.
func (s *Store) UpsertCurrentKnowledgeGraph(graph KnowledgeGraph) error {
	return s.upsertKnowledgeGraph(graph, true, false)
}

// UpsertCorpusAnalysisGraph additionally requires every endpoint not created by
// this fragment to remain an active claim with fully current stored evidence.
func (s *Store) UpsertCorpusAnalysisGraph(graph KnowledgeGraph) error {
	if err := validateCorpusAnalysisFragment(graph); err != nil {
		return err
	}
	return s.upsertKnowledgeGraph(graph, true, true)
}

func validateCorpusAnalysisFragment(graph KnowledgeGraph) error {
	if len(graph.Nodes) == 0 {
		return fmt.Errorf("corpus analysis graph has no findings")
	}
	findings := make(map[string]bool, len(graph.Nodes))
	findingLinks := make(map[string]int, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if (node.Kind != KnowledgeNodeContradiction && node.Kind != KnowledgeNodeGap) ||
			node.Status != KnowledgeStatusDraft || node.Origin != KnowledgeOriginGenerated {
			return fmt.Errorf("corpus analysis node %q must be a generated draft contradiction/gap", node.ID)
		}
		findings[node.ID] = true
	}
	for _, edge := range graph.Edges {
		if edge.Status != KnowledgeStatusDraft || edge.Origin != KnowledgeOriginGenerated {
			return fmt.Errorf("corpus analysis edge %q must be a generated draft", edge.ID)
		}
		switch edge.Kind {
		case KnowledgeRelationContradicts:
			if findings[edge.From] || findings[edge.To] {
				return fmt.Errorf("corpus contradiction edge %q must connect existing claims", edge.ID)
			}
		case KnowledgeRelationDerivedFrom:
			if !findings[edge.From] || findings[edge.To] {
				return fmt.Errorf("corpus derived_from edge %q has invalid direction", edge.ID)
			}
			findingLinks[edge.From]++
		case KnowledgeRelationRevealsGap:
			if findings[edge.From] || !findings[edge.To] {
				return fmt.Errorf("corpus reveals_gap edge %q has invalid direction", edge.ID)
			}
			findingLinks[edge.To]++
		default:
			return fmt.Errorf("corpus analysis edge %q has unsupported kind %q", edge.ID, edge.Kind)
		}
	}
	for findingID := range findings {
		if findingLinks[findingID] < 2 {
			return fmt.Errorf("corpus finding %q has fewer than two claim links", findingID)
		}
	}
	return nil
}

func (s *Store) upsertKnowledgeGraph(graph KnowledgeGraph, requireCurrentEvidence, requireActiveExternalClaims bool) error {
	graph = normalizeKnowledgeGraph(graph)
	if err := ValidateKnowledgeGraph(graph); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if requireCurrentEvidence {
		for _, node := range graph.Nodes {
			if err := s.requireCurrentKnowledgeEvidence(node.Evidence); err != nil {
				return fmt.Errorf("knowledge node %q: %w", node.ID, err)
			}
		}
		for _, edge := range graph.Edges {
			if err := s.requireCurrentKnowledgeEvidence(edge.Evidence); err != nil {
				return fmt.Errorf("knowledge edge %q: %w", edge.ID, err)
			}
		}
	}

	type existingKnowledgeNode struct {
		kind   KnowledgeNodeKind
		status KnowledgeStatus
	}
	existing := make(map[string]existingKnowledgeNode)
	rows, err := s.db.Query(`SELECT id, kind, status FROM knowledge_nodes`)
	if err != nil {
		return fmt.Errorf("read existing knowledge nodes: %w", err)
	}
	for rows.Next() {
		var id string
		var node existingKnowledgeNode
		if err := rows.Scan(&id, &node.kind, &node.status); err != nil {
			rows.Close()
			return err
		}
		existing[id] = node
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, node := range graph.Nodes {
		existing[node.ID] = existingKnowledgeNode{kind: node.Kind, status: node.Status}
	}
	for _, edge := range graph.Edges {
		_, fromExists := existing[edge.From]
		_, toExists := existing[edge.To]
		if !fromExists || !toExists {
			return fmt.Errorf("knowledge edge %q references missing endpoint %q -> %q", edge.ID, edge.From, edge.To)
		}
	}
	if requireActiveExternalClaims {
		supplied := make(map[string]bool, len(graph.Nodes))
		for _, node := range graph.Nodes {
			supplied[node.ID] = true
		}
		checked := make(map[string]bool)
		for _, edge := range graph.Edges {
			for _, endpoint := range []string{edge.From, edge.To} {
				if supplied[endpoint] || checked[endpoint] {
					continue
				}
				checked[endpoint] = true
				node := existing[endpoint]
				if node.status != KnowledgeStatusActive || node.kind != KnowledgeNodeClaim {
					return fmt.Errorf("%w: corpus endpoint %s is %s/%s", ErrKnowledgeEndpointsNotActive, endpoint, node.kind, node.status)
				}
				anchors, err := loadKnowledgeEvidence(s.db, "knowledge_node_evidence", "node_id", endpoint)
				if err != nil {
					return fmt.Errorf("read corpus endpoint evidence %q: %w", endpoint, err)
				}
				if err := s.requireCurrentKnowledgeEvidence(anchors); err != nil {
					return fmt.Errorf("corpus endpoint %q: %w", endpoint, err)
				}
			}
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin knowledge graph update: %w", err)
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%v; knowledge graph rollback failed: %w", cause, rollbackErr)
		}
		return cause
	}
	for _, node := range graph.Nodes {
		status, err := knowledgeStatusForUpsert(tx, "knowledge_nodes", "knowledge_node_evidence", "node_id", node.ID, node.Status, node.Origin, node.Evidence)
		if err != nil {
			return rollback(fmt.Errorf("preserve knowledge node review %q: %w", node.ID, err))
		}
		label, body := node.Label, node.Body
		if node.Origin == KnowledgeOriginGenerated {
			label, body, err = knowledgeContentForUpsert(tx, KnowledgeObjectNode, node.ID, label, body)
			if err != nil {
				return rollback(fmt.Errorf("preserve knowledge node edit %q: %w", node.ID, err))
			}
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_nodes
(id, kind, label, body, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, label=excluded.label, body=excluded.body,
status=excluded.status, origin=excluded.origin, confidence=excluded.confidence, updated=excluded.updated`,
			node.ID, node.Kind, label, body, status, node.Origin,
			node.Confidence, node.Created, node.Updated); err != nil {
			return rollback(fmt.Errorf("upsert knowledge node %q: %w", node.ID, err))
		}
		if _, err := tx.Exec(`DELETE FROM knowledge_node_evidence WHERE node_id = ?`, node.ID); err != nil {
			return rollback(err)
		}
		for ordinal, anchor := range node.Evidence {
			if err := insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", node.ID, ordinal, anchor); err != nil {
				return rollback(err)
			}
		}
	}
	for _, edge := range graph.Edges {
		status, err := knowledgeStatusForUpsert(tx, "knowledge_edges", "knowledge_edge_evidence", "edge_id", edge.ID, edge.Status, edge.Origin, edge.Evidence)
		if err != nil {
			return rollback(fmt.Errorf("preserve knowledge edge review %q: %w", edge.ID, err))
		}
		label := edge.Label
		if edge.Origin == KnowledgeOriginGenerated {
			label, _, err = knowledgeContentForUpsert(tx, KnowledgeObjectEdge, edge.ID, label, "")
			if err != nil {
				return rollback(fmt.Errorf("preserve knowledge edge edit %q: %w", edge.ID, err))
			}
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_edges
(id, from_node, to_node, kind, label, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET from_node=excluded.from_node, to_node=excluded.to_node,
kind=excluded.kind, label=excluded.label, status=excluded.status, origin=excluded.origin,
confidence=excluded.confidence, updated=excluded.updated`,
			edge.ID, edge.From, edge.To, edge.Kind, label, status,
			edge.Origin, edge.Confidence, edge.Created, edge.Updated); err != nil {
			return rollback(fmt.Errorf("upsert knowledge edge %q: %w", edge.ID, err))
		}
		if _, err := tx.Exec(`DELETE FROM knowledge_edge_evidence WHERE edge_id = ?`, edge.ID); err != nil {
			return rollback(err)
		}
		for ordinal, anchor := range edge.Evidence {
			if err := insertKnowledgeEvidence(tx, "knowledge_edge_evidence", "edge_id", edge.ID, ordinal, anchor); err != nil {
				return rollback(err)
			}
		}
	}
	if err := invalidateChangedKnowledgeNodeMerges(tx, s.entries); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`UPDATE knowledge_edges
SET status = ?, updated = ?
WHERE origin = ? AND status = ? AND (
  EXISTS (SELECT 1 FROM knowledge_nodes n WHERE n.id = knowledge_edges.from_node AND n.status != ?)
  OR EXISTS (SELECT 1 FROM knowledge_nodes n WHERE n.id = knowledge_edges.to_node AND n.status != ?)
)`, KnowledgeStatusDraft, time.Now().UTC().Format(time.RFC3339), KnowledgeOriginGenerated,
		KnowledgeStatusActive, KnowledgeStatusActive, KnowledgeStatusActive); err != nil {
		return rollback(fmt.Errorf("reconcile generated edge review status: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit knowledge graph update: %w", err)
	}
	return nil
}

func (s *Store) requireCurrentKnowledgeEvidence(anchors []EvidenceAnchor) error {
	for _, anchor := range anchors {
		resolution := resolveEvidenceAnchorFromEntries(anchor, s.entries)
		if resolution.State != EvidenceCurrent {
			return fmt.Errorf("%w: %s is %s", ErrKnowledgeEvidenceNotCurrent, anchor.CitationID, resolution.State)
		}
	}
	return nil
}

func knowledgeStatusForUpsert(tx *sql.Tx, objectTable, evidenceTable, ownerColumn, id string, incoming KnowledgeStatus, origin KnowledgeOrigin, evidence []EvidenceAnchor) (KnowledgeStatus, error) {
	if incoming != KnowledgeStatusDraft || origin != KnowledgeOriginGenerated {
		return incoming, nil
	}
	var existing KnowledgeStatus
	var existingOrigin KnowledgeOrigin
	if err := tx.QueryRow("SELECT status, origin FROM "+objectTable+" WHERE id = ?", id).Scan(&existing, &existingOrigin); err != nil {
		if err == sql.ErrNoRows {
			return incoming, nil
		}
		return "", err
	}
	if existingOrigin != KnowledgeOriginGenerated ||
		(existing != KnowledgeStatusActive && existing != KnowledgeStatusRejected && existing != KnowledgeStatusResolved) {
		return incoming, nil
	}
	stored, err := loadKnowledgeEvidence(tx, evidenceTable, ownerColumn, id)
	if err != nil {
		return "", err
	}
	if equalEvidenceAnchors(stored, evidence) {
		return existing, nil
	}
	return incoming, nil
}

func equalEvidenceAnchors(left, right []EvidenceAnchor) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[EvidenceAnchor]int, len(left))
	for _, anchor := range left {
		counts[anchor]++
	}
	for _, anchor := range right {
		if counts[anchor] == 0 {
			return false
		}
		counts[anchor]--
	}
	return true
}

func normalizeKnowledgeGraph(graph KnowledgeGraph) KnowledgeGraph {
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range graph.Nodes {
		if graph.Nodes[i].Status == "" {
			graph.Nodes[i].Status = KnowledgeStatusActive
		}
		if graph.Nodes[i].Origin == "" {
			graph.Nodes[i].Origin = KnowledgeOriginGenerated
		}
		if graph.Nodes[i].Created == "" {
			graph.Nodes[i].Created = now
		}
		if graph.Nodes[i].Updated == "" {
			graph.Nodes[i].Updated = now
		}
	}
	for i := range graph.Edges {
		if graph.Edges[i].Status == "" {
			graph.Edges[i].Status = KnowledgeStatusActive
		}
		if graph.Edges[i].Origin == "" {
			graph.Edges[i].Origin = KnowledgeOriginGenerated
		}
		if graph.Edges[i].Created == "" {
			graph.Edges[i].Created = now
		}
		if graph.Edges[i].Updated == "" {
			graph.Edges[i].Updated = now
		}
	}
	return graph
}

func insertKnowledgeEvidence(tx *sql.Tx, table, ownerColumn, ownerID string, ordinal int, anchor EvidenceAnchor) error {
	query := fmt.Sprintf(`INSERT INTO %s
(%s, ordinal, citation_id, document_id, document_revision, chunk_hash, evidence_hash,
 source_path, page, block_index, block_chunk_index, excerpt)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, table, ownerColumn)
	_, err := tx.Exec(query, ownerID, ordinal, anchor.CitationID, anchor.DocumentID,
		anchor.DocumentRevision, anchor.ChunkHash, anchor.EvidenceHash, anchor.SourcePath,
		anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex, anchor.Excerpt)
	if err != nil {
		return fmt.Errorf("store knowledge evidence for %q: %w", ownerID, err)
	}
	return nil
}

func (s *Store) LoadKnowledgeGraph() (KnowledgeGraph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var graph KnowledgeGraph
	rows, err := s.db.Query(`SELECT id, kind, label, body, status, origin, confidence, created, updated
FROM knowledge_nodes ORDER BY id`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var node KnowledgeNode
		if err := rows.Scan(&node.ID, &node.Kind, &node.Label, &node.Body, &node.Status,
			&node.Origin, &node.Confidence, &node.Created, &node.Updated); err != nil {
			rows.Close()
			return graph, err
		}
		graph.Nodes = append(graph.Nodes, node)
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	rows, err = s.db.Query(`SELECT id, from_node, to_node, kind, label, status, origin, confidence, created, updated
FROM knowledge_edges ORDER BY id`)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var edge KnowledgeEdge
		if err := rows.Scan(&edge.ID, &edge.From, &edge.To, &edge.Kind, &edge.Label,
			&edge.Status, &edge.Origin, &edge.Confidence, &edge.Created, &edge.Updated); err != nil {
			rows.Close()
			return graph, err
		}
		graph.Edges = append(graph.Edges, edge)
	}
	if err := rows.Close(); err != nil {
		return graph, err
	}
	for i := range graph.Nodes {
		graph.Nodes[i].Evidence, err = loadKnowledgeEvidence(s.db, "knowledge_node_evidence", "node_id", graph.Nodes[i].ID)
		if err != nil {
			return KnowledgeGraph{}, err
		}
	}
	for i := range graph.Edges {
		graph.Edges[i].Evidence, err = loadKnowledgeEvidence(s.db, "knowledge_edge_evidence", "edge_id", graph.Edges[i].ID)
		if err != nil {
			return KnowledgeGraph{}, err
		}
	}
	if err := ValidateKnowledgeGraph(graph); err != nil {
		return KnowledgeGraph{}, fmt.Errorf("stored knowledge graph is invalid: %w", err)
	}
	nodeIDs := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeIDs[node.ID] = true
	}
	for _, edge := range graph.Edges {
		if !nodeIDs[edge.From] || !nodeIDs[edge.To] {
			return KnowledgeGraph{}, fmt.Errorf("stored knowledge edge %q references a missing endpoint", edge.ID)
		}
	}
	return graph, nil
}

type knowledgeEvidenceQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func loadKnowledgeEvidence(q knowledgeEvidenceQuerier, table, ownerColumn, ownerID string) ([]EvidenceAnchor, error) {
	query := fmt.Sprintf(`SELECT citation_id, document_id, document_revision, chunk_hash, evidence_hash,
source_path, page, block_index, block_chunk_index, excerpt FROM %s WHERE %s = ? ORDER BY ordinal`, table, ownerColumn)
	rows, err := q.Query(query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EvidenceAnchor
	for rows.Next() {
		var anchor EvidenceAnchor
		if err := rows.Scan(&anchor.CitationID, &anchor.DocumentID, &anchor.DocumentRevision,
			&anchor.ChunkHash, &anchor.EvidenceHash, &anchor.SourcePath, &anchor.Page,
			&anchor.BlockIndex, &anchor.BlockChunkIndex, &anchor.Excerpt); err != nil {
			return nil, err
		}
		result = append(result, anchor)
	}
	return result, rows.Err()
}

func (s *Store) ResolveEvidenceAnchor(anchor EvidenceAnchor) EvidenceResolution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return resolveEvidenceAnchorFromEntries(anchor, s.entries)
}

func resolveEvidenceAnchorFromEntries(anchor EvidenceAnchor, entries []Entry) EvidenceResolution {
	resolution := EvidenceResolution{Anchor: anchor, State: EvidenceMissing}
	if validateEvidenceAnchor(anchor) != nil {
		return resolution
	}
	for _, entry := range entries {
		citationID, _ := CitationForEntry(entry)
		if citationID != anchor.CitationID {
			continue
		}
		resolution.State = EvidenceStale
		resolution.CurrentDocumentRevision = entry.DocumentRevision
		resolution.CurrentChunkHash = entry.ChunkHash
		if entry.DocumentID == anchor.DocumentID && entry.DocumentRevision == anchor.DocumentRevision &&
			entry.ChunkHash == anchor.ChunkHash && strings.Contains(entry.Text, anchor.Excerpt) {
			resolution.State = EvidenceCurrent
		}
		return resolution
	}
	return resolution
}
