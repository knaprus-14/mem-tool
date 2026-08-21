package mem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// KnowledgeCoverageOptions selects the current, versioned source chunks that
// form the denominator of a coverage report. An empty scope means the complete
// current corpus. Page bounds refer to physical page coordinates stored during
// import; unlocated chunks are therefore excluded when a page range is set.
type KnowledgeCoverageOptions struct {
	Document      string  `json:"document,omitempty"`
	Tag           string  `json:"tag,omitempty"`
	PageFrom      int     `json:"page_from,omitempty"`
	PageTo        int     `json:"page_to,omitempty"`
	LowConfidence float64 `json:"low_confidence_threshold,omitempty"`
}

type KnowledgeCoverageSummary struct {
	Documents              int     `json:"documents"`
	ChunksWithText         int     `json:"chunks_with_text"`
	PagesWithText          int     `json:"pages_with_text"`
	ProcessedChunks        int     `json:"processed_chunks"`
	UnprocessedChunks      int     `json:"unprocessed_chunks"`
	ProcessingPercent      float64 `json:"processing_percent"`
	CoveredChunks          int     `json:"covered_chunks"`
	UncoveredChunks        int     `json:"uncovered_chunks"`
	CoveragePercent        float64 `json:"coverage_percent"`
	FullyCoveredPages      int     `json:"fully_covered_pages"`
	PartiallyCoveredPages  int     `json:"partially_covered_pages"`
	UncoveredPages         int     `json:"uncovered_pages"`
	UnlocatedChunks        int     `json:"unlocated_chunks"`
	LowConfidenceOCRChunks int     `json:"low_confidence_ocr_chunks"`
	LowConfidenceOCRPages  int     `json:"low_confidence_ocr_pages"`
	WarningChunks          int     `json:"warning_chunks"`
	ExtractedNodes         int     `json:"extracted_nodes"`
	ExtractedRelations     int     `json:"extracted_relations"`
	DraftObjects           int     `json:"draft_objects"`
	ActiveObjects          int     `json:"active_objects"`
	RejectedObjects        int     `json:"rejected_objects"`
	ResolvedObjects        int     `json:"resolved_objects"`
	StaleEvidenceObjects   int     `json:"stale_evidence_objects"`
	MissingEvidenceObjects int     `json:"missing_evidence_objects"`
}

type KnowledgeDocumentCoverage struct {
	DocumentID             string                     `json:"document_id"`
	DocumentRevision       string                     `json:"document_revision"`
	SourcePath             string                     `json:"source_path"`
	Title                  string                     `json:"title"`
	MediaType              string                     `json:"media_type,omitempty"`
	Tags                   []string                   `json:"tags,omitempty"`
	ChunksWithText         int                        `json:"chunks_with_text"`
	PagesWithText          int                        `json:"pages_with_text"`
	ProcessedChunks        int                        `json:"processed_chunks"`
	UnprocessedChunks      int                        `json:"unprocessed_chunks"`
	ProcessingPercent      float64                    `json:"processing_percent"`
	CoveredChunks          int                        `json:"covered_chunks"`
	UncoveredChunks        int                        `json:"uncovered_chunks"`
	CoveragePercent        float64                    `json:"coverage_percent"`
	FullyCoveredPages      int                        `json:"fully_covered_pages"`
	PartiallyCoveredPages  int                        `json:"partially_covered_pages"`
	UncoveredPages         []int                      `json:"uncovered_pages,omitempty"`
	UnlocatedChunks        int                        `json:"unlocated_chunks"`
	LowConfidenceOCRChunks int                        `json:"low_confidence_ocr_chunks"`
	LowConfidenceOCRPages  []int                      `json:"low_confidence_ocr_pages,omitempty"`
	WarningChunks          int                        `json:"warning_chunks"`
	ExtractedNodes         int                        `json:"extracted_nodes"`
	ExtractedRelations     int                        `json:"extracted_relations"`
	DraftObjects           int                        `json:"draft_objects"`
	ActiveObjects          int                        `json:"active_objects"`
	RejectedObjects        int                        `json:"rejected_objects"`
	ResolvedObjects        int                        `json:"resolved_objects"`
	StaleEvidenceObjects   int                        `json:"stale_evidence_objects"`
	MissingEvidenceObjects int                        `json:"missing_evidence_objects"`
	Warnings               []KnowledgeCoverageWarning `json:"warnings,omitempty"`
}

