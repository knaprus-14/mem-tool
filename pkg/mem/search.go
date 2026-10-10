package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

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

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
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
	Query          string
	QueryVector    []float32
	Backend        string
	EmbeddingSpace string
	Tags           []string
	TagFilter      string
	From           string
	To             string
	VectorOnly     bool
	Document       string
	ImportantOnly  bool
	PageFrom       int
	PageTo         int
	AllBackends    bool
}

// initLexicalIndex probes the current SQLite build once. FTS5 is preferred,
// but a missing extension must not make an existing .mem database unusable.
func initLexicalIndex(db *sql.DB) string {
	tx, err := db.Begin()
	if err != nil {
		return lexicalFallback
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='entries_fts'`).Scan(&exists); err != nil {
		return lexicalFallback
	}
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts
		USING fts5(entry_id UNINDEXED, title, text, tags)`); err != nil {
		return lexicalFallback
	}
	if exists == 0 {
		if _, err := tx.Exec(`UPDATE entry_fts_state SET generation=-1 WHERE singleton=1`); err != nil {
			return lexicalFallback
		}
	}
	if err := tx.Commit(); err != nil {
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
func (s *Store) SearchWithOptions(options SearchOptions) (applogResult0 []Entry, applogResult1 error) {
	defer applog.Track("pkg.mem.Store.SearchWithOptions")(&applogResult1)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshEntryCacheIfStaleUnlocked("hybrid search"); err != nil {
		return nil, err
	}

	query := strings.TrimSpace(options.Query)
	lexicalScores := make(map[int64]float64)
	if !options.VectorOnly && query != "" {
		var err error
		lexicalScores, err = s.lexicalScoresLocked(query)
		if err != nil {
			return nil, err
		}
		lexicalScores = filterLexicalScores(s.entries, lexicalScores, options)
	}

	return scoreSearchSnapshot(s.entries, s.vectors, lexicalScores, options), nil
}

// scoreSearchSnapshot is shared by CLI stores and bounded read-only web queries.
func scoreSearchSnapshot(entries []Entry, vectors [][]float32, lexicalScores map[int64]float64, options SearchOptions) []Entry {
	query := strings.TrimSpace(options.Query)
	type candidate struct {
		entry        Entry
		vectorScore  float64
		lexicalScore float64
		lexicalHit   bool
	}
	candidates := make([]candidate, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		if (!options.AllBackends && entry.Backend != options.Backend) || !matchesSearchFilters(entry, options) {
			continue
		}

		vectorScore := 0.0
		vectorAvailable := len(options.QueryVector) > 0 && len(entry.Embedding) == len(options.QueryVector) &&
			embeddingSpaceCompatible(entry.EmbeddingSpace, options.EmbeddingSpace)
		if vectorAvailable {
			vectorScore = normalizeCosine(CosineSimilarity(options.QueryVector, vectors[i]))
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
		return nil
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
	return results
}

// embeddingSpaceCompatible preserves the legacy API when the caller has no
// identity, while identity-aware production paths require an exact match. An
// unknown legacy row is therefore never compared to a known query space.
func embeddingSpaceCompatible(entrySpace, querySpace string) bool {
	if querySpace == "" {
		return true
	}
	return entrySpace != "" && entrySpace == querySpace
}

// filterLexicalScores нормализует лексические оценки в выбранной области.
// Исключённые по backend, тегам или дате записи не включают fusion и не
// задают его диапазон для оставшихся результатов.
func filterLexicalScores(entries []Entry, scores map[int64]float64, options SearchOptions) map[int64]float64 {
	ranks := make([]lexicalRank, 0, len(scores))
	for _, entry := range entries {
		if score, hit := scores[entry.ID]; hit && (options.AllBackends || entry.Backend == options.Backend) && matchesSearchFilters(entry, options) {
			ranks = append(ranks, lexicalRank{entryID: entry.ID, raw: -score})
		}
	}
	return normalizeLexicalRanks(ranks)
}

func matchesSearchFilters(entry Entry, options SearchOptions) bool {
	if options.Document != "" && LibraryDocumentID(firstNonEmpty(entry.SourcePath, entry.SourceFile), entry.DocumentID) != options.Document {
		return false
	}
	if options.ImportantOnly && !entry.Important {
		return false
	}
	if options.PageFrom > 0 && entry.Page < options.PageFrom {
		return false
	}
	if options.PageTo > 0 && (entry.Page == 0 || entry.Page > options.PageTo) {
		return false
	}
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
		if err == nil && !t.Before(toTime.AddDate(0, 0, 1)) {
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
		const maxFTSSnapshotAttempts = 4
		for attempt := 0; attempt < maxFTSSnapshotAttempts; attempt++ {
			if s.lexicalDirty {
				if err := s.rebuildFTSLocked(); err != nil {
					// A busy/changing database is a per-call coordination failure,
					// not proof that this SQLite build lost FTS5 support.
					return fallbackLexicalScores(s.entries, query), nil
				}
			}

			// Pin the generation check and FTS query to one SQLite read
			// snapshot. An external entry commit after this transaction starts
			// can be linearized after the current search; a commit before it is
			// detected and forces a cache refresh/rebuild retry.
			tx, err := sqliteutil.BeginRead(s.db)
			if err != nil {
				return fallbackLexicalScores(s.entries, query), nil
			}
			databaseGeneration, generationErr := loadEntryCacheGeneration(tx)
			if generationErr != nil {
				_ = tx.Rollback()
				s.invalidateEntryCacheUnlocked()
				return nil, generationErr
			}
			ftsGeneration, ftsGenerationErr := loadEntryFTSGeneration(tx)
			if ftsGenerationErr != nil {
				_ = tx.Rollback()
				return fallbackLexicalScores(s.entries, query), nil
			}
			if databaseGeneration != s.entryGeneration {
				if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
					return nil, fmt.Errorf("refresh lexical cache: rollback stale snapshot: %w", rollbackErr)
				}
				if err := s.loadAll(); err != nil {
					s.invalidateEntryCacheUnlocked()
					return nil, fmt.Errorf("refresh lexical cache after external entry change: %w", err)
				}
				s.lexicalDirty = true
				continue
			}
			if ftsGeneration != databaseGeneration {
				if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
					return nil, fmt.Errorf("refresh lexical index: rollback mismatched generation: %w", rollbackErr)
				}
				s.lexicalDirty = true
				continue
			}
			scores, queryErr := queryFTS(tx, query)
			if queryErr != nil {
				_ = tx.Rollback()
				return fallbackLexicalScores(s.entries, query), nil
			}
			if err := tx.Commit(); err != nil {
				return fallbackLexicalScores(s.entries, query), nil
			}
			return scores, nil
		}
		// Keep this call available under sustained external writer churn. The
		// process-local entries and the fallback scores describe one coherent
		// snapshot, while the next call is still free to retry FTS5.
		return fallbackLexicalScores(s.entries, query), nil
	}
	return fallbackLexicalScores(s.entries, query), nil
}

func (s *Store) rebuildFTSLocked() error {
	tx, err := s.beginEntryWriteTx("FTS sync")
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("%v; FTS rollback failed: %w", cause, rollbackErr)
		}
		return cause
	}
	if err := syncFTSTx(tx, s.entries); err != nil {
		return rollback(err)
	}
	generation, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return rollback(err)
	}
	if generation != s.entryGeneration {
		return rollback(fmt.Errorf("FTS rebuild generation %d does not match entry cache generation %d", generation, s.entryGeneration))
	}
	if _, err := tx.Exec(`UPDATE entry_fts_state SET generation = ? WHERE singleton = 1`, generation); err != nil {
		return rollback(fmt.Errorf("write entry FTS generation: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return rollback(err)
	}
	s.lexicalDirty = false
	return nil
}

// syncFTSTx applies the persistent row journal under the same writer lock as
// the FTS generation marker. A new or untracked index still gets a full rebuild.
func syncFTSTx(tx *sql.Tx, entries []Entry) error {
	indexed, err := loadEntryFTSGeneration(tx)
	if err != nil {
		return err
	}
	current, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return err
	}
	var dirty int
	if err := tx.QueryRow(`SELECT count(*) FROM entry_fts_dirty`).Scan(&dirty); err != nil {
		return err
	}
	if indexed < 0 || (indexed != current && dirty == 0) {
		if err := rebuildFTSTx(tx, entries); err != nil {
			return err
		}
	} else if dirty > 0 {
		rows, err := tx.Query(`SELECT e.id, e.title, e.text, e.tags FROM entry_fts_dirty d JOIN entries e ON e.id=d.entry_id ORDER BY e.id`)
		if err != nil {
			return err
		}
		var changed []Entry
		for rows.Next() {
			var e Entry
			var tags string
			if err := rows.Scan(&e.ID, &e.Title, &e.Text, &tags); err != nil {
				rows.Close()
				return err
			}
			e.Tags, err = tagsFromJSON(tags)
			if err != nil {
				rows.Close()
				return err
			}
			changed = append(changed, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM entries_fts WHERE rowid IN (SELECT entry_id FROM entry_fts_dirty)`); err != nil {
			return err
		}
		if err := insertFTSEntriesTx(tx, changed); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`DELETE FROM entry_fts_dirty`)
	return err
}

func rebuildFTSTx(tx *sql.Tx, entries []Entry) error {
	if _, err := tx.Exec(`DELETE FROM entries_fts`); err != nil {
		return err
	}
	return insertFTSEntriesTx(tx, entries)
}

func insertFTSEntriesTx(tx *sql.Tx, entries []Entry) error {
	for _, entry := range entries {
		if _, err := tx.Exec(`INSERT INTO entries_fts(rowid, entry_id, title, text, tags)
			VALUES (?, ?, ?, ?, ?)`, entry.ID, entry.ID, entry.Title, entry.Text, strings.Join(entry.Tags, " ")); err != nil {
			return err
		}
	}
	return nil
}

func queryFTS(q entryQuerier, query string) (map[int64]float64, error) {
	match := makeFTSQuery(query)
	if match == "" {
		return map[int64]float64{}, nil
	}
	rows, err := q.Query(`SELECT entry_id, bm25(entries_fts)
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
	chunkWithinBlock := entry.ChunkIndex
	if entry.BlockTotalChunks > 0 {
		chunkWithinBlock = entry.BlockChunkIndex
	}
	id := fmt.Sprintf("cite-%s-p%d-b%d-c%d", hex.EncodeToString(digest[:]), entry.Page, entry.BlockIndex+1, chunkWithinBlock+1)
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
