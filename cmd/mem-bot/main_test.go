package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestGetOrCreateStoreConcurrentSameUser(t *testing.T) {
	d := &botData{dataDir: t.TempDir(), stores: make(map[int64]*userStore)}
	t.Cleanup(func() {
		if err := d.closeStores(); err != nil {
			t.Errorf("closeStores: %v", err)
		}
	})

	const callers = 24
	start := make(chan struct{})
	results := make(chan *userStore, callers)
	errors := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store, err := d.getOrCreateStore(42)
			if err != nil {
				errors <- err
				return
			}
			results <- store
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Fatalf("getOrCreateStore: %v", err)
	}
	var first *userStore
	count := 0
	for store := range results {
		count++
		if first == nil {
			first = store
			continue
		}
		if store != first {
			t.Fatal("concurrent callers received different userStore instances")
		}
	}
	if count != callers {
		t.Fatalf("successful callers = %d, want %d", count, callers)
	}

	d.mu.Lock()
	cacheSize := len(d.stores)
	d.mu.Unlock()
	if cacheSize != 1 {
		t.Fatalf("cache size = %d, want 1", cacheSize)
	}
}

func TestGetOrCreateStoreRejectsCorruptConfig(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, "7", mem.MemDirName)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mem.ConfigPathIn(memDir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := &botData{dataDir: root, stores: make(map[int64]*userStore)}
	_, err := d.getOrCreateStore(7)
	if err == nil || !strings.Contains(err.Error(), "config:") {
		t.Fatalf("error = %v, want actionable config error", err)
	}
}

func TestCloseStoresReleasesSQLiteFile(t *testing.T) {
	d := &botData{dataDir: t.TempDir(), stores: make(map[int64]*userStore)}
	store, err := d.getOrCreateStore(9)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(store.dir, "store.db")

	if err := d.closeStores(); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	cacheSize := len(d.stores)
	d.mu.Unlock()
	if cacheSize != 0 {
		t.Fatalf("cache size after close = %d, want 0", cacheSize)
	}

	movedPath := dbPath + ".moved"
	if err := os.Rename(dbPath, movedPath); err != nil {
		t.Fatalf("SQLite file is still held after close: %v", err)
	}
	if err := os.Rename(movedPath, dbPath); err != nil {
		t.Fatalf("restore SQLite file: %v", err)
	}
}

func TestTruncatePreservesUnicode(t *testing.T) {
	if got := truncate("абвг", 3); got != "абв..." {
		t.Fatalf("truncate = %q, want %q", got, "абв...")
	}
	if got := truncate("абв", 0); got != "" {
		t.Fatalf("truncate with zero limit = %q, want empty", got)
	}

	message := strings.Repeat("🙂", 4100)
	got := truncateMessage(message)
	if !utf8.ValidString(got) {
		t.Fatal("truncateMessage returned invalid UTF-8")
	}
	if runes := len([]rune(got)); runes != 4000 {
		t.Fatalf("truncateMessage rune count = %d, want 4000", runes)
	}
	if !strings.HasSuffix(got, "\n\n... _(обрезано)_") {
		t.Fatalf("truncateMessage suffix missing: %q", got[len(got)-40:])
	}
}
