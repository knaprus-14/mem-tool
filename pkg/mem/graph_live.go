package mem

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// NewKnowledgeMapLiveHandler returns a read-only handler that renders the
// current graph directly from store on every request. Network binding and
// process lifetime remain the caller's responsibility.
func NewKnowledgeMapLiveHandler(store *Store, title string) http.Handler {
	return newKnowledgeMapHandler(store, title, nil)
}

// NewKnowledgeMapWorkspaceHandler returns a live handler for local source
// navigation, visual layout persistence, and pinned review actions. Mutations
// require a same-origin request plus the short-lived session capability
// embedded in the generated page.
func NewKnowledgeMapWorkspaceHandler(store *Store, title, sessionToken, viewName string) http.Handler {
	if strings.TrimSpace(viewName) == "" {
		viewName = DefaultKnowledgeMapView
	}
	return newKnowledgeMapHandler(store, title, &KnowledgeMapWorkspace{
		SessionToken: sessionToken,
		ViewName:     viewName,
	})
}

func newKnowledgeMapHandler(store *Store, title string, workspace *KnowledgeMapWorkspace) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setKnowledgeMapSecurityHeaders(w.Header())
		if !knowledgeMapLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/":
			serveKnowledgeMapPage(w, r, store, title, workspace)
		case "/api/layout":
			serveKnowledgeMapLayout(w, r, store, workspace)
		case "/api/source":
			serveKnowledgeMapSource(w, r, store, workspace)
		case "/api/review/approve":
			serveKnowledgeMapApproval(w, r, store, workspace)
		case "/api/review/reject":
			serveKnowledgeMapReviewMutation(w, r, store, workspace, KnowledgeReviewActionReject)
		case "/api/review/reopen":
			serveKnowledgeMapReviewMutation(w, r, store, workspace, KnowledgeReviewActionReopen)
		case "/api/review/undo":
			serveKnowledgeMapReviewMutation(w, r, store, workspace, KnowledgeReviewActionUndo)
		case "/api/edit":
			serveKnowledgeMapEditMutation(w, r, store, workspace, KnowledgeEditActionEdit)
		case "/api/edit/undo":
			serveKnowledgeMapEditMutation(w, r, store, workspace, KnowledgeEditActionUndo)
		case "/api/workspace/create":
			serveKnowledgeMapWorkspaceCreate(w, r, store, workspace)
		default:
			http.NotFound(w, r)
		}
	})
}

const MaxKnowledgeMapWorkspaceCreateJSON = 128 << 10

