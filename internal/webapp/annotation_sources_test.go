package webapp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func annotationHTTPMap(t *testing.T, f annotationHTTPFixture) (mem.WebMapCreated, string, string) {
	t.Helper()
	created, err := mem.CreateWebMapIn(context.Background(), f.dir, "alice", mem.WebMapCreate{Title: "Annotation target", RequestID: strings.Repeat("b", 32)})
	if err != nil {
		t.Fatal(err)
	}
	view, err := mem.ReadWebMapIn(context.Background(), f.dir, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(f.path, "/annotations") + "/maps/" + created.ID
	return created, view.Map.RootNodeID, prefix
}

func TestAnnotationSourcesHTTPAccessAttachAndUndo(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Раздел 1: важное условие.", "важное условие", len([]rune("Раздел 1: ")))
	annotation := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	item := f.path + "/" + annotation.ID
	entry := f.path + "/entry/" + strconv.FormatInt(f.hit.ID, 10)
	created, nodeID, mapPath := annotationHTTPMap(t, f)
	attachPath := mapPath + "/nodes/" + nodeID + "/annotations"
	input := mem.WebMapAnnotationAttach{Revision: 1, AnnotationID: annotation.ID, AnnotationRevision: 1, RequestID: strings.Repeat("c", 32)}
	for _, path := range []string{entry, item + "/source", item + "/file?revision=1", item + "/links"} {
		if w := f.bob.call("GET", path, nil); w.Code != 404 {
			t.Fatalf("foreign %s: %d %s", path, w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call("GET", path, nil); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
		if w := f.alice.call("POST", path, map[string]string{}); w.Code != 405 {
			t.Fatalf("method %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := f.bob.call("POST", attachPath, input); w.Code != 404 {
		t.Fatal("foreign attach", w.Code, w.Body)
	}
	if w := (&client{app: f.app}).call("POST", attachPath, input); w.Code != 401 {
		t.Fatal("anonymous attach", w.Code, w.Body)
	}
	csrf := f.alice.csrf
	f.alice.csrf = "bad"
	w := f.alice.call("POST", attachPath, input)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("attach csrf", w.Code)
	}
	if w = f.alice.call("POST", attachPath, map[string]any{"actor": "bob", "path": "C:/private"}); w.Code != 400 {
		t.Fatal("attach unknown fields", w.Code, w.Body)
	}
	if w = f.alice.call("GET", attachPath, nil); w.Code != 405 {
		t.Fatal("attach method", w.Code)
	}
	w = f.alice.call("GET", entry+"?pin="+f.hit.Pin, nil)
	var hit mem.QueryHit
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &hit) != nil || hit.Pin != f.hit.Pin || hit.Text != f.hit.Text {
		t.Fatal("entry", w.Code, w.Body)
	}
	for _, suffix := range []string{"?pin=bad", "?pin=sha256:" + strings.Repeat("0", 64)} {
		want := 400
		if strings.Contains(suffix, "sha256") {
			want = 409
		}
		if w = f.alice.call("GET", entry+suffix, nil); w.Code != want {
			t.Fatal("entry pin", w.Code, w.Body)
		}
	}
	w = f.alice.call("GET", item+"/source", nil)
	var source mem.WebMapDocument
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &source) != nil || source.State != "current" || source.Excerpt != f.create.Quote || len(source.Passages) != 1 || source.Passages[0].Pin != hit.Pin {
		t.Fatal("source", w.Code, w.Body)
	}
	privatePath, _ := json.Marshal(filepath.Dir(f.dir))
	if strings.Contains(w.Body.String(), string(privatePath[1:len(privatePath)-1])) {
		t.Fatal("source exposed private path", w.Body)
	}
	if w = f.alice.call("GET", item+"/file?revision=1", nil); w.Code != 409 {
		t.Fatal("non-PDF source served as PDF", w.Code)
	}
	checkLinks := func(total int) {
		t.Helper()
		w := f.alice.call("GET", item+"/links", nil)
		var links mem.DocumentAnnotationLinkPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &links) != nil || links.Total != total || len(links.Items) != total {
			t.Fatal("links", w.Code, w.Body)
		}
		if total == 1 && (links.Items[0].MapID != created.ID || links.Items[0].NodeID != nodeID) {
			t.Fatal("wrong link target", links)
		}
	}
	checkLinks(0)
	w = f.alice.call("POST", attachPath, input)
	var attached mem.WebMapNodeEdited
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &attached) != nil || attached.Revision != 2 {
		t.Fatal("attach", w.Code, w.Body)
	}
	if w = f.alice.call("POST", attachPath, input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("attach replay", w.Code, w.Body)
	}
	checkLinks(1)
	history, err := mem.ListWebMapHistoryIn(context.Background(), f.dir, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	w = f.alice.call("POST", mapPath+"/undo", mem.WebMapUndo{Revision: 2, ChangeID: history.UndoID, RequestID: strings.Repeat("d", 32)})
	if w.Code != 200 {
		t.Fatal("undo", w.Code, w.Body)
	}
	checkLinks(0)
	if w = f.alice.call("POST", attachPath, input); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("late attach retry", w.Code, w.Body)
	}
	checkLinks(0)
	history, err = mem.ListWebMapHistoryIn(context.Background(), f.dir, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	w = f.alice.call("POST", mapPath+"/redo", mem.WebMapUndo{Revision: 3, ChangeID: history.RedoID, RequestID: strings.Repeat("e", 32)})
	if w.Code != 200 {
		t.Fatal("redo", w.Code, w.Body)
	}
	checkLinks(1)
	archived := true
	readAnnotationHTTPResult(t, f.alice.call("POST", item+"/edit", mem.DocumentAnnotationEdit{Revision: 1, Archived: &archived, RequestID: strings.Repeat("f", 32)}))
	input.Revision, input.AnnotationRevision, input.RequestID = 4, 2, strings.Repeat("1", 32)
	if w = f.alice.call("POST", attachPath, input); w.Code != 409 {
		t.Fatal("archived annotation attached", w.Code, w.Body)
	}
	input.AnnotationRevision = 1
	input.RequestID = strings.Repeat("2", 32)
	if w = f.alice.call("POST", attachPath, input); w.Code != 409 {
		t.Fatal("stale annotation revision attached", w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes, f.app.cfg.MaxUserBytes = 1, 1
	for _, path := range []string{entry, item + "/source", item + "/links"} {
		if w = f.alice.call("GET", path, nil); w.Code != 200 {
			t.Fatal("quota read", path, w.Code, w.Body)
		}
	}
	if w = f.alice.call("POST", attachPath, input); w.Code != 507 {
		t.Fatal("quota attach", w.Code, w.Body)
	}
}

func TestAnnotationEntryHTTPLongVersionedChunkKeepsSelectionPin(t *testing.T) {
	text := strings.Repeat("а", 17000)
	f := newAnnotationHTTPFixture(t, text, text[:20], 0)
	w := f.alice.call("GET", f.path+"/entry/"+strconv.FormatInt(f.hit.ID, 10), nil)
	var hit mem.QueryHit
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &hit) != nil || hit.Pin != f.hit.Pin {
		t.Fatalf("a long versioned chunk must retain its pin for short selections: %d pin=%q", w.Code, hit.Pin)
	}
	f.create.Pin = hit.Pin
	readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create))
}

