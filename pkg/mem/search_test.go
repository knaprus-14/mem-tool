package mem

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteFTS5Availability(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE probe_fts USING fts5(title, text)`)
	if err != nil {
		t.Logf("FTS5 unavailable; deterministic lexical fallback is required: %v", err)
		return
	}
	t.Log("FTS5 available in the pure-Go SQLite build")
}

func TestSearchWithOptionsFiltersBeforeFinalCandidates(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	if _, err := store.Add("vector distractor one", "", []string{"other"}, "test", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("vector distractor two", "", []string{"other"}, "test", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("needle exact", "", []string{"keep"}, "test", []float32{0, 1}, false); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchWithOptions(SearchOptions{
		Query: "needle", QueryVector: []float32{1, 0}, Backend: "test", Tags: []string{"keep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Text != "needle exact" || results[0].LexicalScore == 0 {
		t.Fatalf("filtered lexical candidate was lost: %#v", results)
	}
}

func TestSearchWithOptionsLexicalExactTermCanBeatVectorOnlyCandidate(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	if _, err := store.Add("semantic unrelated", "", nil, "test", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("zephyr exact", "", nil, "test", []float32{0.6, 0.8}, false); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchWithOptions(SearchOptions{
		Query: "zephyr", QueryVector: []float32{1, 0}, Backend: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Text != "zephyr exact" || results[0].LexicalScore == 0 {
		t.Fatalf("exact lexical result did not participate in fusion: %#v", results)
	}
}

func TestSearchWithOptionsKeepsWeakFTSOnlyHitWithoutCompatibleVector(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	strong, err := store.Add("needle needle needle", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := store.Add("needle", "", nil, "test", []float32{1, 0, 0}, false)
	if err != nil {
		t.Fatal(err)
	}

	options := SearchOptions{Query: "needle", QueryVector: []float32{1, 0}, Backend: "test"}
	results, err := store.SearchWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	var strongResult, weakResult *Entry
	for i := range results {
		switch results[i].ID {
		case strong.ID:
			strongResult = &results[i]
		case weak.ID:
			weakResult = &results[i]
		}
	}
	if strongResult == nil || weakResult == nil || !weakResult.LexicalHit {
		t.Fatalf("weak FTS-only lexical hit was lost: %#v", results)
	}
	if strongResult.LexicalScore == weakResult.LexicalScore {
		t.Fatalf("expected distinct BM25-normalized scores: %#v", results)
	}
	if weakResult.LexicalScore != 0 {
		t.Fatalf("expected weakest normalized score to remain transparent at zero: %#v", weakResult)
	}

	again, err := store.SearchWithOptions(options)
	if err != nil || len(again) != len(results) {
		t.Fatalf("repeat FTS search changed result set: len=%d/%d err=%v", len(again), len(results), err)
	}
	for i := range results {
		if results[i].ID != again[i].ID || results[i].Score != again[i].Score || results[i].LexicalHit != again[i].LexicalHit {
			t.Fatalf("non-deterministic FTS ordering at %d: %#v vs %#v", i, results, again)
		}
	}
}

func TestSearchWithOptionsKeepsWeakFallbackHitWithoutCompatibleVector(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	store.lexicalMode = lexicalFallback
	strong, err := store.Add("fallbackterm fallbackterm", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := store.Add("fallbackterm", "", nil, "test", []float32{1, 0, 0}, false)
	if err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchWithOptions(SearchOptions{Query: "fallbackterm", QueryVector: []float32{1, 0}, Backend: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var strongResult, weakResult *Entry
	for i := range results {
		switch results[i].ID {
		case strong.ID:
			strongResult = &results[i]
		case weak.ID:
			weakResult = &results[i]
		}
	}
	if strongResult == nil || weakResult == nil || !weakResult.LexicalHit {
		t.Fatalf("weak fallback-only lexical hit was lost: %#v", results)
	}
	if strongResult.LexicalScore == weakResult.LexicalScore || weakResult.LexicalScore != 0 {
		t.Fatalf("fallback score normalization or membership is wrong: %#v", results)
	}
}

func TestSearchWithOptionsOrderingIsDeterministic(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	for _, text := range []string{"same alpha", "same beta", "other"} {
		if _, err := store.Add(text, "", nil, "test", []float32{1, 0}, false); err != nil {
			t.Fatal(err)
		}
	}
	options := SearchOptions{Query: "same", QueryVector: []float32{1, 0}, Backend: "test"}
	first, err := store.SearchWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SearchWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("result count changed: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Score != second[i].Score {
			t.Fatalf("non-deterministic result at %d: %#v vs %#v", i, first[i], second[i])
		}
	}
}

func TestSearchCitationsPreservePageAndLegacyHonesty(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	_, err := store.AddDocumentChunk("page text", "Book", nil, "test", []float32{1, 0},
		"section", 0, 1, false, Provenance{
			DocumentID: "doc-1", DocumentRevision: ChunkContentHash("document revision fixture"),
			ChunkHash: ChunkContentHash("page text"), SourcePath: "C:/docs/book.pdf", MediaType: "application/pdf",
			Page: 7, BlockIndex: 1, BlockMarker: "<!-- page: 7 -->",
			BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchWithOptions(SearchOptions{Query: "page", QueryVector: []float32{1, 0}, Backend: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].CitationID == "" || !strings.Contains(results[0].CitationLabel, "page 7") || !strings.Contains(results[0].CitationLabel, "block 2") {
		t.Fatalf("citation lost provenance: %#v", results)
	}
	again, err := store.SearchWithOptions(SearchOptions{Query: "page", QueryVector: []float32{1, 0}, Backend: "test"})
	if err != nil || again[0].CitationID != results[0].CitationID {
		t.Fatalf("citation ID is not stable: %#v vs %#v (err=%v)", results, again, err)
	}

	legacy, err := store.Add("legacy row", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	legacyResults, err := store.SearchWithOptions(SearchOptions{Query: "legacy", QueryVector: []float32{1, 0}, Backend: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var legacyResult *Entry
	for i := range legacyResults {
		if legacyResults[i].ID == legacy.ID {
			legacyResult = &legacyResults[i]
			break
		}
	}
	if legacyResult == nil || !strings.Contains(legacyResult.CitationLabel, "no provenance") || strings.Contains(legacyResult.CitationLabel, "page ") {
		t.Fatalf("legacy citation invented provenance: %#v", legacyResults)
	}
	basic, err := store.Search([]float32{1, 0}, "test", 1)
	if err != nil || len(basic) != 1 {
		t.Fatalf("basic Search compatibility broke: len=%d err=%v", len(basic), err)
	}
}

func TestCitationIDsAreStableAndDistinctForDocumentIdentities(t *testing.T) {
	first := Entry{DocumentID: "doc-first", SourcePath: "C:/docs/first.pdf", Page: 4, BlockIndex: 2, ChunkIndex: 1, BlockTotalChunks: 1}
	second := Entry{DocumentID: "doc-second", SourcePath: "C:/docs/second.pdf", Page: 4, BlockIndex: 2, ChunkIndex: 1, BlockTotalChunks: 1}
	firstID, firstLabel := CitationForEntry(first)
	secondID, secondLabel := CitationForEntry(second)
	if firstID == secondID {
		t.Fatalf("distinct document identities collided: %q", firstID)
	}
	if firstID != mustCitationID(first) || secondID != mustCitationID(second) {
		t.Fatal("citation ID changed between equivalent calculations")
	}
	if !strings.Contains(firstLabel, "page 4") || !strings.Contains(secondLabel, "page 4") {
		t.Fatalf("citation labels lost physical page: %q / %q", firstLabel, secondLabel)
	}
}

func TestCitationIDUsesBlockLocalChunkCoordinate(t *testing.T) {
	before := Entry{
		DocumentID: "doc-stable", SourcePath: "C:/docs/book.pdf", Page: 9,
		BlockIndex: 3, ChunkIndex: 4, BlockChunkIndex: 1, BlockTotalChunks: 3,
	}
	afterEarlierBlockGrowth := before
	afterEarlierBlockGrowth.ChunkIndex = 11
	beforeID, _ := CitationForEntry(before)
	afterID, _ := CitationForEntry(afterEarlierBlockGrowth)
	if beforeID != afterID {
		t.Fatalf("source anchor changed after an earlier block grew: %q vs %q", beforeID, afterID)
	}
	differentLocalChunk := before
	differentLocalChunk.BlockChunkIndex++
	if otherID, _ := CitationForEntry(differentLocalChunk); otherID == beforeID {
		t.Fatalf("distinct block-local chunks share citation ID %q", beforeID)
	}
}

func mustCitationID(entry Entry) string {
	id, _ := CitationForEntry(entry)
	return id
}

func TestFTSRebuildAfterMutationAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !store.lexicalDirty {
		store.Close()
		t.Fatal("new store must rebuild persisted FTS state before first lexical query")
	}
	entry, err := store.Add("before-token", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	before, err := store.SearchWithOptions(SearchOptions{Query: "before-token", Backend: "test"})
	if err != nil || len(before) != 1 || before[0].ID != entry.ID {
		store.Close()
		t.Fatalf("initial lexical search failed: %#v err=%v", before, err)
	}
	if store.lexicalDirty {
		store.Close()
		t.Fatal("successful lexical search did not clear dirty state")
	}
	if _, err := store.SearchWithOptions(SearchOptions{Query: "before-token", Backend: "test"}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if store.lexicalDirty {
		store.Close()
		t.Fatal("read-only repeat search dirtied the lexical index")
	}
	if err := store.UpdateById(entry.ID, "after-token", "", nil, []float32{1, 0}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if !store.lexicalDirty {
		store.Close()
		t.Fatal("entry mutation did not dirty the lexical index")
	}
	old, err := store.SearchWithOptions(SearchOptions{Query: "before-token", Backend: "test"})
	if err != nil || len(old) != 0 {
		store.Close()
		t.Fatalf("stale lexical row survived mutation: %#v err=%v", old, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.lexicalDirty {
		t.Fatal("reopened store trusted possibly stale persisted FTS state")
	}
	after, err := reopened.SearchWithOptions(SearchOptions{Query: "after-token", Backend: "test"})
	if err != nil || len(after) != 1 || after[0].ID != entry.ID || !after[0].LexicalHit {
		t.Fatalf("lexical index was not rebuilt after reopen: %#v err=%v", after, err)
	}
	if reopened.lexicalDirty {
		t.Fatal("reopened store remained dirty after successful rebuild")
	}
}

func TestFTSDeleteInvalidatesAndRemovesLexicalHit(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	entry, err := store.Add("delete-me-token", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchWithOptions(SearchOptions{Query: "delete-me-token", Backend: "test"})
	if err != nil || len(results) != 1 {
		t.Fatalf("initial lexical search failed: %#v err=%v", results, err)
	}
	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	if !store.lexicalDirty {
		t.Fatal("delete did not dirty the lexical index")
	}
	results, err = store.SearchWithOptions(SearchOptions{Query: "delete-me-token", Backend: "test"})
	if err != nil || len(results) != 0 {
		t.Fatalf("deleted lexical hit survived rebuild: %#v err=%v", results, err)
	}
}

func TestFTSRebuildRollsBackPartialFailure(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	if _, err := store.Add("good", "good", nil, "test", []float32{1}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("bad", "bad", nil, "test", []float32{1}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE entries_fts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE entries_fts (
		entry_id INTEGER, title TEXT CHECK(title != 'bad'), text TEXT, tags TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO entries_fts(rowid, entry_id, title, text, tags)
		VALUES (999, 999, 'sentinel', 'sentinel', '')`); err != nil {
		t.Fatal(err)
	}

	if err := store.rebuildFTSLocked(); err == nil {
		t.Fatal("rebuild unexpectedly succeeded despite CHECK constraint")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM entries_fts WHERE entry_id = 999 AND title = 'sentinel'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed rebuild did not restore prior index state: sentinel count=%d", count)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM entries_fts WHERE entry_id != 999`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial FTS rows survived rollback: count=%d", count)
	}
}

func newSearchTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	if store.LexicalMode() != lexicalFTS5 {
		t.Fatalf("expected FTS5 in current pure-Go SQLite build, got %q", store.LexicalMode())
	}
	return store
}
