package webapp

import (
	"encoding/json"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestAnnotationTransferHTTPPrivacyPreviewAndReplay(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Text: quotation 😀.", "quotation 😀", 6)
	a := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	export := mem.DocumentAnnotationExportRequest{IDs: []string{a.ID}, Format: "json"}
	for _, route := range []string{"export", "import-preview", "import"} {
		p := f.path + "/" + route
		if w := f.bob.call("POST", p, export); w.Code != 404 {
			t.Fatal("foreign", route, w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call("POST", p, export); w.Code != 401 {
			t.Fatal("anonymous", route, w.Code)
		}
		if w := f.alice.call("GET", p, nil); w.Code != 405 {
			t.Fatal("method", route, w.Code)
		}
		csrf := f.alice.csrf
		f.alice.csrf = "bad"
		w := f.alice.call("POST", p, export)
		f.alice.csrf = csrf
		if w.Code != 403 {
			t.Fatal("csrf", route, w.Code)
		}
		if w = f.alice.call("POST", p, map[string]string{"private_path": "secret"}); w.Code != 400 {
			t.Fatal("strict input", route, w.Code)
		}
	}
	w := f.alice.call("POST", f.path+"/export", export)
	var artifact struct{ Filename, MediaType, Content, Digest string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &artifact) != nil || artifact.Content == "" || artifact.Filename == "" {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(artifact.Content, f.dir) || strings.Contains(artifact.Content, "source_path") || strings.Contains(artifact.Content, "entry_id") {
		t.Fatal("private locators leaked")
	}
	previewInput := mem.DocumentAnnotationImportPreviewInput{Content: artifact.Content}
	w = f.alice.call("POST", f.path+"/import-preview", previewInput)
	var preview mem.DocumentAnnotationImportPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Current != 1 || preview.Count != 1 {
		t.Fatal(w.Code, w.Body)
	}
	input := mem.DocumentAnnotationImportInput{Content: artifact.Content, Digest: preview.Digest, RequestID: strings.Repeat("f", 32)}
	bad := input
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	if w = f.alice.call("POST", f.path+"/import", bad); w.Code != 409 {
		t.Fatal("stale preview", w.Code, w.Body)
	}
	w = f.alice.call("POST", f.path+"/import", input)
	var result mem.DocumentAnnotationImportResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Imported != 1 || result.Items[0].ID == a.ID || result.Replayed {
		t.Fatal("import", w.Code, w.Body)
	}
	w = f.alice.call("POST", f.path+"/import", input)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Replayed {
		t.Fatal("retry", w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	for _, route := range []struct {
		name  string
		input any
	}{{"export", export}, {"import-preview", previewInput}} {
		if w = f.alice.call("POST", f.path+"/"+route.name, route.input); w.Code != 200 {
			t.Fatal("read at capacity", route.name, w.Code, w.Body)
		}
	}
	input.RequestID = strings.Repeat("e", 32)
	if w = f.alice.call("POST", f.path+"/import", input); w.Code != 507 {
		t.Fatal("write at capacity", w.Code, w.Body)
	}
}
