package mem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateEmbeddingTextRejectsSilentTruncation(t *testing.T) {
	if err := validateEmbeddingText(strings.Repeat("я", maxEmbedChars)); err != nil {
		t.Fatalf("text at limit rejected: %v", err)
	}
	if err := validateEmbeddingText(strings.Repeat("я", maxEmbedChars+1)); err == nil {
		t.Fatal("text over embedding limit was accepted and could be silently truncated")
	}
}

func TestGetEmbeddingContextCancelsInflightOllamaRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	cfg := DefaultLocalConfig()
	cfg.Ollama.BaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := GetEmbeddingContext(ctx, cfg, "cancel me")
		done <- err
	}()

	select {
	case <-requestStarted:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("embedding request did not start")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("in-flight cancellation was not returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("embedding request did not stop after cancellation")
	}
}
