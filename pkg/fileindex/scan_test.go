package fileindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

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
	entry := &FileEntry{
		Path:      "restored.txt",
		Name:      "restored.txt",
		Ext:       ".txt",
		Size:      info.Size(),
		Mtime:     info.ModTime().Unix(),
		Backend:   cfg.Backend,
		Embedding: []float32{1, 0},
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

func TestSearchFiltersBackendAndMetadataOnlyEntries(t *testing.T) {
	store := openTestStore(t)
	entries := []*FileEntry{
		{Path: "match.txt", Name: "match.txt", Backend: "ollama", Embedding: []float32{1, 0}},
		{Path: "other.txt", Name: "other.txt", Backend: "polza", Embedding: []float32{1, 0}},
		{Path: "metadata.txt", Name: "metadata.txt", Backend: "ollama"},
	}
	for _, entry := range entries {
		if err := store.Upsert(entry); err != nil {
			t.Fatal(err)
		}
	}

	results, err := store.Search([]float32{1, 0}, "ollama", 10, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Entry.Path != "match.txt" {
		t.Fatalf("unexpected results: %+v", results)
	}
	if _, err := store.Search(nil, "ollama", 10, ""); err == nil {
		t.Fatal("empty query vector should fail")
	}
	if _, err := store.Search([]float32{1}, "", 10, ""); err == nil {
		t.Fatal("empty backend should fail")
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
