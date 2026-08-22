package mem

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

func TestKnowledgeMapWorkspaceSwitchesAndProtectsNamedViews(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "named-workspace-node", Kind: KnowledgeNodeTopic, Label: "Равновесие",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	const token = "named-view-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)
	layout := KnowledgeMapLayout{
		Version:  KnowledgeMapLayoutVersion,
		Nodes:    map[string]KnowledgeMapNodePosition{"named-workspace-node": {X: 77, Y: 33, Pinned: true}},
		Viewport: KnowledgeMapViewport{Scale: 1.7, X: 8, Y: 9},
		State: &KnowledgeMapViewState{
			Filters: KnowledgeMapViewFilters{
				Statuses: []KnowledgeStatus{KnowledgeStatusDraft}, Evidence: []EvidenceState{EvidenceCurrent},
				NodeKinds: []KnowledgeNodeKind{KnowledgeNodeTopic}, RelationKinds: []KnowledgeRelationKind{},
			},
			Focus: &KnowledgeMapFocus{NodeID: "named-workspace-node", Depth: 2}, ClusterLayout: true,
		},
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	viewName := "Композиция кадра"
	saved := requestKnowledgeMapLayoutView(t, handler, http.MethodPut, host, "http://"+host, token, viewName, raw)
	if saved.Code != http.StatusOK {
		t.Fatalf("named view was not saved: status=%d body=%q", saved.Code, saved.Body.String())
	}
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/?view="+url.QueryEscape(viewName), host)
	for _, marker := range []string{`"view_name":"Композиция кадра"`, `"name":"Композиция кадра"`, `"scale":1.7`, `"depth":2`, `saveViewBtn`, `navigationAction`, `КЛАСТЕРЫ`} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("named workspace page is missing %q: status=%d", marker, page.Code)
		}
	}
	if got := requestKnowledgeMap(t, handler, http.MethodGet, "/?view=a&view=b", host); got.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous view selection returned %d", got.Code)
	}
	if got := requestKnowledgeMapLayoutView(t, handler, http.MethodPut, host, "http://"+host, "wrong", "Чужой вид", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized named view write returned %d", got.Code)
	}
	deleted := requestKnowledgeMapLayoutView(t, handler, http.MethodDelete, host, "http://"+host, token, viewName, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("named view was not deleted: status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	if loaded, err := store.LoadKnowledgeMapLayout(viewName); err != nil || loaded != nil {
		t.Fatalf("deleted named view still exists: layout=%#v err=%v", loaded, err)
	}
}

func TestKnowledgeMapWorkspaceServesOnlyAuthorizedCurrentPDFEvidence(t *testing.T) {
	store, anchor, source := knowledgeMapPDFSourceFixture(t)
	defer store.Close()
	const token = "source-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)

	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "ОТКРЫТЬ PDF") ||
		!strings.Contains(page.Body.String(), "/api/source?citation=") {
		t.Fatalf("workspace page has no physical-page source action: status=%d", page.Code)
	}
	var sourceCookie *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, "mem_map_source_") {
			sourceCookie = cookie
			break
		}
	}
	if sourceCookie == nil || !sourceCookie.HttpOnly || sourceCookie.SameSite != http.SameSiteStrictMode || sourceCookie.Path != "/api/source" {
		t.Fatalf("source session cookie is missing or unsafe: %#v", sourceCookie)
	}
	if got := requestKnowledgeMapSource(t, handler, http.MethodGet, host, anchor.CitationID, nil, "same-origin", ""); got.Code != http.StatusForbidden {
		t.Fatalf("source request without session cookie was accepted: %d", got.Code)
	}
	if got := requestKnowledgeMapSource(t, handler, http.MethodGet, host, anchor.CitationID, sourceCookie, "cross-site", ""); got.Code != http.StatusForbidden {
		t.Fatalf("cross-site source request was accepted: %d", got.Code)
	}
	partial := requestKnowledgeMapSource(t, handler, http.MethodGet, host, anchor.CitationID, sourceCookie, "same-origin", "bytes=0-3")
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "%PDF" ||
		partial.Header().Get("Content-Type") != "application/pdf" ||
		!strings.Contains(partial.Header().Get("Content-Disposition"), filepath.Base(source)) {
		t.Fatalf("authorized PDF range response is invalid: status=%d type=%q disposition=%q body=%q",
			partial.Code, partial.Header().Get("Content-Type"), partial.Header().Get("Content-Disposition"), partial.Body.String())
	}
	if got := requestKnowledgeMapSource(t, handler, http.MethodGet, host, "cite-unknown", sourceCookie, "same-origin", ""); got.Code != http.StatusNotFound {
		t.Fatalf("unknown source citation returned %d", got.Code)
	}
	if got := requestKnowledgeMapSource(t, handler, http.MethodPost, host, anchor.CitationID, sourceCookie, "same-origin", ""); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("source write method was accepted: status=%d allow=%q", got.Code, got.Header().Get("Allow"))
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapSource(t, readOnly, http.MethodGet, host, anchor.CitationID, sourceCookie, "same-origin", ""); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed local PDF source: %d", got.Code)
	}
}

