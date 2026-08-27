package mem

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxClassicMindMapRequestJSON = 1 << 20

const classicMindMapSourceSandboxPolicy = "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// NewClassicMindMapWorkspaceHandler exposes a loopback-only library and tree
// editor. Every data request requires the short-lived capability embedded in
// the page plus a same-origin browser request.
func NewClassicMindMapWorkspaceHandler(store *Store, sessionToken string) http.Handler {
	return newClassicMindMapWorkspaceHandler(store, sessionToken, nil)
}

// NewClassicMindMapWorkspaceHandlerWithAI adds the preview-first assistant to
// the same loopback/session-protected workspace. The classic constructor stays
// available for callers and tests that need the manual editor only.
func NewClassicMindMapWorkspaceHandlerWithAI(store *Store, sessionToken string, assistant *ClassicMindMapAIWorkspace) http.Handler {
	return newClassicMindMapWorkspaceHandler(store, sessionToken, assistant)
}

func newClassicMindMapWorkspaceHandler(store *Store, sessionToken string, assistant *ClassicMindMapAIWorkspace) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setKnowledgeMapSecurityHeaders(w.Header())
		if !knowledgeMapLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			page, err := RenderClassicMindMapWorkspace(sessionToken)
			if err != nil {
				http.Error(w, "mind map workspace is unavailable", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, knowledgeMapSourceCookie(sessionToken))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
		if store == nil {
			http.Error(w, "mind map store is unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/source/mindmap" {
			serveClassicMindMapSourceFile(w, r, store, sessionToken)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !knowledgeMapWorkspaceAuthorized(r, sessionToken) {
			http.Error(w, "forbidden mind map request", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/maps/list":
			serveClassicMindMapList(w, r, store)
		case "/api/maps/load":
			serveClassicMindMapLoad(w, r, store)
		case "/api/maps/create":
			serveClassicMindMapCreate(w, r, store)
		case "/api/maps/edit":
			serveClassicMindMapEdit(w, r, store)
		case "/api/maps/duplicate":
			serveClassicMindMapDuplicate(w, r, store)
		case "/api/maps/export":
			serveClassicMindMapExport(w, r, store)
		case "/api/workbench/templates":
			serveClassicMindMapTemplates(w, r)
		case "/api/workbench/template/create":
			serveClassicMindMapTemplateCreate(w, r, store)
		case "/api/workbench/branch":
			serveClassicMindMapBranchManifest(w, r, store)
		case "/api/workbench/ask":
			serveClassicMindMapBranchQuestion(w, r, store, assistant)
		case "/api/workbench/compare":
			serveClassicMindMapCompare(w, r, store)
		case "/api/workbench/study":
			serveClassicMindMapStudy(w, r, store)
		case "/api/nodes/add":
			serveClassicMindMapNodeAdd(w, r, store)
		case "/api/nodes/edit":
			serveClassicMindMapNodeEdit(w, r, store)
		case "/api/nodes/move":
			serveClassicMindMapNodeMove(w, r, store)
		case "/api/nodes/delete":
			serveClassicMindMapNodeDelete(w, r, store)
		case "/api/sources/documents":
			serveClassicMindMapSourceDocuments(w, r, store)
		case "/api/sources/search":
			serveClassicMindMapSourceSearch(w, r, store)
		case "/api/sources/knowledge/search":
			serveClassicMindMapKnowledgeSearch(w, r, store)
		case "/api/sources/evidence/add":
			serveClassicMindMapEvidenceAdd(w, r, store)
		case "/api/sources/add":
			serveClassicMindMapSourceAdd(w, r, store)
		case "/api/sources/upload":
			serveClassicMindMapSourceUpload(w, r, store)
		case "/api/sources/remove":
			serveClassicMindMapSourceRemove(w, r, store)
		case "/api/sources/move":
			serveClassicMindMapSourceMove(w, r, store)
		case "/api/history/list":
			serveClassicMindMapHistory(w, r, store)
		case "/api/history/undo":
			serveClassicMindMapUndo(w, r, store)
		case "/api/history/redo":
			serveClassicMindMapRedo(w, r, store)
		case "/api/snapshots/list":
			serveClassicMindMapSnapshots(w, r, store)
		case "/api/snapshots/create":
			serveClassicMindMapSnapshotCreate(w, r, store)
		case "/api/assistant/start":
			serveClassicMindMapAIStart(w, r, assistant)
		case "/api/assistant/status":
			serveClassicMindMapAIStatus(w, r, assistant)
		case "/api/assistant/cancel":
			serveClassicMindMapAICancel(w, r, assistant)
		case "/api/assistant/publish":
			serveClassicMindMapAIPublish(w, r, assistant)
		default:
			http.NotFound(w, r)
		}
	})
}

type classicMindMapRefRequest struct {
	MapID string `json:"map_id"`
}

type classicMindMapListRequest struct {
	IncludeArchived bool `json:"include_archived"`
}

type classicMindMapCreateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type classicMindMapEditRequest struct {
	MapID            string                `json:"map_id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Title            *string               `json:"title,omitempty"`
	Description      *string               `json:"description,omitempty"`
	Status           *ClassicMindMapStatus `json:"status,omitempty"`
}

type classicMindMapDuplicateRequest struct {
	MapID string `json:"map_id"`
	Title string `json:"title"`
}

type classicMindMapExportRequest struct {
	MapID               string                     `json:"map_id"`
	Format              ClassicMindMapExportFormat `json:"format"`
	ExpectedRevision    int64                      `json:"expected_revision"`
	ExpectedDigest      string                     `json:"expected_digest"`
	ExpectedStateDigest string                     `json:"expected_state_digest"`
}

type classicMindMapWorkbenchBranchRequest struct {
	MapID                  string `json:"map_id"`
	NodeID                 string `json:"node_id"`
	Question               string `json:"question,omitempty"`
	ExpectedRevision       int64  `json:"expected_revision,omitempty"`
	ExpectedDigest         string `json:"expected_digest,omitempty"`
	ExpectedStateDigest    string `json:"expected_state_digest,omitempty"`
	ExpectedManifestDigest string `json:"expected_manifest_digest,omitempty"`
}

type classicMindMapWorkbenchCompareRequest struct {
	LeftMapID           string `json:"left_map_id"`
	RightMapID          string `json:"right_map_id"`
	LeftExpectedDigest  string `json:"left_expected_digest,omitempty"`
	RightExpectedDigest string `json:"right_expected_digest,omitempty"`
}

type classicMindMapExportFunc func(ClassicMindMapExportRequest) (ClassicMindMapExportArtifact, error)

var classicMindMapPNGExportSlots = make(chan struct{}, 1)

type classicMindMapNodeAddRequest struct {
	MapID            string                 `json:"map_id"`
	ParentID         string                 `json:"parent_id"`
	Label            string                 `json:"label"`
	Position         int                    `json:"position"`
	Kind             ClassicMindMapNodeKind `json:"kind"`
	Summary          string                 `json:"summary"`
	BodyMarkdown     string                 `json:"body_markdown"`
	ExpectedRevision int64                  `json:"expected_revision"`
}

type classicMindMapNodeEditRequest struct {
	MapID            string                  `json:"map_id"`
	NodeID           string                  `json:"node_id"`
	Label            *string                 `json:"label,omitempty"`
	Summary          *string                 `json:"summary,omitempty"`
	BodyMarkdown     *string                 `json:"body_markdown,omitempty"`
	Kind             *ClassicMindMapNodeKind `json:"kind,omitempty"`
	Locked           *bool                   `json:"locked,omitempty"`
	ExpectedRevision int64                   `json:"expected_revision"`
}

type classicMindMapNodeMoveRequest struct {
	MapID            string `json:"map_id"`
	NodeID           string `json:"node_id"`
	ParentID         string `json:"parent_id"`
	Position         int    `json:"position"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type classicMindMapNodeDeleteRequest struct {
	MapID            string                   `json:"map_id"`
	NodeID           string                   `json:"node_id"`
	Mode             ClassicMindMapDeleteMode `json:"mode"`
	ExpectedRevision int64                    `json:"expected_revision"`
}

type classicMindMapHistoryRequest struct {
	MapID string `json:"map_id"`
	Limit int    `json:"limit"`
}

type classicMindMapUndoRequest struct {
	MapID            string `json:"map_id"`
	ChangeID         int64  `json:"change_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type classicMindMapSnapshotCreateRequest struct {
	MapID            string `json:"map_id"`
	Reason           string `json:"reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type classicMindMapSourceSearchRequest struct {
	Query    string `json:"query"`
	Document string `json:"document"`
	Page     int    `json:"page"`
	Limit    int    `json:"limit"`
}

type classicMindMapKnowledgeSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type classicMindMapEvidenceAddRequest struct {
	MapID            string `json:"map_id"`
	NodeID           string `json:"node_id"`
	EntryID          int64  `json:"entry_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type classicMindMapSourceAddRequest struct {
	MapID            string                   `json:"map_id"`
	NodeID           string                   `json:"node_id"`
	Kind             ClassicMindMapSourceKind `json:"kind"`
	Title            string                   `json:"title"`
	Locator          string                   `json:"locator"`
	URL              string                   `json:"url"`
	KnowledgeNodeID  string                   `json:"knowledge_node_id"`
	ExpectedRevision int64                    `json:"expected_revision"`
}

type classicMindMapSourceMutationRequest struct {
	MapID            string `json:"map_id"`
	NodeID           string `json:"node_id"`
	SourceID         string `json:"source_id"`
	Position         int    `json:"position"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func serveClassicMindMapList(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapListRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	items, err := store.ListClassicMindMaps(request.IncludeArchived)
	writeClassicMindMapResult(w, items, err)
}

func serveClassicMindMapLoad(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapRefRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.LoadClassicMindMap(request.MapID)
	if err == nil {
		err = validateClassicMindMapBrowserRender(doc.Nodes)
	}
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapCreate(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapCreateRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.CreateClassicMindMap(request.Title, request.Description)
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapEdit(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapEditRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.EditClassicMindMap(request.MapID, ClassicMindMapPatch{
		Title: request.Title, Description: request.Description, Status: request.Status,
	}, request.ExpectedRevision, "browser", "изменено в редакторе")
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapDuplicate(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapDuplicateRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.DuplicateClassicMindMap(request.MapID, request.Title)
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapExport(w http.ResponseWriter, r *http.Request, store *Store) {
	serveClassicMindMapExportWith(w, r, store.ExportClassicMindMap, classicMindMapPNGExportSlots)
}

func serveClassicMindMapExportWith(w http.ResponseWriter, r *http.Request, export classicMindMapExportFunc, pngSlots chan struct{}) {
	var request classicMindMapExportRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	if classicMindMapExportRequestCancelled(w, r) {
		return
	}
	if request.ExpectedRevision <= 0 || strings.TrimSpace(request.ExpectedDigest) == "" || strings.TrimSpace(request.ExpectedStateDigest) == "" {
		http.Error(w, "mind map export requires expected_revision, expected_digest and expected_state_digest", http.StatusBadRequest)
		return
	}
	if request.Format == ClassicMindMapExportPNG {
		select {
		case pngSlots <- struct{}{}:
			defer func() { <-pngSlots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "PNG export is busy; retry shortly", http.StatusTooManyRequests)
			return
		}
		if classicMindMapExportRequestCancelled(w, r) {
			return
		}
	}
	artifact, err := export(ClassicMindMapExportRequest{
		MapRef:              request.MapID,
		Format:              request.Format,
		ExpectedRevision:    request.ExpectedRevision,
		ExpectedDigest:      request.ExpectedDigest,
		ExpectedStateDigest: request.ExpectedStateDigest,
	})
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, ErrClassicMindMapNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrClassicMindMapExportChanged):
			status = http.StatusConflict
		}
		http.Error(w, strings.TrimSpace(err.Error()), status)
		return
	}
	if classicMindMapExportRequestCancelled(w, r) {
		return
	}
	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Filename}))
	w.Header().Set("X-Mem-Map-Revision", strconv.FormatInt(artifact.Revision, 10))
	w.Header().Set("X-Mem-Map-Digest", artifact.Digest)
	w.Header().Set("X-Mem-Map-State-Digest", artifact.StateDigest)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(artifact.Data)
}

func classicMindMapExportRequestCancelled(w http.ResponseWriter, r *http.Request) bool {
	select {
	case <-r.Context().Done():
		http.Error(w, "mind map export request was cancelled", http.StatusRequestTimeout)
		return true
	default:
		return false
	}
}

func serveClassicMindMapTemplates(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	writeClassicMindMapResult(w, ListClassicMindMapTemplates(), nil)
}

func serveClassicMindMapTemplateCreate(w http.ResponseWriter, r *http.Request, store *Store) {
	var request ClassicMindMapTemplateRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.CreateClassicMindMapFromTemplate(request, "browser")
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapBranchManifest(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapWorkbenchBranchRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	manifest, err := store.BuildClassicMindMapBranchManifest(classicMindMapBranchRequest(request))
	writeClassicMindMapResult(w, manifest, err)
}

func serveClassicMindMapBranchQuestion(w http.ResponseWriter, r *http.Request, store *Store, assistant *ClassicMindMapAIWorkspace) {
	var request classicMindMapWorkbenchBranchRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	if assistant == nil || assistant.Service == nil {
		http.Error(w, "answer-модель для вопроса по ветви недоступна", http.StatusServiceUnavailable)
		return
	}
	answer, err := store.AnswerClassicMindMapBranch(r.Context(), assistant.Service, ClassicMindMapBranchQuestionRequest{
		ClassicMindMapBranchRequest: classicMindMapBranchRequest(request), Question: request.Question,
		ExpectedManifestDigest: request.ExpectedManifestDigest,
	})
	writeClassicMindMapResult(w, answer, err)
}

func serveClassicMindMapCompare(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapWorkbenchCompareRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	comparison, err := store.CompareClassicMindMaps(ClassicMindMapCompareRequest{
		LeftMapRef: request.LeftMapID, RightMapRef: request.RightMapID,
		LeftExpectedDigest: request.LeftExpectedDigest, RightExpectedDigest: request.RightExpectedDigest,
	})
	writeClassicMindMapResult(w, comparison, err)
}

func serveClassicMindMapStudy(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapWorkbenchBranchRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	pack, err := store.BuildClassicMindMapStudyPack(classicMindMapBranchRequest(request))
	writeClassicMindMapResult(w, pack, err)
}

func classicMindMapBranchRequest(request classicMindMapWorkbenchBranchRequest) ClassicMindMapBranchRequest {
	return ClassicMindMapBranchRequest{MapRef: request.MapID, NodeRef: request.NodeID,
		ExpectedRevision: request.ExpectedRevision, ExpectedDigest: request.ExpectedDigest,
		ExpectedStateDigest: request.ExpectedStateDigest}
}

func serveClassicMindMapNodeAdd(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapNodeAddRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, node, err := store.AddClassicMindMapNode(request.MapID, request.ParentID, request.Label,
		request.Position, request.Kind, request.Summary, request.BodyMarkdown, request.ExpectedRevision,
		"browser", "добавлен узел в редакторе")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Node     ClassicMindMapNode     `json:"node"`
	}{doc, node}, err)
}

func serveClassicMindMapNodeEdit(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapNodeEditRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, node, err := store.EditClassicMindMapNode(request.MapID, request.NodeID, ClassicMindMapNodePatch{
		Label: request.Label, Summary: request.Summary, BodyMarkdown: request.BodyMarkdown,
		Kind: request.Kind, Locked: request.Locked,
	}, request.ExpectedRevision, "browser", "изменён узел в редакторе")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Node     ClassicMindMapNode     `json:"node"`
	}{doc, node}, err)
}

