package mem

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const MaxClassicMindMapRequestJSON = 1 << 20

// NewClassicMindMapWorkspaceHandler exposes a loopback-only library and tree
// editor. Every data request requires the short-lived capability embedded in
// the page plus a same-origin browser request.
func NewClassicMindMapWorkspaceHandler(store *Store, sessionToken string) http.Handler {
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
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
		if store == nil {
			http.Error(w, "mind map store is unavailable", http.StatusServiceUnavailable)
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
		case "/api/nodes/add":
			serveClassicMindMapNodeAdd(w, r, store)
		case "/api/nodes/edit":
			serveClassicMindMapNodeEdit(w, r, store)
		case "/api/nodes/move":
			serveClassicMindMapNodeMove(w, r, store)
		case "/api/nodes/delete":
			serveClassicMindMapNodeDelete(w, r, store)
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
		}
		http.Error(w, strings.TrimSpace(err.Error()), status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, fmt.Sprintf("encode mind map response: %v", err), http.StatusInternalServerError)
	}
}

func parseClassicMindMapLimit(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
