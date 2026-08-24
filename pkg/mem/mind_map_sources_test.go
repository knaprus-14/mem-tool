package mem

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClassicMindMapSourceSearchAttachOrderDetachAndUndo(t *testing.T) {
	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	documentPath := filepath.Join(storeDir, "manual.pdf")
	if err := os.WriteFile(documentPath, []byte("%PDF-1.4\nfixture"), 0600); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = documentPath
		chunks[i].Text = []string{"предел огнестойкости стены", "эвакуационный выход"}[i]
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
	}
	if err := store.ReplaceDocumentChunks(documentPath, chunks); err != nil {
		t.Fatal(err)
	}

	documents := store.ListClassicMindMapSourceDocuments()
	if len(documents) != 1 || documents[0].ChunkCount != 2 || documents[0].PageCount != 1 || documents[0].SourcePath != documentPath {
		t.Fatalf("unexpected source documents: %#v", documents)
	}
	candidates, err := store.SearchClassicMindMapEvidence(ClassicMindMapEvidenceSearchOptions{Query: "огнестойкости", Limit: 20})
	if err != nil || len(candidates) != 1 || candidates[0].EntryID <= 0 || !strings.Contains(candidates[0].Excerpt, "огнестойкости") {
		t.Fatalf("unexpected evidence candidates: %#v err=%v", candidates, err)
	}

	doc, err := store.CreateClassicMindMap("Источники", "")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.GetByID(candidates[0].EntryID)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	doc, evidence, err := store.AttachClassicMindMapEvidence(doc.Map.ID, doc.Map.RootNodeID, anchor, doc.Map.Revision, "test", "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.EvidenceState != EvidenceCurrent {
		t.Fatalf("evidence source is not current: %#v", evidence)
	}
	externalPath := filepath.Join(storeDir, "drawing.png")
	if err := os.WriteFile(externalPath, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	doc, external, err := store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
		Kind: ClassicMindMapSourceExternalFile, Locator: externalPath,
	}, doc.Map.Revision, "test", "file")
	if err != nil {
		t.Fatal(err)
	}
	doc, web, err := store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
		Kind: ClassicMindMapSourceURL, URL: "https://example.org/spec", Title: "Спецификация",
	}, doc.Map.Revision, "test", "url")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "kn-source", Kind: KnowledgeNodeClaim, Label: "Проверенный предел",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	doc, knowledge, err := store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
		Kind: ClassicMindMapSourceKnowledgeNode, KnowledgeNodeID: "kn-source",
	}, doc.Map.Revision, "test", "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if knowledge.Title != "Проверенный предел" || len(doc.Nodes[0].Sources) != 4 {
		t.Fatalf("unexpected attached sources: knowledge=%#v doc=%#v", knowledge, doc)
	}
	for _, source := range doc.Nodes[0].Sources {
		if source.EvidenceState != EvidenceCurrent {
			t.Errorf("source %s state=%q, want current", source.ID, source.EvidenceState)
		}
	}

	doc, err = store.MoveClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, web.ID, 0, doc.Map.Revision, "test", "move")
	if err != nil || doc.Nodes[0].Sources[0].ID != web.ID {
		t.Fatalf("source order was not changed: %#v err=%v", doc.Nodes[0].Sources, err)
	}
	beforeDetachRevision := doc.Map.Revision
	doc, err = store.DetachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, external.ID, doc.Map.Revision, "test", "detach")
	if err != nil || len(doc.Nodes[0].Sources) != 3 {
		t.Fatalf("source was not detached: %#v err=%v", doc.Nodes[0].Sources, err)
	}
	doc, _, err = store.UndoClassicMindMapChange(doc.Map.ID, 0, doc.Map.Revision, "test", "undo detach")
	if err != nil || len(doc.Nodes[0].Sources) != 4 || doc.Map.Revision != beforeDetachRevision+2 {
		t.Fatalf("detach undo failed: doc=%#v err=%v", doc, err)
	}
	resolved, err := store.ResolveClassicMindMapFileSource(doc.Map.ID, evidence.ID)
	if err != nil || resolved.Path != documentPath || resolved.Page != 4 {
		t.Fatalf("evidence file was not resolved: %#v err=%v", resolved, err)
	}
	if err := os.Remove(externalPath); err != nil {
		t.Fatal(err)
	}
	missingDoc, err := store.LoadClassicMindMap(doc.Map.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range missingDoc.Nodes[0].Sources {
		if source.ID == external.ID && source.EvidenceState != EvidenceMissing {
			t.Fatalf("removed external file state=%q, want missing", source.EvidenceState)
		}
	}

	knowledgeItems, err := store.SearchClassicMindMapKnowledgeNodes("предел", 10)
	if err != nil || len(knowledgeItems) != 1 || knowledgeItems[0].ID != "kn-source" {
		t.Fatalf("knowledge source search failed: %#v err=%v", knowledgeItems, err)
	}
}