type KnowledgeCoverageWarning struct {
	Page            int      `json:"page,omitempty"`
	BlockIndex      int      `json:"block_index"`
	BlockChunkIndex int      `json:"block_chunk_index"`
	Messages        []string `json:"messages"`
}

type KnowledgeCoverageReport struct {
	Scope          KnowledgeCoverageOptions    `json:"scope"`
	SnapshotDigest string                      `json:"snapshot_digest"`
	Summary        KnowledgeCoverageSummary    `json:"summary"`
	Documents      []KnowledgeDocumentCoverage `json:"documents"`
	Limitations    []string                    `json:"limitations"`
}

type knowledgeCoverageDocumentBuilder struct {
	report            KnowledgeDocumentCoverage
	chunkIDs          map[string]bool
	coveredChunkIDs   map[string]bool
	processedChunkIDs map[string]bool
	pageChunks        map[int]map[string]bool
	coveredPages      map[int]map[string]bool
	lowPages          map[int]bool
	objectStatuses    map[string]KnowledgeStatus
	nodeObjects       map[string]bool
	edgeObjects       map[string]bool
	staleObjects      map[string]bool
	missingObjects    map[string]bool
	tags              map[string]string
}

type knowledgeCoverageChunk struct {
	entry  Entry
	docKey string
}

