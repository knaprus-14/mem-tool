package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type client struct {
	app    *App
	cookie *http.Cookie
	csrf   string
}

func (c *client) call(method, path string, body any) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, c.app.cfg.PublicOrigin+path, &b)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != "GET" {
		r.Header.Set("Origin", c.app.cfg.PublicOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Mem-CSRF", c.csrf)
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.app.ServeHTTP(w, r)
	for _, cookie := range w.Result().Cookies() {
		c.cookie = cookie
	}
	return w
}
func (c *client) session(t *testing.T) {
	t.Helper()
	w := c.call("GET", "/api/v1/session", nil)
	var s struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || !s.Authenticated {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	c.csrf = s.CSRF
}
func testApp(t *testing.T, local bool) *App {
	t.Helper()
	m, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	c := Config{Local: local, Listen: "127.0.0.1:8765", PublicOrigin: "http://127.0.0.1:8765"}
	if !local {
		t.Setenv("MEM_TEST_ALICE_KEY", strings.Repeat("a", 40))
		t.Setenv("MEM_TEST_BOB_KEY", strings.Repeat("b", 40))
		c.Users = []User{{ID: "alice", TokenEnv: "MEM_TEST_ALICE_KEY"}, {ID: "bob", TokenEnv: "MEM_TEST_BOB_KEY"}}
	}
	a, err := New(c, m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestLocalCreateOpenAndReadWithCSRFAndHostProtection(t *testing.T) {
	a := testApp(t, true)
	c := &client{app: a}
	c.session(t)
	if w := c.call("GET", "/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Библиотека") {
		t.Fatalf("shell: %d", w.Code)
	}
	if w := c.call("POST", "/api/v1/workspaces", map[string]string{"name": "Рабочая база"}); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w := c.call("GET", "/api/v1/workspaces", nil)
	var list []struct{ ID, Path string }
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Path == "" {
		t.Fatalf("list: %s", w.Body)
	}
	if w = c.call("GET", "/api/v1/workspaces/"+list[0].ID+"/library", nil); w.Code != 200 {
		t.Fatalf("library: %d %s", w.Code, w.Body)
	}
	csrf := c.csrf
	c.csrf = "wrong"
	if w = c.call("POST", "/api/v1/workspaces", map[string]string{"name": "blocked"}); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	c.csrf = csrf
	for _, modify := range []func(*http.Request){func(r *http.Request) { r.Host = "evil.example" }, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }} {
		r := httptest.NewRequest("GET", a.cfg.PublicOrigin+"/api/v1/workspaces", nil)
		r.AddCookie(c.cookie)
		modify(r)
		w = httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross-site request accepted: %d", w.Code)
		}
	}
	if w = c.call("POST", "/api/v1/workspaces/open", map[string]string{"path": list[0].Path}); w.Code != 200 {
		t.Fatalf("open: %d %s", w.Code, w.Body)
	}
}

func TestServerIsolatesUsersDocumentsSettingsAndLocalPaths(t *testing.T) {
	a := testApp(t, false)
	alice, bob := &client{app: a}, &client{app: a}
	if w := alice.call("GET", "/api/v1/workspaces", nil); w.Code != 401 {
		t.Fatal("anonymous access")
	}
	for _, item := range []struct {
		c           *client
		user, token string
	}{{alice, "alice", strings.Repeat("a", 40)}, {bob, "bob", strings.Repeat("b", 40)}} {
		if w := item.c.call("POST", "/api/v1/login", map[string]string{"user": item.user, "token": item.token}); w.Code != 200 {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		item.c.session(t)
	}
	w := alice.call("POST", "/api/v1/workspaces", map[string]string{"name": "Private"})
	if w.Code != 201 {
		t.Fatalf("create: %s", w.Body)
	}
	var created struct {
		ID   string
		Path string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Path != "" {
		t.Fatal("server path leaked")
	}
	owned, err := a.manager.Resolve(context.Background(), workspace.Actor{ID: "alice"}, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mem.LoadConfigIn(filepath.Join(owned.Root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Polza.APIKey = "do-not-expose-this-secret"
	if err = mem.SaveConfigIn(filepath.Join(owned.Root, ".mem"), cfg); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"library", "settings", "entries/1"} {
		if w = bob.call("GET", "/api/v1/workspaces/"+created.ID+"/"+suffix, nil); w.Code != 404 {
			t.Fatalf("cross-user %s: %d %s", suffix, w.Code, w.Body)
		}
	}
	if w = alice.call("GET", "/api/v1/workspaces/"+created.ID+"/settings", nil); w.Code != 200 || strings.Contains(w.Body.String(), cfg.Polza.APIKey) || strings.Contains(w.Body.String(), "api_key") {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	if w = bob.call("GET", "/api/v1/workspaces", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("other user's list: %s", w.Body)
	}
	if w = bob.call("POST", "/api/v1/workspaces/open", map[string]string{"path": owned.Root}); w.Code != 403 {
		t.Fatal("server attach enabled")
	}
	if w = bob.call("POST", "/api/v1/workspaces", map[string]string{"name": "escape", "path": t.TempDir()}); w.Code != 400 {
		t.Fatal("server arbitrary path enabled")
	}
	if w = alice.call("POST", "/api/v1/logout", map[string]string{}); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = alice.call("GET", "/api/v1/workspaces", nil); w.Code != 401 {
		t.Fatal("logout retained authorization")
	}
}

func TestServerConfigFailsClosedBeforeDataCreation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "server.json")
	for _, body := range []string{
		`{"data_dir":"data","public_origin":"https://mem.example.org"}`,
		`{"data_dir":"data","listen":"0.0.0.0:8765","public_origin":"https://mem.example.org"}`,
		`{"data_dir":"data","public_origin":"http://public.example.org"}`,
		`{"data_dir":"data","public_origin":"https://mem.example.org","unknown":true}`,
		`{"data_dir":"data","public_origin":"https://mem.example.org"} {}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("accepted config %s", body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
		t.Fatalf("invalid config created data: %v", err)
	}
}

func TestLoginRateLimitAndSecureSession(t *testing.T) {
	a := testApp(t, false)
	a.cfg.PublicOrigin = "https://mem.example.org"
	c := &client{app: a}
	if w := c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": strings.Repeat("a", 40)}); w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	if !c.cookie.HttpOnly || !c.cookie.Secure || c.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie protections: %+v", c.cookie)
	}
	for range 9 {
		c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": "incorrect"})
	}
	if w := c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": "incorrect"}); w.Code != 429 {
		t.Fatalf("unlimited login attempts: %d", w.Code)
	}
}

func TestMapLayoutAssetIsServedWithScriptType(t *testing.T) {
	a := testApp(t, true)
	c := &client{app: a}
	w := c.call("GET", "/assets/map-layout.js", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(w.Body.String(), "MemMapLayout") {
		t.Fatalf("layout asset: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	w = c.call("GET", "/", nil)
	if strings.Index(w.Body.String(), "/assets/map-layout.js") > strings.Index(w.Body.String(), "/assets/app.js") {
		t.Fatal("layout dependency loads after app")
	}
}

func TestAllPageScriptsAreEmbeddedAndServed(t *testing.T) {
	c := &client{app: testApp(t, true)}
	page := c.call("GET", "/", nil)
	scripts := regexp.MustCompile(`<script src="([^"]+)"`).FindAllStringSubmatch(page.Body.String(), -1)
	if len(scripts) == 0 {
		t.Fatal("no scripts in fixture page")
	}
	for _, script := range scripts {
		path := script[1]
		w := c.call("GET", path, nil)
		expected, err := assets.ReadFile(strings.TrimPrefix(path, "/"))
		if err != nil || w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.String() != string(expected) {
			t.Fatalf("script %s missing or not served correctly: %d %v", path, w.Code, err)
		}
	}
}
