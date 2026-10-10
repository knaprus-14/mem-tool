package mem

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReviewFixedChunkStopsAtEnd(t *testing.T) {
	for _, tc := range []struct {
		text          string
		size, overlap int
		want          []string
	}{
		{"0123456789", 100, 2, []string{"0123456789"}},
		{"0123456789", 10, 2, []string{"0123456789"}},
		{"абвгдежзиклм", 5, 2, []string{"абвгд", "гдежз", "жзикл", "клм"}},
	} {
		chunks := ChunkDocument(tc.text, tc.size, tc.overlap, "fixed")
		if len(chunks) != len(tc.want) {
			t.Fatalf("%q: chunks=%+v want=%+v", tc.text, chunks, tc.want)
		}
		for i, chunk := range chunks {
			if chunk.Text != tc.want[i] {
				t.Fatalf("chunk %d=%q want=%q", i, chunk.Text, tc.want[i])
			}
		}
	}
}

func TestReviewParagraphRespectsMaxSize(t *testing.T) {
	for _, chunk := range ChunkDocument("12345\n\n6789", 10, 0, "paragraph") {
		if n := utf8.RuneCountInString(chunk.Text); n > 10 {
			t.Fatalf("max_size=10 but chunk has %d runes: %q", n, chunk.Text)
		}
	}
}

func TestReviewFilteredSearchIgnoresExcludedLexicalHits(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Add("semantic candidate", "", []string{"keep"}, "test", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	options := SearchOptions{Query: "needle", QueryVector: []float32{1, 0}, Backend: "test", Tags: []string{"keep"}}
	before, err := store.SearchWithOptions(options)
	if err != nil || len(before) != 1 {
		t.Fatalf("before: %v %v", before, err)
	}
	if _, err := store.Add("needle", "", []string{"excluded"}, "test", []float32{0, 1}, false); err != nil {
		t.Fatal(err)
	}
	after, err := store.SearchWithOptions(options)
	if err != nil || len(after) != 1 {
		t.Fatalf("after: %v %v", after, err)
	}
	if before[0].Score != after[0].Score {
		t.Fatalf("adding an excluded-tag entry changed kept result score: %g -> %g", before[0].Score, after[0].Score)
	}
}

func TestReviewDateIncludesEntireLastDay(t *testing.T) {
	for _, tc := range []struct {
		created string
		want    bool
	}{
		{"2026-09-05T23:59:59.500Z", true},
		{"2026-09-05T23:59:59.999999999Z", true},
		{"2026-09-06T00:00:00Z", false},
		{"2026-09-05T00:00:00Z", true},
		{"2026-09-04T23:59:59.999999999Z", false},
	} {
		if got := matchesSearchDate(tc.created, "2026-09-05", "2026-09-05"); got != tc.want {
			t.Fatalf("date %s included=%v want=%v", tc.created, got, tc.want)
		}
	}
}

func TestReviewSQLiteWaitsForShortExternalWrite(t *testing.T) {
	dir := t.TempDir()
	a, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE entry_cache_state SET generation=generation WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); _ = tx.Rollback(); close(released) }()
	start := time.Now()
	_, addErr := b.Add("concurrent note", "", nil, "test", []float32{1, 0}, false)
	elapsed := time.Since(start)
	<-released
	if addErr != nil {
		t.Fatalf("a 100ms external writer caused Add to fail after %s: %v", elapsed, addErr)
	}
}

func TestReviewIndexExplicitHiddenRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".notes")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	results, err := IndexDirectory(DefaultLocalConfig(), store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("explicit hidden root containing one supported file returned %d results with no error", len(results))
	}
}

func TestReviewSourceDoesNotServeChangedPDF(t *testing.T) {
	store, anchor, source := knowledgeMapPDFSourceFixture(t)
	defer store.Close()
	changed := "%PDF-1.4\nDIFFERENT DOCUMENT AFTER INDEXING\n%%EOF\n"
	if err := os.WriteFile(source, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9876/api/source?citation="+anchor.CitationID, nil)
	request.AddCookie(knowledgeMapSourceCookie("review-token"))
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	NewKnowledgeMapWorkspaceHandler(store, "review", "review-token", "").ServeHTTP(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "DIFFERENT DOCUMENT") {
		t.Fatalf("old citation must reject replacement PDF: status=%d body=%s", response.Code, response.Body)
	}
}
