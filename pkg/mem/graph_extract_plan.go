package mem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxKnowledgeExtractionJobBatches = 128

const knowledgeExtractionJobSystemPrompt = knowledgeExtractionSystemPrompt + `
This is a controlled coverage batch, not a retrieval sample. Inspect every supplied evidence item.
Extract every distinct, directly supported section, topic, definition, claim, formula, example,
procedure, comparison, dependency, cause, effect, risk, and constraint that is useful for later
navigation. Use typed relations whenever the evidence supports them. Do not omit an evidence item
merely because another item is more relevant to the focus. Concision means avoiding duplicates, not
skipping supported facts.`

// KnowledgeExtractionJobBatch is one complete, context-bounded model request.
// Entries are retained only in the in-memory deterministic plan; durable runs
// store their exact prompt/evidence digest instead of duplicating source text.
type KnowledgeExtractionJobBatch struct {
	BatchID string                    `json:"batch_id"`
	Prompt  KnowledgeExtractionPrompt `json:"-"`
	Entries []Entry                   `json:"-"`
}

// KnowledgeExtractionJobPlan selects only current chunks that are neither
// represented by a current graph object nor recorded as processed by an earlier
// completed extraction run for the exact same revision.
type KnowledgeExtractionJobPlan struct {
	Scope                     KnowledgeCoverageOptions      `json:"scope"`
	SnapshotDigest            string                        `json:"snapshot_digest"`
	Batches                   []KnowledgeExtractionJobBatch `json:"batches"`
	ScopedChunks              int                           `json:"scoped_chunks"`
	AlreadyCoveredChunks      int                           `json:"already_covered_chunks"`
	PreviouslyProcessedChunks int                           `json:"previously_processed_chunks"`
	EligibleChunks            int                           `json:"eligible_chunks"`
	SelectedChunks            int                           `json:"selected_chunks"`
	RemainingChunks           int                           `json:"remaining_chunks"`
	Documents                 int                           `json:"documents"`
	Pages                     int                           `json:"pages"`
}

// BuildKnowledgeExtractionJobPlan creates deterministic sequential batches for
// the selected document/page/tag scope. Retrieval scores are intentionally not
// involved: this is controlled coverage work, not a focus-search shortcut.
func (s *Store) BuildKnowledgeExtractionJobPlan(focus string, scope KnowledgeCoverageOptions, contextBudget, maxBatches int) (KnowledgeExtractionJobPlan, error) {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return KnowledgeExtractionJobPlan{}, errors.New("knowledge extraction job focus is empty")
	}
	if contextBudget <= 0 {
		contextBudget = DefaultAnswerContextChars
	}
	if contextBudget > MaxAnswerContextChars {
		return KnowledgeExtractionJobPlan{}, fmt.Errorf("knowledge extraction context budget must not exceed %d", MaxAnswerContextChars)
	}
	if maxBatches < 1 || maxBatches > MaxKnowledgeExtractionJobBatches {
		return KnowledgeExtractionJobPlan{}, fmt.Errorf("knowledge extraction batches must be between 1 and %d", MaxKnowledgeExtractionJobBatches)
	}

	report, err := s.BuildKnowledgeCoverageReport(scope)
	if err != nil {
		return KnowledgeExtractionJobPlan{}, err
	}
	scope = report.Scope
	entries := s.knowledgeCoverageEntries(scope)
	covered, err := s.currentKnowledgeCoveredCitations(entries)
	if err != nil {
		return KnowledgeExtractionJobPlan{}, err
	}
	processed, err := s.currentKnowledgeExtractionProcessedCitations(entries)
	if err != nil {
		return KnowledgeExtractionJobPlan{}, err
	}

	plan := KnowledgeExtractionJobPlan{
		Scope: scope, SnapshotDigest: report.SnapshotDigest, ScopedChunks: len(entries),
		AlreadyCoveredChunks: len(covered), PreviouslyProcessedChunks: len(processed),
		Documents: report.Summary.Documents, Pages: report.Summary.PagesWithText,
	}
	eligible := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		citationID, _ := CitationForEntry(entry)
		if covered[citationID] || processed[citationID] {
			continue
		}
		eligible = append(eligible, entry)
	}
	plan.EligibleChunks = len(eligible)
	if len(eligible) == 0 {
		return plan, nil
	}

	for len(eligible) > 0 && len(plan.Batches) < maxBatches {
		batchEntries := make([]Entry, 0)
		var batchPrompt KnowledgeExtractionPrompt
		for len(eligible) > 0 {
			trial := append(append([]Entry(nil), batchEntries...), eligible[0])
			prompt, buildErr := buildKnowledgeExtractionJobPrompt(focus, trial, contextBudget, scope.LowConfidence)
			if buildErr != nil {
				return KnowledgeExtractionJobPlan{}, buildErr
			}
			if knowledgeExtractionJobPromptFits(prompt, trial, contextBudget) {
				batchEntries = trial
				batchPrompt = prompt
				eligible = eligible[1:]
				continue
			}
			if len(batchEntries) == 0 {
				return KnowledgeExtractionJobPlan{}, fmt.Errorf("knowledge extraction context budget %d cannot fit complete chunk #%d; increase -context-chars or reimport with smaller chunks", contextBudget, eligible[0].ID)
			}
			break
		}
		batch := KnowledgeExtractionJobBatch{
			Prompt: batchPrompt, Entries: append([]Entry(nil), batchEntries...),
		}
		batch.BatchID = stableKnowledgeExtractionJobBatchID(focus, batch.Prompt.Evidence)
		plan.SelectedChunks += len(batchEntries)
		plan.Batches = append(plan.Batches, batch)
	}
	plan.RemainingChunks = plan.EligibleChunks - plan.SelectedChunks
	return plan, nil
}