func TestKnowledgeMapWorkspaceApprovesPinnedDraftWithoutUserFacingID(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const objectID = "workspace-review-node"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: objectID, Kind: KnowledgeNodeClaim, Label: "Проверяемое утверждение",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	const token = "review-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "ПОДТВЕРДИТЬ") ||
		!strings.Contains(page.Body.String(), "/api/review/approve") ||
		!strings.Contains(page.Body.String(), "expected_evidence_digest") {
		t.Fatalf("workspace page has no pinned review action: status=%d", page.Code)
	}

	request := KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectNode, ID: objectID, Reviewer: "Руслан",
		Comment: "Источник проверен", ExpectedEvidenceDigest: digest,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for name, response := range map[string]*httptest.ResponseRecorder{
		"missing origin": requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "", token, "same-origin", "application/json", raw),
		"wrong token":    requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, "wrong", "same-origin", "application/json", raw),
		"cross site":     requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "cross-site", "application/json", raw),
	} {
		if response.Code != http.StatusForbidden {
			t.Errorf("%s review request returned %d", name, response.Code)
		}
	}
	if got := requestKnowledgeMapApproval(t, handler, http.MethodGet, host, "http://"+host, token, "same-origin", "application/json", nil); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "POST" {
		t.Fatalf("review GET was accepted: status=%d allow=%q", got.Code, got.Header().Get("Allow"))
	}
	if got := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "text/plain", raw); got.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("review request with unsafe content type returned %d", got.Code)
	}
	unpinned := request
	unpinned.ExpectedEvidenceDigest = ""
	unpinnedRaw, err := json.Marshal(unpinned)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", unpinnedRaw); got.Code != http.StatusBadRequest {
		t.Fatalf("unpinned review request returned %d: %s", got.Code, got.Body.String())
	}
	changed := request
	changed.ExpectedEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	changedRaw, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", changedRaw); got.Code != http.StatusConflict {
		t.Fatalf("changed evidence digest returned %d: %s", got.Code, got.Body.String())
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapApproval(t, readOnly, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", raw); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed review mutation: %d", got.Code)
	}

	approved := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", raw)
	if approved.Code != http.StatusOK {
		t.Fatalf("valid review failed: status=%d body=%q", approved.Code, approved.Body.String())
	}
	var result KnowledgeApprovalResult
	if err := json.Unmarshal(approved.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != objectID || result.Status != KnowledgeStatusActive || result.Review.Reviewer != "Руслан" || result.Review.Comment != "Источник проверен" {
		t.Fatalf("approval response lost review data: %#v", result)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].Status != KnowledgeStatusActive {
		t.Fatalf("approved object was not activated: graph=%#v err=%v", graph, err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 1 || reviews[0].ObjectID != objectID {
		t.Fatalf("approval audit was not appended: reviews=%#v err=%v", reviews, err)
	}
}

func TestKnowledgeMapWorkspaceRejectsReopensAndUndoesWithoutUserFacingIDs(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const objectID = "workspace-review-lifecycle"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: objectID, Kind: KnowledgeNodeClaim, Label: "Решение по утверждению",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	digest, err := KnowledgeEvidenceDigest([]EvidenceAnchor{anchor})
	if err != nil {
		t.Fatal(err)
	}
	const token = "review-lifecycle-session-capability"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	for _, marker := range []string{"ОТКЛОНИТЬ", "ВЕРНУТЬ В РАБОТУ", "ОТМЕНИТЬ ПОДТВЕРЖДЕНИЕ", "/api/review/reject", "/api/review/reopen", "/api/review/undo", "expected_review_id"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("workspace page is missing %q: status=%d", marker, page.Code)
		}
	}
	request := KnowledgeReviewMutationRequest{
		ObjectType: KnowledgeObjectNode, ID: objectID, Reviewer: "Руслан", ExpectedEvidenceDigest: digest,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/review/reject", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusBadRequest {
		t.Fatalf("rejection without reason returned %d: %s", got.Code, got.Body.String())
	}
	request.Comment = "Нет подтверждения в приведённой выдержке"
	raw, _ = json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/review/reject", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("rejection with wrong session returned %d", got.Code)
	}
	rejected := requestKnowledgeMapMutation(t, handler, "/api/review/reject", host, "http://"+host, token, "same-origin", raw)
	if rejected.Code != http.StatusOK {
		t.Fatalf("valid rejection failed: status=%d body=%q", rejected.Code, rejected.Body.String())
	}
	var rejectedResult KnowledgeReviewMutationResult
	if err := json.Unmarshal(rejected.Body.Bytes(), &rejectedResult); err != nil || rejectedResult.Status != KnowledgeStatusRejected {
		t.Fatalf("rejection response is invalid: result=%#v err=%v", rejectedResult, err)
	}

	request.Comment = "Вернуть для повторной проверки"
	raw, _ = json.Marshal(request)
	reopened := requestKnowledgeMapMutation(t, handler, "/api/review/reopen", host, "http://"+host, token, "same-origin", raw)
	if reopened.Code != http.StatusOK {
		t.Fatalf("valid reopen failed: status=%d body=%q", reopened.Code, reopened.Body.String())
	}
	approvalRaw, _ := json.Marshal(KnowledgeApprovalRequest{
		ObjectType: KnowledgeObjectNode, ID: objectID, Reviewer: "Руслан", ExpectedEvidenceDigest: digest,
	})
	approved := requestKnowledgeMapApproval(t, handler, http.MethodPost, host, "http://"+host, token, "same-origin", "application/json", approvalRaw)
	if approved.Code != http.StatusOK {
		t.Fatalf("approval after reopen failed: status=%d body=%q", approved.Code, approved.Body.String())
	}
	var approvalResult KnowledgeApprovalResult
	if err := json.Unmarshal(approved.Body.Bytes(), &approvalResult); err != nil {
		t.Fatal(err)
	}
	request.Comment = "Подтверждение было преждевременным"
	request.ExpectedReviewID = approvalResult.Review.ID + 1
	raw, _ = json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/review/undo", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusConflict {
		t.Fatalf("undo with stale review pin returned %d: %s", got.Code, got.Body.String())
	}
	request.ExpectedReviewID = approvalResult.Review.ID
	raw, _ = json.Marshal(request)
	undone := requestKnowledgeMapMutation(t, handler, "/api/review/undo", host, "http://"+host, token, "same-origin", raw)
	if undone.Code != http.StatusOK {
		t.Fatalf("valid undo failed: status=%d body=%q", undone.Code, undone.Body.String())
	}
	var undoResult KnowledgeReviewMutationResult
	if err := json.Unmarshal(undone.Body.Bytes(), &undoResult); err != nil || undoResult.Status != KnowledgeStatusDraft || undoResult.Review.RevertsReviewID != approvalResult.Review.ID {
		t.Fatalf("undo response is invalid: result=%#v err=%v", undoResult, err)
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapMutation(t, readOnly, "/api/review/reject", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed rejection: %d", got.Code)
	}
}

