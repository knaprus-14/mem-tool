package mem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(maxSize int, strategy string) *Config {
	cfg := DefaultLocalConfig()
	cfg.Chunking = ChunkConfig{MaxSize: maxSize, Strategy: strategy}
	return cfg
}

func fakeEmbedding(_ *Config, text string) ([]float32, error) {
	return []float32{float32(len([]rune(text))), 1}, nil
}

func TestIndexFileUsesFullPathAsDocumentIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	paths := []string{filepath.Join(root, "one", "notes.txt"), filepath.Join(root, "two", "notes.txt")}
	for i, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat(string(rune('a'+i)), 8)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding); err != nil {
			t.Fatalf("index %s: %v", path, err)
		}
	}
	sources := store.SourceFiles()
	if len(sources) != 2 {
		t.Fatalf("got %d source identities, want 2: %#v", len(sources), sources)
	}
	for _, path := range paths {
		canonical, err := CanonicalSourcePath(path)
		if err != nil {
			t.Fatal(err)
		}
		if sources[canonical] != 1 {
			t.Errorf("source %q has %d chunks, want 1", canonical, sources[canonical])
		}
	}
}

func TestIndexFileRemovesStaleTailAfterShorterReindex(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("aaaa bbbb cccc dddd eeee ffff"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := indexFileWithEmbedder(testConfig(8, "fixed"), store, path, fakeEmbedding)
	if err != nil {
		t.Fatal(err)
	}
	if first.Chunks < 2 {
		t.Fatalf("test setup produced only %d chunk(s)", first.Chunks)
	}
	if err := os.WriteFile(path, []byte("коротко"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding)
	if err != nil {
		t.Fatal(err)
	}
	if second.Chunks != 1 {
		t.Fatalf("reindex saved %d chunks, want 1", second.Chunks)
	}
	source, _ := CanonicalSourcePath(path)
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 {
		t.Fatalf("stale tail remains: got %d chunks, want 1", len(entries))
	}
	if entries[0].Text != "коротко" || entries[0].TotalChunks != 1 {
		t.Fatalf("unexpected remaining chunk: %#v", entries[0])
	}
}

func TestIndexFileRemovesAllChunksWhenSourceBecomesEmpty(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("old searchable text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, func(*Config, string) ([]float32, error) {
		t.Fatal("empty document must not call the embedder")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped || result.Chunks != 0 || result.Failed != 0 {
		t.Fatalf("unexpected empty reindex result: %#v", result)
	}
	source, _ := CanonicalSourcePath(path)
	if entries := store.GetBySourceFile(source); len(entries) != 0 {
		t.Fatalf("empty source left %d stale searchable chunks", len(entries))
	}
	snapshots, err := store.ListDocumentHistorySnapshots(source)
	if err != nil || len(snapshots) != 1 || snapshots[0].ChunkCount != 1 {
		t.Fatalf("empty replacement did not preserve the prior document: snapshots=%#v err=%v", snapshots, err)
	}
	plan, err := store.BuildDocumentRestorePlan(source, snapshots[0].SnapshotID)
	if err != nil {
		t.Fatalf("preview restore from empty state: %v", err)
	}
	run, err := store.ApplyDocumentRestore(source, snapshots[0].SnapshotID, plan.PlanDigest)
	if err != nil {
		t.Fatalf("restore document removed by empty source: %v", err)
	}
	if entries := store.GetBySourceFile(source); len(entries) != 1 || entries[0].Text != "old searchable text" {
		t.Fatalf("restore from empty state returned wrong document: %#v", entries)
	}
	rollback, err := store.BuildDocumentRestoreRollbackPlan(run.ID)
	if err != nil {
		t.Fatalf("preview rollback to empty state: %v", err)
	}
	if _, err := store.ApplyDocumentRestoreRollback(run.ID, rollback.PlanDigest); err != nil {
		t.Fatalf("rollback to empty state: %v", err)
	}
	if entries := store.GetBySourceFile(source); len(entries) != 0 {
		t.Fatalf("rollback did not restore empty state: %#v", entries)
	}
	if err := os.WriteFile(path, []byte("new searchable text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding); err != nil {
		t.Fatalf("replace empty state with new content: %v", err)
	}
	snapshots, err = store.ListDocumentHistorySnapshots(source)
	if err != nil {
		t.Fatal(err)
	}
	emptySnapshotID := ""
	for _, snapshot := range snapshots {
		if snapshot.ChunkCount == 0 {
			emptySnapshotID = snapshot.SnapshotID
			break
		}
	}
	if emptySnapshotID == "" {
		t.Fatalf("empty -> non-empty transition was not archived: %#v", snapshots)
	}
	emptyPlan, err := store.BuildDocumentRestorePlan(source, emptySnapshotID)
	if err != nil {
		t.Fatalf("preview restored empty snapshot: %v", err)
	}
	emptyRun, err := store.ApplyDocumentRestore(source, emptySnapshotID, emptyPlan.PlanDigest)
	if err != nil {
		t.Fatalf("restore exact empty snapshot: %v", err)
	}
	if entries := store.GetBySourceFile(source); len(entries) != 0 {
		t.Fatalf("empty snapshot remained searchable: %#v", entries)
	}
	newRollback, err := store.BuildDocumentRestoreRollbackPlan(emptyRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDocumentRestoreRollback(emptyRun.ID, newRollback.PlanDigest); err != nil {
		t.Fatalf("rollback empty restore to new content: %v", err)
	}
	if entries := store.GetBySourceFile(source); len(entries) != 1 || entries[0].Text != "new searchable text" {
		t.Fatalf("rollback did not restore post-empty content: %#v", entries)
	}
}

func TestIndexFileEmbeddingFailureLeavesExistingDocumentUntouched(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("stable original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("11111 22222 33333"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	failingEmbedder := func(_ *Config, text string) ([]float32, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("test embedding failure")
		}
		return []float32{float32(len(text)), 1}, nil
	}
	result, err := indexFileWithEmbedder(testConfig(6, "fixed"), store, path, failingEmbedder)
	if err == nil {
		t.Fatal("partial embedding failure reported success")
	}
	if result.Failed == 0 || result.Chunks != 0 {
		t.Fatalf("unexpected result after failure: %#v", result)
	}
	source, _ := CanonicalSourcePath(path)
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 || entries[0].Text != "stable original" {
		t.Fatalf("existing document changed after embedding failure: %#v", entries)
	}
}

func TestIndexFileWriteFailureRollsBackExistingDocument(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("stable original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), store, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_second_index_chunk
BEFORE INSERT ON entries WHEN NEW.source_file != '' AND NEW.chunk_index = 1
BEGIN SELECT RAISE(ABORT, 'synthetic second-chunk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("11111 22222 33333"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := indexFileWithEmbedder(testConfig(6, "fixed"), store, path, fakeEmbedding)
	if err == nil || result.Chunks != 0 || result.Failed == 0 {
		t.Fatalf("write failure was not reported atomically: result=%#v err=%v", result, err)
	}
	source, _ := CanonicalSourcePath(path)
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 || entries[0].Text != "stable original" || entries[0].TotalChunks != 1 {
		t.Fatalf("failed index replacement changed the previous document: %#v", entries)
	}
}
