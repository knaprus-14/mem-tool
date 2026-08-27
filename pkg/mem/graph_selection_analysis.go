package mem

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const KnowledgeSelectionAnalysisVersion = 1

type KnowledgeSelectionAnalysisRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
}

type KnowledgeSelectionSourceRef struct {
	CitationID      string `json:"citation_id"`
	SourcePath      string `json:"source_path"`
	Page            int    `json:"page"`
	BlockIndex      int    `json:"block_index"`
	BlockChunkIndex int    `json:"block_chunk_index"`
}

type KnowledgeSelectionAnalysisObject struct {
	ObjectType    KnowledgeObjectType           `json:"object_type"`
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	Label         string                        `json:"label,omitempty"`
	Status        KnowledgeStatus               `json:"status"`
	Origin        KnowledgeOrigin               `json:"origin"`
	EvidenceState EvidenceState                 `json:"evidence_state"`
	Sources       []KnowledgeSelectionSourceRef `json:"sources,omitempty"`
}

type KnowledgeSelectionAnalysisEndpoint struct {
	ID       string            `json:"id"`
	Kind     KnowledgeNodeKind `json:"kind"`
	Label    string            `json:"label"`
	Selected bool              `json:"selected"`
}

type KnowledgeSelectionAnalysisRelation struct {
	ID            string                             `json:"id"`
	Kind          KnowledgeRelationKind              `json:"kind"`
	Label         string                             `json:"label,omitempty"`
	Status        KnowledgeStatus                    `json:"status"`
	Origin        KnowledgeOrigin                    `json:"origin"`
	EvidenceState EvidenceState                      `json:"evidence_state"`
	Explicit      bool                               `json:"explicit"`
	From          KnowledgeSelectionAnalysisEndpoint `json:"from"`
	To            KnowledgeSelectionAnalysisEndpoint `json:"to"`
	Sources       []KnowledgeSelectionSourceRef      `json:"sources,omitempty"`
}

type KnowledgeSelectionAnalysisDocument struct {
	DocumentID string   `json:"document_id"`
	SourcePath string   `json:"source_path"`
	Pages      []int    `json:"pages,omitempty"`
	ObjectIDs  []string `json:"object_ids"`
	Evidence   int      `json:"evidence"`
}

type KnowledgeSelectionAnalysisSummary struct {
	Objects           int `json:"objects"`
	Documents         int `json:"documents"`
	InternalRelations int `json:"internal_relations"`
	BoundaryRelations int `json:"boundary_relations"`
	Comparisons       int `json:"comparisons"`
	Dependencies      int `json:"dependencies"`
	Contradictions    int `json:"contradictions"`
	Gaps              int `json:"gaps"`
	AnalyticalObjects int `json:"analytical_objects"`
}

// KnowledgeSelectionAnalysis is a deterministic host-side report. It reports
// only objects and typed relations that already exist in the graph; absence of
// a relation is never interpreted as proof that a fact or definition is absent.
type KnowledgeSelectionAnalysis struct {
	Version        int                                  `json:"version"`
	Digest         string                               `json:"digest"`
	ManifestDigest string                               `json:"manifest_digest"`
	Ready          bool                                 `json:"ready"`
	Summary        KnowledgeSelectionAnalysisSummary    `json:"summary"`
	Objects        []KnowledgeSelectionAnalysisObject   `json:"objects"`
	Documents      []KnowledgeSelectionAnalysisDocument `json:"documents"`
	Internal       []KnowledgeSelectionAnalysisRelation `json:"internal_relations,omitempty"`
	Boundary       []KnowledgeSelectionAnalysisRelation `json:"boundary_relations,omitempty"`
	Comparisons    []KnowledgeSelectionAnalysisRelation `json:"comparisons,omitempty"`
	Dependencies   []KnowledgeSelectionAnalysisRelation `json:"dependencies,omitempty"`
	Contradictions []KnowledgeSelectionAnalysisRelation `json:"contradictions,omitempty"`
	Gaps           []KnowledgeSelectionAnalysisRelation `json:"gaps,omitempty"`
	Analytical     []KnowledgeSelectionAnalysisObject   `json:"analytical_objects,omitempty"`
	Blockers       []KnowledgeSelectionBlocker          `json:"blockers,omitempty"`
}

type KnowledgeSelectionAnalysisSaveRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	ExpectedAnalysisDigest string                    `json:"expected_analysis_digest"`
	Label                  string                    `json:"label"`
	Author                 string                    `json:"author"`
	Comment                string                    `json:"comment,omitempty"`
}

type KnowledgeSelectionReportRecord struct {
	ID             int64                     `json:"id"`
	NodeID         string                    `json:"node_id"`
	EdgeIDs        []string                  `json:"edge_ids"`
	Selection      KnowledgeSelectionRequest `json:"selection"`
	ManifestDigest string                    `json:"manifest_digest"`
	AnalysisDigest string                    `json:"analysis_digest"`
	Author         string                    `json:"author"`
	Comment        string                    `json:"comment,omitempty"`
	ContentDigest  string                    `json:"content_digest"`
	EvidenceDigest string                    `json:"evidence_digest"`
	Created        string                    `json:"created"`
}

type KnowledgeSelectionAnalysisSaveResult struct {
	Node     KnowledgeNode                  `json:"node"`
	Edges    []KnowledgeEdge                `json:"edges"`
	Analysis KnowledgeSelectionAnalysis     `json:"analysis"`
	Report   KnowledgeSelectionReportRecord `json:"report"`
}

func (s *Store) AnalyzeKnowledgeSelection(request KnowledgeSelectionAnalysisRequest) (KnowledgeSelectionAnalysis, error) {
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil {
		return KnowledgeSelectionAnalysis{}, err
	}
	if request.ExpectedManifestDigest == "" || request.ExpectedManifestDigest != manifest.Digest {
		return KnowledgeSelectionAnalysis{}, fmt.Errorf("%w: expected %s, current %s", ErrKnowledgeSelectionChanged, request.ExpectedManifestDigest, manifest.Digest)
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionAnalysis{}, err
	}
	review, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionAnalysis{}, err
	}
	nodes := make(map[string]KnowledgeNode, len(graph.Nodes))
	reviews := make(map[string]KnowledgeReviewItem, len(review.Items))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, item := range review.Items {
		reviews[string(item.ObjectType)+":"+item.ID] = item
	}
	selectedNodes := make(map[string]bool, len(request.Selection.NodeIDs))
	selectedEdges := make(map[string]bool, len(request.Selection.EdgeIDs))
	for _, id := range request.Selection.NodeIDs {
		selectedNodes[id] = true
	}
	for _, id := range request.Selection.EdgeIDs {
		selectedEdges[id] = true
	}
	result := KnowledgeSelectionAnalysis{
		Version: KnowledgeSelectionAnalysisVersion, ManifestDigest: manifest.Digest,
		Ready: manifest.Ready, Blockers: append([]KnowledgeSelectionBlocker(nil), manifest.Blockers...),
		Objects: make([]KnowledgeSelectionAnalysisObject, 0, len(manifest.Nodes)+len(manifest.Edges)),
	}
	documents := make(map[string]*knowledgeSelectionAnalysisDocumentBuilder)
	for _, selected := range append(append([]KnowledgeSelectionObject(nil), manifest.Nodes...), manifest.Edges...) {
		var anchors []EvidenceAnchor
		if selected.ObjectType == KnowledgeObjectNode {
			anchors = nodes[selected.ID].Evidence
		} else {
			for _, edge := range graph.Edges {
				if edge.ID == selected.ID {
					anchors = edge.Evidence
					break
				}
			}
		}
		object := KnowledgeSelectionAnalysisObject{
			ObjectType: selected.ObjectType, ID: selected.ID, Kind: selected.Kind, Label: selected.Label,
			Status: selected.Status, Origin: selected.Origin, EvidenceState: selected.EvidenceState,
			Sources: knowledgeSelectionSourceRefs(anchors),
		}
		result.Objects = append(result.Objects, object)
		if selected.ObjectType == KnowledgeObjectNode && isKnowledgeSelectionAnalyticalKind(KnowledgeNodeKind(selected.Kind)) {
			result.Analytical = append(result.Analytical, object)
		}
		knowledgeSelectionAddDocuments(documents, string(selected.ObjectType)+":"+selected.ID, anchors)
	}
	sort.Slice(graph.Edges, func(i, j int) bool { return graph.Edges[i].ID < graph.Edges[j].ID })
	for _, edge := range graph.Edges {
		fromSelected, toSelected := selectedNodes[edge.From], selectedNodes[edge.To]
		explicit := selectedEdges[edge.ID]
		if !explicit && !fromSelected && !toSelected {
			continue
		}
		relation := knowledgeSelectionAnalysisRelation(edge, nodes, reviews, selectedNodes, explicit)
		if explicit || (fromSelected && toSelected) {
			result.Internal = append(result.Internal, relation)
		} else if fromSelected != toSelected {
			result.Boundary = append(result.Boundary, relation)
		}
		switch edge.Kind {
		case KnowledgeRelationCompares:
			result.Comparisons = append(result.Comparisons, relation)
		case KnowledgeRelationPrerequisite, KnowledgeRelationDependsOn, KnowledgeRelationConstrains:
			result.Dependencies = append(result.Dependencies, relation)
		case KnowledgeRelationContradicts:
			result.Contradictions = append(result.Contradictions, relation)
		case KnowledgeRelationRevealsGap:
			result.Gaps = append(result.Gaps, relation)
		}
	}
	result.Documents = knowledgeSelectionAnalysisDocuments(documents)
	sort.Slice(result.Objects, func(i, j int) bool {
		if result.Objects[i].ObjectType != result.Objects[j].ObjectType {
			return result.Objects[i].ObjectType < result.Objects[j].ObjectType
		}
		return result.Objects[i].ID < result.Objects[j].ID
	})
	sort.Slice(result.Analytical, func(i, j int) bool { return result.Analytical[i].ID < result.Analytical[j].ID })
	result.Summary = KnowledgeSelectionAnalysisSummary{
		Objects: len(result.Objects), Documents: len(result.Documents), InternalRelations: len(result.Internal),
		BoundaryRelations: len(result.Boundary), Comparisons: len(result.Comparisons), Dependencies: len(result.Dependencies),
		Contradictions: len(result.Contradictions), Gaps: len(result.Gaps), AnalyticalObjects: len(result.Analytical),
	}
	result.Digest, err = knowledgeSelectionAnalysisDigest(result)
	if err != nil {
		return KnowledgeSelectionAnalysis{}, err
	}
	current, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil || current.Digest != manifest.Digest {
		return KnowledgeSelectionAnalysis{}, fmt.Errorf("%w: selection changed while analysis was running", ErrKnowledgeSelectionChanged)
	}
	return result, nil
}