func TestKnowledgeMapWorkspaceEditsAndUndoesPinnedContent(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const objectID = "workspace-edit-node"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: objectID, Kind: KnowledgeNodeClaim, Label: "Исходное название", Body: "Исходное описание",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	item := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, objectID)
	const token = "edit-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	for _, marker := range []string{"РЕДАКТИРОВАНИЕ", "СОХРАНИТЬ ПРАВКУ", "ОТМЕНИТЬ ПОСЛЕДНЮЮ ПРАВКУ", "/api/edit", "/api/edit/undo", "expected_content_digest", "expected_edit_id"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("workspace page is missing %q: status=%d", marker, page.Code)
		}
	}
	request := KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: objectID, Editor: "Руслан", Comment: "Уточнение по источнику",
		Label: "Уточнённое название", Body: "Уточнённое описание",
		ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest,
		ExpectedEvidenceDigest: item.EvidenceDigest,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/edit", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("edit with wrong session returned %d", got.Code)
	}
	invalid := append(append([]byte{}, raw...), []byte(` {}`)...)
	if got := requestKnowledgeMapMutation(t, handler, "/api/edit", host, "http://"+host, token, "same-origin", invalid); got.Code != http.StatusBadRequest {
		t.Fatalf("multi-object edit request returned %d", got.Code)
	}
	editedResponse := requestKnowledgeMapMutation(t, handler, "/api/edit", host, "http://"+host, token, "same-origin", raw)
	if editedResponse.Code != http.StatusOK {
		t.Fatalf("valid edit failed: status=%d body=%q", editedResponse.Code, editedResponse.Body.String())
	}
	var edited KnowledgeEditResult
	if err := json.Unmarshal(editedResponse.Body.Bytes(), &edited); err != nil || edited.Label != request.Label || edited.Edit.Action != KnowledgeEditActionEdit {
		t.Fatalf("edit response is invalid: result=%#v err=%v", edited, err)
	}
	undo := KnowledgeEditUndoRequest{
		ObjectType: KnowledgeObjectNode, ID: objectID, Editor: "Руслан", Comment: "Отмена проверки",
		ExpectedStatus: edited.Status, ExpectedContentDigest: edited.ContentDigest,
		ExpectedEvidenceDigest: edited.EvidenceDigest, ExpectedEditID: edited.Edit.ID + 1,
	}
	raw, _ = json.Marshal(undo)
	if got := requestKnowledgeMapMutation(t, handler, "/api/edit/undo", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusConflict {
		t.Fatalf("undo with stale edit pin returned %d: %s", got.Code, got.Body.String())
	}
	undo.ExpectedEditID = edited.Edit.ID
	raw, _ = json.Marshal(undo)
	undoneResponse := requestKnowledgeMapMutation(t, handler, "/api/edit/undo", host, "http://"+host, token, "same-origin", raw)
	if undoneResponse.Code != http.StatusOK {
		t.Fatalf("valid edit undo failed: status=%d body=%q", undoneResponse.Code, undoneResponse.Body.String())
	}
	var undone KnowledgeEditResult
	if err := json.Unmarshal(undoneResponse.Body.Bytes(), &undone); err != nil || undone.Label != "Исходное название" || undone.Edit.RevertsEditID != edited.Edit.ID {
		t.Fatalf("edit undo response is invalid: result=%#v err=%v", undone, err)
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapMutation(t, readOnly, "/api/edit", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed content editing: %d", got.Code)
	}
}

func TestKnowledgeMapWorkspaceCreatesPinnedManualBranch(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const parentID = "workspace-create-parent"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: parentID, Kind: KnowledgeNodeClaim, Label: "Исходное утверждение", Body: "Текст источника",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	parent := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, parentID)
	const token = "workspace-create-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	for _, marker := range []string{"РАБОЧИЙ СЛОЙ", "СОЗДАТЬ И ПРИВЯЗАТЬ", "workspaceCreateAction", "/api/workspace/create", "expected_parent_content_digest", "workspace_creations"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("workspace page is missing %q: status=%d", marker, page.Code)
		}
	}
	request := KnowledgeWorkspaceCreateRequest{
		ParentNodeID: parentID, Kind: KnowledgeNodeQuestion, Label: "Что проверить дальше?",
		Body: "Уточнить условия применимости", Author: "Руслан", Comment: "Рабочий вопрос",
		ExpectedParentStatus: parent.Status, ExpectedParentContent: parent.ContentDigest,
		ExpectedEvidence: parent.EvidenceDigest,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/workspace/create", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("workspace creation with wrong session returned %d", got.Code)
	}
	stale := request
	stale.ExpectedParentContent = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	staleRaw, _ := json.Marshal(stale)
	if got := requestKnowledgeMapMutation(t, handler, "/api/workspace/create", host, "http://"+host, token, "same-origin", staleRaw); got.Code != http.StatusConflict {
		t.Fatalf("workspace creation with stale parent pin returned %d: %s", got.Code, got.Body.String())
	}
	invalid := append(append([]byte{}, raw...), []byte(` {}`)...)
	if got := requestKnowledgeMapMutation(t, handler, "/api/workspace/create", host, "http://"+host, token, "same-origin", invalid); got.Code != http.StatusBadRequest {
		t.Fatalf("multi-object workspace request returned %d", got.Code)
	}
	createdResponse := requestKnowledgeMapMutation(t, handler, "/api/workspace/create", host, "http://"+host, token, "same-origin", raw)
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("valid workspace creation failed: status=%d body=%q", createdResponse.Code, createdResponse.Body.String())
	}
	var created KnowledgeWorkspaceCreateResult
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil ||
		created.Node.Kind != KnowledgeNodeQuestion || created.Edge.Kind != KnowledgeRelationAsks ||
		created.Creation.Author != request.Author {
		t.Fatalf("workspace creation response is invalid: result=%#v err=%v", created, err)
	}
	refreshed := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if refreshed.Code != http.StatusOK || !strings.Contains(refreshed.Body.String(), "Что проверить дальше?") ||
		!strings.Contains(refreshed.Body.String(), `"author":"Руслан"`) {
		t.Fatalf("created workspace object is absent from refreshed map: status=%d", refreshed.Code)
	}
	if got := requestKnowledgeMap(t, handler, http.MethodGet, "/api/workspace/create", host); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "POST" {
		t.Fatalf("workspace creation accepted a read method: status=%d allow=%q", got.Code, got.Header().Get("Allow"))
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapMutation(t, readOnly, "/api/workspace/create", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusNotFound {
		t.Fatalf("read-only handler exposed workspace creation: %d", got.Code)
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
	return requestKnowledgeMapLayoutView(t, handler, method, host, origin, token, "", body)
}

func requestKnowledgeMapLayoutView(t *testing.T, handler http.Handler, method, host, origin, token, viewName string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	path := "http://127.0.0.1/api/layout"
	if viewName != "" {
		path += "?view=" + url.QueryEscape(viewName)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
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

func requestKnowledgeMapSource(t *testing.T, handler http.Handler, method, host, citation string, cookie *http.Cookie, fetchSite, byteRange string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1/api/source?citation="+url.QueryEscape(citation), nil)
	req.Host = host
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func requestKnowledgeMapApproval(t *testing.T, handler http.Handler, method, host, origin, token, fetchSite, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1/api/review/approve", bytes.NewReader(body))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("X-Mem-Session", token)
	}
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func requestKnowledgeMapMutation(t *testing.T, handler http.Handler, path, host, origin, token, fetchSite string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, bytes.NewReader(body))
	req.Host = host
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Mem-Session", token)
	req.Header.Set("Sec-Fetch-Site", fetchSite)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}
