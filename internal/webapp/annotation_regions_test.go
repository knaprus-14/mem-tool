package webapp

import (
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestAnnotationRegionsHTTPAccessValidationAndNoFalsePreview(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Annotation source text", "source", 11)
	annotation := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	path := f.path + "/" + annotation.ID
	for _, endpoint := range []string{path + "/regions", path + "/page?revision=1"} {
		if w := f.bob.call("GET", endpoint, nil); w.Code != 404 {
			t.Fatal(w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call("GET", endpoint, nil); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := f.alice.call("GET", path+"/regions", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Code, w.Body)
	}
	input := mem.DocumentAnnotationRegionCreate{Revision: 1, FileHash: "sha256:" + strings.Repeat("a", 64), X: .1, Y: .1, Width: .2, Height: .2, RequestID: strings.Repeat("b", 32)}
	csrf := f.alice.csrf
	f.alice.csrf = "bad"
	w := f.alice.call("POST", path+"/regions", input)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = f.alice.call("POST", path+"/regions", map[string]any{"file_path": "private.pdf"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = f.alice.call("POST", path+"/regions", input); w.Code != 409 {
		t.Fatal("non PDF source", w.Code, w.Body)
	}
	if w = f.alice.call("GET", path+"/page?revision=1", nil); w.Code != 409 {
		t.Fatal("false successful preview", w.Code, w.Body)
	}
	if w = f.alice.call("GET", path+"/page", nil); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}