func serveClassicMindMapNodeMove(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapNodeMoveRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, node, err := store.MoveClassicMindMapNode(request.MapID, request.NodeID, request.ParentID,
		request.Position, request.ExpectedRevision, "browser", "перемещён узел в редакторе")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Node     ClassicMindMapNode     `json:"node"`
	}{doc, node}, err)
}

func serveClassicMindMapNodeDelete(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapNodeDeleteRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.DeleteClassicMindMapNode(request.MapID, request.NodeID, request.Mode,
		request.ExpectedRevision, "browser", "удалён узел в редакторе")
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapSourceDocuments(w http.ResponseWriter, r *http.Request, store *Store) {
	var request struct{}
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	writeClassicMindMapResult(w, store.ListClassicMindMapSourceDocuments(), nil)
}

func serveClassicMindMapSourceSearch(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapSourceSearchRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	items, err := store.SearchClassicMindMapEvidence(ClassicMindMapEvidenceSearchOptions{
		Query: request.Query, Document: request.Document, Page: request.Page, Limit: request.Limit,
	})
	writeClassicMindMapResult(w, items, err)
}

func serveClassicMindMapKnowledgeSearch(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapKnowledgeSearchRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	items, err := store.SearchClassicMindMapKnowledgeNodes(request.Query, request.Limit)
	writeClassicMindMapResult(w, items, err)
}

func serveClassicMindMapEvidenceAdd(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapEvidenceAddRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	entry, err := store.GetByID(request.EntryID)
	if err != nil {
		writeClassicMindMapResult(w, nil, err)
		return
	}
	anchor, err := EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		writeClassicMindMapResult(w, nil, err)
		return
	}
	doc, source, err := store.AttachClassicMindMapEvidence(request.MapID, request.NodeID, anchor,
		request.ExpectedRevision, "browser", "привязан фрагмент активной базы")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Source   ClassicMindMapSource   `json:"source"`
	}{doc, source}, err)
}