func buildKnowledgeExtractionJobPrompt(focus string, entries []Entry, contextBudget int, lowConfidence float64) (KnowledgeExtractionPrompt, error) {
	prompt, err := BuildKnowledgeExtractionPrompt(focus, entries, contextBudget, lowConfidence)
	if err != nil {
		return KnowledgeExtractionPrompt{}, err
	}
	prompt.System = knowledgeExtractionJobSystemPrompt
	if utf8.RuneCountInString(prompt.System)+utf8.RuneCountInString(prompt.User) > contextBudget {
		return prompt, nil
	}
	return prompt, nil
}

func (s *Store) knowledgeCoverageEntries(scope KnowledgeCoverageOptions) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if !coverageEntryMatchesDocument(entry, scope.Document) || !coverageEntryHasTag(entry, scope.Tag) ||
			!coverageEntryInPages(entry, scope.PageFrom, scope.PageTo) || strings.TrimSpace(entry.Text) == "" ||
			entry.DocumentID == "" || entry.DocumentRevision == "" || entry.ChunkHash == "" || entry.SourcePath == "" ||
			!isSHA256ContentHash(entry.DocumentRevision) || entry.ChunkHash != ChunkContentHash(entry.Text) {
			continue
		}
		citationID, _ := CitationForEntry(entry)
		if citationID == "" {
			continue
		}
		entries = append(entries, cloneEntry(entry))
	}
	sort.Slice(entries, func(i, j int) bool {
		if !strings.EqualFold(entries[i].SourcePath, entries[j].SourcePath) {
			return strings.ToLower(entries[i].SourcePath) < strings.ToLower(entries[j].SourcePath)
		}
		if entries[i].Page != entries[j].Page {
			return entries[i].Page < entries[j].Page
		}
		if entries[i].BlockIndex != entries[j].BlockIndex {
			return entries[i].BlockIndex < entries[j].BlockIndex
		}
		if entries[i].BlockChunkIndex != entries[j].BlockChunkIndex {
			return entries[i].BlockChunkIndex < entries[j].BlockChunkIndex
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

func (s *Store) currentKnowledgeCoveredCitations(entries []Entry) (map[string]bool, error) {
	selected := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		citationID, _ := CitationForEntry(entry)
		selected[citationID] = entry
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool)
	visit := func(anchors []EvidenceAnchor) {
		for _, anchor := range anchors {
			entry, ok := selected[anchor.CitationID]
			if ok && coverageAnchorMatchesEntry(anchor, entry) {
				covered[anchor.CitationID] = true
			}
		}
	}
	for _, node := range graph.Nodes {
		visit(node.Evidence)
	}
	for _, edge := range graph.Edges {
		visit(edge.Evidence)
	}
	return covered, nil
}

func knowledgeExtractionPromptContainsFullEntries(prompt KnowledgeExtractionPrompt, entries []Entry) bool {
	if len(prompt.Evidence) != len(entries) {
		return false
	}
	for i := range entries {
		citationID, _ := CitationForEntry(entries[i])
		if prompt.Evidence[i].CitationID != citationID || prompt.Evidence[i].Text != entries[i].Text {
			return false
		}
	}
	return true
}

func knowledgeExtractionJobPromptFits(prompt KnowledgeExtractionPrompt, entries []Entry, contextBudget int) bool {
	return knowledgeExtractionPromptContainsFullEntries(prompt, entries) &&
		utf8.RuneCountInString(prompt.System)+utf8.RuneCountInString(prompt.User) <= contextBudget
}

func stableKnowledgeExtractionJobBatchID(focus string, evidence []GroundedEvidence) string {
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-extraction-job-batch-v1")
	writeKnowledgeIDField(h, normalizeKnowledgeIdentityText(focus))
	for _, item := range evidence {
		writeKnowledgeIDField(h, item.CitationID)
		writeKnowledgeIDField(h, item.DocumentRevision)
		writeKnowledgeIDField(h, item.ChunkHash)
		writeKnowledgeIDField(h, item.EvidenceHash)
	}
	return "keb-" + hex.EncodeToString(h.Sum(nil)[:16])
}
