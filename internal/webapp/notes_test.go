package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestNotesHTTPAuthorizesValidatesAndPreservesRetry(t *testing.T) {
	a := testApp(t, false)
	alice := &client{app: a}
	bob := &client{app: a}
	for _, user := range []struct {
		c         *client
		id, token string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := user.c.call("POST", "/api/v1/login", map[string]string{"user": user.id, "token": user.token}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		user.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Notes", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, ".mem")
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"embedding":[1,0]}`)) }))
	defer provider.Close()
	cfg.Ollama.BaseURL = provider.URL
	if err = mem.SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/notes"
	text := "Fixture note"
	body := mem.NoteInput{RequestID: strings.Repeat("a", 32), Text: &text, Title: "Note", Tags: []string{"tag"}, Important: true}
	if w := bob.call("POST", path, body); w.Code != 404 {
		t.Fatalf("foreign: %d", w.Code)
	}
	if w := (&client{app: a}).call("POST", path, body); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w := alice.call("POST", path, body); w.Code != 403 {
		t.Fatalf("CSRF: %d", w.Code)
	}
	alice.csrf = csrf
	if w := alice.call("POST", path, map[string]string{"path": "secret", "model": "bad"}); w.Code != 400 {
		t.Fatalf("unknown fields: %d", w.Code)
	}
	a.queryBusy["alice"] = true
	if w := alice.call("POST", path, body); w.Code != 429 {
		t.Fatalf("busy: %d", w.Code)
	}
	delete(a.queryBusy, "alice")
	a.queries <- struct{}{}
	a.queries <- struct{}{}
	if w := alice.call("POST", path, body); w.Code != 429 {
		t.Fatalf("capacity: %d", w.Code)
	}
	<-a.queries
	<-a.queries
	w := alice.call("POST", path, body)
	var result mem.NoteWriteResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID < 1 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = alice.call("POST", path, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("retry: %s", w.Body)
	}
	notePath := path + "/1"
	if w = bob.call("GET", notePath, nil); w.Code != 404 {
		t.Fatal("foreign note read")
	}
	if w = alice.call("DELETE", notePath, nil); w.Code != 405 {
		t.Fatal("delete allowed")
	}
	w = alice.call("GET", notePath, nil)
	var note mem.NoteView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &note) != nil || note.Text != text || !strings.HasPrefix(note.Pin, "sha256:") || len(note.Pin) != 71 {
		t.Fatalf("read: %s", w.Body)
	}
	for _, secret := range []string{base.Root, "base_url", "embedding_space"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("internal field exposed")
		}
	}
	body.ID = note.ID
	body.Pin = note.Pin
	body.RequestID = strings.Repeat("b", 32)
	body.Text = nil
	body.Title = "Metadata changed"
	if w = alice.call("POST", path, body); w.Code != 200 {
		t.Fatalf("metadata: %s", w.Body)
	}
	body.RequestID = strings.Repeat("c", 32)
	if w = alice.call("POST", path, body); w.Code != 409 {
		t.Fatalf("stale edit: %d", w.Code)
	}
}

func TestNoteHistoryHTTP(t *testing.T) {
	a := testApp(t, false)
	alice := &client{app: a}
	bob := &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "History", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base.Root, ".mem")
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err := s.Add("private fixture note", "Before", []string{"saved"}, "ollama", []float32{1, 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	n, err := mem.ReadNoteIn(context.Background(), dir, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + base.ID + "/notes"
	id := strconv.FormatInt(e.ID, 10)
	action := mem.NoteAction{Action: "delete", ID: e.ID, Pin: n.Pin, RequestID: strings.Repeat("c", 32)}
	for _, tail := range []string{"/trash", "/" + id + "/history", "/" + id + "/history/1"} {
		if w := bob.call("GET", path+tail, nil); w.Code != 404 {
			t.Fatalf("foreign %s: %d", tail, w.Code)
		}
		if w := (&client{app: a}).call("GET", path+tail, nil); w.Code != 401 {
			t.Fatalf("anonymous: %d", w.Code)
		}
	}
	if w := bob.call("POST", path+"/actions", action); w.Code != 404 {
		t.Fatal("foreign mutation allowed")
	}
	csrf := alice.csrf
	alice.csrf = "bad"
	if w := alice.call("POST", path+"/actions", action); w.Code != 403 {
		t.Fatal("CSRF allowed")
	}
	alice.csrf = csrf
	if w := alice.call("POST", path+"/actions", map[string]any{"action": "delete", "path": "private"}); w.Code != 400 {
		t.Fatal("unknown field allowed")
	}
	if w := alice.call("GET", path+"/trash?offset=-1", nil); w.Code != 400 {
		t.Fatal("negative offset")
	}
	a.queryBusy["alice"] = true
	if w := alice.call("POST", path+"/actions", action); w.Code != 429 {
		t.Fatal("busy action allowed")
	}
	delete(a.queryBusy, "alice")
	if w := alice.call("POST", path+"/actions", action); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := alice.call("GET", path+"/"+id, nil); w.Code != 404 {
		t.Fatal("deleted readable")
	}
	w := alice.call("GET", path+"/trash", nil)
	var trash mem.NoteHistoryPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &trash) != nil || trash.Total != 1 {
		t.Fatalf("trash: %s", w.Body)
	}
	version := trash.Items[0]
	versionPath := path + "/" + id + "/history/" + strconv.FormatInt(version.VersionID, 10)
	w = alice.call("GET", versionPath, nil)
	var preview mem.NoteVersionView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Note.Text != n.Text {
		t.Fatalf("preview: %s", w.Body)
	}
	for _, secret := range []string{dir, "vector", "embedding", "backend"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("snapshot internals exposed")
		}
	}
	action = mem.NoteAction{Action: "restore", ID: e.ID, VersionID: version.VersionID, VersionPin: version.Pin, RequestID: strings.Repeat("d", 32)}
	if w = alice.call("POST", path+"/actions", action); w.Code != 200 {
		t.Fatalf("restore: %s", w.Body)
	}
	if w = alice.call("POST", path+"/actions", action); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("replay: %s", w.Body)
	}
	action.RequestID = strings.Repeat("e", 32)
	if w = alice.call("POST", path+"/actions", action); w.Code != 409 {
		t.Fatal("stale trash allowed")
	}
	if w = alice.call("GET", path+"/"+id+"/history", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("history: %s", w.Body)
	}
}
