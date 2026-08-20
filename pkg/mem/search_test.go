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
			DocumentID: "doc-1", SourcePath: "C:/docs/book.pdf", MediaType: "application/pdf",
			Page: 7, BlockIndex: 1, BlockMarker: "<!-- page: 7 -->", ExtractionMethod: "text",
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