func serveClassicMindMapSourceAdd(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapSourceAddRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	if request.Kind == ClassicMindMapSourceExternalFile {
		path := strings.TrimSpace(request.Locator)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			http.Error(w, "локальный файл должен быть указан абсолютным нормализованным путём", http.StatusBadRequest)
			return
		}
	}
	if !classicMindMapSourceAddFieldsMatchKind(request) {
		http.Error(w, "источник содержит поля другого типа", http.StatusBadRequest)
		return
	}
	doc, source, err := store.AttachClassicMindMapSource(request.MapID, request.NodeID, ClassicMindMapSource{
		Kind: request.Kind, Title: request.Title, Locator: request.Locator,
		URL: request.URL, KnowledgeNodeID: request.KnowledgeNodeID,
	}, request.ExpectedRevision, "browser", "привязан внешний источник")
	var pathErr *os.PathError
	if request.Kind == ClassicMindMapSourceExternalFile && errors.As(err, &pathErr) {
		http.Error(w, "локальный файл не найден или недоступен", http.StatusBadRequest)
		return
	}
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Source   ClassicMindMapSource   `json:"source"`
	}{doc, source}, err)
}

func classicMindMapSourceAddFieldsMatchKind(request classicMindMapSourceAddRequest) bool {
	locator := strings.TrimSpace(request.Locator)
	webURL := strings.TrimSpace(request.URL)
	knowledgeNodeID := strings.TrimSpace(request.KnowledgeNodeID)
	switch request.Kind {
	case ClassicMindMapSourceExternalFile:
		return locator != "" && webURL == "" && knowledgeNodeID == ""
	case ClassicMindMapSourceURL:
		return locator == "" && webURL != "" && knowledgeNodeID == ""
	case ClassicMindMapSourceKnowledgeNode:
		return locator == "" && webURL == "" && knowledgeNodeID != ""
	default:
		return false
	}
}

