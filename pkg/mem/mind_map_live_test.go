package mem

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassicMindMapLibraryMetadataAndDuplicate(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("Исходная", "Описание")
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err = store.AddClassicMindMapNode(doc.Map.ID, doc.Map.RootNodeID, "Ветвь", -1,
		ClassicMindMapNodeFact, "Кратко", "Подробно", doc.Map.Revision, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	renamed, description := "Рабочая карта", "Новое описание"
	ready := ClassicMindMapStatusReady
	doc, err = store.EditClassicMindMap(doc.Map.ID, ClassicMindMapPatch{
		Title: &renamed, Description: &description, Status: &ready,
	}, doc.Map.Revision, "test", "rename")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Map.Title != renamed || doc.Map.Status != ready || doc.Map.Revision != 3 {
		t.Fatalf("unexpected metadata edit: %#v", doc.Map)
	}
	if root := findClassicMindMapNode(t, doc, renamed); root.ID != doc.Map.RootNodeID {
		t.Fatalf("automatic root rename selected wrong node: %#v", root)
	}
	copy, err := store.DuplicateClassicMindMap(doc.Map.ID, "Независимая копия")
	if err != nil {
		t.Fatal(err)
	}
	if copy.Map.ID == doc.Map.ID || copy.Map.Title != "Независимая копия" || copy.Map.Mode != ClassicMindMapModeManual || len(copy.Nodes) != len(doc.Nodes) {
		t.Fatalf("unexpected duplicate: %#v", copy)
	}
	if copy.Map.RootNodeID == doc.Map.RootNodeID || findClassicMindMapNode(t, copy, "Ветвь").ID == findClassicMindMapNode(t, doc, "Ветвь").ID {
		t.Fatal("duplicate reused source map or node IDs")
	}
}

func TestClassicMindMapWorkspaceEmptyLibraryIsJSONArray(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewClassicMindMapWorkspaceHandler(store, "session")

	response := classicMindMapWorkspaceRequest(t, handler, "/api/maps/list", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if got := strings.TrimSpace(response.Body.String()); got != "[]" {
		t.Fatalf("empty library JSON=%q, want []", got)
	}
}

func TestClassicMindMapWorkspaceRequiresLoopbackAndSession(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewClassicMindMapWorkspaceHandler(store, "secret-token")

	page := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	page.Host = "127.0.0.1:8765"
	pageResult := httptest.NewRecorder()
	handler.ServeHTTP(pageResult, page)
	if pageResult.Code != http.StatusOK || !strings.Contains(pageResult.Body.String(), "Библиотека карт мыслей") || !strings.Contains(pageResult.Body.String(), "secret-token") {
		t.Fatalf("workspace page was not rendered: status=%d body=%q", pageResult.Code, pageResult.Body.String())
	}
	if csp := pageResult.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || pageResult.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("workspace security headers missing: %#v", pageResult.Header())
	}

	for _, test := range []struct {
		name, host, origin, token string
		want                      int
	}{
		{"foreign host", "example.com", "http://example.com", "secret-token", http.StatusForbidden},
		{"missing token", "127.0.0.1:8765", "http://127.0.0.1:8765", "", http.StatusForbidden},
		{"foreign origin", "127.0.0.1:8765", "http://evil.invalid", "secret-token", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := classicMindMapWorkspaceRequest(t, handler, "/api/maps/list", test.host, test.origin, test.token, map[string]any{})
			defer response.Result().Body.Close()
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestClassicMindMapWorkspaceCRUDAndRevisionConflict(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewClassicMindMapWorkspaceHandler(store, "session")

	createdResponse := classicMindMapWorkspaceRequest(t, handler, "/api/maps/create", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"title": "Редактор", "description": "Проверка",
	})
	var created ClassicMindMapDocument
	decodeClassicMindMapTestResponse(t, createdResponse, http.StatusOK, &created)

	addedResponse := classicMindMapWorkspaceRequest(t, handler, "/api/nodes/add", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": created.Map.ID, "parent_id": created.Map.RootNodeID, "label": "Ветка",
		"position": -1, "kind": "subtopic", "summary": "", "body_markdown": "", "expected_revision": created.Map.Revision,
	})
	var added struct {
		Document ClassicMindMapDocument `json:"document"`
		Node     ClassicMindMapNode     `json:"node"`
	}
	decodeClassicMindMapTestResponse(t, addedResponse, http.StatusOK, &added)
	if added.Document.Map.Revision != 2 || added.Node.Label != "Ветка" {
		t.Fatalf("unexpected add response: %#v", added)
	}

	conflict := classicMindMapWorkspaceRequest(t, handler, "/api/nodes/edit", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": created.Map.ID, "node_id": added.Node.ID, "label": "Просрочено", "expected_revision": created.Map.Revision,
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale browser edit status=%d body=%q", conflict.Code, conflict.Body.String())
	}

	historyResponse := classicMindMapWorkspaceRequest(t, handler, "/api/history/list", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": created.Map.ID, "limit": 20,
	})
	var history []ClassicMindMapChange
	decodeClassicMindMapTestResponse(t, historyResponse, http.StatusOK, &history)
	if len(history) != 2 || history[0].Action != "add_node" {
		t.Fatalf("unexpected browser history: %#v", history)
	}

	undoResponse := classicMindMapWorkspaceRequest(t, handler, "/api/history/undo", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": created.Map.ID, "change_id": 0, "expected_revision": added.Document.Map.Revision,
	})
	var undone struct {
		Document ClassicMindMapDocument `json:"document"`
	}
	decodeClassicMindMapTestResponse(t, undoResponse, http.StatusOK, &undone)
	if len(undone.Document.Nodes) != 1 || undone.Document.Map.Revision != 3 {
		t.Fatalf("browser undo did not restore tree: %#v", undone.Document)
	}
	redoResponse := classicMindMapWorkspaceRequest(t, handler, "/api/history/redo", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": created.Map.ID, "change_id": 0, "expected_revision": undone.Document.Map.Revision,
	})
	var redone struct {
		Document ClassicMindMapDocument `json:"document"`
	}
	decodeClassicMindMapTestResponse(t, redoResponse, http.StatusOK, &redone)
	if len(redone.Document.Nodes) != 2 || redone.Document.Map.Revision != 4 {
		t.Fatalf("browser redo did not restore change: %#v", redone.Document)
	}
}

