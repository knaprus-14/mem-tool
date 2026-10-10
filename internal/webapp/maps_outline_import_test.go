package webapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapOutlineImportHTTPAccessArchiveAndRetryAtQuota(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c         *client
		name, key string
	}{{alice, "alice", "a"}, {bob, "bob", "b"}} {
		if w := u.c.call("POST", "/api/v1/login", map[string]string{"user": u.name, "token": strings.Repeat(u.key, 40)}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "External import", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	path := "/api/v1/workspaces/" + base.ID + "/maps/"
	raw := []byte("\ufeff# Карта\r\n- <script>alert(1)</script>\r\nТекст")
	input := mem.OutlineImportInput{Format: "markdown", Filename: "Карта.md", ContentBase64: base64.StdEncoding.EncodeToString(raw)}
	for _, suffix := range []string{"outline-preview", "outline-apply"} {
		if w := bob.call("POST", path+"import/"+suffix, input); w.Code != 404 {
			t.Fatal("foreign", w.Code)
		}
		if w := (&client{app: a}).call("POST", path+"import/"+suffix, input); w.Code != 401 {
			t.Fatal("anonymous", w.Code)
		}
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w := alice.call("POST", path+"import/outline-preview", input); w.Code != 403 {
		t.Fatal(w.Code)
	}
	alice.csrf = csrf
	if w := alice.call("GET", path+"import/outline-preview", nil); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := alice.call("POST", path+"import/outline-preview?actor=bob", input); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := alice.call("POST", path+"import/outline-preview", map[string]string{"format": "markdown", "filename": "map.md", "content_base64": input.ContentBase64, "extra": "bad"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	limit := a.cfg.MaxWorkspaceBytes
	a.cfg.MaxWorkspaceBytes = 1
	w := alice.call("POST", path+"import/outline-preview", input)
	var preview mem.OutlineImportPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.NodeCount != 2 || preview.SourceBytes != len(raw) {
		t.Fatal(w.Code, w.Body)
	}
	apply := mem.OutlineImportApplyInput{OutlineImportInput: input, Digest: preview.Digest, RequestID: strings.Repeat("d", 32)}
	if w = alice.call("POST", path+"import/outline-apply", apply); w.Code != 507 {
		t.Fatal("quota", w.Code, w.Body)
	}
	a.cfg.MaxWorkspaceBytes = limit
	bad := apply
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	if w = alice.call("POST", path+"import/outline-apply", bad); w.Code != 409 {
		t.Fatal("preview mismatch", w.Code, w.Body)
	}
	w = alice.call("POST", path+"import/outline-apply", apply)
	var result mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" {
		t.Fatal(w.Code, w.Body)
	}
	a.cfg.MaxWorkspaceBytes = 1
	if w = alice.call("POST", path+"import/outline-apply", apply); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("replay at quota", w.Code, w.Body)
	}
	download := path + result.ID + "/import-source"
	w = alice.call("GET", download, nil)
	typ, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if w.Code != 200 || err != nil || typ != "attachment" || params["filename"] != input.Filename || !bytes.Equal(w.Body.Bytes(), raw) || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Header(), w.Body, err)
	}
	if w = bob.call("GET", download, nil); w.Code != 404 {
		t.Fatal("foreign archive", w.Code)
	}
	if w = (&client{app: a}).call("GET", download, nil); w.Code != 401 {
		t.Fatal("anonymous archive", w.Code)
	}
	if w = alice.call("GET", download+"?path=C:/private", nil); w.Code != 400 {
		t.Fatal("archive query", w.Code)
	}
	a.cfg.MaxWorkspaceBytes = limit
	if w = alice.call("POST", download, nil); w.Code != 405 {
		t.Fatal("archive method", w.Code)
	}
	w = alice.call("GET", path+result.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_import_source":true`) {
		t.Fatal("archive discoverability", w.Code, w.Body)
	}
	page, err := mem.ListWebMapsIn(context.Background(), dir, 0, true)
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
}
