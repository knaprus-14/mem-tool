package webapp

import (
	"encoding/json"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapViewPresetsHTTPAccessCASAndQuota(t *testing.T) {
	f := newMapViewHTTPFixture(t)
	f.path = strings.TrimSuffix(f.path, "view-state") + "view-presets"
	in := mem.WebMapViewPresetInput{WebMapViewStateInput: f.input, Operation: "create", ID: "mvp-" + strings.Repeat("b", 32), Name: "Вопросы"}
	for _, method := range []string{"GET", "POST"} {
		if w := f.bob.call(method, f.path, in); w.Code != 404 {
			t.Fatal("foreign", w.Code, w.Body)
		}
		if w := (&client{app: f.app}).call(method, f.path, in); w.Code != 401 {
			t.Fatal("anonymous", w.Code)
		}
		if w := f.alice.call(method, f.path+"?actor=bob", in); w.Code != 400 {
			t.Fatal("query", w.Code)
		}
	}
	old := f.alice.csrf
	f.alice.csrf = "bad"
	w := f.alice.call("POST", f.path, in)
	f.alice.csrf = old
	if w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if w = f.alice.call("PUT", f.path, in); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w = f.alice.call("GET", f.path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = f.alice.call("POST", f.path, in); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	f.app.cfg.MaxWorkspaceBytes = 1
	f.app.cfg.MaxUserBytes = 1
	if w = f.alice.call("POST", f.path, in); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("quota replay", w.Code, w.Body)
	}
	if w = f.alice.call("GET", f.path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Вопросы") {
		t.Fatal(w.Code, w.Body)
	}
	in.RequestID = strings.Repeat("c", 32)
	in.Operation = "rename"
	in.Name = "Other"
	if w = f.alice.call("POST", f.path, in); w.Code != 409 {
		t.Fatal("CAS", w.Code, w.Body)
	}
	in.Revision = 1
	if w = f.alice.call("POST", f.path, in); w.Code != 507 {
		t.Fatal("quota", w.Code, w.Body)
	}
	in.Operation = "delete"
	in.Name = ""
	if w = f.alice.call("POST", f.path, in); w.Code != 200 {
		t.Fatal("delete at quota", w.Code, w.Body)
	}
	if w = f.alice.call("GET", f.path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatal(w.Code, w.Body)
	}
}
func TestMapViewPresetsHTTPStrictCompleteJSONAndBounds(t *testing.T) {
	f := newMapViewHTTPFixture(t)
	f.path = strings.TrimSuffix(f.path, "view-state") + "view-presets"
	input := mem.WebMapViewPresetInput{WebMapViewStateInput: f.input, Operation: "create", ID: "mvp-" + strings.Repeat("b", 32), Name: "Вопросы"}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	post := func(raw string, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", f.app.cfg.PublicOrigin+f.path, strings.NewReader(raw))
		r.AddCookie(f.alice.cookie)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", f.app.cfg.PublicOrigin)
		r.Header.Set("X-Mem-CSRF", f.alice.csrf)
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		return w
	}
	cases := []string{
		strings.Replace(valid, `"operation":"create"`, `"Operation":"create"`, 1),
		strings.Replace(valid, `"name":"Вопросы"`, `"name":"Вопросы","name":"Other"`, 1),
		strings.Replace(valid, `"operation":"create",`, "", 1),
		strings.Replace(valid, `"revision":0`, `"revision":0,"revision":0`, 1), strings.Replace(valid, `"revision":0`, `"Revision":0`, 1), strings.Replace(valid, `"revision":0`, `"revision":0,"actor":"bob"`, 1),
		strings.Replace(valid, `"scale":0.25`, `"scale":0.25,"scale":1`, 1), strings.Replace(valid, `"table_descending":true`, `"table_descending":null`, 1), strings.Replace(valid, `"table_descending":true,`, "", 1),
		strings.Replace(valid, `"table_query":"источник 😀"`, `"table_query":"\ud800"`, 1), strings.Replace(valid, `"table_query":"источник 😀"`, `"table_query":"\u0000"`, 1), strings.Replace(valid, "источник 😀", string([]byte{0xff}), 1),
		strings.Replace(valid, `"scale":0.25`, `"scale":1e999`, 1), strings.Replace(valid, `"scale":0.25`, `"scale":0.049`, 1), strings.Replace(valid, `"table_page":4`, `"table_page":1000`, 1), valid + ` {}`, `null`,
	}
	for i, raw := range cases {
		if w := post(raw, "application/json"); w.Code != 400 {
			t.Fatalf("case%d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := post(valid, "text/plain"); w.Code != 415 {
		t.Fatal("content type", w.Code, w.Body)
	}
	if w := post(strings.Repeat(" ", (256<<10)+1), "application/json"); w.Code != 413 {
		t.Fatal("raw body limit", w.Code, w.Body)
	}
	// An escaped supplementary rune is valid and retains its exact text.
	escaped := strings.ReplaceAll(valid, "😀", `\ud83d\ude00`)
	if w := post(escaped, "application/json; charset=utf-8"); w.Code != 200 {
		t.Fatal("valid Unicode escapes", w.Code, w.Body)
	}
}