func TestClassicMindMapAttachmentImportIsPrivateAndBounded(t *testing.T) {
	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path, digest, err := store.ImportClassicMindMapAttachment("../notes.txt", strings.NewReader("private notes"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(storeDir, "mindmap-files") || filepath.Base(path) == "notes.txt" || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("unexpected imported attachment path=%q digest=%q", path, digest)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		t.Fatalf("attachment permissions are not private: info=%#v err=%v", info, err)
	}
	if _, _, err := store.ImportClassicMindMapAttachment("large.bin", bytes.NewReader(make([]byte, MaxClassicMindMapUploadBytes+1))); err == nil {
		t.Fatal("oversized attachment was accepted")
	}
}

func TestClassicMindMapURLSourceValidation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("URL sources", "")
	if err != nil {
		t.Fatal(err)
	}

	const valid = "https://example.org/spec?q=fire%20safety#section-2"
	doc, attached, err := store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
		Kind: ClassicMindMapSourceURL, URL: valid,
	}, doc.Map.Revision, "test", "valid URL")
	if err != nil {
		t.Fatal(err)
	}
	if attached.URL != valid || attached.Title != "example.org" {
		t.Fatalf("valid URL was changed unexpectedly: %#v", attached)
	}

	invalid := []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///C:/private.txt",
		"/relative/document",
		"https://user:secret@example.org/private",
		"https://:443/path",
		"https://example.org/" + strings.Repeat("a", MaxClassicMindMapTextRunes),
	}
	for _, raw := range invalid {
		t.Run(raw[:min(len(raw), 40)], func(t *testing.T) {
			beforeRevision := doc.Map.Revision
			if _, _, err := store.AttachClassicMindMapSource(doc.Map.ID, doc.Map.RootNodeID, ClassicMindMapSource{
				Kind: ClassicMindMapSourceURL, URL: raw,
			}, beforeRevision, "test", "invalid URL"); err == nil {
				t.Fatalf("unsafe URL was accepted: %q", raw)
			}
			loaded, loadErr := store.LoadClassicMindMap(doc.Map.ID)
			if loadErr != nil || loaded.Map.Revision != beforeRevision {
				t.Fatalf("rejected URL changed the map: revision=%d err=%v", loaded.Map.Revision, loadErr)
			}
		})
	}

	_, err = store.ImportClassicMindMap(ClassicMindMapDraft{
		Title: "Unsafe import",
		Nodes: []ClassicMindMapNodeDraft{{
			Ref: "root", Label: "Unsafe import", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeImported,
			Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceURL, URL: "javascript:alert(1)"}},
		}},
	}, "test", "unsafe import")
	if err == nil {
		t.Fatal("central draft validation accepted an unsafe URL")
	}
}