type knowledgeSelectionAnalysisDocumentBuilder struct {
	documentID string
	sourcePath string
	pages      map[int]bool
	objects    map[string]bool
	evidence   map[string]bool
}

func knowledgeSelectionAddDocuments(documents map[string]*knowledgeSelectionAnalysisDocumentBuilder, objectID string, anchors []EvidenceAnchor) {
	for _, anchor := range anchors {
		key := firstNonEmpty(anchor.DocumentID, anchor.SourcePath)
		if key == "" {
			continue
		}
		builder := documents[key]
		if builder == nil {
			builder = &knowledgeSelectionAnalysisDocumentBuilder{documentID: anchor.DocumentID, sourcePath: anchor.SourcePath, pages: map[int]bool{}, objects: map[string]bool{}, evidence: map[string]bool{}}
			documents[key] = builder
		}
		if anchor.Page > 0 {
			builder.pages[anchor.Page] = true
		}
		builder.objects[objectID] = true
		builder.evidence[anchor.CitationID+"\x00"+anchor.EvidenceHash] = true
	}
}

func knowledgeSelectionAnalysisDocuments(builders map[string]*knowledgeSelectionAnalysisDocumentBuilder) []KnowledgeSelectionAnalysisDocument {
	result := make([]KnowledgeSelectionAnalysisDocument, 0, len(builders))
	for _, builder := range builders {
		document := KnowledgeSelectionAnalysisDocument{DocumentID: builder.documentID, SourcePath: builder.sourcePath, Evidence: len(builder.evidence)}
		for page := range builder.pages {
			document.Pages = append(document.Pages, page)
		}
		for object := range builder.objects {
			document.ObjectIDs = append(document.ObjectIDs, object)
		}
		sort.Ints(document.Pages)
		sort.Strings(document.ObjectIDs)
		result = append(result, document)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourcePath != result[j].SourcePath {
			return result[i].SourcePath < result[j].SourcePath
		}
		return result[i].DocumentID < result[j].DocumentID
	})
	return result
}

