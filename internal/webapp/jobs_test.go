package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/webjobs"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (c *client) upload(t *testing.T, id, name, text string) *httptest.ResponseRecorder {
	return c.uploadFields(t, id+"/uploads", name, text, map[string]string{"metadata": `{"tags":["web-test"]}`})
}

func (c *client) uploadFields(t *testing.T, route, name, text string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	f, err := form.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(f, text)
	for key, value := range fields {
		form.WriteField(key, value)
	}
	form.Close()
	r := httptest.NewRequest("POST", c.app.cfg.PublicOrigin+"/api/v1/workspaces/"+route, &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Origin", c.app.cfg.PublicOrigin)
	r.Header.Set("X-Mem-CSRF", c.csrf)
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.app.ServeHTTP(w, r)
	return w
}

func TestHTTPUploadImportDuplicateAndJobActions(t *testing.T) {
	a := testApp(t, true)
	c := &client{app: a}
	c.session(t)
	created := c.call("POST", "/api/v1/workspaces", map[string]string{"name": "Web upload"})
	var w workspace.Workspace
	if err := json.Unmarshal(created.Body.Bytes(), &w); err != nil {
		t.Fatal(err)
	}
	base, err := a.manager.Resolve(context.Background(), workspace.Actor{ID: "local"}, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Listing jobs on an untouched base must remain read-only.
	if res := c.call("GET", "/api/v1/workspaces/"+w.ID+"/jobs", nil); res.Code != 200 || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatalf("empty jobs: %d %s", res.Code, res.Body)
	}
	if _, err = os.Stat(filepath.Join(base.Root, ".mem", "web-jobs.db")); !os.IsNotExist(err) {
		t.Fatalf("GET created a queue: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, `{"embedding":[1,0.5,0.2]}`) }))
	defer server.Close()
	cfg, err := mem.LoadConfigIn(filepath.Join(base.Root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ollama.BaseURL = server.URL
	if err = mem.SaveConfigIn(filepath.Join(base.Root, ".mem"), cfg); err != nil {
		t.Fatal(err)
	}
	res := c.upload(t, w.ID, "Пример.md", "<!-- page: 1 -->\n\n# Web upload\n\nТекст первого документа.")
	if res.Code != 202 {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}
	var accepted struct {
		Job       webjobs.Job
		Duplicate bool
	}
	if err = json.Unmarshal(res.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	jobPath := "/api/v1/workspaces/" + w.ID + "/jobs/" + accepted.Job.ID
	if res = c.call("POST", jobPath+"/cancel", map[string]any{}); res.Code != 200 || !strings.Contains(res.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("queued cancel: %d %s", res.Code, res.Body)
	}
	if res = c.call("POST", jobPath+"/retry", map[string]any{}); res.Code != 200 {
		t.Fatalf("retry: %s", res.Body)
	}
	if worked, err := a.jobs.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("import: %t %v", worked, err)
	}
	if res = c.call("GET", jobPath, nil); res.Code != 200 || !strings.Contains(res.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("job: %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), base.Root) || strings.Contains(res.Body.String(), "relative_path") || strings.Contains(res.Body.String(), "run_id") {
		t.Fatalf("internal data in job: %s", res.Body)
	}
	if res = c.call("GET", "/api/v1/workspaces/"+w.ID+"/library", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "Пример.md") {
		t.Fatalf("library: %d %s", res.Code, res.Body)
	}
	res = c.upload(t, w.ID, "Пример.md", "<!-- page: 1 -->\n\n# Web upload\n\nТекст первого документа.")
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate: %d %s", res.Code, res.Body)
	}
	csrf := c.csrf
	c.csrf = "bad"
	if res = c.upload(t, w.ID, "blocked.md", "blocked"); res.Code != 403 {
		t.Fatalf("upload CSRF: %d", res.Code)
	}
	c.csrf = csrf
	if res = c.upload(t, w.ID, "blocked.exe", "blocked"); res.Code != 400 {
		t.Fatalf("extension: %d %s", res.Code, res.Body)
	}
	if res = c.call("GET", jobPath+"/history", nil); res.Code != 200 || !strings.Contains(res.Body.String(), `"has_report":true`) {
		t.Fatalf("history: %d %s", res.Code, res.Body)
	}
	if res = c.call("GET", jobPath+"/attempts/2/pages?after=0", nil); res.Code != 200 || !strings.Contains(res.Body.String(), `"document_updated":true`) {
		t.Fatalf("pages: %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "run_id") || strings.Contains(res.Body.String(), "source_path") || strings.Contains(res.Body.String(), base.Root) {
		t.Fatalf("private report: %s", res.Body)
	}
	for _, tail := range []string{"/attempts/1/pages", "/attempts/999/pages"} {
		if res = c.call("GET", jobPath+tail, nil); res.Code == 200 {
			t.Fatalf("unowned attempt: %s", res.Body)
		}
	}
	for _, tail := range []string{"/attempts/2/pages?after=-1", "/attempts/2/pages?after=x", "/attempts/2/pages?run_id=1", "/attempts/x/pages"} {
		if res = c.call("GET", jobPath+tail, nil); res.Code != 400 {
			t.Fatalf("invalid report: %d %s", res.Code, res.Body)
		}
	}
	// Revision preview exposes the opaque state pin, never its physical path.
	revisionRoute := w.ID + "/revisions/" + accepted.Job.DocumentKey
	res = c.call("GET", "/api/v1/workspaces/"+revisionRoute, nil)
	var pin webjobs.Revision
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &pin) != nil || pin.State == "" {
		t.Fatalf("preview: %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "source_path") || strings.Contains(res.Body.String(), base.Root) {
		t.Fatalf("path leak: %s", res.Body)
	}
	fields := map[string]string{"metadata": `{"relative_path":"Курс/Новая.md","tags":["updated"]}`, "state": pin.State}
	res = c.uploadFields(t, revisionRoute, "Новая.md", "New HTTP revision.", fields)
	if res.Code != 202 {
		t.Fatalf("revision upload: %d %s", res.Code, res.Body)
	}
	if worked, err := a.jobs.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("revision process: %t %v", worked, err)
	}
	res = c.call("GET", "/api/v1/workspaces/"+w.ID+"/library", nil)
	var library workspace.Library
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &library) != nil || library.DocumentCount != 1 || library.Documents[0].Name != "Новая.md" || library.Documents[0].ManagedID != pin.DocumentKey {
		t.Fatalf("updated library: %d %s", res.Code, res.Body)
	}
	if res = c.uploadFields(t, revisionRoute, "Новая.md", "Stale revision.", fields); res.Code != 409 {
		t.Fatalf("stale HTTP state: %d %s", res.Code, res.Body)
	}
	c.csrf = "bad"
	if res = c.uploadFields(t, revisionRoute, "Новая.md", "Blocked.", fields); res.Code != 403 {
		t.Fatalf("revision CSRF: %d", res.Code)
	}
	c.csrf = csrf
	if res = c.uploadFields(t, w.ID+"/uploads", "a.md", "Bad label.", map[string]string{"metadata": `{"relative_path":"../a.md"}`}); res.Code != 400 {
		t.Fatalf("folder traversal: %d %s", res.Code, res.Body)
	}
}

func TestServerJobsRejectCrossOwnerAndAnonymousRequests(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, x := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if res := x.c.call("POST", "/api/v1/login", map[string]string{"user": x.id, "token": x.key}); res.Code != 200 {
			t.Fatal(res.Body)
		}
		x.c.session(t)
	}
	res := alice.call("POST", "/api/v1/workspaces", map[string]string{"name": "Private uploads"})
	var w workspace.Workspace
	if err := json.Unmarshal(res.Body.Bytes(), &w); err != nil {
		t.Fatal(err)
	}
	res = alice.upload(t, w.ID, "private.md", "private document")
	var upload struct{ Job webjobs.Job }
	if res.Code != 202 || json.Unmarshal(res.Body.Bytes(), &upload) != nil {
		t.Fatalf("upload: %s", res.Body)
	}
	for _, path := range []string{"/jobs", "/jobs/" + upload.Job.ID, "/jobs/" + upload.Job.ID + "/history", "/jobs/" + upload.Job.ID + "/attempts/1/pages", "/revisions/" + upload.Job.DocumentKey} {
		if res = bob.call("GET", "/api/v1/workspaces/"+w.ID+path, nil); res.Code != 404 {
			t.Fatalf("cross-owner read: %d", res.Code)
		}
	}
	for _, action := range []string{"cancel", "retry"} {
		if res = bob.call("POST", "/api/v1/workspaces/"+w.ID+"/jobs/"+upload.Job.ID+"/"+action, map[string]any{}); res.Code != 404 {
			t.Fatalf("cross-owner mutation: %d", res.Code)
		}
	}
	if res = bob.upload(t, w.ID, "intrusion.md", "intrusion"); res.Code != 404 {
		t.Fatalf("cross-owner upload: %d", res.Code)
	}
	if res = bob.uploadFields(t, w.ID+"/revisions/"+upload.Job.DocumentKey, "intrusion.md", "intrusion", nil); res.Code != 404 {
		t.Fatalf("cross-owner revision: %d", res.Code)
	}
	if res = (&client{app: a}).uploadFields(t, w.ID+"/revisions/"+upload.Job.DocumentKey, "anonymous.md", "anonymous", nil); res.Code != 401 {
		t.Fatalf("anonymous revision: %d", res.Code)
	}
	if res = (&client{app: a}).upload(t, w.ID, "anonymous.md", "anonymous"); res.Code != 401 {
		t.Fatalf("anonymous upload: %d", res.Code)
	}
}