func TestClassicMindMapWorkspaceExportsPinnedAttachment(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("Переносимая карта", "Проверка HTTP-экспорта")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewClassicMindMapWorkspaceHandler(store, "session")

	exported := classicMindMapWorkspaceRequest(t, handler, "/api/maps/export", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": doc.Map.ID, "format": "html", "expected_revision": doc.Map.Revision, "expected_digest": doc.Digest, "expected_state_digest": doc.StateDigest,
	})
	if exported.Code != http.StatusOK || exported.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(exported.Header().Get("Content-Disposition"), ".html") ||
		exported.Header().Get("X-Mem-Map-Digest") != doc.Digest ||
		exported.Header().Get("X-Mem-Map-State-Digest") != doc.StateDigest ||
		!strings.Contains(exported.Body.String(), "Переносимая карта") {
		t.Fatalf("export status=%d headers=%#v body=%q", exported.Code, exported.Header(), exported.Body.String())
	}

	stale := classicMindMapWorkspaceRequest(t, handler, "/api/maps/export", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": doc.Map.ID, "format": "json", "expected_revision": doc.Map.Revision + 1,
		"expected_digest": doc.Digest, "expected_state_digest": doc.StateDigest,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale export status=%d body=%q", stale.Code, stale.Body.String())
	}
	loaded, err := store.LoadClassicMindMap(doc.Map.ID)
	if err != nil || loaded.Map.Revision != doc.Map.Revision || loaded.Digest != doc.Digest {
		t.Fatalf("export mutated map: revision=%d digest=%q err=%v", loaded.Map.Revision, loaded.Digest, err)
	}

	unpinned := classicMindMapWorkspaceRequest(t, handler, "/api/maps/export", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"map_id": doc.Map.ID, "format": "json",
	})
	if unpinned.Code != http.StatusBadRequest || !strings.Contains(unpinned.Body.String(), "expected_state_digest") {
		t.Fatalf("unpinned export status=%d body=%q", unpinned.Code, unpinned.Body.String())
	}
}

