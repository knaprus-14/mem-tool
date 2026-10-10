package webapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestAnnotationCandidatesHTTPAccessStrictParamsAndQuota(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "😀 До цитаты. Условие. Ещё: Условие.", "Условие", 13)
	a := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	path := f.path + "/" + a.ID + "/reanchor-candidates"
	valid := path + "?revision=1"
	for _, who := range []struct {
		client *client
		status int
	}{{f.bob, 404}, {&client{app: f.app}, 401}} {
		if w := who.client.call("GET", valid, nil); w.Code != who.status {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, query := range []string{"", "?revision=0", "?revision=-1", "?revision=x", "?revision=9223372036854775808", "?revision=1&limit=0", "?revision=1&limit=51", "?revision=1&limit=", "?revision=1&actor=bob", "?revision=1&quote=other", "?revision=1&revision=1", "?revision=1&limit=1&limit=1", "?revision=1&%FF%ZZ=bad", "?revision=1;limit=1"} {
		if w := f.alice.call("GET", path+query, nil); w.Code != 400 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	if w := f.alice.call("GET", path+"?revision=2", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.alice.call("POST", valid, nil); w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal(w.Code, w.Body)
	}
	csrf := f.alice.csrf
	f.alice.csrf = "invalid"
	w := f.alice.call("POST", valid, nil)
	f.alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("csrf", w.Code, w.Body)
	}
	req := httptest.NewRequest("GET", f.app.cfg.PublicOrigin+valid, nil)
	req.AddCookie(f.alice.cookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	f.app.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-site GET", w.Code, w.Body)
	}
	f.app.mu.Lock()
	f.app.queryBusy["alice"] = true
	f.app.mu.Unlock()
	w = f.alice.call("GET", valid, nil)
	f.app.mu.Lock()
	delete(f.app.queryBusy, "alice")
	f.app.mu.Unlock()
	if w.Code != 429 {
		t.Fatal("user gate", w.Code, w.Body)
	}
	f.app.queries <- struct{}{}
	f.app.queries <- struct{}{}
	w = f.alice.call("GET", valid, nil)
	<-f.app.queries
	<-f.app.queries
	if w.Code != 429 {
		t.Fatal("process gate", w.Code, w.Body)
	}
	other, err := f.app.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Other candidates", "")
	if err != nil {
		t.Fatal(err)
	}
	if w = f.alice.call("GET", "/api/v1/workspaces/"+other.ID+"/annotations/"+a.ID+"/reanchor-candidates?revision=1", nil); w.Code != 404 {
		t.Fatal("workspace boundary", w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	w = f.alice.call("GET", valid, nil)
	var out mem.DocumentAnnotationCandidates
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Complete || len(out.Items) != 2 || out.Limit != 20 || out.Items[0].Start != a.Start || out.Items[0].Pin != f.hit.Pin {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "private-source") && strings.Contains(w.Body.String(), "source_path") || strings.Contains(w.Body.String(), f.dir) {
		t.Fatal("raw path leak", w.Body)
	}
	w = f.alice.call("GET", valid+"&limit=1", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Complete || len(out.Items) != 1 || len(out.TruncatedReasons) != 1 || out.TruncatedReasons[0] != "candidate_limit" {
		t.Fatal(w.Code, w.Body)
	}
	var history mem.DocumentAnnotationHistory
	w = f.alice.call("GET", f.path+"/"+a.ID+"/history", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &history) != nil || history.Total != 1 {
		t.Fatal("search wrote history", w.Code, w.Body)
	}
}

func TestAnnotationCandidatesHTTPExplicitSelectionUsesExistingPreview(t *testing.T) {
	f := newAnnotationHTTPFixture(t, "😀 слово / слово", "слово", 2)
	a := readAnnotationHTTPResult(t, f.alice.call("POST", f.path, f.create)).Annotation
	path := f.path + "/" + a.ID
	w := f.alice.call("GET", path+"/reanchor-candidates?revision=1&limit=50", nil)
	var out mem.DocumentAnnotationCandidates
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 2 {
		t.Fatal(w.Code, w.Body)
	}
	c := out.Items[1]
	in := mem.DocumentAnnotationReanchorPreviewInput{Revision: a.Revision, EntryID: c.EntryID, Pin: c.Pin, Start: c.Start, End: c.End, Quote: c.Quote}
	w = f.alice.call("POST", path+"/reanchor-preview", in)
	var preview mem.DocumentAnnotationReanchorPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Target.Start != 10 {
		t.Fatal(w.Code, w.Body)
	}
	apply := mem.DocumentAnnotationReanchorInput{Revision: in.Revision, EntryID: in.EntryID, Pin: in.Pin, Start: in.Start, End: in.End, Quote: in.Quote, Digest: preview.Digest, RequestID: strings.Repeat("e", 32)}
	w = f.alice.call("POST", path+"/reanchor", apply)
	var result mem.DocumentAnnotationReanchorResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Annotation.Start != 10 || result.Annotation.ID == a.ID || result.Annotation.State != mem.EvidenceCurrent {
		t.Fatal(w.Code, w.Body)
	}
}
