package fileindex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func testEmbeddingIdentity(t *testing.T, cfg *mem.Config) mem.EmbeddingIdentity {
	t.Helper()
	identity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatalf("EmbeddingIdentityForConfig: %v", err)
	}
	return identity
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), ".fileindex"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestScanNoEmbedAddsMetadataOnlyEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	cfg := mem.DefaultLocalConfig()

	report, err := Scan(ScanOptions{RootDir: root, Embed: false}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Errors) != 0 || report.Added != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}

	entry, ok := store.GetByPath("notes.txt")
	if !ok {
		t.Fatal("metadata-only entry was not stored")
	}
	if len(entry.Embedding) != 0 || entry.Dims != 0 {
		t.Fatalf("expected empty embedding, got dims=%d len=%d", entry.Dims, len(entry.Embedding))
	}
	if entry.Backend != cfg.Backend {
		t.Fatalf("backend=%q, want %q", entry.Backend, cfg.Backend)
	}
	if entry.EmbeddingModel != "" || entry.EmbeddingSpace != "" {
		t.Fatalf("metadata-only entry received embedding identity: %+v", entry)
	}
}

func TestScanRestoresStaleFileWithUnchangedMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "restored.txt")
	if err := os.WriteFile(path, []byte("restored"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	cfg := mem.DefaultLocalConfig()
	identity := testEmbeddingIdentity(t, cfg)
	entry := &FileEntry{
		Path:           "restored.txt",
		Name:           "restored.txt",
		Ext:            ".txt",
		Size:           info.Size(),
		Mtime:          info.ModTime().Unix(),
		Backend:        cfg.Backend,
		EmbeddingModel: identity.Model,
		EmbeddingSpace: identity.SpaceID,
		Embedding:      []float32{1, 0},
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStale([]string{entry.Path}); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(ScanOptions{RootDir: root, Embed: false}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Errors) != 0 || report.Updated != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	got, ok := store.GetByPath(entry.Path)
	if !ok || got.Stale {
		t.Fatalf("restored entry not active: found=%v stale=%v", ok, got.Stale)
	}
	if len(got.Embedding) != 2 {
		t.Fatalf("existing embedding was not preserved: %v", got.Embedding)
	}
	if got.EmbeddingModel != identity.Model || got.EmbeddingSpace != identity.SpaceID {
		t.Fatalf("existing embedding identity was not preserved: %+v", got)
	}
}

func TestScanNoEmbedClearsVectorWhenAnnotationChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("new annotation"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mem.DefaultLocalConfig()
	identity := testEmbeddingIdentity(t, cfg)
	store := openTestStore(t)
	if err := store.Upsert(&FileEntry{
		Path: "notes.txt", Name: "notes.txt", Ext: ".txt", Size: info.Size(), Mtime: info.ModTime().Unix(),
		Annotation: "old annotation", Backend: identity.Backend,
		EmbeddingModel: identity.Model, EmbeddingSpace: identity.SpaceID, Embedding: []float32{1, 0},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(ScanOptions{RootDir: root, Enrich: true, Embed: false}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Updated != 1 || len(report.Errors) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	got, ok := store.GetByPath("notes.txt")
	if !ok || got.Annotation != "new annotation" {
		t.Fatalf("annotation was not refreshed: %+v", got)
	}
	if len(got.Embedding) != 0 || got.Dims != 0 || got.EmbeddingModel != "" || got.EmbeddingSpace != "" {
		t.Fatalf("changed search text kept a stale vector identity: %+v", got)
	}
}

func TestScanIncompleteWalkDoesNotMarkEntriesStale(t *testing.T) {
	store := openTestStore(t)
	cfg := mem.DefaultLocalConfig()
	entry := &FileEntry{Path: "keep.txt", Name: "keep.txt", Backend: cfg.Backend, Embedding: []float32{1}}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}

	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")
	report, err := Scan(ScanOptions{RootDir: missingRoot, Embed: false}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Errors) < 2 || !strings.Contains(report.Errors[len(report.Errors)-1], "stale reconciliation skipped") {
		t.Fatalf("incomplete walk was not reported: %+v", report.Errors)
	}
	got, _ := store.GetByPath(entry.Path)
	if got.Stale {
		t.Fatal("entry was marked stale after incomplete walk")
	}
}

func TestScanDryRunDoesNotMutateStaleState(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t)
	cfg := mem.DefaultLocalConfig()
	entry := &FileEntry{Path: "missing.txt", Name: "missing.txt", Backend: cfg.Backend, Embedding: []float32{1}}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(ScanOptions{RootDir: root, Embed: false, DryRun: true}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Stale != 1 {
		t.Fatalf("stale=%d, want 1", report.Stale)
	}
	got, _ := store.GetByPath(entry.Path)
	if got.Stale {
		t.Fatal("dry-run mutated stale state")
	}
}

func TestSearchFiltersEmbeddingSpaceAndMetadataOnlyEntries(t *testing.T) {
	store := openTestStore(t)
	entries := []*FileEntry{
		{Path: "match.txt", Name: "match.txt", Backend: "ollama", EmbeddingModel: "model-a", EmbeddingSpace: "space-a", Embedding: []float32{1, 0}},
		{Path: "same-dims-other-model.txt", Name: "same-dims-other-model.txt", Backend: "ollama", EmbeddingModel: "model-b", EmbeddingSpace: "space-b", Embedding: []float32{1, 0}},
		{Path: "legacy.txt", Name: "legacy.txt", Backend: "ollama", Embedding: []float32{1, 0}},
		{Path: "metadata.txt", Name: "metadata.txt", Backend: "ollama"},
	}
	for _, entry := range entries {
		if err := store.Upsert(entry); err != nil {
			t.Fatal(err)
		}
	}

	results, err := store.Search([]float32{1, 0}, "space-a", 10, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Entry.Path != "match.txt" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := store.Search(nil, "space-a", 10, ""); err == nil {
		t.Fatal("empty query vector should fail")
	}
	if _, err := store.Search([]float32{1}, "", 10, ""); err == nil {
		t.Fatal("empty embedding space should fail")
	}
}

func TestScanSkipsExactCurrentEmbeddingSpace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "current.txt")
	if err := os.WriteFile(path, []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 0}})
	}))
	defer server.Close()

	cfg := mem.DefaultLocalConfig()
	cfg.Ollama.BaseURL = server.URL
	identity := testEmbeddingIdentity(t, cfg)
	store := openTestStore(t)
	if err := store.Upsert(&FileEntry{
		Path: "current.txt", Name: "current.txt", Size: info.Size(), Mtime: info.ModTime().Unix(),
		Backend: identity.Backend, EmbeddingModel: identity.Model,
		EmbeddingSpace: identity.SpaceID, Embedding: []float32{1, 0},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(ScanOptions{RootDir: root, Embed: true}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Skipped != 1 || report.Updated != 0 || len(report.Errors) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if requests.Load() != 0 {
		t.Fatalf("exact current vector was regenerated: requests=%d", requests.Load())
	}
}

func TestScanRefreshesLegacyAndChangedEmbeddingSpaces(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"legacy.txt", "changed.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	var retainedAnnotationSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err == nil && strings.Contains(request.Prompt, "retained annotation") {
			retainedAnnotationSeen.Store(true)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0, 1}})
	}))
	defer server.Close()

	cfg := mem.DefaultLocalConfig()
	cfg.Ollama.BaseURL = server.URL
	cfg.Ollama.Model = "current-model"
	current := testEmbeddingIdentity(t, cfg)
	oldCfg := *cfg
	oldCfg.Ollama.Model = "old-model"
	old := testEmbeddingIdentity(t, &oldCfg)
	store := openTestStore(t)

	for _, tc := range []struct {
		name, model, space, annotation string
	}{
		{name: "legacy.txt"},
		{name: "changed.txt", model: old.Model, space: old.SpaceID, annotation: "retained annotation"},
	} {
		info, err := os.Stat(filepath.Join(root, tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Upsert(&FileEntry{
			Path: tc.name, Name: tc.name, Size: info.Size(), Mtime: info.ModTime().Unix(),
			Backend: "ollama", EmbeddingModel: tc.model, EmbeddingSpace: tc.space,
			Embedding: []float32{1, 0}, Annotation: tc.annotation,
		}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := Scan(ScanOptions{RootDir: root, Embed: true}, store, cfg)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Updated != 2 || report.Skipped != 0 || len(report.Errors) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if requests.Load() != 2 {
		t.Fatalf("embedding requests=%d, want 2", requests.Load())
	}
	if !retainedAnnotationSeen.Load() {
		t.Fatal("ordinary rescan did not preserve the existing annotation in embedding text")
	}
	for _, name := range []string{"legacy.txt", "changed.txt"} {
		got, ok := store.GetByPath(name)
		if !ok || got.EmbeddingModel != current.Model || got.EmbeddingSpace != current.SpaceID {
			t.Fatalf("%s identity was not refreshed: %+v", name, got)
		}
		if len(got.Embedding) != 2 || got.Embedding[0] != 0 || got.Embedding[1] != 1 {
			t.Fatalf("%s vector was not refreshed: %v", name, got.Embedding)
		}
	}
}

func TestStoreCopiesEmbeddingAndTags(t *testing.T) {
	store := openTestStore(t)
	embedding := []float32{1, 2}
	tags := []string{"one"}
	entry := &FileEntry{Path: "copy.txt", Name: "copy.txt", Backend: "ollama", Embedding: embedding, Tags: tags}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	embedding[0] = 99
	tags[0] = "changed"

	got, _ := store.GetByPath(entry.Path)
	if got.Embedding[0] != 1 || got.Tags[0] != "one" {
		t.Fatalf("store retained caller-owned slices: %+v", got)
	}
	got.Embedding[0] = 77
	got.Tags[0] = "again"
	second, _ := store.GetByPath(entry.Path)
	if second.Embedding[0] != 1 || second.Tags[0] != "one" {
		t.Fatalf("GetByPath exposed store-owned slices: %+v", second)
	}
}