func TestClassicMindMapWorkspaceSourceAPIAndPhysicalPage(t *testing.T) {
	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	documentPath := filepath.Join(storeDir, "source.pdf")
	if err := os.WriteFile(documentPath, []byte("%PDF-1.4\nsource"), 0600); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = documentPath
	}
	if err := store.ReplaceDocumentChunks(documentPath, chunks); err != nil {
		t.Fatal(err)
	}
	doc, err := store.CreateClassicMindMap("Browser sources", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewClassicMindMapWorkspaceHandler(store, "source-session")

	searchResponse := classicMindMapWorkspaceRequest(t, handler, "/api/sources/search", "127.0.0.1:9010", "http://127.0.0.1:9010", "source-session", map[string]any{
		"query": "chunk-0", "document": "", "page": 0, "limit": 10,
	})
	var candidates []ClassicMindMapEvidenceCandidate
	decodeClassicMindMapTestResponse(t, searchResponse, http.StatusOK, &candidates)
	if len(candidates) != 1 {
		t.Fatalf("unexpected source API candidates: %#v", candidates)
	}
	attachResponse := classicMindMapWorkspaceRequest(t, handler, "/api/sources/evidence/add", "127.0.0.1:9010", "http://127.0.0.1:9010", "source-session", map[string]any{
		"map_id": doc.Map.ID, "node_id": doc.Map.RootNodeID, "entry_id": candidates[0].EntryID, "expected_revision": doc.Map.Revision,
	})
	var attached struct {
		Document ClassicMindMapDocument `json:"document"`
		Source   ClassicMindMapSource   `json:"source"`
	}
	decodeClassicMindMapTestResponse(t, attachResponse, http.StatusOK, &attached)

	pageRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9010/", nil)
	pageRequest.Host = "127.0.0.1:9010"
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK {
		t.Fatalf("workspace page status=%d", pageResponse.Code)
	}
	cookies := pageResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/api/source" || !cookies[0].HttpOnly {
		t.Fatalf("source capability cookie missing: %#v", cookies)
	}
	openRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9010/api/source/mindmap?map_id="+doc.Map.ID+"&source_id="+attached.Source.ID, nil)
	openRequest.Host = "127.0.0.1:9010"
	openRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	openRequest.AddCookie(cookies[0])
	openResponse := httptest.NewRecorder()
	handler.ServeHTTP(openResponse, openRequest)
	if openResponse.Code != http.StatusOK || openResponse.Header().Get("Content-Type") != "application/pdf" || !strings.Contains(openResponse.Body.String(), "%PDF") {
		t.Fatalf("physical source was not served: status=%d headers=%#v body=%q", openResponse.Code, openResponse.Header(), openResponse.Body.String())
	}
	forbidden := httptest.NewRecorder()
	requestWithoutCookie := httptest.NewRequest(http.MethodGet, openRequest.URL.String(), nil)
	requestWithoutCookie.Host = "127.0.0.1:9010"
	handler.ServeHTTP(forbidden, requestWithoutCookie)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("source endpoint accepted request without capability: %d", forbidden.Code)
	}
}

func TestClassicMindMapWorkspaceUploadsAttachment(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("Upload", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewClassicMindMapWorkspaceHandler(store, "upload-session")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"map_id": doc.Map.ID, "node_id": doc.Map.RootNodeID,
		"expected_revision": "1", "title": "Приложение",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "appendix.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("attachment")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9020/api/sources/upload", &body)
	request.Host = "127.0.0.1:9020"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "http://127.0.0.1:9020")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-Mem-Session", "upload-session")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var attached struct {
		Document ClassicMindMapDocument `json:"document"`
		Source   ClassicMindMapSource   `json:"source"`
	}
	decodeClassicMindMapTestResponse(t, response, http.StatusOK, &attached)
	if attached.Source.Kind != ClassicMindMapSourceExternalFile || attached.Source.Title != "Приложение" || attached.Source.EvidenceState != EvidenceCurrent {
		t.Fatalf("unexpected uploaded source: %#v", attached.Source)
	}
	if _, err := os.Stat(attached.Source.Locator); err != nil {
		t.Fatalf("uploaded attachment missing: %v", err)
	}
}