func serveClassicMindMapSourceUpload(w http.ResponseWriter, r *http.Request, store *Store) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxClassicMindMapUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(MaxClassicMindMapUploadBytes + (1 << 20)); err != nil {
		http.Error(w, "не удалось прочитать выбранный файл", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "файл не выбран", http.StatusBadRequest)
		return
	}
	defer file.Close()
	expectedRevision, err := strconv.ParseInt(r.FormValue("expected_revision"), 10, 64)
	if err != nil || expectedRevision < 0 {
		http.Error(w, "expected_revision некорректен", http.StatusBadRequest)
		return
	}
	path, digest, err := store.ImportClassicMindMapAttachment(header.Filename, file)
	if err != nil {
		if strings.Contains(err.Error(), "превышает лимит") {
			http.Error(w, "выбранный файл превышает допустимый размер", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "не удалось безопасно сохранить выбранный файл", http.StatusInternalServerError)
		}
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	doc, source, err := store.AttachClassicMindMapSource(r.FormValue("map_id"), r.FormValue("node_id"), ClassicMindMapSource{
		Kind: ClassicMindMapSourceExternalFile, Title: title, Locator: path,
	}, expectedRevision, "browser", "импортирован и привязан файл "+digest)
	if err != nil {
		_ = os.Remove(path)
	}
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Source   ClassicMindMapSource   `json:"source"`
	}{doc, source}, err)
}

