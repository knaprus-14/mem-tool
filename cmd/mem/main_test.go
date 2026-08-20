package main

import (
	"context"
	"errors"
	"io"
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

type fakeAnswerProvider struct {
	answer string
	calls  int
}

func (p *fakeAnswerProvider) Generate(_ context.Context, _ mem.AnswerRequest) (string, error) {
	p.calls++
	return p.answer, nil
}

func TestHandleAskKeepsStatusOnStderrAndVerifiedAnswerOnStdout(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry, err := store.AddDocumentChunk("Русский факт о запуске", "Book", nil, "test", []float32{1, 0},
		"section", 0, 1, false, mem.Provenance{DocumentID: "doc-ask", SourcePath: "C:/docs/book.pdf", Page: 3, BlockIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	citationID, _ := mem.CitationForEntry(*entry)
	fake := &fakeAnswerProvider{answer: "Ответ подтверждён [" + citationID + "]"}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	cfg.Answer.ContextChars = 1000
	originalEmbedding := getEmbeddingContext
	originalProvider := newAnswerProvider
	defer func() { getEmbeddingContext = originalEmbedding; newAnswerProvider = originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleAsk(cfg, store, []string{"где", "запуск"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Ответ подтверждён") || !strings.Contains(stdout, citationID) || strings.Contains(stdout, "[ASK]") {
		t.Fatalf("stdout is not a scriptable answer stream: %q", stdout)
	}
	if !strings.Contains(stderr, "[ASK] retrieval") || !strings.Contains(stderr, "[ASK] evidence") || fake.calls != 1 {
		t.Fatalf("stderr/provider status contract failed: stderr=%q calls=%d", stderr, fake.calls)
	}
}

func TestHandleAskReturnsInsufficientEvidenceWithoutGeneration(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fake := &fakeAnswerProvider{answer: "should not be called"}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalEmbedding := getEmbeddingContext
	originalProvider := newAnswerProvider
	defer func() { getEmbeddingContext = originalEmbedding; newAnswerProvider = originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, _, err := captureCLIStreams(func() error {
		return handleAsk(cfg, store, []string{"вопрос"})
	})
	if err != nil || !strings.Contains(stdout, "Недостаточно") || fake.calls != 0 {
		t.Fatalf("insufficient evidence was not honest: stdout=%q err=%v calls=%d", stdout, err, fake.calls)
	}
}

func captureCLIStreams(fn func() error) (string, string, error) {
	oldOut, oldErr := os.Stdout, os.Stderr
	outReader, outWriter, _ := os.Pipe()
	errReader, errWriter, _ := os.Pipe()
	os.Stdout, os.Stderr = outWriter, errWriter
	err := fn()
	_ = outWriter.Close()
	_ = errWriter.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(outReader)
	errOut, _ := io.ReadAll(errReader)
	_ = outReader.Close()
	_ = errReader.Close()
	return string(out), string(errOut), err
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
