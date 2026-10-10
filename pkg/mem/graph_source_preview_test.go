package mem

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestKnowledgeMapPNGRequiresVerifiedSourceSnapshot(t *testing.T) {
	store, anchor, source := knowledgeMapPDFSourceFixture(t)
	defer store.Close()
	workspace := &KnowledgeMapWorkspace{SessionToken: "test-pdf-page-capability"}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	render := func(ctx context.Context, path string, page int, _ ingest.ToolConfig) ([]byte, error) {
		calls++
		if path == source || page != anchor.Page {
			t.Fatalf("renderer used unpinned coordinates: %q page %d", path, page)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing request deadline")
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, original) {
			t.Fatalf("snapshot: %v", err)
		}
		return []byte("verified image bytes"), nil
	}
	request := func(citation string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1/api/source?format=png&citation="+citation+"&page=999&path=ignored.pdf", nil)
		if authenticated {
			r.AddCookie(knowledgeMapSourceCookie(workspace.SessionToken))
		}
		w := httptest.NewRecorder()
		serveKnowledgeMapSourceWithRenderer(w, r, store, workspace, render)
		return w
	}
	if w := request(anchor.CitationID, false); w.Code != 403 {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	if w := request("cite-unknown", true); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	w := request(anchor.CitationID, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || w.Body.String() != "verified image bytes" {
		t.Fatalf("PNG response: %d %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(source, []byte("changed PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := request(anchor.CitationID, true); w.Code != 409 {
		t.Fatalf("changed source: %d", w.Code)
	}
	if calls != 1 {
		t.Fatalf("renderer called %d times", calls)
	}
}

func TestKnowledgeMapPNGFailureDoesNotHangOrReturnSuccess(t *testing.T) {
	store, anchor, _ := knowledgeMapPDFSourceFixture(t)
	defer store.Close()
	workspace := &KnowledgeMapWorkspace{SessionToken: "test-render-error"}
	r := httptest.NewRequest("GET", "http://127.0.0.1/api/source?format=png&citation="+anchor.CitationID, nil)
	r.AddCookie(knowledgeMapSourceCookie(workspace.SessionToken))
	w := httptest.NewRecorder()
	serveKnowledgeMapSourceWithRenderer(w, r, store, workspace, func(context.Context, string, int, ingest.ToolConfig) ([]byte, error) {
		return nil, errors.New("renderer unavailable")
	})
	if w.Code != 422 {
		t.Fatalf("renderer failure: %d %s", w.Code, w.Body.String())
	}
	knowledgeSourcePreviewSlots <- struct{}{}
	knowledgeSourcePreviewSlots <- struct{}{}
	defer func() { <-knowledgeSourcePreviewSlots; <-knowledgeSourcePreviewSlots }()
	w = httptest.NewRecorder()
	serveKnowledgeMapSourceWithRenderer(w, r, store, workspace, func(context.Context, string, int, ingest.ToolConfig) ([]byte, error) {
		t.Fatal("renderer exceeded concurrency limit")
		return nil, nil
	})
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("busy response: %d", w.Code)
	}
}
