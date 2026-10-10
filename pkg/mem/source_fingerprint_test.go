package mem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestPhysicalSourceFingerprintSurvivesReopenAndRejectsLegacy(t *testing.T) {
	store, anchor, _ := knowledgeMapPDFSourceFixture(t)
	dir := filepath.Dir(store.Path())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewKnowledgeMapWorkspaceHandler(store, "source-test", "token", "")
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9876/api/source?citation="+anchor.CitationID, nil)
		r.AddCookie(knowledgeMapSourceCookie("token"))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != http.StatusOK {
		t.Fatalf("reopened source: %d %s", w.Code, w.Body)
	}
	if _, err := store.db.Exec(`DELETE FROM document_source_files`); err != nil {
		t.Fatal(err)
	}
	w := request()
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "%PDF") || !strings.Contains(w.Body.String(), "mem import") {
		t.Fatalf("legacy source not rejected with repair guidance: %d %s", w.Code, w.Body)
	}
}

func TestImportRejectsSourceChangedDuringEmbedding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(path, []byte("# Original\n\nSource contents."), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := ingest.Extract(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = importExtractedDocumentWithEmbedder(DefaultLocalConfig(), store, doc, ImportOptions{}, func(_ *Config, _ string) ([]float32, error) {
		if err := os.WriteFile(path, []byte("# Replacement\n\nUnrelated contents."), 0600); err != nil {
			return nil, err
		}
		return []float32{1, 0}, nil
	})
	if err == nil {
		t.Fatal("changed source imported")
	}
	if entries := store.GetBySourceFile(doc.SourcePath); len(entries) != 0 {
		t.Fatalf("partial document committed: %+v", entries)
	}
	var hashes int
	if err := store.db.QueryRow(`SELECT count(*) FROM document_source_files`).Scan(&hashes); err != nil || hashes != 0 {
		t.Fatalf("failed import persisted fingerprints: %d %v", hashes, err)
	}
}