// BuildKnowledgeCoverageReport measures how much of the selected current
// source material is referenced by current knowledge-map objects. It never
// calls a model and never treats retrieval rank as coverage.
func (s *Store) BuildKnowledgeCoverageReport(options KnowledgeCoverageOptions) (KnowledgeCoverageReport, error) {
	options.Document = strings.TrimSpace(options.Document)
	options.Tag = strings.TrimSpace(options.Tag)
	if options.LowConfidence <= 0 {
		options.LowConfidence = DefaultAnswerLowConfidence
	}
	if options.PageFrom < 0 || options.PageTo < 0 || (options.PageFrom == 0) != (options.PageTo == 0) ||
		(options.PageFrom > 0 && options.PageFrom > options.PageTo) {
		return KnowledgeCoverageReport{}, errors.New("coverage page range is invalid")
	}

	s.mu.RLock()
	entries := make([]Entry, len(s.entries))
	for i := range s.entries {
		entries[i] = cloneEntry(s.entries[i])
	}
	s.mu.RUnlock()

	builders := make(map[string]*knowledgeCoverageDocumentBuilder)
	selected := make(map[string]knowledgeCoverageChunk)
	documentMatched := options.Document == ""
	for _, entry := range entries {
		if !coverageEntryMatchesDocument(entry, options.Document) {
			continue
		}
		documentMatched = true
		if !coverageEntryHasTag(entry, options.Tag) || !coverageEntryInPages(entry, options.PageFrom, options.PageTo) {
			continue
		}
		if strings.TrimSpace(entry.Text) == "" || entry.DocumentID == "" || entry.DocumentRevision == "" ||
			entry.ChunkHash == "" || entry.SourcePath == "" || !isSHA256ContentHash(entry.DocumentRevision) ||
			entry.ChunkHash != ChunkContentHash(entry.Text) {
			continue
		}
		citationID, _ := CitationForEntry(entry)
		if citationID == "" {
			continue
		}
		docKey := entry.DocumentID + "\x00" + entry.DocumentRevision
		builder := builders[docKey]
		if builder == nil {
			title := strings.TrimSpace(entry.Title)
			if title == "" {
				title = filepath.Base(entry.SourcePath)
			}
			builder = &knowledgeCoverageDocumentBuilder{
				report: KnowledgeDocumentCoverage{
					DocumentID: entry.DocumentID, DocumentRevision: entry.DocumentRevision,
					SourcePath: entry.SourcePath, Title: title, MediaType: entry.MediaType,
				},
				chunkIDs: make(map[string]bool), coveredChunkIDs: make(map[string]bool), processedChunkIDs: make(map[string]bool),
				pageChunks: make(map[int]map[string]bool), coveredPages: make(map[int]map[string]bool),
				lowPages: make(map[int]bool), objectStatuses: make(map[string]KnowledgeStatus),
				nodeObjects: make(map[string]bool), edgeObjects: make(map[string]bool),
				staleObjects: make(map[string]bool), missingObjects: make(map[string]bool),
				tags: make(map[string]string),
			}
			builders[docKey] = builder
		}
		if builder.chunkIDs[citationID] {
			continue
		}
		builder.chunkIDs[citationID] = true
		selected[citationID] = knowledgeCoverageChunk{entry: entry, docKey: docKey}
		builder.report.ChunksWithText++
		for _, tag := range entry.Tags {
			trimmed := strings.TrimSpace(tag)
			if trimmed != "" {
				builder.tags[strings.ToLower(trimmed)] = trimmed
			}
		}
		if entry.Page > 0 {
			if builder.pageChunks[entry.Page] == nil {
				builder.pageChunks[entry.Page] = make(map[string]bool)
			}
			builder.pageChunks[entry.Page][citationID] = true
		} else {
			builder.report.UnlocatedChunks++
		}
		if entry.ExtractionMethod == "ocr" && entry.OCRConfidence >= 0 && entry.OCRConfidence < options.LowConfidence {
			builder.report.LowConfidenceOCRChunks++
			if entry.Page > 0 {
				builder.lowPages[entry.Page] = true
			}
		}
		if len(entry.Warnings) > 0 {
			builder.report.WarningChunks++
			builder.report.Warnings = append(builder.report.Warnings, KnowledgeCoverageWarning{
				Page: entry.Page, BlockIndex: entry.BlockIndex, BlockChunkIndex: entry.BlockChunkIndex,
				Messages: append([]string(nil), entry.Warnings...),
			})
		}
	}
	if !documentMatched {
		return KnowledgeCoverageReport{}, fmt.Errorf("coverage document %q was not found in current entries", options.Document)
	}
	selectedEntries := make([]Entry, 0, len(selected))
	for _, chunk := range selected {
		selectedEntries = append(selectedEntries, chunk.entry)
	}
	processed, err := s.currentKnowledgeExtractionProcessedCitations(selectedEntries)
	if err != nil {
		return KnowledgeCoverageReport{}, err
	}
	for citationID := range processed {
		chunk := selected[citationID]
		builders[chunk.docKey].processedChunkIDs[citationID] = true
	}

	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeCoverageReport{}, err
	}
	visit := func(objectKey string, objectType KnowledgeObjectType, status KnowledgeStatus, anchors []EvidenceAnchor) {
		matchedDocs := make(map[string]bool)
		staleDocs := make(map[string]bool)
		missingDocs := make(map[string]bool)
		for _, anchor := range anchors {
			if chunk, ok := selected[anchor.CitationID]; ok && coverageAnchorMatchesEntry(anchor, chunk.entry) {
				builder := builders[chunk.docKey]
				builder.coveredChunkIDs[anchor.CitationID] = true
				builder.processedChunkIDs[anchor.CitationID] = true
				if chunk.entry.Page > 0 {
					if builder.coveredPages[chunk.entry.Page] == nil {
						builder.coveredPages[chunk.entry.Page] = make(map[string]bool)
					}
					builder.coveredPages[chunk.entry.Page][anchor.CitationID] = true
				}
				matchedDocs[chunk.docKey] = true
				continue
			}
			for docKey, builder := range builders {
				if !coverageAnchorBelongsToDocument(anchor, builder.report, options.PageFrom, options.PageTo) {
					continue
				}
				switch resolveEvidenceAnchorFromEntries(anchor, entries).State {
				case EvidenceStale:
					staleDocs[docKey] = true
				case EvidenceMissing:
					missingDocs[docKey] = true
				}
			}
		}
		for docKey := range matchedDocs {
			builder := builders[docKey]
			builder.objectStatuses[objectKey] = status
			if objectType == KnowledgeObjectNode {
				builder.nodeObjects[objectKey] = true
			} else {
				builder.edgeObjects[objectKey] = true
			}
		}
		for docKey := range staleDocs {
			builders[docKey].staleObjects[objectKey] = true
		}
		for docKey := range missingDocs {
			builders[docKey].missingObjects[objectKey] = true
		}
	}
	for _, node := range graph.Nodes {
		visit("node:"+node.ID, KnowledgeObjectNode, node.Status, node.Evidence)
	}
	for _, edge := range graph.Edges {
		visit("edge:"+edge.ID, KnowledgeObjectEdge, edge.Status, edge.Evidence)
	}

	report := KnowledgeCoverageReport{
		Scope:     options,
		Documents: make([]KnowledgeDocumentCoverage, 0, len(builders)),
		Limitations: []string{
			"Coverage is measured only against current versioned chunks stored in the active database.",
			"Blank, unextracted, or failed physical pages are not part of the denominator unless import preserved a chunk for them.",
			"A covered chunk means at least one current map object cites it; it does not prove that every fact in the chunk was extracted.",
		},
	}
	report.SnapshotDigest = knowledgeCoverageSnapshotDigest(options, selected)
	docKeys := make([]string, 0, len(builders))
	for docKey := range builders {
		docKeys = append(docKeys, docKey)
	}
	sort.Slice(docKeys, func(i, j int) bool {
		left, right := builders[docKeys[i]].report, builders[docKeys[j]].report
		if !strings.EqualFold(left.SourcePath, right.SourcePath) {
			return strings.ToLower(left.SourcePath) < strings.ToLower(right.SourcePath)
		}
		return left.DocumentRevision < right.DocumentRevision
	})
	globalObjects := make(map[string]KnowledgeStatus)
	globalNodes, globalEdges := make(map[string]bool), make(map[string]bool)
	globalStale, globalMissing := make(map[string]bool), make(map[string]bool)
	globalLowPages := make(map[string]bool)
	for _, docKey := range docKeys {
		builder := builders[docKey]
		finalizeKnowledgeDocumentCoverage(builder)
		report.Documents = append(report.Documents, builder.report)
		report.Summary.Documents++
		report.Summary.ChunksWithText += builder.report.ChunksWithText
		report.Summary.PagesWithText += builder.report.PagesWithText
		report.Summary.ProcessedChunks += builder.report.ProcessedChunks
		report.Summary.UnprocessedChunks += builder.report.UnprocessedChunks
		report.Summary.CoveredChunks += builder.report.CoveredChunks
		report.Summary.UncoveredChunks += builder.report.UncoveredChunks
		report.Summary.FullyCoveredPages += builder.report.FullyCoveredPages
		report.Summary.PartiallyCoveredPages += builder.report.PartiallyCoveredPages
		report.Summary.UncoveredPages += len(builder.report.UncoveredPages)
		report.Summary.UnlocatedChunks += builder.report.UnlocatedChunks
		report.Summary.LowConfidenceOCRChunks += builder.report.LowConfidenceOCRChunks
		report.Summary.WarningChunks += builder.report.WarningChunks
		for _, page := range builder.report.LowConfidenceOCRPages {
			globalLowPages[docKey+":"+strconv.Itoa(page)] = true
		}
		for key, status := range builder.objectStatuses {
			globalObjects[key] = status
		}
		for key := range builder.nodeObjects {
			globalNodes[key] = true
		}
		for key := range builder.edgeObjects {
			globalEdges[key] = true
		}
		for key := range builder.staleObjects {
			globalStale[key] = true
		}
		for key := range builder.missingObjects {
			globalMissing[key] = true
		}
	}
	report.Summary.LowConfidenceOCRPages = len(globalLowPages)
	report.Summary.ExtractedNodes = len(globalNodes)
	report.Summary.ExtractedRelations = len(globalEdges)
	report.Summary.StaleEvidenceObjects = len(globalStale)
	report.Summary.MissingEvidenceObjects = len(globalMissing)
	for _, status := range globalObjects {
		incrementKnowledgeCoverageStatus(&report.Summary.DraftObjects, &report.Summary.ActiveObjects,
			&report.Summary.RejectedObjects, &report.Summary.ResolvedObjects, status)
	}
	report.Summary.CoveragePercent = coveragePercent(report.Summary.CoveredChunks, report.Summary.ChunksWithText)
	report.Summary.ProcessingPercent = coveragePercent(report.Summary.ProcessedChunks, report.Summary.ChunksWithText)
	return report, nil
}

