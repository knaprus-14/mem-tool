package mem

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKnowledgeMapLiveHandlerRendersCurrentGraphWithSecurityHeaders(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "live-first", Kind: KnowledgeNodeClaim, Label: "Первый узел",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	handler := NewKnowledgeMapLiveHandler(store, "Живая карта")

	first := requestKnowledgeMap(t, handler, http.MethodGet, "/", "127.0.0.1:8765")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Первый узел") ||
		!strings.Contains(first.Body.String(), "live-mark") || !strings.Contains(first.Body.String(), "ОБНОВИТЬ") {
		t.Fatalf("live map response is incomplete: status=%d body=%q", first.Code, first.Body.String())
	}
	for _, name := range []string{"Cache-Control", "Content-Security-Policy", "Cross-Origin-Resource-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if first.Header().Get(name) == "" {
			t.Errorf("live map response is missing %s", name)
		}
	}
	if !strings.Contains(first.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal("live map CSP does not allow its same-origin layout API")
	}

	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "live-second", Kind: KnowledgeNodeTopic, Label: "Новый узел после запуска",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	second := requestKnowledgeMap(t, handler, http.MethodGet, "/", "localhost:8765")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "Новый узел после запуска") {
		t.Fatalf("live map did not reload the current graph: status=%d body=%q", second.Code, second.Body.String())
	}
}

func TestKnowledgeMapLiveHandlerRejectsUnsafeRequests(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	handler := NewKnowledgeMapLiveHandler(store, "")

	if got := requestKnowledgeMap(t, handler, http.MethodGet, "/", "attacker.example"); got.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host was accepted: %d", got.Code)
	}
	if got := requestKnowledgeMap(t, handler, http.MethodPost, "/", "127.0.0.1:8765"); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("write method was accepted: status=%d allow=%q", got.Code, got.Header().Get("Allow"))
	}
	if got := requestKnowledgeMap(t, handler, http.MethodGet, "/missing", "[::1]:8765"); got.Code != http.StatusNotFound {
		t.Fatalf("unknown live-map route was accepted: %d", got.Code)
	}
	if got := requestKnowledgeMap(t, NewKnowledgeMapLiveHandler(nil, ""), http.MethodGet, "/", "127.0.0.1"); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store did not fail closed: %d", got.Code)
	}
	if got := requestKnowledgeMap(t, handler, http.MethodPut, "/api/layout", "127.0.0.1:8765"); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed layout writes: %d", got.Code)
	}
}

func TestKnowledgeMapWorkspacePersistsAndProtectsLayout(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "workspace-node", Kind: KnowledgeNodeClaim, Label: "Закреплённый узел",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	const token = "test-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)

	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), token) || !strings.Contains(page.Body.String(), "resetLayoutBtn") {
		t.Fatalf("workspace page is incomplete: status=%d", page.Code)
	}
	layout := KnowledgeMapLayout{
		Version: KnowledgeMapLayoutVersion,
		Nodes: map[string]KnowledgeMapNodePosition{
			"workspace-node": {X: 45, Y: -12, Pinned: true},
		},
		Viewport: KnowledgeMapViewport{Scale: 1.5, X: 7, Y: 9},
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string][2]string{
		"missing origin": {"", token},
		"wrong token":    {"http://" + host, "wrong"},
	} {
		t.Run(name, func(t *testing.T) {
			got := requestKnowledgeMapLayout(t, handler, http.MethodPut, host, request[0], request[1], raw)
			if got.Code != http.StatusForbidden {
				t.Fatalf("unsafe workspace request was accepted: %d", got.Code)
			}
		})
	}
	saved := requestKnowledgeMapLayout(t, handler, http.MethodPut, host, "http://"+host, token, raw)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"updated"`) {
		t.Fatalf("valid layout was not saved: status=%d body=%q", saved.Code, saved.Body.String())
	}
	loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView)
	if err != nil || loaded == nil || loaded.Nodes["workspace-node"].X != 45 {
		t.Fatalf("saved layout is unavailable: layout=%#v err=%v", loaded, err)
	}
	page = requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"scale":1.5`) {
		t.Fatalf("saved layout was not embedded in refreshed page: status=%d", page.Code)
	}
	unknown := bytes.Replace(raw, []byte("workspace-node"), []byte("unknown-node"), 1)
	if got := requestKnowledgeMapLayout(t, handler, http.MethodPut, host, "http://"+host, token, unknown); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown node layout was accepted: %d", got.Code)
	}
	deleted := requestKnowledgeMapLayout(t, handler, http.MethodDelete, host, "http://"+host, token, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("layout was not deleted: status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	if loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView); err != nil || loaded != nil {
		t.Fatalf("deleted workspace layout still exists: layout=%#v err=%v", loaded, err)
	}
}

func requestKnowledgeMap(t *testing.T, handler http.Handler, method, path, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
	req.Host = host
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func requestKnowledgeMapLayout(t *testing.T, handler http.Handler, method, host, origin, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1/api/layout", bytes.NewReader(body))
	req.Host = host
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Mem-Session", token)
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}
