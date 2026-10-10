package fileindex

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestReviewScanInvalidatesChangedContentVector(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("new contents"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, ".fileindex"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mem.DefaultLocalConfig()
	identity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entry := &FileEntry{Path: "note.txt", Name: "note.txt", Ext: ".txt", Size: 1, Mtime: info.ModTime().Add(-time.Hour).Unix(), Backend: identity.Backend, EmbeddingModel: identity.Model, EmbeddingSpace: identity.SpaceID, Embedding: []float32{1, 0}, Annotation: "old contents"}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	// A normal metadata scan notices the file change but carries the old annotation/vector forward.
	if _, err := Scan(ScanOptions{RootDir: root}, store, cfg); err != nil {
		t.Fatal(err)
	}
	current, ok := store.GetByPath("note.txt")
	if !ok {
		t.Fatal("entry disappeared")
	}
	if current.EmbeddingSpace != "" && current.Annotation == "old contents" && current.Mtime == info.ModTime().Unix() {
		t.Fatal("changed file now has fresh size/mtime with the old annotation and a vector still marked current; subsequent scan -embed skips it")
	}
}

func TestScanRefreshesStaleAnnotationAfterReopen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("new contents"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mem.DefaultLocalConfig()
	var calls atomic.Int32
	var wrongPrompt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !strings.Contains(request.Prompt, "new contents") || strings.Contains(request.Prompt, "old contents") {
			wrongPrompt.Store(true)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0, 1}})
	}))
	defer server.Close()
	cfg.Ollama.BaseURL = server.URL
	identity := testEmbeddingIdentity(t, cfg)
	if err := store.Upsert(&FileEntry{Path: "note.txt", Name: "note.txt", Ext: ".txt", Size: 1, Mtime: 1,
		Annotation: "old contents", Backend: identity.Backend, EmbeddingModel: identity.Model,
		EmbeddingSpace: identity.SpaceID, Embedding: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(ScanOptions{RootDir: root}, store, cfg); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stale, ok := store.GetByPath("note.txt")
	if !ok || !stale.AnnotationStale || len(stale.Embedding) != 0 {
		t.Fatalf("stale marker lost after reopen: %+v", stale)
	}
	report, err := Scan(ScanOptions{RootDir: root, Embed: true}, store, cfg)
	if err != nil || len(report.Errors) > 0 {
		t.Fatalf("refresh: %+v %v", report, err)
	}
	fresh, _ := store.GetByPath("note.txt")
	if fresh.AnnotationStale || fresh.Annotation != "new contents" || fresh.EmbeddingSpace != identity.SpaceID || len(fresh.Embedding) != 2 || calls.Load() != 1 || wrongPrompt.Load() {
		t.Fatalf("annotation/vector did not refresh: %+v calls=%d wrongPrompt=%v", fresh, calls.Load(), wrongPrompt.Load())
	}
	if _, err := Scan(ScanOptions{RootDir: root, Embed: true}, store, cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("current file was embedded again")
	}
}

func TestFileLookupIndexesSurviveDeleteAndReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := &FileEntry{Path: "first.txt", Backend: "test"}
	second := &FileEntry{Path: filepath.Join("sub", "second.txt"), Backend: "test", Embedding: []float32{1, 0}}
	for _, entry := range []*FileEntry{first, second} {
		if err := store.Upsert(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetByPath(first.Path); ok {
		t.Fatal("deleted file still indexed")
	}
	second.Annotation = "updated"
	if err := store.Upsert(second); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.GetByPath("sub/second.txt"); !ok || got.ID != second.ID || got.Annotation != "updated" {
		t.Fatalf("shifted path index: %+v %v", got, ok)
	}
	store.Close()
	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Get(second.ID)
	if err != nil || got.Annotation != "updated" || len(store.files) != 1 {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	got.Embedding[0] = 99
	if copy, _ := store.GetByPath(second.Path); copy.Embedding[0] != 1 {
		t.Fatal("lookup returned mutable cache")
	}
}

func TestScanFailuresNeverKeepChangedContentVector(t *testing.T) {
	for _, ext := range []string{".txt", ".epub"} {
		t.Run(ext, func(t *testing.T) {
			root := t.TempDir()
			name := "note" + ext
			if err := os.WriteFile(filepath.Join(root, name), []byte("new contents, not a ZIP"), 0600); err != nil {
				t.Fatal(err)
			}
			store := openTestStore(t)
			cfg := mem.DefaultLocalConfig()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "provider unavailable", http.StatusBadRequest)
			}))
			defer server.Close()
			cfg.Ollama.BaseURL = server.URL
			identity := testEmbeddingIdentity(t, cfg)
			if err := store.Upsert(&FileEntry{Path: name, Name: name, Ext: ext, Size: 1, Mtime: 1, Annotation: "old contents", Backend: identity.Backend, EmbeddingModel: identity.Model, EmbeddingSpace: identity.SpaceID, Embedding: []float32{1, 0}}); err != nil {
				t.Fatal(err)
			}
			extract := ExtractAnnotation
			if ext == ".epub" {
				// Simulate an unavailable source after stat, without changing OS ACLs.
				extract = func(string, string) (string, error) { return "", errors.New("source became unreadable") }
			}
			report, err := scanWithAnnotationExtractor(ScanOptions{RootDir: root, Embed: true}, store, cfg, extract)
			if err != nil || len(report.Errors) == 0 {
				t.Fatalf("failure not reported: %+v %v", report, err)
			}
			got, ok := store.GetByPath(name)
			if !ok || len(got.Embedding) != 0 || got.EmbeddingSpace != "" {
				t.Fatalf("failed refresh kept a vector: %+v", got)
			}
			if ext == ".epub" && !got.AnnotationStale {
				t.Fatal("failed extraction lost retry marker")
			}
			results, err := store.Search([]float32{1, 0}, identity.SpaceID, 5, "")
			if err != nil || len(results) != 0 {
				t.Fatalf("stale vector was searchable: %+v %v", results, err)
			}
		})
	}
}
