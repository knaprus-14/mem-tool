package webapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMapDocumentHTTPComparisonAndVerifiedOriginal(t *testing.T) {
	a := testApp(t, false)
	c, bob, anonymous := &client{app: a}, &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c         *client
		name, key string
	}{{c, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.name, "token": u.key})
		u.c.session(t)
	}
	ctx := context.Background()
	base, err := a.manager.Create(ctx, workspace.Actor{ID: "alice"}, "Compare", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, ".mem")
	created, err := mem.CreateWebMapIn(ctx, dir, "alice", mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	view, err := mem.ReadWebMapIn(ctx, dir, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base.Root, "source.pdf")
	content := "%PDF-1.4\nHTTP fixture\n%%EOF"
	if err = os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chunk := mem.DocumentChunk{Text: "Section 4: grounded evidence", Backend: "test", Embedding: []float32{1, 0}, TotalChunks: 1, Provenance: mem.Provenance{
		DocumentID: "doc-http", DocumentRevision: mem.ChunkContentHash("revision"), SourcePath: path, SourceFileHash: mem.ChunkContentHash(content),
		ChunkHash: mem.ChunkContentHash("Section 4: grounded evidence"), MediaType: "application/pdf", Page: 4, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	}}
	if err = s.ReplaceDocumentChunks(path, []mem.DocumentChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	page, err := mem.ListWebMapCandidatesIn(ctx, dir, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	_, err = mem.ActWebMapSourceIn(ctx, dir, "alice", created.ID, view.Map.RootNodeID, "add", mem.WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("b", 32), EntryID: page.Items[0].ID, Pin: page.Items[0].Pin})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := mem.ListWebMapSourcesIn(ctx, dir, created.ID, view.Map.RootNodeID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/maps/" + created.ID + "/nodes/" + view.Map.RootNodeID + "/sources/" + sources.Items[0].ID
	for _, operation := range []string{"document", "file"} {
		url := prefix + "/" + operation + "?revision=2"
		if w := bob.call("GET", url, nil); w.Code != 404 {
			t.Fatal("owner isolation", w.Code)
		}
		if w := anonymous.call("GET", url, nil); w.Code != 401 {
			t.Fatal("anonymous access", w.Code)
		}
		if w := c.call("GET", prefix+"/"+operation, nil); w.Code < 400 {
			t.Fatal("missing revision", w.Code)
		}
	}
	w := c.call("GET", prefix+"/document?revision=2", nil)
	var result mem.WebMapDocument
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Page != 4 || !result.OriginalPDF || len(result.Passages) != 1 || !result.Passages[0].Match {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), base.Root) {
		t.Fatal("private path leaked", w.Body)
	}
	if w = c.call("GET", prefix+"/document?revision=1", nil); w.Code != 409 {
		t.Fatal("stale map", w.Code)
	}
	w = c.call("GET", prefix+"/file?revision=2", nil)
	if w.Code != 200 || w.Body.String() != content || w.Header().Get("Content-Type") != "application/pdf" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	w = c.call("HEAD", prefix+"/file?revision=2", nil)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", a.cfg.PublicOrigin+prefix+"/file?revision=2", nil)
	r.AddCookie(c.cookie)
	r.Header.Set("Range", "bytes=0-4")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "%PDF-" {
		t.Fatal("PDF range", w.Code, w.Body)
	}
	if err = os.WriteFile(path, []byte("%PDF-1.4 changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if w = c.call("GET", prefix+"/file?revision=2", nil); w.Code != 409 || strings.Contains(w.Body.String(), base.Root) {
		t.Fatal("changed original", w.Code, w.Body)
	}
}