func TestAnnotationAttachExistingEvidenceDoesNotLeaveUntrackedLink(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "Exact quote", "Exact quote", 0)
	annotation := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	created, nodeID, mapPath := annotationHTTPMap(t, f)
	if _, err := mem.ActWebMapSourceIn(context.Background(), f.dir, "alice", created.ID, nodeID, "add", mem.WebMapSourceInput{Revision: 1, EntryID: f.hit.ID, Pin: f.hit.Pin, RequestID: strings.Repeat("c", 32)}); err != nil {
		t.Fatal(err)
	}
	w := f.alice.call("POST", mapPath+"/nodes/"+nodeID+"/annotations", mem.WebMapAnnotationAttach{Revision: 2, AnnotationID: annotation.ID, AnnotationRevision: 1, RequestID: strings.Repeat("d", 32)})
	if w.Code != 409 {
		t.Fatal("pre-existing evidence must reject attachment without an untracked link", w.Code, w.Body)
	}
	w = f.alice.call("GET", f.path+"/"+annotation.ID+"/links", nil)
	var links mem.DocumentAnnotationLinkPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &links) != nil || links.Total != 0 {
		t.Fatal("an undone or rejected attach retained an annotation link", w.Code, w.Body)
	}
	sources, err := mem.ListWebMapSourcesIn(context.Background(), f.dir, created.ID, nodeID)
	if err != nil || len(sources.Items) != 1 {
		t.Fatal("pre-existing source was changed", sources, err)
	}
	view, err := mem.ReadWebMapIn(context.Background(), f.dir, created.ID)
	if err != nil || view.Map.Revision != 2 {
		t.Fatal("rejected attachment changed map revision", view.Map.Revision, err)
	}
}
