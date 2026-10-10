package mem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestQueryAnswerExactExcerptReferenceSelectsCorrectEntry(t *testing.T) {
	dir, store, cfg := queryFixture(t)
	first, err := store.Add("needle pressure is 5 bar", "First", nil, "legacy", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Add("needle pressure is 9 bar", "Second", nil, "legacy", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Two current import copies have a shared logical chunk ID but distinct
	// text and revisions. The resulting model references must remain separate.
	for _, item := range []Entry{*first, *second} {
		path := filepath.Join(dir, "private-query", item.Title+".pdf")
		if _, err = store.db.Exec(`UPDATE entries SET source_file=?,source_path=?,document_id='shared-document',document_revision=?,chunk_hash=?,page=1,block_index=0,block_chunk_index=0,block_total_chunks=1 WHERE id=?`, path, path, ChunkContentHash(item.Text), ChunkContentHash(item.Text), item.ID); err != nil {
			t.Fatal(err)
		}
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"claims":[{"text":"Second source first","citations":["E2"]},{"text":"First source second","citations":["E1"]}]}`}, "done": true})
	}))
	defer model.Close()
	cfg.Answer.BaseURL, cfg.Answer.Model = model.URL, "test-chat"
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := QueryIn(context.Background(), dir, QueryRequest{Query: "needle", Mode: "text", Limit: 2}, true)
	if err != nil || result.Status != "answered" || len(result.Hits) != 2 || len(result.Sources) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Sources[0].ID != result.Hits[1].ID || result.Sources[1].ID != result.Hits[0].ID || result.Sources[0].ID == result.Sources[1].ID {
		t.Fatalf("exact model refs selected wrong entries: hits=%#v sources=%#v", result.Hits, result.Sources)
	}
	if result.Sources[0].CitationID != result.Sources[1].CitationID || result.Sources[0].Text != result.Hits[1].Text || result.Sources[1].Text != result.Hits[0].Text || result.Sources[0].DocumentRevision != result.Hits[1].DocumentRevision {
		t.Fatalf("source excerpts or revision metadata changed: hits=%#v sources=%#v", result.Hits, result.Sources)
	}
}
