package webapp

import "testing"

func TestClientEventsRequireSessionCSRFAndBoundedMetadata(t *testing.T) {
	a := testApp(t, false)
	c := &client{app: a}
	body := map[string]any{"kind": "error", "asset": "app.js", "line": 12, "column": 4}
	if w := c.call("POST", "/api/v1/client-events", body); w.Code != 401 {
		t.Fatal(w.Code)
	}
	c.call("POST", "/api/v1/login", map[string]string{"user": "alice", "token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	c.session(t)
	csrf := c.csrf
	c.csrf = "bad"
	if w := c.call("POST", "/api/v1/client-events", body); w.Code != 403 {
		t.Fatal(w.Code)
	}
	c.csrf = csrf
	if w := c.call("POST", "/api/v1/client-events", map[string]any{"kind": "error", "message": "private document"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := c.call("POST", "/api/v1/client-events", map[string]any{"kind": "error", "asset": "../../private.js"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := c.call("POST", "/api/v1/client-events", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := c.call("POST", "/api/v1/client-events", body); w.Code != 429 {
		t.Fatal(w.Code)
	}
}