func serveClassicMindMapSourceRemove(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapSourceMutationRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.DetachClassicMindMapSource(request.MapID, request.NodeID, request.SourceID,
		request.ExpectedRevision, "browser", "источник отвязан в редакторе")
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapSourceMove(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapSourceMutationRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, err := store.MoveClassicMindMapSource(request.MapID, request.NodeID, request.SourceID,
		request.Position, request.ExpectedRevision, "browser", "изменён порядок источников")
	writeClassicMindMapResult(w, doc, err)
}

func serveClassicMindMapSourceFile(w http.ResponseWriter, r *http.Request, store *Store, sessionToken string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !knowledgeMapSourceAuthorized(r, sessionToken) {
		http.Error(w, "forbidden source request", http.StatusForbidden)
		return
	}
	mapIDs, sourceIDs := r.URL.Query()["map_id"], r.URL.Query()["source_id"]
	if len(mapIDs) != 1 || len(sourceIDs) != 1 {
		http.NotFound(w, r)
		return
	}
	resolved, err := store.ResolveClassicMindMapFileSource(mapIDs[0], sourceIDs[0])
	if err != nil {
		// Resolution errors can contain absolute paths and storage details. The
		// browser only needs to know that its pinned source is no longer usable.
		http.Error(w, "источник недоступен или изменён", http.StatusConflict)
		return
	}
	file, err := os.Open(resolved.Path)
	if err != nil {
		http.Error(w, "файл источника недоступен", http.StatusGone)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "файл источника недоступен", http.StatusGone)
		return
	}
	mediaType, disposition := classicMindMapSourcePresentation(resolved.MediaType)
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(resolved.Path)}))
	// Source documents share the editor's loopback origin. Give even otherwise
	// safe inline formats a scriptless unique-origin sandbox. Active formats
	// such as HTML, SVG and XML are additionally forced to octet-stream and
	// attachment so they cannot inherit the editor origin or read its token.
	w.Header().Set("Content-Security-Policy", classicMindMapSourceSandboxPolicy)
	http.ServeContent(w, r, filepath.Base(resolved.Path), info.ModTime(), file)
}

