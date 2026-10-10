package webapp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMaintenanceQuotaPreservesReadsAndOwnership(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	for _, u := range []struct {
		c       *client
		id, key string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := u.c.call("POST", "/api/v1/login", map[string]string{"user": u.id, "token": u.key}); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		u.c.session(t)
	}
	base, e := a.manager.Create(context.Background(), workspace.Actor{ID: "alice"}, "quota", "")
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/workspaces/" + base.ID
	a.cfg.MaxWorkspaceBytes = 1
	a.cfg.MaxUserBytes = 1
	for _, suffix := range []string{"/storage", "/maintenance"} {
		if w := bob.call("GET", path+suffix, nil); w.Code != 404 {
			t.Fatal(suffix, w.Code)
		}
	}
	if w := alice.call("GET", path+"/storage", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := alice.call("POST", path+"/maintenance/coverage", map[string]any{}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := alice.call("POST", path+"/maintenance", map[string]any{}); w.Code != 507 {
		t.Fatal(w.Code, w.Body)
	}
	if w := alice.call("POST", "/api/v1/workspaces", map[string]string{"name": "over quota"}); w.Code != 507 {
		t.Fatal(w.Code, w.Body)
	}
	// A map snapshot is a write, unlike a document-history report.
	for _, p := range []string{path + "/maps/map1/snapshots", path + "/maintenance/merge"} {
		if !storageWriteRequest(httptest.NewRequest("POST", p, nil)) {
			t.Fatal("write exemption", p)
		}
	}
	alice.csrf = "wrong"
	if w := alice.call("POST", path+"/maintenance/coverage", map[string]any{}); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestPortableModelPolicy(t *testing.T) {
	a := testApp(t, false)
	cfg := mem.DefaultLocalConfig()
	settings := workspace.SettingsFromConfig(cfg)
	if a.validatePortableModels(cfg) == nil {
		t.Fatal("unapproved archive model accepted")
	}
	a.cfg.Models = []ModelChoice{{Role: "embedding", Backend: settings.Backend, Model: settings.EmbeddingModel}}
	if settings.AnswerModel != "" {
		a.cfg.Models = append(a.cfg.Models, ModelChoice{Role: "answer", Backend: "ollama", Model: settings.AnswerModel})
	}
	if e := a.validatePortableModels(cfg); e != nil {
		t.Fatal(e)
	}
	cfg.Answer.Model = "unapproved"
	if a.validatePortableModels(cfg) == nil {
		t.Fatal("unapproved answer model accepted")
	}
	a.cfg.Local = true
	if e := a.validatePortableModels(cfg); e != nil {
		t.Fatal(e)
	}
}

func TestMaintenanceApprovalRequiresReviewedContent(t *testing.T) {
	a := testApp(t, true)
	base, err := a.manager.Create(context.Background(), workspace.Actor{ID: "local"}, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	c := &client{app: a}
	c.session(t)
	w := c.call("POST", "/api/v1/workspaces/"+base.ID+"/maintenance/approve-batch", map[string]any{
		"manifest": []map[string]string{{"object_type": "node", "id": "old-plan", "reviewer": "reviewer", "expected_evidence_digest": "sha256:old-plan"}},
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "expected_review_digest") {
		t.Fatal("legacy plan did not receive actionable validation error:", w.Code, w.Body)
	}
}
