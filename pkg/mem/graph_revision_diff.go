package mem

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// KnowledgeRevisionDiffOptions limits a revision report to one document.
// Document accepts the same document ID or source path as map coverage.
type KnowledgeRevisionDiffOptions struct {
	Document string `json:"document,omitempty"`
}

type KnowledgeRevisionDiffSummary struct {
	Documents        int `json:"documents"`
	CurrentDocuments int `json:"current_documents"`
	ChangedDocuments int `json:"changed_documents"`
	MissingDocuments int `json:"missing_documents"`
	CurrentAnchors   int `json:"current_anchors"`
	ChangedAnchors   int `json:"changed_anchors"`
	MissingAnchors   int `json:"missing_anchors"`
	AffectedNodes    int `json:"affected_nodes"`
	AffectedEdges    int `json:"affected_edges"`
}

type KnowledgeRevisionAnchorDiff struct {
	State                    EvidenceState `json:"state"`
	CitationID               string        `json:"citation_id"`
	Page                     int           `json:"page"`
	BlockIndex               int           `json:"block_index"`
	BlockChunkIndex          int           `json:"block_chunk_index"`
	PreviousDocumentRevision string        `json:"previous_document_revision"`
	CurrentDocumentRevision  string        `json:"current_document_revision,omitempty"`
	PreviousChunkHash        string        `json:"previous_chunk_hash"`
	CurrentChunkHash         string        `json:"current_chunk_hash,omitempty"`
	PreviousExcerpt          string        `json:"previous_excerpt"`
	CurrentText              string        `json:"current_text,omitempty"`
}

type KnowledgeRevisionObjectDiff struct {
	ObjectType    KnowledgeObjectType           `json:"object_type"`
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	Label         string                        `json:"label,omitempty"`
	Status        KnowledgeStatus               `json:"status"`
	EvidenceState EvidenceState                 `json:"evidence_state"`
	Evidence      []KnowledgeRevisionAnchorDiff `json:"evidence"`
}

type KnowledgeDocumentRevisionDiff struct {
	DocumentID        string                        `json:"document_id"`
	SourcePath        string                        `json:"source_path"`
	State             EvidenceState                 `json:"state"`
	PreviousRevisions []string                      `json:"previous_revisions"`
	CurrentRevision   string                        `json:"current_revision,omitempty"`
	CurrentChunks     int                           `json:"current_chunks"`
	CurrentAnchors    int                           `json:"current_anchors"`
	ChangedAnchors    int                           `json:"changed_anchors"`
	MissingAnchors    int                           `json:"missing_anchors"`
	AffectedNodes     int                           `json:"affected_nodes"`
	AffectedEdges     int                           `json:"affected_edges"`
	Objects           []KnowledgeRevisionObjectDiff `json:"objects"`
}

type KnowledgeRevisionDiffReport struct {
	GeneratedAt string                          `json:"generated_at"`
	Scope       KnowledgeRevisionDiffOptions    `json:"scope"`
	Summary     KnowledgeRevisionDiffSummary    `json:"summary"`
	Documents   []KnowledgeDocumentRevisionDiff `json:"documents"`
	Limitations []string                        `json:"limitations"`
}

// BuildKnowledgeRevisionDiff compares the immutable evidence snapshots kept by
// the map with the current versioned chunks in the active database. It is
// read-only and never calls a model. The report deliberately does not claim a
// complete textual diff: old non-cited chunks are not retained by Store.
func (s *Store) BuildKnowledgeRevisionDiff(options KnowledgeRevisionDiffOptions) (KnowledgeRevisionDiffReport, error) {
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeRevisionDiffReport{}, err
	}
	review, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return KnowledgeRevisionDiffReport{}, err
	}
	return s.buildKnowledgeRevisionDiff(options, graph, review)
}