func knowledgeSelectionSourceRefs(anchors []EvidenceAnchor) []KnowledgeSelectionSourceRef {
	refs := make([]KnowledgeSelectionSourceRef, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	for _, anchor := range anchors {
		key := anchor.CitationID + "\x00" + anchor.EvidenceHash
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, KnowledgeSelectionSourceRef{CitationID: anchor.CitationID, SourcePath: anchor.SourcePath, Page: anchor.Page, BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].SourcePath != refs[j].SourcePath {
			return refs[i].SourcePath < refs[j].SourcePath
		}
		if refs[i].Page != refs[j].Page {
			return refs[i].Page < refs[j].Page
		}
		if refs[i].BlockIndex != refs[j].BlockIndex {
			return refs[i].BlockIndex < refs[j].BlockIndex
		}
		if refs[i].BlockChunkIndex != refs[j].BlockChunkIndex {
			return refs[i].BlockChunkIndex < refs[j].BlockChunkIndex
		}
		return refs[i].CitationID < refs[j].CitationID
	})
	return refs
}

func knowledgeSelectionAnalysisRelation(edge KnowledgeEdge, nodes map[string]KnowledgeNode, reviews map[string]KnowledgeReviewItem, selected map[string]bool, explicit bool) KnowledgeSelectionAnalysisRelation {
	item := reviews[string(KnowledgeObjectEdge)+":"+edge.ID]
	from, to := nodes[edge.From], nodes[edge.To]
	return KnowledgeSelectionAnalysisRelation{
		ID: edge.ID, Kind: edge.Kind, Label: edge.Label, Status: edge.Status, Origin: edge.Origin,
		EvidenceState: item.EvidenceState, Explicit: explicit, Sources: knowledgeSelectionSourceRefs(edge.Evidence),
		From: KnowledgeSelectionAnalysisEndpoint{ID: from.ID, Kind: from.Kind, Label: from.Label, Selected: selected[from.ID]},
		To:   KnowledgeSelectionAnalysisEndpoint{ID: to.ID, Kind: to.Kind, Label: to.Label, Selected: selected[to.ID]},
	}
}

func isKnowledgeSelectionAnalyticalKind(kind KnowledgeNodeKind) bool {
	return KnowledgeNodeLayerForKind(kind) == KnowledgeLayerAnalytics
}

