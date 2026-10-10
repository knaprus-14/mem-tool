package webapp

import (
	"encoding/json"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestAnnotationReanchorHTTPPreviewApplyLineageAndIsolation(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "First old; second new 😀.", "old", 6)
	a := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	p := f.path + "/" + a.ID
	input := mem.DocumentAnnotationReanchorPreviewInput{Revision: a.Revision, EntryID: f.hit.ID, Pin: f.hit.Pin, Start: 18, End: 23, Quote: "new 😀"}
	for _, route := range []struct{ method, suffix string }{{"POST", "/reanchor-preview"}, {"POST", "/reanchor"}, {"GET", "/lineage"}} {
		if w := f.bob.call(route.method, p+route.suffix, input); w.Code != 404 {
			t.Fatal("foreign", w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call(route.method, p+route.suffix, input); w.Code != 401 {
			t.Fatal("anonymous", w.Code)
		}
		if route.method == "POST" {
			csrf := f.alice.csrf
			f.alice.csrf = "bad"
			w := f.alice.call("POST", p+route.suffix, input)
			f.alice.csrf = csrf
			if w.Code != 403 {
				t.Fatal("csrf", w.Code)
			}
			if w = f.alice.call("POST", p+route.suffix, map[string]string{"actor": "bob"}); w.Code != 400 {
				t.Fatal("unknownfields", w.Code)
			}
		}
	}
	w := f.alice.call("POST", p+"/reanchor-preview", input)
	var preview mem.DocumentAnnotationReanchorPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Old.ID != a.ID || preview.Target.Quote != input.Quote {
		t.Fatal("preview", w.Code, w.Body)
	}
	apply := mem.DocumentAnnotationReanchorInput{Revision: input.Revision, EntryID: input.EntryID, Pin: input.Pin, Start: input.Start, End: input.End, Quote: input.Quote, Digest: preview.Digest, RequestID: strings.Repeat("d", 32)}
	bad := apply
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	if w = f.alice.call("POST", p+"/reanchor", bad); w.Code != 409 {
		t.Fatal("digest", w.Code, w.Body)
	}
	w = f.alice.call("POST", p+"/reanchor", apply)
	var result mem.DocumentAnnotationReanchorResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Annotation.ID == a.ID || result.Annotation.Quote != input.Quote || result.Replayed {
		t.Fatal("apply", w.Code, w.Body)
	}
	w = f.alice.call("POST", p+"/reanchor", apply)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Replayed {
		t.Fatal("replay", w.Code, w.Body)
	}
	for _, id := range []string{a.ID, result.Annotation.ID} {
		w = f.alice.call("GET", f.path+"/"+id+"/lineage", nil)
		var history mem.DocumentAnnotationLineagePage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &history) != nil || history.Total != 1 || history.Items[0].OldID != a.ID || history.Items[0].NewID != result.Annotation.ID {
			t.Fatal("lineage", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), f.dir) {
			t.Fatal("private path")
		}
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	if w = f.alice.call("POST", p+"/reanchor-preview", input); w.Code != 200 {
		t.Fatal("preview atcapacity", w.Code, w.Body)
	}
	apply.RequestID = strings.Repeat("e", 32)
	if w = f.alice.call("POST", p+"/reanchor", apply); w.Code != 507 {
		t.Fatal("mutation atcapacity", w.Code, w.Body)
	}
}