func finalizeKnowledgeDocumentCoverage(builder *knowledgeCoverageDocumentBuilder) {
	report := &builder.report
	report.CoveredChunks = len(builder.coveredChunkIDs)
	report.UncoveredChunks = report.ChunksWithText - report.CoveredChunks
	report.CoveragePercent = coveragePercent(report.CoveredChunks, report.ChunksWithText)
	report.ProcessedChunks = len(builder.processedChunkIDs)
	report.UnprocessedChunks = report.ChunksWithText - report.ProcessedChunks
	report.ProcessingPercent = coveragePercent(report.ProcessedChunks, report.ChunksWithText)
	report.PagesWithText = len(builder.pageChunks)
	for page, chunks := range builder.pageChunks {
		covered := len(builder.coveredPages[page])
		switch {
		case covered == 0:
			report.UncoveredPages = append(report.UncoveredPages, page)
		case covered == len(chunks):
			report.FullyCoveredPages++
		default:
			report.PartiallyCoveredPages++
		}
	}
	for page := range builder.lowPages {
		report.LowConfidenceOCRPages = append(report.LowConfidenceOCRPages, page)
	}
	sort.Ints(report.UncoveredPages)
	sort.Ints(report.LowConfidenceOCRPages)
	report.ExtractedNodes = len(builder.nodeObjects)
	report.ExtractedRelations = len(builder.edgeObjects)
	report.StaleEvidenceObjects = len(builder.staleObjects)
	report.MissingEvidenceObjects = len(builder.missingObjects)
	for _, status := range builder.objectStatuses {
		incrementKnowledgeCoverageStatus(&report.DraftObjects, &report.ActiveObjects,
			&report.RejectedObjects, &report.ResolvedObjects, status)
	}
	keys := make([]string, 0, len(builder.tags))
	for key := range builder.tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report.Tags = append(report.Tags, builder.tags[key])
	}
}

