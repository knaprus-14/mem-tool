package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	lexicalFTS5     = "fts5"
	lexicalFallback = "deterministic-fallback"
	// VectorFusionWeight and LexicalFusionWeight are transparent equal weights,
	// not probabilities.
	VectorFusionWeight  = 0.5
	LexicalFusionWeight = 0.5
)

// SearchOptions controls the candidate stage of search. It deliberately has
// no limit: filters and vector/lexical fusion run over the complete candidate
// set, and callers apply the final display limit only after scoring.
type SearchOptions struct {
	Query       string
	QueryVector []float32
	Backend     string
	Tags        []string
	TagFilter   string
	From        string
	To          string
	VectorOnly  bool
}

// initLexicalIndex probes the current SQLite build once. FTS5 is preferred,
// but a missing extension must not make an existing .mem database unusable.
func initLexicalIndex(db *sql.DB) string {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts
		USING fts5(entry_id UNINDEXED, title, text, tags)`); err != nil {
		return lexicalFallback
	}
	return lexicalFTS5
}

// LexicalMode reports the active lexical implementation for diagnostics and
// CLI output. It is a capability indicator, not a quality or probability.
func (s *Store) LexicalMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lexicalMode
}

// SearchWithOptions returns all filtered vector and lexical candidates. The
// final limit belongs to the caller so a lexical match cannot disappear
// behind an earlier vector-only top-K truncation.
func (s *Store) SearchWithOptions(options SearchOptions) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := strings.TrimSpace(options.Query)
	lexicalScores := make(map[int64]float64)
	if !options.VectorOnly && query != "" {
		var err error
		lexicalScores, err = s.lexicalScoresLocked(query)
		if err != nil {
			return nil, err
		}
	}

	type candidate struct {
		entry        Entry
		vectorScore  float64
		lexicalScore float64
		lexicalHit   bool
	}
	candidates := make([]candidate, 0, len(s.entries))
	for i := range s.entries {
		entry := s.entries[i]
		if entry.Backend != options.Backend || !matchesSearchFilters(entry, options) {
			continue
		}

		vectorScore := 0.0
		vectorAvailable := len(options.QueryVector) > 0 && len(entry.Embedding) == len(options.QueryVector)
		if vectorAvailable {
			vectorScore = normalizeCosine(CosineSimilarity(options.QueryVector, s.vectors[i]))
		}
		lexicalScore, lexicalHit := lexicalScores[entry.ID]
		if !vectorAvailable && !lexicalHit {
			continue
		}
		candidates = append(candidates, candidate{
			entry: entry, vectorScore: vectorScore, lexicalScore: lexicalScore,
			lexicalHit: lexicalHit,
		})
	}

	if len(candidates) == 0 {
		return nil, nil
	}
	useFusion := !options.VectorOnly && query != "" && len(lexicalScores) > 0
	for i := range candidates {
		fusion := candidates[i].vectorScore
		if useFusion {
			fusion = VectorFusionWeight*candidates[i].vectorScore + LexicalFusionWeight*candidates[i].lexicalScore
		}
		candidates[i].entry.VectorScore = candidates[i].vectorScore
		candidates[i].entry.LexicalScore = candidates[i].lexicalScore
		candidates[i].entry.LexicalHit = candidates[i].lexicalHit
		candidates[i].entry.FusionScore = fusion
		candidates[i].entry.Score = fusion
		annotateCitation(&candidates[i].entry)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].entry.Score != candidates[j].entry.Score {
			return candidates[i].entry.Score > candidates[j].entry.Score
		}
		if candidates[i].lexicalScore != candidates[j].lexicalScore {
			return candidates[i].lexicalScore > candidates[j].lexicalScore
		}
		if candidates[i].vectorScore != candidates[j].vectorScore {
			return candidates[i].vectorScore > candidates[j].vectorScore
		}
		return candidates[i].entry.ID < candidates[j].entry.ID
	})

	results := make([]Entry, len(candidates))
	for i := range candidates {
		results[i] = cloneEntry(candidates[i].entry)
	}
	return results, nil
}

func matchesSearchFilters(entry Entry, options SearchOptions) bool {
	if len(options.Tags) > 0 && !hasAnySearchTag(entry.Tags, options.Tags) {
		return false
	}
	if options.TagFilter != "" {
		found := false
		for _, tag := range entry.Tags {
			if tag == options.TagFilter {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return matchesSearchDate(entry.Created, options.From, options.To)
}

func hasAnySearchTag(entryTags, filterTags []string) bool {
	for _, filter := range filterTags {
		filter = strings.ToLower(strings.TrimSpace(filter))
		for _, tag := range entryTags {
			if strings.ToLower(strings.TrimSpace(tag)) == filter {
				return true
			}
		}
	}
	return false
}

func matchesSearchDate(created, from, to string) bool {
	if from == "" && to == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return true
	}
	if from != "" {
		fromTime, err := time.Parse("2006-01-02", from)
		if err == nil && t.Before(fromTime) {
			return false
		}
	}
	if to != "" {
		toTime, err := time.Parse("2006-01-02", to)
		if err == nil && t.After(toTime.Add(24*time.Hour-time.Second)) {
			return false
		}
	}
	return true
}

func normalizeCosine(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func (s *Store) lexicalScoresLocked(query string) (map[int64]float64, error) {
	if s.lexicalMode == lexicalFTS5 {
		if err := s.rebuildFTSLocked(); err == nil {
			scores, queryErr := s.queryFTSLocked(query)
			if queryErr == nil {
				return scores, nil
			}
		}
		// A runtime FTS failure degrades this Store instance to a deterministic
		// scan instead of making search unavailable.
		s.lexicalMode = lexicalFallback
	}
	return fallbackLexicalScores(s.entries, query), nil
}

func (s *Store) rebuildFTSLocked() error {
	if _, err := s.db.Exec(`DELETE FROM entries_fts`); err != nil {
		return err
	}
	for _, entry := range s.entries {
		if _, err := s.db.Exec(`INSERT INTO entries_fts(rowid, entry_id, title, text, tags)
			VALUES (?, ?, ?, ?, ?)`, entry.ID, entry.ID, entry.Title, entry.Text, strings.Join(entry.Tags, " ")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queryFTSLocked(query string) (map[int64]float64, error) {
	match := makeFTSQuery(query)
	if match == "" {
		return map[int64]float64{}, nil
	}
	rows, err := s.db.Query(`SELECT entry_id, bm25(entries_fts)
		FROM entries_fts WHERE entries_fts MATCH ?
		ORDER BY bm25(entries_fts), entry_id`, match)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ranks := make([]lexicalRank, 0)
	for rows.Next() {
		var entryID int64
		var rank float64
		if err := rows.Scan(&entryID, &rank); err != nil {
			return nil, err
		}
		ranks = append(ranks, lexicalRank{entryID: entryID, raw: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return normalizeLexicalRanks(ranks), nil
}

type lexicalRank struct {
	entryID int64
	raw     float64
}

func normalizeLexicalRanks(ranks []lexicalRank) map[int64]float64 {
	result := make(map[int64]float64, len(ranks))
	if len(ranks) == 0 {
		return result
	}
	best, worst := ranks[0].raw, ranks[0].raw
	for _, rank := range ranks[1:] {
		if rank.raw < best {
			best = rank.raw
		}
		if rank.raw > worst {
			worst = rank.raw
		}
	}
	for _, rank := range ranks {
		score := 1.0
		if math.Abs(worst-best) > 1e-12 {
			score = (worst - rank.raw) / (worst - best)
		}
		result[rank.entryID] = clampScore(score)
	}
	return result
}

func makeFTSQuery(query string) string {
	parts := make([]string, 0)
	seen := make(map[string]bool)
	for _, token := range lexicalTokens(query) {
		if seen[token] {
			continue
		}
		seen[token] = true
		parts = append(parts, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " AND ")
}

func fallbackLexicalScores(entries []Entry, query string) map[int64]float64 {
	queryTokens := lexicalTokens(query)
	raw := make([]lexicalRank, 0)
	for _, entry := range entries {
		title := tokenCounts(entry.Title)
		text := tokenCounts(entry.Text)
		tags := tokenCounts(strings.Join(entry.Tags, " "))
		matched := 0
		score := 0.0
		for _, token := range queryTokens {
			hits := title[token] + text[token] + tags[token]
			if hits == 0 {
				continue
			}
			matched++
			score += float64(title[token]*3 + tags[token]*2 + text[token])
		}
		if matched == len(queryTokens) && matched > 0 {
			raw = append(raw, lexicalRank{entryID: entry.ID, raw: -score})
		}
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].raw != raw[j].raw {
			return raw[i].raw < raw[j].raw
		}
		return raw[i].entryID < raw[j].entryID
	})
	return normalizeLexicalRanks(raw)
}

func lexicalTokens(value string) []string {
	counts := tokenCounts(value)
	tokens := make([]string, 0, len(counts))
	for token := range counts {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func tokenCounts(value string) map[string]int {
	counts := make(map[string]int)
	var token []rune
	flush := func() {
		if len(token) > 0 {
			counts[string(token)]++
			token = token[:0]
		}
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token = append(token, r)
		} else {
			flush()
		}
	}
	flush()
	return counts
}

func clampScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// CitationForEntry returns a stable future-RAG identifier and a human-readable
// label. It never invents a physical page for legacy rows.
func CitationForEntry(entry Entry) (string, string) {
	source := entry.SourcePath
	if source == "" {
		source = entry.SourceFile
	}
	if source == "" {
		return fmt.Sprintf("entry-%d", entry.ID), fmt.Sprintf("entry #%d (no provenance)", entry.ID)
	}
	key := entry.DocumentID
	if key == "" {
		key = source
	}
	digest := sha256.Sum256([]byte(key))
	id := fmt.Sprintf("cite-%s-%d-%d", hex.EncodeToString(digest[:]), entry.BlockIndex+1, entry.ChunkIndex+1)
	parts := []string{source}
	if entry.Page > 0 {
		parts = append(parts, fmt.Sprintf("page %d", entry.Page))
	}
	if entry.DocumentID != "" || entry.BlockMarker != "" || entry.ExtractionMethod != "" {
		parts = append(parts, fmt.Sprintf("block %d", entry.BlockIndex+1))
	}
	if entry.TotalChunks > 0 {
		parts = append(parts, fmt.Sprintf("chunk %d/%d", entry.ChunkIndex+1, entry.TotalChunks))
	}
	return id, strings.Join(parts, " | ")
}

func annotateCitation(entry *Entry) {
	entry.CitationID, entry.CitationLabel = CitationForEntry(*entry)
}