func classicMindMapSourcePresentation(rawMediaType string) (mediaType, disposition string) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(rawMediaType))
	if err != nil {
		return "application/octet-stream", "attachment"
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "application/pdf", "text/plain", "text/csv", "text/markdown",
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon":
		return mediaType, "inline"
	}
	if strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") {
		return mediaType, "inline"
	}
	return "application/octet-stream", "attachment"
}

func serveClassicMindMapHistory(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapHistoryRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	changes, err := store.ListClassicMindMapChanges(request.MapID, request.Limit)
	writeClassicMindMapResult(w, changes, err)
}

func serveClassicMindMapUndo(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapUndoRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, change, err := store.UndoClassicMindMapChange(request.MapID, request.ChangeID,
		request.ExpectedRevision, "browser", "отмена из редактора")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Change   ClassicMindMapChange   `json:"change"`
	}{doc, change}, err)
}

func serveClassicMindMapRedo(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapUndoRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	doc, change, err := store.RedoClassicMindMapChange(request.MapID, request.ExpectedRevision,
		"browser", "повтор из редактора")
	writeClassicMindMapResult(w, struct {
		Document ClassicMindMapDocument `json:"document"`
		Change   ClassicMindMapChange   `json:"change"`
	}{doc, change}, err)
}

func serveClassicMindMapSnapshots(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapHistoryRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	items, err := store.ListClassicMindMapSnapshots(request.MapID, request.Limit)
	writeClassicMindMapResult(w, items, err)
}

func serveClassicMindMapSnapshotCreate(w http.ResponseWriter, r *http.Request, store *Store) {
	var request classicMindMapSnapshotCreateRequest
	if !decodeClassicMindMapJSON(w, r, &request) {
		return
	}
	digest, err := store.CreateClassicMindMapSnapshot(request.MapID, request.Reason, request.ExpectedRevision)
	writeClassicMindMapResult(w, map[string]string{"digest": digest}, err)
}

func decodeClassicMindMapJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if r.ContentLength > MaxClassicMindMapRequestJSON {
		http.Error(w, "mind map request is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxClassicMindMapRequestJSON)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "mind map request is too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid mind map request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "mind map request must contain one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

func writeClassicMindMapResult(w http.ResponseWriter, result any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, ErrClassicMindMapNotFound), errors.Is(err, ErrClassicMindMapNodeNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrClassicMindMapRevisionConflict), errors.Is(err, ErrClassicMindMapLocked):
			status = http.StatusConflict
		case errors.Is(err, ErrClassicMindMapRenderLimit):
			status = http.StatusUnprocessableEntity
		}
		http.Error(w, strings.TrimSpace(err.Error()), status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, "не удалось сформировать ответ редактора", http.StatusInternalServerError)
	}
}

func parseClassicMindMapLimit(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