func knowledgeSelectionAnalysisDigest(analysis KnowledgeSelectionAnalysis) (string, error) {
	pinned := analysis
	pinned.Digest = ""
	encoded, err := json.Marshal(pinned)
	if err != nil {
		return "", fmt.Errorf("encode knowledge selection analysis: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (s *Store) SaveKnowledgeSelectionAnalysis(request KnowledgeSelectionAnalysisSaveRequest) (KnowledgeSelectionAnalysisSaveResult, error) {
	nodeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.NodeIDs, MaxKnowledgeSelectionNodes, "node")
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, err
	}
	edgeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.EdgeIDs, MaxKnowledgeSelectionEdges, "edge")
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, err
	}
	request.Selection = KnowledgeSelectionRequest{NodeIDs: nodeIDs, EdgeIDs: edgeIDs}
	request.Label = strings.TrimSpace(request.Label)
	request.Author = strings.TrimSpace(request.Author)
	request.Comment = strings.TrimSpace(request.Comment)
	if request.Label == "" || !utf8.ValidString(request.Label) || utf8.RuneCountInString(request.Label) > MaxKnowledgeLabelRunes {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("knowledge selection report label must contain 1..%d runes", MaxKnowledgeLabelRunes)
	}
	if request.Author == "" || !utf8.ValidString(request.Author) || utf8.RuneCountInString(request.Author) > MaxKnowledgeReviewerRunes {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("knowledge selection report author must contain 1..%d runes", MaxKnowledgeReviewerRunes)
	}
	if !utf8.ValidString(request.Comment) || utf8.RuneCountInString(request.Comment) > MaxKnowledgeCommentRunes {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("knowledge selection report comment exceeds %d runes", MaxKnowledgeCommentRunes)
	}
	analysis, err := s.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{Selection: request.Selection, ExpectedManifestDigest: request.ExpectedManifestDigest})
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, err
	}
	if !analysis.Ready {
		return KnowledgeSelectionAnalysisSaveResult{}, ErrKnowledgeSelectionNotCurrent
	}
	if request.ExpectedAnalysisDigest == "" || request.ExpectedAnalysisDigest != analysis.Digest {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("%w: analysis changed", ErrKnowledgeSelectionChanged)
	}
	body, err := formatKnowledgeSelectionAnalysisReport(analysis)
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, err
	}
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil || manifest.Digest != analysis.ManifestDigest {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("%w: manifest changed before save", ErrKnowledgeSelectionChanged)
	}
	anchors := make([]EvidenceAnchor, 0, len(manifest.Evidence))
	for _, evidence := range manifest.Evidence {
		anchor, anchorErr := evidenceAnchorFromGrounded(evidence)
		if anchorErr != nil {
			return KnowledgeSelectionAnalysisSaveResult{}, anchorErr
		}
		anchors = append(anchors, anchor)
	}
	nodeID, edgePrefix, err := newKnowledgeSelectionReportIDs()
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("begin knowledge selection report: %w", err)
	}
	rollback := func(cause error) (KnowledgeSelectionAnalysisSaveResult, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("%v; knowledge selection report rollback failed: %w", cause, rollbackErr)
		}
		return KnowledgeSelectionAnalysisSaveResult{}, cause
	}
	if err := verifyKnowledgeSelectionManifestTx(tx, manifest); err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	node := KnowledgeNode{ID: nodeID, Kind: KnowledgeNodeNote, Label: request.Label, Body: body, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now, Evidence: anchors}
	if err := validateKnowledgeNode(node); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_nodes
(id, kind, label, body, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, node.ID, node.Kind, node.Label, node.Body, node.Status, node.Origin, node.Confidence, node.Created, node.Updated); err != nil {
		return rollback(fmt.Errorf("create knowledge selection report node: %w", err))
	}
	for ordinal, anchor := range node.Evidence {
		if err := insertKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", node.ID, ordinal, anchor); err != nil {
			return rollback(err)
		}
	}
	edges := make([]KnowledgeEdge, 0, len(manifest.Nodes))
	for ordinal, selected := range manifest.Nodes {
		evidence, loadErr := loadKnowledgeEvidence(tx, "knowledge_node_evidence", "node_id", selected.ID)
		if loadErr != nil {
			return rollback(loadErr)
		}
		edge := KnowledgeEdge{ID: fmt.Sprintf("%s-%03d", edgePrefix, ordinal+1), From: node.ID, To: selected.ID, Kind: KnowledgeRelationDerivedFrom, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Created: now, Updated: now, Evidence: evidence}
		if err := validateKnowledgeEdge(edge); err != nil {
			return rollback(err)
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_edges
(id, from_node, to_node, kind, label, status, origin, confidence, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, edge.ID, edge.From, edge.To, edge.Kind, edge.Label, edge.Status, edge.Origin, edge.Confidence, edge.Created, edge.Updated); err != nil {
			return rollback(fmt.Errorf("create knowledge selection report edge: %w", err))
		}
		for evidenceOrdinal, anchor := range edge.Evidence {
			if err := insertKnowledgeEvidence(tx, "knowledge_edge_evidence", "edge_id", edge.ID, evidenceOrdinal, anchor); err != nil {
				return rollback(err)
			}
		}
		edges = append(edges, edge)
	}
	contentDigest, err := KnowledgeContentDigest(KnowledgeObjectNode, node.Label, node.Body)
	if err != nil {
		return rollback(err)
	}
	evidenceDigest, err := KnowledgeEvidenceDigest(node.Evidence)
	if err != nil {
		return rollback(err)
	}
	record := KnowledgeSelectionReportRecord{NodeID: node.ID, Selection: request.Selection, ManifestDigest: manifest.Digest, AnalysisDigest: analysis.Digest, Author: request.Author, Comment: request.Comment, ContentDigest: contentDigest, EvidenceDigest: evidenceDigest, Created: now}
	for _, edge := range edges {
		record.EdgeIDs = append(record.EdgeIDs, edge.ID)
	}
	record, err = insertKnowledgeSelectionReport(tx, record)
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeSelectionAnalysisSaveResult{}, fmt.Errorf("commit knowledge selection report: %w", err)
	}
	return KnowledgeSelectionAnalysisSaveResult{Node: node, Edges: edges, Analysis: analysis, Report: record}, nil
}