func serveKnowledgeMapWorkspaceCreate(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapWorkspaceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden workspace creation request", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxKnowledgeMapWorkspaceCreateJSON {
		http.Error(w, "workspace creation request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeMapWorkspaceCreateJSON)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request KnowledgeWorkspaceCreateRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "workspace creation request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid workspace creation request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "workspace creation request must contain one JSON object", http.StatusBadRequest)
		return
	}
	result, err := store.CreateKnowledgeWorkspaceNode(request)
	if err != nil {
		switch {
		case errors.Is(err, ErrKnowledgeContentChanged), errors.Is(err, ErrKnowledgeEvidenceChanged),
			errors.Is(err, ErrKnowledgeEvidenceNotCurrent):
			http.Error(w, "parent state changed; refresh the map before retrying", http.StatusConflict)
		default:
			http.Error(w, "workspace creation request was rejected", http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

func serveKnowledgeMapEditMutation(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace, action string) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapWorkspaceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden edit request", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxKnowledgeMapEditJSON {
		http.Error(w, "edit request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeMapEditJSON)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var editRequest KnowledgeEditRequest
	var undoRequest KnowledgeEditUndoRequest
	switch action {
	case KnowledgeEditActionEdit:
		err = decoder.Decode(&editRequest)
	case KnowledgeEditActionUndo:
		err = decoder.Decode(&undoRequest)
	default:
		err = errors.New("unsupported edit action")
	}
	if err == nil {
		if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
			err = errors.New("edit request must contain one JSON object")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "edit request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid edit request", http.StatusBadRequest)
		return
	}
	var result KnowledgeEditResult
	switch action {
	case KnowledgeEditActionEdit:
		result, err = store.EditKnowledgeObject(editRequest)
	case KnowledgeEditActionUndo:
		result, err = store.UndoKnowledgeEdit(undoRequest)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "edit request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		switch {
		case errors.Is(err, ErrKnowledgeContentChanged), errors.Is(err, ErrKnowledgeEditChanged),
			errors.Is(err, ErrKnowledgeEditNotReversible), errors.Is(err, ErrKnowledgeEvidenceChanged),
			errors.Is(err, ErrKnowledgeEvidenceNotCurrent), errors.Is(err, ErrKnowledgeActiveRelations),
			errors.Is(err, ErrKnowledgeEndpointsNotActive):
			http.Error(w, "edit state changed; refresh the map before retrying", http.StatusConflict)
		default:
			http.Error(w, "edit request was rejected", http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

const MaxKnowledgeMapEditJSON = 512 << 10

func serveKnowledgeMapReviewMutation(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace, action string) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapWorkspaceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden review request", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxKnowledgeMapReviewJSON {
		http.Error(w, "review request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeMapReviewJSON)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request KnowledgeReviewMutationRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "review request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid review request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "review request must contain one JSON object", http.StatusBadRequest)
		return
	}
	var result KnowledgeReviewMutationResult
	switch action {
	case KnowledgeReviewActionReject:
		result, err = store.RejectKnowledgeObject(request)
	case KnowledgeReviewActionReopen:
		result, err = store.ReopenKnowledgeObject(request)
	case KnowledgeReviewActionUndo:
		result, err = store.UndoKnowledgeReview(request)
	default:
		err = errors.New("unsupported review action")
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrKnowledgeEvidenceChanged), errors.Is(err, ErrKnowledgeEvidenceNotCurrent),
			errors.Is(err, ErrKnowledgeReviewChanged), errors.Is(err, ErrKnowledgeActiveRelations),
			errors.Is(err, ErrKnowledgeReviewNotReversible):
			http.Error(w, "review state changed; refresh the map before retrying", http.StatusConflict)
		default:
			http.Error(w, "review request was rejected", http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

const MaxKnowledgeMapReviewJSON = 32 << 10

func serveKnowledgeMapApproval(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapWorkspaceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden review request", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxKnowledgeMapReviewJSON {
		http.Error(w, "review request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeMapReviewJSON)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request KnowledgeApprovalRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "review request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid review request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "review request must contain one JSON object", http.StatusBadRequest)
		return
	}
	batch, err := store.ApproveKnowledgeObjects([]KnowledgeApprovalRequest{request})
	if err != nil {
		switch {
		case errors.Is(err, ErrKnowledgeEvidenceChanged):
			http.Error(w, "source evidence changed; refresh the map before approval", http.StatusConflict)
		case errors.Is(err, ErrKnowledgeEvidenceNotCurrent):
			http.Error(w, "source evidence is not current", http.StatusConflict)
		case errors.Is(err, ErrKnowledgeEndpointsNotActive):
			http.Error(w, "approve both endpoint nodes before approving this relation", http.StatusConflict)
		default:
			http.Error(w, "review request was rejected", http.StatusBadRequest)
		}
		return
	}
	approved := batch.Approved[0]
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(approved); err != nil {
		return
	}
}

func serveKnowledgeMapPage(w http.ResponseWriter, r *http.Request, store *Store, title string, workspace *KnowledgeMapWorkspace) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	viewName := DefaultKnowledgeMapView
	if workspace != nil {
		requestedView, err := knowledgeMapRequestedView(r, workspace.ViewName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		viewName = requestedView
	}
	data, err := store.BuildKnowledgeMapViewDataForView(viewName)
	if err != nil {
		http.Error(w, fmt.Sprintf("build knowledge map: %v", err), http.StatusInternalServerError)
		return
	}
	if workspace != nil {
		copy := *workspace
		copy.ViewName = viewName
		copy.Views, err = store.ListKnowledgeMapViews()
		if err != nil {
			http.Error(w, fmt.Sprintf("list knowledge map views: %v", err), http.StatusInternalServerError)
			return
		}
		data.Workspace = &copy
		if copy.SessionToken != "" {
			http.SetCookie(w, knowledgeMapSourceCookie(copy.SessionToken))
		}
	}
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, title, data); err != nil {
		http.Error(w, fmt.Sprintf("render knowledge map: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", output.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(output.Bytes())
	}
}

func serveKnowledgeMapSource(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapSourceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden source request", http.StatusForbidden)
		return
	}
	values := r.URL.Query()["citation"]
	if len(values) != 1 {
		http.NotFound(w, r)
		return
	}
	anchor, err := store.ResolveKnowledgeMapPDFSource(values[0])
	if err != nil {
		switch {
		case errors.Is(err, ErrKnowledgeMapSourceNotFound):
			http.NotFound(w, r)
		case errors.Is(err, ErrKnowledgeMapSourceNotCurrent), errors.Is(err, ErrKnowledgeMapSourceAmbiguous):
			http.Error(w, "source evidence is not current", http.StatusConflict)
		case errors.Is(err, ErrKnowledgeMapSourceUnsupported):
			http.Error(w, "source is not a page-addressable PDF", http.StatusUnsupportedMediaType)
		default:
			http.Error(w, fmt.Sprintf("resolve knowledge map source: %v", err), http.StatusInternalServerError)
		}
		return
	}
	file, err := os.Open(anchor.SourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "source file is unavailable", http.StatusGone)
			return
		}
		http.Error(w, fmt.Sprintf("open knowledge map source: %v", err), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "source file is unavailable", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(anchor.SourcePath)}))
	http.ServeContent(w, r, filepath.Base(anchor.SourcePath), info.ModTime(), file)
}

func knowledgeMapSourceCookie(sessionToken string) *http.Cookie {
	return &http.Cookie{
		Name:     knowledgeMapSourceCookieName(sessionToken),
		Value:    sessionToken,
		Path:     "/api/source",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func knowledgeMapSourceCookieName(sessionToken string) string {
	digest := sha256.Sum256([]byte(sessionToken))
	return fmt.Sprintf("mem_map_source_%x", digest[:8])
}

func knowledgeMapSourceAuthorized(r *http.Request, expectedToken string) bool {
	if expectedToken == "" {
		return false
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" {
		return false
	}
	cookie, err := r.Cookie(knowledgeMapSourceCookieName(expectedToken))
	if err != nil || len(cookie.Value) != len(expectedToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expectedToken)) == 1
}

func serveKnowledgeMapLayout(w http.ResponseWriter, r *http.Request, store *Store, workspace *KnowledgeMapWorkspace) {
	if workspace == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if store == nil {
		http.Error(w, "knowledge map store is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !knowledgeMapWorkspaceAuthorized(r, workspace.SessionToken) {
		http.Error(w, "forbidden workspace request", http.StatusForbidden)
		return
	}
	viewName, err := knowledgeMapRequestedView(r, workspace.ViewName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		if err := store.DeleteKnowledgeMapLayout(viewName); err != nil {
			http.Error(w, fmt.Sprintf("delete knowledge map layout: %v", err), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxKnowledgeMapLayoutJSON {
		http.Error(w, "knowledge map layout is too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeMapLayoutJSON)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "knowledge map layout is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("read knowledge map layout: %v", err), http.StatusBadRequest)
		return
	}
	layout, err := decodeKnowledgeMapLayout(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := store.SaveKnowledgeMapLayout(viewName, layout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(saved); err != nil {
		return
	}
}

func knowledgeMapRequestedView(r *http.Request, fallback string) (string, error) {
	values, present := r.URL.Query()["view"]
	if !present {
		return validateKnowledgeMapViewName(fallback)
	}
	if len(values) != 1 {
		return "", errors.New("knowledge map request must contain one view")
	}
	return validateKnowledgeMapViewName(values[0])
}

func knowledgeMapWorkspaceAuthorized(r *http.Request, expectedToken string) bool {
	if expectedToken == "" || !strings.EqualFold(r.Header.Get("Origin"), "http://"+r.Host) {
		return false
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" {
		return false
	}
	provided := r.Header.Get("X-Mem-Session")
	return len(provided) == len(expectedToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(expectedToken)) == 1
}

func setKnowledgeMapSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func knowledgeMapLoopbackHost(hostPort string) bool {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return false
	}
	host := hostPort
	if parsed, _, err := net.SplitHostPort(hostPort); err == nil {
		host = parsed
	} else if strings.HasPrefix(hostPort, "[") && strings.HasSuffix(hostPort, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(hostPort, "["), "]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