func (s *Store) buildKnowledgeRevisionDiff(options KnowledgeRevisionDiffOptions, graph KnowledgeGraph, review KnowledgeReviewReport) (KnowledgeRevisionDiffReport, error) {
	options.Document = strings.TrimSpace(options.Document)
	s.mu.RLock()
	entries := make([]Entry, len(s.entries))
	for i := range s.entries {
		entries[i] = cloneEntry(s.entries[i])
	}
	s.mu.RUnlock()

	currentByCitation := make(map[string]Entry, len(entries))
	currentByDocument := make(map[string][]Entry)
	for _, entry := range entries {
		if entry.DocumentID == "" || entry.DocumentRevision == "" || entry.SourcePath == "" {
			continue
		}
		citationID, _ := CitationForEntry(entry)
		currentByCitation[citationID] = entry
		currentByDocument[entry.DocumentID] = append(currentByDocument[entry.DocumentID], entry)
	}

	type documentBuilder struct {
		diff      KnowledgeDocumentRevisionDiff
		revisions map[string]bool
		nodes     map[string]bool
		edges     map[string]bool
	}
	builders := make(map[string]*documentBuilder)
	matched := options.Document == ""
	for _, item := range review.Items {
		for _, resolution := range item.Evidence {
			anchor := resolution.Anchor
			if options.Document != "" && anchor.DocumentID != options.Document && !coveragePathsEqual(anchor.SourcePath, options.Document) {
				continue
			}
			matched = true
			builder := builders[anchor.DocumentID]
			if builder == nil {
				builder = &documentBuilder{
					diff:      KnowledgeDocumentRevisionDiff{DocumentID: anchor.DocumentID, SourcePath: anchor.SourcePath, State: EvidenceCurrent},
					revisions: make(map[string]bool), nodes: make(map[string]bool), edges: make(map[string]bool),
				}
				builders[anchor.DocumentID] = builder
			}
			builder.revisions[anchor.DocumentRevision] = true
		}
	}
	if !matched {
		return KnowledgeRevisionDiffReport{}, fmt.Errorf("revision diff document %q was not found in map evidence", options.Document)
	}

	reviewByObject := make(map[string]KnowledgeReviewItem, len(review.Items))
	for _, item := range review.Items {
		reviewByObject[string(item.ObjectType)+"\x00"+item.ID] = item
	}
	appendObject := func(objectType KnowledgeObjectType, id, kind, label string, status KnowledgeStatus) {
		item, ok := reviewByObject[string(objectType)+"\x00"+id]
		if !ok {
			return
		}
		byDocument := make(map[string][]KnowledgeRevisionAnchorDiff)
		stateByDocument := make(map[string]EvidenceState)
		for _, resolution := range item.Evidence {
			anchor := resolution.Anchor
			builder := builders[anchor.DocumentID]
			if builder == nil {
				continue
			}
			change := KnowledgeRevisionAnchorDiff{
				State: resolution.State, CitationID: anchor.CitationID,
				Page: anchor.Page, BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex,
				PreviousDocumentRevision: anchor.DocumentRevision, CurrentDocumentRevision: resolution.CurrentDocumentRevision,
				PreviousChunkHash: anchor.ChunkHash, CurrentChunkHash: resolution.CurrentChunkHash,
				PreviousExcerpt: anchor.Excerpt,
			}
			if current, exists := currentByCitation[anchor.CitationID]; exists {
				change.CurrentText = revisionDiffText(current.Text, 4000)
			}
			byDocument[anchor.DocumentID] = append(byDocument[anchor.DocumentID], change)
			previous := stateByDocument[anchor.DocumentID]
			if previous == "" || resolution.State == EvidenceMissing || (resolution.State == EvidenceStale && previous != EvidenceMissing) {
				stateByDocument[anchor.DocumentID] = resolution.State
			}
		}
		for documentID, evidence := range byDocument {
			builder := builders[documentID]
			sort.Slice(evidence, func(i, j int) bool {
				if evidence[i].Page != evidence[j].Page {
					return evidence[i].Page < evidence[j].Page
				}
				if evidence[i].BlockIndex != evidence[j].BlockIndex {
					return evidence[i].BlockIndex < evidence[j].BlockIndex
				}
				return evidence[i].BlockChunkIndex < evidence[j].BlockChunkIndex
			})
			builder.diff.Objects = append(builder.diff.Objects, KnowledgeRevisionObjectDiff{
				ObjectType: objectType, ID: id, Kind: kind, Label: label, Status: status,
				EvidenceState: stateByDocument[documentID], Evidence: evidence,
			})
			if stateByDocument[documentID] != EvidenceCurrent {
				if objectType == KnowledgeObjectNode {
					builder.nodes[id] = true
				} else {
					builder.edges[id] = true
				}
			}
		}
	}
	for _, node := range graph.Nodes {
		appendObject(KnowledgeObjectNode, node.ID, string(node.Kind), node.Label, node.Status)
	}
	for _, edge := range graph.Edges {
		label := edge.Label
		if strings.TrimSpace(label) == "" {
			label = string(edge.Kind)
		}
		appendObject(KnowledgeObjectEdge, edge.ID, string(edge.Kind), label, edge.Status)
	}

	report := KnowledgeRevisionDiffReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), Scope: options,
		Limitations: []string{
			"Сравниваются сохранённые evidence карты и текущие versioned chunks активной базы; модель не вызывается.",
			"Полный текст старого документа не хранится: отчёт показывает точные прежние выдержки карты, но не выдаёт полный построчный diff всего файла.",
			"Текущий chunk без прежнего evidence нельзя достоверно назвать добавленным знанием; для этого требуется отдельный исторический снимок корпуса.",
		},
	}
	globalNodes, globalEdges := make(map[string]bool), make(map[string]bool)
	for _, builder := range builders {
		for revision := range builder.revisions {
			builder.diff.PreviousRevisions = append(builder.diff.PreviousRevisions, revision)
		}
		sort.Strings(builder.diff.PreviousRevisions)
		current := currentByDocument[builder.diff.DocumentID]
		builder.diff.CurrentChunks = len(current)
		if len(current) == 0 {
			builder.diff.State = EvidenceMissing
		} else {
			builder.diff.CurrentRevision = current[0].DocumentRevision
			if current[0].SourcePath != "" {
				builder.diff.SourcePath = current[0].SourcePath
			}
		}
		for _, object := range builder.diff.Objects {
			for _, anchor := range object.Evidence {
				switch anchor.State {
				case EvidenceCurrent:
					builder.diff.CurrentAnchors++
				case EvidenceStale:
					builder.diff.ChangedAnchors++
					if builder.diff.State != EvidenceMissing {
						builder.diff.State = EvidenceStale
					}
				case EvidenceMissing:
					builder.diff.MissingAnchors++
					builder.diff.State = EvidenceMissing
				}
			}
		}
		builder.diff.AffectedNodes, builder.diff.AffectedEdges = len(builder.nodes), len(builder.edges)
		for id := range builder.nodes {
			globalNodes[id] = true
		}
		for id := range builder.edges {
			globalEdges[id] = true
		}
		sort.Slice(builder.diff.Objects, func(i, j int) bool {
			left, right := builder.diff.Objects[i], builder.diff.Objects[j]
			if left.EvidenceState != right.EvidenceState {
				return revisionDiffStateOrder(left.EvidenceState) < revisionDiffStateOrder(right.EvidenceState)
			}
			if left.ObjectType != right.ObjectType {
				return left.ObjectType < right.ObjectType
			}
			return strings.ToLower(left.Label) < strings.ToLower(right.Label)
		})
		report.Documents = append(report.Documents, builder.diff)
	}
	sort.Slice(report.Documents, func(i, j int) bool {
		return strings.ToLower(report.Documents[i].SourcePath) < strings.ToLower(report.Documents[j].SourcePath)
	})
	for _, document := range report.Documents {
		report.Summary.Documents++
		report.Summary.CurrentAnchors += document.CurrentAnchors
		report.Summary.ChangedAnchors += document.ChangedAnchors
		report.Summary.MissingAnchors += document.MissingAnchors
		switch document.State {
		case EvidenceCurrent:
			report.Summary.CurrentDocuments++
		case EvidenceStale:
			report.Summary.ChangedDocuments++
		case EvidenceMissing:
			report.Summary.MissingDocuments++
		}
	}
	report.Summary.AffectedNodes, report.Summary.AffectedEdges = len(globalNodes), len(globalEdges)
	return report, nil
}

func revisionDiffStateOrder(state EvidenceState) int {
	switch state {
	case EvidenceMissing:
		return 0
	case EvidenceStale:
		return 1
	default:
		return 2
	}
}

func revisionDiffText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
