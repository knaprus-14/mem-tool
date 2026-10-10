package webapp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapImportHTTPRoundTripAccessPreviewAndQuota(t *testing.T) {
	a := testApp(t, false)
	alice := &client{app: a}
	bob := &client{app: a}
	for _, user := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := user.c.call("POST", "/api/v1/login", map[string]string{"user": user.id, "token": user.key}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		user.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Import", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	original, err := mem.CreateWebMapIn(context.Background(), dir, "alice", mem.WebMapCreate{Title: "Оригинал", TemplateID: "study", RequestID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := mem.ExportWebMapIn(context.Background(), dir, original.ID, mem.ClassicMindMapExportJSON, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/maps/import/"
	input := mem.WebMapImportPreviewInput{Content: string(artifact.Data)}
	for _, suffix := range []string{"preview", "apply"} {
		if w := bob.call("POST", path+suffix, input); w.Code != 404 {
			t.Fatal("foreign", w.Code)
		}
		if w := (&client{app: a}).call("POST", path+suffix, input); w.Code != 401 {
			t.Fatal("anonymous", w.Code)
		}
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w := alice.call("POST", path+"preview", input); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	alice.csrf = csrf
	if w := alice.call("POST", path+"preview", map[string]string{"content": input.Content, "path": "C:/private"}); w.Code != 400 {
		t.Fatal("unknown field", w.Code)
	}
	if w := alice.call("GET", path+"preview", nil); w.Code != 405 {
		t.Fatal("method", w.Code)
	}
	if w := alice.call("POST", path+"preview", mem.WebMapImportPreviewInput{Content: `{"export_version":1,"export_version":1}`}); w.Code != 400 {
		t.Fatal("invalid", w.Code)
	}
	// A full workspace may inspect an import; applying still needs capacity.
	limit := a.cfg.MaxWorkspaceBytes
	a.cfg.MaxWorkspaceBytes = 1
	w := alice.call("POST", path+"preview", input)
	var preview mem.ClassicMindMapImportPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Title != "Оригинал" || preview.NodeCount < 2 {
		t.Fatal(w.Code, w.Body)
	}
	apply := mem.WebMapImportInput{Content: input.Content, Digest: preview.Digest, RequestID: strings.Repeat("c", 32)}
	if w = alice.call("POST", path+"apply", apply); w.Code == 200 {
		t.Fatal("quota apply succeeded")
	}
	a.cfg.MaxWorkspaceBytes = limit
	bad := apply
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	if w = alice.call("POST", path+"apply", bad); w.Code != 409 {
		t.Fatal("preview pin", w.Code, w.Body)
	}
	w = alice.call("POST", path+"apply", apply)
	var result mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == original.ID || result.ID == "" {
		t.Fatal(w.Code, w.Body)
	}
	w = alice.call("POST", path+"apply", apply)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Code, w.Body)
	}
	list, err := mem.ListWebMapsIn(context.Background(), dir, 0, true)
	if err != nil || list.Total != 2 {
		t.Fatal(list, err)
	}
	if w = alice.call("GET", "/assets/map-import.js", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "installMemMapImport") {
		t.Fatal("missing embedded script", w.Code)
	}
}
