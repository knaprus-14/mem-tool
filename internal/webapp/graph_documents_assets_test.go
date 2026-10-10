package webapp

import (
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGraphDocumentsAssetsServed(t *testing.T) {
	a := testApp(t, true)
	c := &client{app: a}
	c.session(t)
	page := c.call("GET", "/", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `src="/assets/graph-documents.js"`) || !strings.Contains(page.Body.String(), `id="document-graph"`) {
		t.Fatal(page.Code, "graph entry points missing")
	}
	script := c.call("GET", "/assets/graph-documents.js", nil)
	if script.Code != 200 || !strings.Contains(script.Header().Get("Content-Type"), "javascript") || !strings.Contains(script.Body.String(), "installMemGraphDocuments") {
		t.Fatal(script.Code, "graph module not served")
	}
}

func TestGraphMissingAnswerModelReturnsActionableBadRequest(t *testing.T) {
	a := testApp(t, true)
	w := httptest.NewRecorder()
	a.maintenanceError(w, mem.ErrWebGraphAnswerModel)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Настройки базы") {
		t.Fatal(w.Code, w.Body)
	}
}
