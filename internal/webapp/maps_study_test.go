package webapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWebMapStudyHTTPIsolationAndWorkflow(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key})
		u.c.session(t)
	}
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "Study", "")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/workspaces/" + base.ID + "/maps"
	w := alice.call("POST", prefix, mem.WebMapCreate{Title: "Study", TemplateID: "study", RequestID: strings.Repeat("a", 32)})
	var created mem.WebMapCreated
	if w.Code != 200 && w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := prefix + "/" + created.ID
	w = alice.call("GET", path, nil)
	var view mem.WebMapView
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	root := path + "/nodes/" + view.Map.RootNodeID
	for _, suffix := range []string{"/study", "/study-review"} {
		url := root + suffix
		if got := bob.call("POST", url, map[string]any{}); got.Code != 404 {
			t.Fatal(got.Code)
		}
		if got := (&client{app: a}).call("POST", url, map[string]any{}); got.Code != 401 {
			t.Fatal(got.Code)
		}
		csrf := alice.csrf
		alice.csrf = "invalid"
		got := alice.call("POST", url, map[string]any{})
		alice.csrf = csrf
		if got.Code != 403 {
			t.Fatal(got.Code)
		}
		if got = alice.call("POST", url, map[string]any{"actor": "bob"}); got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	w = alice.call("POST", root+"/study", map[string]any{"revision": 1})
	var study mem.WebMapStudyView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &study) != nil || len(study.Study.Cards) == 0 {
		t.Fatal(w.Code, w.Body)
	}
	in := mem.WebMapStudyReview{Revision: 1, Manifest: study.Manifest.Digest, CardID: study.Study.Cards[0].ID, RequestID: strings.Repeat("c", 32), Grade: mem.KnowledgeLearningGradeGood}
	w = alice.call("POST", root+"/study-review", in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = alice.call("POST", root+"/study", map[string]any{"revision": 1})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &study) != nil || study.Progress[in.CardID].Repetitions != 1 {
		t.Fatal(w.Code, w.Body)
	}
}
