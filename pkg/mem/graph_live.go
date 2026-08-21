package mem

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
)

// NewKnowledgeMapLiveHandler returns a read-only handler that renders the
// current graph directly from store on every request. Network binding and
// process lifetime remain the caller's responsibility.
func NewKnowledgeMapLiveHandler(store *Store, title string) http.Handler {
	return newKnowledgeMapHandler(store, title, nil)
}

// NewKnowledgeMapWorkspaceHandler returns a live handler that may persist only
// visual layout state. Mutations require a same-origin request plus the
// short-lived session capability embedded in the generated page.
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
		default:
			http.NotFound(w, r)
		}
	})
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
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		http.Error(w, fmt.Sprintf("build knowledge map: %v", err), http.StatusInternalServerError)
		return
	}
	if workspace != nil {
		copy := *workspace
		data.Workspace = &copy
		if copy.ViewName != DefaultKnowledgeMapView {
			data.Layout, err = store.LoadKnowledgeMapLayout(copy.ViewName)
			if err != nil {
				http.Error(w, fmt.Sprintf("load knowledge map layout: %v", err), http.StatusInternalServerError)
				return
			}
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
	if r.Method == http.MethodDelete {
		if err := store.DeleteKnowledgeMapLayout(workspace.ViewName); err != nil {
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
	saved, err := store.SaveKnowledgeMapLayout(workspace.ViewName, layout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(saved); err != nil {
		return
	}
}

func knowledgeMapWorkspaceAuthorized(r *http.Request, expectedToken string) bool {
	if expectedToken == "" || !strings.EqualFold(r.Header.Get("Origin"), "http://"+r.Host) {
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
