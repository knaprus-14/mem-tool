package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestHandleAddFileStoresExactlyEmbeddedText(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := strings.Repeat("Русский текст. ", 45)
	path := filepath.Join(root, "single.txt")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	var embeddedText string
	getEmbedding = func(_ *Config, text string) ([]float32, error) { embeddedText = text; return []float32{1, 2, 3}, nil }
	if err := handleAddFile(testCLIConfig(1500, "paragraph"), store, []string{path}); err != nil {
		t.Fatal(err)
	}
	source, _ := mem.CanonicalSourcePath(path)
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 {
		t.Fatalf("got %d stored entries, want 1", len(entries))
	}
	want := strings.TrimSpace(input)
	if entries[0].Text != want || embeddedText != want || entries[0].Text != embeddedText {
		t.Fatalf("stored and embedded text differ: stored=%d runes embedded=%d runes want=%d runes", len([]rune(entries[0].Text)), len([]rune(embeddedText)), len([]rune(want)))
	}
}

func TestHandleAddFileDoesNotReportPartialEmbeddingAsSuccess(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "multi.txt")
	if err := os.WriteFile(path, []byte("11111 22222 33333"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	calls := 0
	getEmbedding = func(_ *Config, text string) ([]float32, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("test embedding failure")
		}
		return []float32{float32(len(text)), 1}, nil
	}
	if err := handleAddFile(testCLIConfig(6, "fixed"), store, []string{path}); err == nil {
		t.Fatal("partial embedding failure reported success")
	}
	if got := store.Stats()["total_entries"]; got != 0 {
		t.Fatalf("partial document was stored: total_entries=%v", got)
	}
}

func testCLIConfig(maxSize int, strategy string) *Config {
	return &Config{Backend: "test", Chunking: ChunkConfig{MaxSize: maxSize, Strategy: strategy}}
}