func incrementKnowledgeCoverageStatus(draft, active, rejected, resolved *int, status KnowledgeStatus) {
	switch status {
	case KnowledgeStatusDraft:
		(*draft)++
	case KnowledgeStatusActive:
		(*active)++
	case KnowledgeStatusRejected:
		(*rejected)++
	case KnowledgeStatusResolved:
		(*resolved)++
	}
}

func coveragePercent(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(covered) * 100 / float64(total)
}

func coverageEntryMatchesDocument(entry Entry, selector string) bool {
	if selector == "" {
		return true
	}
	if entry.DocumentID == selector {
		return true
	}
	return coveragePathsEqual(entry.SourcePath, selector) || coveragePathsEqual(entry.SourceFile, selector)
}

func coveragePathsEqual(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil {
		return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func coverageEntryHasTag(entry Entry, tag string) bool {
	if tag == "" {
		return true
	}
	for _, entryTag := range entry.Tags {
		if strings.EqualFold(strings.TrimSpace(entryTag), tag) {
			return true
		}
	}
	return false
}

func coverageEntryInPages(entry Entry, from, to int) bool {
	if from == 0 {
		return true
	}
	return entry.Page >= from && entry.Page <= to
}

func coverageAnchorMatchesEntry(anchor EvidenceAnchor, entry Entry) bool {
	return anchor.DocumentID == entry.DocumentID && anchor.DocumentRevision == entry.DocumentRevision &&
		anchor.ChunkHash == entry.ChunkHash && anchor.SourcePath == entry.SourcePath &&
		anchor.Page == entry.Page && anchor.BlockIndex == entry.BlockIndex &&
		anchor.BlockChunkIndex == entry.BlockChunkIndex && strings.Contains(entry.Text, anchor.Excerpt)
}

func coverageAnchorBelongsToDocument(anchor EvidenceAnchor, document KnowledgeDocumentCoverage, from, to int) bool {
	if anchor.DocumentID != document.DocumentID && !coveragePathsEqual(anchor.SourcePath, document.SourcePath) {
		return false
	}
	if from > 0 && (anchor.Page < from || anchor.Page > to) {
		return false
	}
	return true
}

func knowledgeCoverageSnapshotDigest(options KnowledgeCoverageOptions, selected map[string]knowledgeCoverageChunk) string {
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-coverage-snapshot-v1")
	writeKnowledgeIDField(h, options.Document)
	writeKnowledgeIDField(h, strings.ToLower(options.Tag))
	writeKnowledgeIDField(h, strconv.Itoa(options.PageFrom))
	writeKnowledgeIDField(h, strconv.Itoa(options.PageTo))
	writeKnowledgeIDField(h, strconv.FormatFloat(options.LowConfidence, 'g', -1, 64))
	ids := make([]string, 0, len(selected))
	for citationID := range selected {
		ids = append(ids, citationID)
	}
	sort.Strings(ids)
	for _, citationID := range ids {
		chunk := selected[citationID]
		writeKnowledgeIDField(h, citationID)
		writeKnowledgeIDField(h, chunk.entry.DocumentRevision)
		writeKnowledgeIDField(h, chunk.entry.ChunkHash)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
