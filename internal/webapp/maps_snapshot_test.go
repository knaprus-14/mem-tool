package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapSnapshotHTTPPreviewRestoreAndIsolation(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Snapshots", "")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", prefix, mem.WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	path := prefix + "/" + created.ID
	w = alice.call("GET", path+"/snapshots", nil)
	var page mem.WebMapSnapshotPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	snapshot := page.Items[0]
	previewPath := fmt.Sprintf("%s/snapshots/%d", path, snapshot.ID)
	w = alice.call("GET", previewPath+"?revision=1", nil)
	var preview mem.WebMapSnapshotPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Map.Title != "Root" || preview.Revision != 1 {
		t.Fatal(w.Code, w.Body)
	}
	for _, method := range []string{"GET", "POST"} {
		url := previewPath + "?revision=1"
		var body any
		if method == "POST" {
			url = previewPath + "/restore"
			body = mem.WebMapSnapshotRestore{Revision: 1, SnapshotDigest: snapshot.DocumentDigest, RequestID: strings.Repeat("b", 32)}
		}
		if got := bob.call(method, url, body); got.Code != 404 {
			t.Fatal("foreign", method, got.Code, got.Body)
		}
		if got := (&client{app: a}).call(method, url, body); got.Code != 401 {
			t.Fatal("anonymous", method, got.Code)
		}
	}
	in := mem.WebMapSnapshotRestore{Revision: 1, SnapshotDigest: snapshot.DocumentDigest, RequestID: strings.Repeat("b", 32)}
	csrf := alice.csrf
	alice.csrf = "wrong"
	w = alice.call("POST", previewPath+"/restore", in)
	alice.csrf = csrf
	if w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w = alice.call("POST", previewPath+"/restore", map[string]any{"revision": 1, "snapshot_digest": in.SnapshotDigest, "request_id": in.RequestID, "actor": "bob"})
	if w.Code != 400 {
		t.Fatal("unknown fields", w.Code, w.Body)
	}
	w = alice.call("GET", previewPath+"?revision=2", nil)
	if w.Code != 409 {
		t.Fatal("stale", w.Code, w.Body)
	}
	w = alice.call("GET", path+"/snapshots/999999?revision=1", nil)
	if w.Code != 404 {
		t.Fatal("missing", w.Code, w.Body)
	}
	wrong := in
	wrong.SnapshotDigest = "sha256:" + strings.Repeat("0", 64)
	w = alice.call("POST", previewPath+"/restore", wrong)
	if w.Code != 409 {
		t.Fatal("wrong digest", w.Code, w.Body)
	}
	w = alice.call("POST", previewPath+"/restore", in)
	var result mem.WebMapNodeEdited
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Revision != 2 {
		t.Fatal(w.Code, w.Body)
	}
	w = alice.call("POST", previewPath+"/restore", in)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Replayed {
		t.Fatal("retry", w.Code, w.Body)
	}
	w = alice.call("GET", previewPath+"?revision=2", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