func formatKnowledgeSelectionAnalysisReport(analysis KnowledgeSelectionAnalysis) (string, error) {
	var body strings.Builder
	body.WriteString("Зафиксированный анализ выбранной области\n\n")
	fmt.Fprintf(&body, "Объекты: %d; документы: %d; внутренние связи: %d; граничные связи: %d.\n", analysis.Summary.Objects, analysis.Summary.Documents, analysis.Summary.InternalRelations, analysis.Summary.BoundaryRelations)
	fmt.Fprintf(&body, "Сравнения: %d; зависимости: %d; противоречия: %d; пробелы: %d.\n", analysis.Summary.Comparisons, analysis.Summary.Dependencies, analysis.Summary.Contradictions, analysis.Summary.Gaps)
	appendRelations := func(title string, relations []KnowledgeSelectionAnalysisRelation) {
		if len(relations) == 0 {
			return
		}
		body.WriteString("\n" + title + ":\n")
		for _, relation := range relations {
			fmt.Fprintf(&body, "- %s -> %s [%s]", relation.From.Label, relation.To.Label, relation.Kind)
			if relation.Label != "" {
				fmt.Fprintf(&body, ": %s", relation.Label)
			}
			body.WriteByte('\n')
		}
	}
	appendRelations("Сравнения", analysis.Comparisons)
	appendRelations("Зависимости", analysis.Dependencies)
	appendRelations("Противоречия", analysis.Contradictions)
	appendRelations("Пробелы", analysis.Gaps)
	appendRelations("Связи с объектами вне выбора", analysis.Boundary)
	if len(analysis.Documents) > 0 {
		body.WriteString("\nИсточники:\n")
		for _, document := range analysis.Documents {
			name := filepath.Base(document.SourcePath)
			if name == "." || name == "" {
				name = document.DocumentID
			}
			fmt.Fprintf(&body, "- %s", name)
			if len(document.Pages) > 0 {
				fmt.Fprintf(&body, "; страницы %v", document.Pages)
			}
			fmt.Fprintf(&body, "; evidence %d\n", document.Evidence)
		}
	}
	if utf8.RuneCountInString(body.String()) > MaxKnowledgeBodyRunes {
		return "", fmt.Errorf("knowledge selection report exceeds %d runes; reduce the selection", MaxKnowledgeBodyRunes)
	}
	return body.String(), nil
}