func TestClassicMindMapWorkspacePNGExportConcurrencyGuard(t *testing.T) {
	slots := make(chan struct{}, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var pngCalls atomic.Int32
	export := func(request ClassicMindMapExportRequest) (ClassicMindMapExportArtifact, error) {
		if request.Format == ClassicMindMapExportPNG && pngCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		mediaType := "application/json; charset=utf-8"
		if request.Format == ClassicMindMapExportPNG {
			mediaType = "image/png"
		}
		return ClassicMindMapExportArtifact{
			Format: request.Format, Filename: "map." + string(request.Format), MediaType: mediaType,
			Data: []byte("artifact"), MapID: request.MapRef, Revision: request.ExpectedRevision, Digest: request.ExpectedDigest,
		}, nil
	}

	firstResponse := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		serveClassicMindMapExportWith(firstResponse, classicMindMapExportTestRequest(context.Background(), ClassicMindMapExportPNG), export, slots)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first PNG export did not enter renderer")
	}

	busy := httptest.NewRecorder()
	serveClassicMindMapExportWith(busy, classicMindMapExportTestRequest(context.Background(), ClassicMindMapExportPNG), export, slots)
	if busy.Code != http.StatusTooManyRequests || busy.Header().Get("Retry-After") != "1" || pngCalls.Load() != 1 {
		t.Fatalf("concurrent PNG was not rejected before rendering: status=%d headers=%#v calls=%d", busy.Code, busy.Header(), pngCalls.Load())
	}

	jsonResponse := httptest.NewRecorder()
	serveClassicMindMapExportWith(jsonResponse, classicMindMapExportTestRequest(context.Background(), ClassicMindMapExportJSON), export, slots)
	if jsonResponse.Code != http.StatusOK || jsonResponse.Body.String() != "artifact" {
		t.Fatalf("PNG guard limited a non-raster export: status=%d body=%q", jsonResponse.Code, jsonResponse.Body.String())
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first PNG export did not release its slot")
	}
	if firstResponse.Code != http.StatusOK || firstResponse.Body.String() != "artifact" {
		t.Fatalf("first PNG export failed: status=%d body=%q", firstResponse.Code, firstResponse.Body.String())
	}

	afterRelease := httptest.NewRecorder()
	serveClassicMindMapExportWith(afterRelease, classicMindMapExportTestRequest(context.Background(), ClassicMindMapExportPNG), export, slots)
	if afterRelease.Code != http.StatusOK || pngCalls.Load() != 2 {
		t.Fatalf("PNG slot was not reusable: status=%d calls=%d", afterRelease.Code, pngCalls.Load())
	}
}

func TestClassicMindMapWorkspaceExportHonorsRequestCancellation(t *testing.T) {
	slots := make(chan struct{}, 1)
	var calls atomic.Int32
	export := func(request ClassicMindMapExportRequest) (ClassicMindMapExportArtifact, error) {
		calls.Add(1)
		return ClassicMindMapExportArtifact{
			Format: request.Format, Filename: "map.png", MediaType: "image/png", Data: []byte("must not be written"),
		}, nil
	}

	beforeContext, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	before := httptest.NewRecorder()
	serveClassicMindMapExportWith(before, classicMindMapExportTestRequest(beforeContext, ClassicMindMapExportPNG), export, slots)
	if before.Code != http.StatusRequestTimeout || calls.Load() != 0 {
		t.Fatalf("cancelled request reached renderer: status=%d calls=%d", before.Code, calls.Load())
	}

	afterContext, cancelAfter := context.WithCancel(context.Background())
	after := httptest.NewRecorder()
	serveClassicMindMapExportWith(after, classicMindMapExportTestRequest(afterContext, ClassicMindMapExportPNG), func(request ClassicMindMapExportRequest) (ClassicMindMapExportArtifact, error) {
		cancelAfter()
		return export(request)
	}, slots)
	if after.Code != http.StatusRequestTimeout || strings.Contains(after.Body.String(), "must not be written") || calls.Load() != 1 {
		t.Fatalf("post-render cancellation leaked artifact: status=%d body=%q calls=%d", after.Code, after.Body.String(), calls.Load())
	}
}

