package applog

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, e := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, e
}
func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// HTTP logs technical metadata, never query strings, bodies, headers, usernames
// or raw paths. Route segments are allowlisted; dynamic parts become hashes.
func HTTP(component string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Load() == nil {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		id := spanSequence.Add(1)
		f := Fields{"request_id": id, "method": safeMethod(r.Method), "route": route(r.URL.Path)}
		Event("DEBUG", component+".request", f)
		rw := &responseWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			if p != nil {
				rw.status = 500
				f["panic_type"] = fmt.Sprintf("%T", p)
				f["stack"] = stackFrames()
			}
			status := rw.status
			if status == 0 {
				status = 200
			}
			f["status"] = status
			f["bytes"] = rw.bytes
			f["duration_ms"] = time.Since(start).Milliseconds()
			level := "INFO"
			if status >= 500 {
				level = "ERROR"
			} else if status >= 400 {
				level = "WARN"
			}
			Event(level, component+".response", f)
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(rw, r)
	})
}
func safeMethod(method string) string {
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return method
	}
	return "OTHER"
}
func route(path string) string {
	if len(path) > 4096 {
		return "/{" + Ref(path) + "}"
	}
	allowed := " api v1 workspaces maps nodes sources document file login logout session assets healthz readyz library query search ask jobs notes settings snapshots history delete duplicate ai cancel apply from-sources entries import index branch export "
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p != "" && !strings.Contains(allowed, " "+p+" ") {
			parts[i] = "{" + Ref(p) + "}"
		}
	}
	return strings.Join(parts, "/")
}

// Transport wraps an individual client, preserving its transport/TLS settings.
type Transport struct {
	Base      http.RoundTripper
	Component string
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	start := time.Now()
	id := spanSequence.Add(1)
	f := Fields{"request_id": id, "method": safeMethod(r.Method), "endpoint_id": Ref(r.URL.Scheme + "://" + r.URL.Host + r.URL.Path)}
	if deadline, ok := r.Context().Deadline(); ok {
		f["deadline_remaining_ms"] = time.Until(deadline).Milliseconds()
	}
	Event("DEBUG", t.Component+".request", f)
	resp, err := base.RoundTrip(r)
	f["duration_ms"] = time.Since(start).Milliseconds()
	level := "INFO"
	if resp != nil {
		f["status"] = resp.StatusCode
		if resp.StatusCode >= 400 {
			level = "WARN"
		}
	}
	if err != nil {
		level = "ERROR"
		for k, v := range ErrorFields(err) {
			f[k] = v
		}
	}
	Event(level, t.Component+".response", f)
	return resp, err
}
