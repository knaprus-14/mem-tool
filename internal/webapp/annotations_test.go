package webapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type annotationHTTPFixture struct {
	app        *App
	alice, bob *client
	path, dir  string
	hit        mem.QueryHit
	create     mem.DocumentAnnotationCreate
}

func newAnnotationHTTPFixture(t *testing.T, text, quote string, start int) annotationHTTPFixture {
	t.Helper()
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		client  *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := u.client.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key}); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		u.client.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Annotations", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base.Root, "private-source.txt")
	chunk := mem.DocumentChunk{Text: text, Backend: "test", Embedding: []float32{1, 0}, TotalChunks: 1, Provenance: mem.Provenance{
		DocumentID: "doc-annotations-http", DocumentRevision: mem.ChunkContentHash(text), SourcePath: source,
		ChunkHash: mem.ChunkContentHash(text), MediaType: "text/plain", Page: 1, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	}}
	if err = s.ReplaceDocumentChunks(source, []mem.DocumentChunk{chunk}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	page, err := mem.ListWebMapCandidatesIn(context.Background(), dir, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	hit, err := mem.ReadQueryEvidenceIn(context.Background(), dir, page.Items[0].ID, page.Items[0].Pin)
	if err != nil {
		t.Fatal(err)
	}
	return annotationHTTPFixture{
		app: a, alice: alice, bob: bob, path: "/api/v1/workspaces/" + base.ID + "/annotations", dir: dir, hit: hit,
		create: mem.DocumentAnnotationCreate{EntryID: hit.ID, Pin: hit.Pin, Start: start, End: start + len([]rune(quote)), Quote: quote, Note: "Мой комментарий", Color: "yellow", RequestID: strings.Repeat("a", 32)},
	}
}

func readAnnotationHTTPResult(t *testing.T, w *httptest.ResponseRecorder) mem.DocumentAnnotationResult {
	t.Helper()
	var result mem.DocumentAnnotationResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Annotation.ID == "" {
		t.Fatalf("annotation result: %d %s", w.Code, w.Body)
	}
	return result
}

func TestAnnotationsHTTPAccessValidationAndCapacity(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Раздел 1: важное условие.", "важное условие", len([]rune("Раздел 1: ")))
	if w := f.alice.call("GET", f.path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("empty annotation list: %d %s", w.Code, w.Body)
	}
	created := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create))
	item := f.path + "/" + created.Annotation.ID
	edit := mem.DocumentAnnotationEdit{Revision: created.Annotation.Revision, Note: "Обновление", Color: "blue", RequestID: strings.Repeat("b", 32)}
	routes := []struct {
		method, path string
		body         any
	}{{"GET", f.path, nil}, {"GET", item, nil}, {"GET", item + "/history", nil}, {"POST", f.path, f.create}, {"POST", item + "/edit", edit}}
	for _, route := range routes {
		if w := f.bob.call(route.method, route.path, route.body); w.Code != 404 {
			t.Fatalf("foreign %s %s: %d %s", route.method, route.path, w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call(route.method, route.path, route.body); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", route.path, w.Code)
		}
		f.app.mu.Lock()
		f.app.queryBusy["alice"] = true
		f.app.mu.Unlock()
		w := f.alice.call(route.method, route.path, route.body)
		f.app.mu.Lock()
		delete(f.app.queryBusy, "alice")
		f.app.mu.Unlock()
		if w.Code != 429 {
			t.Fatalf("busy %s: %d %s", route.path, w.Code, w.Body)
		}
	}
	for _, route := range routes[3:] {
		csrf := f.alice.csrf
		f.alice.csrf = "invalid"
		w := f.alice.call(route.method, route.path, route.body)
		f.alice.csrf = csrf
		if w.Code != 403 {
			t.Fatalf("csrf %s: %d", route.path, w.Code)
		}
		if w = f.alice.call("POST", route.path, map[string]string{"actor": "bob", "path": "private"}); w.Code != 400 {
			t.Fatalf("unknown fields %s: %d %s", route.path, w.Code, w.Body)
		}
	}
	f.app.queries <- struct{}{}
	f.app.queries <- struct{}{}
	w := f.alice.call("GET", f.path, nil)
	<-f.app.queries
	<-f.app.queries
	if w.Code != 429 {
		t.Fatalf("process capacity: %d", w.Code)
	}
	for _, query := range []string{"?offset=-1", "?offset=1000001", "?offset=x", "?limit=0", "?limit=51", "?archived=invalid"} {
		if w := f.alice.call("GET", f.path+query, nil); w.Code != 400 {
			t.Fatalf("invalid list query %s: %d %s", query, w.Code, w.Body)
		}
	}
	for _, suffix := range []string{"?offset=-1", "?limit=51"} {
		if w := f.alice.call("GET", item+"/history"+suffix, nil); w.Code != 400 {
			t.Fatalf("invalid history: %d %s", w.Code, w.Body)
		}
	}
	if w = f.alice.call("GET", f.path+"/ann-"+strings.Repeat("f", 32), nil); w.Code != 404 {
		t.Fatalf("missing: %d %s", w.Code, w.Body)
	}
	if w = f.alice.call("GET", f.path+"/invalid", nil); w.Code != 400 {
		t.Fatalf("invalid ID: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{f.path, item, item + "/edit", item + "/history"} {
		if w := f.alice.call("DELETE", path, nil); w.Code != 405 {
			t.Fatalf("method %s: %d", path, w.Code)
		}
	}
	other, err := f.app.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Other annotations", "")
	if err != nil {
		t.Fatal(err)
	}
	otherItem := "/api/v1/workspaces/" + other.ID + "/annotations/" + created.Annotation.ID
	for _, path := range []string{otherItem, otherItem + "/history"} {
		if w := f.alice.call("GET", path, nil); w.Code != 404 {
			t.Fatalf("annotation crossed workspace boundary: %d %s", w.Code, w.Body)
		}
	}
}

func TestAnnotationsHTTPWorkflowRetryArchiveAndQuota(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Раздел 1: важное условие.", "важное условие", len([]rune("Раздел 1: ")))
	created := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create))
	if created.Replayed || created.Annotation.Quote != f.create.Quote || created.Annotation.Revision != 1 {
		t.Fatal(created)
	}
	if retry := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)); !retry.Replayed || retry.Annotation.ID != created.Annotation.ID {
		t.Fatal("create retry", retry)
	}
	conflict := f.create
	conflict.Note = "Другой запрос с прежним ключом"
	if w := f.alice.call("POST", f.path, conflict); w.Code != 409 {
		t.Fatalf("request conflict: %d %s", w.Code, w.Body)
	}
	stale := f.create
	stale.Pin = "sha256:" + strings.Repeat("0", 64)
	stale.RequestID = strings.Repeat("c", 32)
	if w := f.alice.call("POST", f.path, stale); w.Code != 409 {
		t.Fatalf("stale source: %d %s", w.Code, w.Body)
	}
	item := f.path + "/" + created.Annotation.ID
	edit := mem.DocumentAnnotationEdit{Revision: 1, Note: "Исправленный комментарий", Color: "pink", RequestID: strings.Repeat("d", 32)}
	updated := readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", edit))
	if updated.Annotation.Revision != 2 || updated.Annotation.Note != edit.Note || updated.Annotation.Color != "pink" || updated.Annotation.Quote != f.create.Quote {
		t.Fatal("edit", updated)
	}
	if retry := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)); !retry.Replayed || retry.Annotation.Revision != 1 || retry.Annotation.Note != f.create.Note {
		t.Fatal("late create retry must preserve the original result", retry)
	}
	if retry := readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", edit)); !retry.Replayed || retry.Annotation.Revision != 2 {
		t.Fatal("edit retry", retry)
	}
	edit.RequestID = strings.Repeat("e", 32)
	if w := f.alice.call("POST", item+"/edit", edit); w.Code != 409 {
		t.Fatalf("stale revision: %d %s", w.Code, w.Body)
	}
	w := f.alice.call("GET", item, nil)
	var annotation mem.DocumentAnnotation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &annotation) != nil || annotation.Note != updated.Annotation.Note {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{item, f.path, item + "/history"} {
		w = f.alice.call("GET", path, nil)
		privatePath, _ := json.Marshal(filepath.Dir(f.dir))
		if w.Code != 200 || strings.Contains(w.Body.String(), string(privatePath[1:len(privatePath)-1])) || strings.Contains(w.Body.String(), `"source_path"`) {
			t.Fatalf("private path exposed: %d %s", w.Code, w.Body)
		}
	}
	assertList := func(path string, total, count int) {
		t.Helper()
		w := f.alice.call("GET", path, nil)
		var page struct {
			Items []json.RawMessage `json:"items"`
			Total int               `json:"total"`
			Limit int               `json:"limit"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != total || len(page.Items) != count || page.Limit < 1 || page.Limit > 50 {
			t.Fatalf("list %s: %d %s", path, w.Code, w.Body)
		}
	}
	assertList(f.path+"?document_id="+f.hit.Document, 1, 1)
	assertList(f.path+"?document_id="+strings.Repeat("0", 64), 0, 0)
	assertList(f.path+"?offset=1&limit=1", 1, 0)
	assertList(item+"/history?offset=1&limit=1", 2, 1)
	archived := true
	archive := mem.DocumentAnnotationEdit{Revision: 2, Archived: &archived, RequestID: strings.Repeat("f", 32)}
	saved := readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", archive))
	if !saved.Annotation.Archived || saved.Annotation.Revision != 3 || saved.Annotation.Note != updated.Annotation.Note {
		t.Fatal("archive", saved)
	}
	assertList(f.path, 0, 0)
	assertList(f.path+"?archived=true", 1, 1)
	archived = false
	archive.Revision = 3
	archive.RequestID = strings.Repeat("1", 32)
	if restored := readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", archive)); restored.Annotation.Archived || restored.Annotation.Revision != 4 {
		t.Fatal("restore", restored)
	}
	edit.RequestID = strings.Repeat("d", 32)
	if retry := readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", edit)); !retry.Replayed || retry.Annotation.Revision != 2 || retry.Annotation.Note != edit.Note {
		t.Fatal("late edit retry must preserve the original result", retry)
	}
	w = f.alice.call("GET", item, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &annotation) != nil || annotation.Revision != 4 {
		t.Fatalf("retries changed the current version: %d %s", w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	assertList(f.path, 1, 1)
	assertList(item+"/history", 4, 4)
	if w = f.alice.call("GET", item, nil); w.Code != 200 {
		t.Fatalf("full quota read: %d %s", w.Code, w.Body)
	}
	for _, request := range []struct {
		path string
		body any
	}{{f.path, f.create}, {item + "/edit", edit}} {
		if w := f.alice.call("POST", request.path, request.body); w.Code != 507 {
			t.Fatalf("quota mutation: %d %s", w.Code, w.Body)
		}
	}
	assertList(item+"/history", 4, 4)
}

func TestAnnotationsHTTPAcceptsBoundedUnicodePayload(t *testing.T) {
	quote := strings.Repeat("😀", 8000)
	f := newAnnotationHTTPFixture(t, quote, quote, 0)
	f.create.Note = quote
	encoded, err := json.Marshal(f.create)
	if err != nil {
		t.Fatal(err)
	}
	// JSON permits surrogate escapes; the HTTP byte limit must also admit a
	// fully escaped representation of two valid 8000-rune strings.
	body := strings.ReplaceAll(string(encoded), "😀", `\ud83d\ude00`)
	if len(body) <= 64<<10 {
		t.Fatal("fixture must exercise the valid payload beyond 64KiB", len(body))
	}
	r := httptest.NewRequest("POST", f.app.cfg.PublicOrigin+f.path, strings.NewReader(body))
	r.AddCookie(f.alice.cookie)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", f.app.cfg.PublicOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Mem-CSRF", f.alice.csrf)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	result := readAnnotationHTTPResult(t, w)
	if result.Annotation.Quote != quote || result.Annotation.Note != quote {
		t.Fatal("Unicode payload changed")
	}
}