func classicMindMapExportTestRequest(ctx context.Context, format ClassicMindMapExportFormat) *http.Request {
	payload, _ := json.Marshal(classicMindMapExportRequest{
		MapID: "mmm-test", Format: format, ExpectedRevision: 7,
		ExpectedDigest: "sha256:test", ExpectedStateDigest: "sha256:state",
	})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9000/api/maps/export", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func classicMindMapWorkspaceRequest(t *testing.T, handler http.Handler, path, host, origin, token string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+host+path, bytes.NewReader(encoded))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if token != "" {
		request.Header.Set("X-Mem-Session", token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeClassicMindMapTestResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, target any) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if response.Code != wantStatus {
		body, _ := io.ReadAll(result.Body)
		t.Fatalf("status=%d want=%d body=%q", response.Code, wantStatus, body)
	}
	if err := json.NewDecoder(result.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func TestClassicMindMapWorkspaceHasOfflineEditorControls(t *testing.T) {
	page, err := RenderClassicMindMapWorkspace("token")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	for _, expected := range []string{
		`id="newMap"`, `id="mapGrid"`, `id="tree"`, `id="inspectorBody"`,
		`id="expandAll"`, `id="collapseAll"`, `id="zoomFit"`, `data-tab="sources"`,
		`/api/nodes/move`, `/api/history/undo`, `/api/history/redo`, `mem-mindmap-theme`, `draggable=node.id!==doc.map.root_node_id`,
		`/api/sources/search`, `/api/sources/evidence/add`, `/api/sources/upload`, `/api/sources/remove`,
		`Из активной базы`, `Импортировать копию файла`, `Узел графа`, `/api/source/mindmap`,
		`Number(item.chunk_index||0)+1`, `Number(e.block_index||0)+1`,
		`id="newMapAI"`, `id="assistantEditor"`, `id="assistantDialog"`, `MEM · AI STUDIO`,
		`/api/assistant/start`, `/api/assistant/status`, `/api/assistant/cancel`, `/api/assistant/publish`,
		`new_map`, `expand_branch`, `fill_node`, `find_sources`, `parent_proposal_id`,
		`Разрешить модельную заготовку без локальных источников`, `Опубликовать выбранное`,
		`preview_id:preview.run_id`, `expected_preview_digest:preview.proposal_digest||''`,
		`job.status==='insufficient'`, `renderAssistantInsufficient`, `Подтверждённых предложений нет`,
		`planned:'План обработки готов'`, `generate:'Генерация структуры'`, `После публикации`,
		`Дополнительные настройки объёма`, `limit:assistantState.setup.limit`,
		`'ai:expand_branch':'AI расширил ветвь'`, `action!=='find_sources'`,
		`id="exportMap"`, `id="exportDialog"`, `id="exportForm"`, `/api/maps/export`,
		`Интерактивный HTML`, `value="svg"`, `value="png"`, `value="json"`, `value="opml"`,
		`expected_revision:doc.map.revision`, `expected_digest:doc.digest`, `expected_state_digest:doc.state_digest`, `Все ветви войдут в файл`,
		`function safeWebSourceURL(value)`, `parsed.protocol!=='http:'`, `parsed.protocol!=='https:'`,
		`parsed.username`, `link.href=webURL`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("workspace missing %q", expected)
		}
	}
	if strings.Contains(text, `<script src=`) || strings.Contains(text, `<link rel="stylesheet"`) {
		t.Fatal("offline workspace unexpectedly depends on an external asset")
	}
	if strings.Contains(text, `link.href=source.url`) {
		t.Fatal("workspace links an unvalidated source URL")
	}
}