func verifyKnowledgeSelectionManifestTx(tx *sql.Tx, manifest KnowledgeSelectionManifest) error {
	objects := append(append([]KnowledgeSelectionObject(nil), manifest.Nodes...), manifest.Edges...)
	for _, selected := range objects {
		var label, body string
		var status KnowledgeStatus
		var kind string
		var origin KnowledgeOrigin
		var table, owner string
		if selected.ObjectType == KnowledgeObjectNode {
			if err := tx.QueryRow(`SELECT kind, label, body, status, origin FROM knowledge_nodes WHERE id = ?`, selected.ID).Scan(&kind, &label, &body, &status, &origin); err != nil {
				return fmt.Errorf("verify selected node %q: %w", selected.ID, err)
			}
			table, owner = "knowledge_node_evidence", "node_id"
		} else {
			if err := tx.QueryRow(`SELECT kind, label, status, origin FROM knowledge_edges WHERE id = ?`, selected.ID).Scan(&kind, &label, &status, &origin); err != nil {
				return fmt.Errorf("verify selected edge %q: %w", selected.ID, err)
			}
			table, owner = "knowledge_edge_evidence", "edge_id"
		}
		if kind != selected.Kind || label != selected.Label || status != selected.Status || origin != selected.Origin {
			return fmt.Errorf("%w: selected %s %q changed", ErrKnowledgeSelectionChanged, selected.ObjectType, selected.ID)
		}
		contentDigest, err := KnowledgeContentDigest(selected.ObjectType, label, body)
		if err != nil || contentDigest != selected.ContentDigest {
			return fmt.Errorf("%w: selected %s %q content changed", ErrKnowledgeSelectionChanged, selected.ObjectType, selected.ID)
		}
		anchors, err := loadKnowledgeEvidence(tx, table, owner, selected.ID)
		if err != nil {
			return err
		}
		evidenceDigest, err := KnowledgeEvidenceDigest(anchors)
		if err != nil || evidenceDigest != selected.EvidenceDigest {
			return fmt.Errorf("%w: selected %s %q evidence changed", ErrKnowledgeSelectionChanged, selected.ObjectType, selected.ID)
		}
		resolutions, err := resolveEvidenceAnchorsFromQuerier(tx, anchors)
		if err != nil {
			return fmt.Errorf("read current selected evidence: %w", err)
		}
		for _, resolution := range resolutions {
			if resolution.State != EvidenceCurrent {
				return ErrKnowledgeSelectionNotCurrent
			}
		}
	}
	return nil
}

func newKnowledgeSelectionReportIDs() (string, string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate knowledge selection report ID: %w", err)
	}
	token := hex.EncodeToString(raw)
	return "selection-report-" + token, "selection-source-" + token, nil
}

func insertKnowledgeSelectionReport(tx *sql.Tx, record KnowledgeSelectionReportRecord) (KnowledgeSelectionReportRecord, error) {
	edgeJSON, err := json.Marshal(record.EdgeIDs)
	if err != nil {
		return KnowledgeSelectionReportRecord{}, err
	}
	selectionJSON, err := json.Marshal(record.Selection)
	if err != nil {
		return KnowledgeSelectionReportRecord{}, err
	}
	result, err := tx.Exec(`INSERT INTO knowledge_selection_reports
(node_id, edge_ids_json, selection_json, manifest_digest, analysis_digest, author, comment, content_digest, evidence_digest, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.NodeID, string(edgeJSON), string(selectionJSON), record.ManifestDigest, record.AnalysisDigest, record.Author, record.Comment, record.ContentDigest, record.EvidenceDigest, record.Created)
	if err != nil {
		return KnowledgeSelectionReportRecord{}, fmt.Errorf("append knowledge selection report: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return KnowledgeSelectionReportRecord{}, fmt.Errorf("read knowledge selection report ID: %w", err)
	}
	return record, nil
}

func (s *Store) ListKnowledgeSelectionReports(limit int) ([]KnowledgeSelectionReportRecord, error) {
	if limit <= 0 || limit > 10000 {
		return nil, errors.New("knowledge selection report limit must be between 1 and 10000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id, node_id, edge_ids_json, selection_json, manifest_digest, analysis_digest, author, comment, content_digest, evidence_digest, created
FROM knowledge_selection_reports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list knowledge selection reports: %w", err)
	}
	defer rows.Close()
	reports := make([]KnowledgeSelectionReportRecord, 0)
	for rows.Next() {
		var report KnowledgeSelectionReportRecord
		var edgeJSON, selectionJSON string
		if err := rows.Scan(&report.ID, &report.NodeID, &edgeJSON, &selectionJSON, &report.ManifestDigest, &report.AnalysisDigest, &report.Author, &report.Comment, &report.ContentDigest, &report.EvidenceDigest, &report.Created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(edgeJSON), &report.EdgeIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(selectionJSON), &report.Selection); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reports, nil
}
