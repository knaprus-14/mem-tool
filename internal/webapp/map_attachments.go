package webapp

import (
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"io"
	"mime"
	"net/http"
	"strconv"
)

func (a *App) mapAttachmentRoute(w http.ResponseWriter, r *http.Request, dir, actor string, p []string) bool {
	if len(p) == 6 && p[5] == "graph-candidates" && r.Method == "GET" {
		offset, err := integerParam(r, "offset", 0, 0, 1000000)
		if err != nil {
			a.mapError(w, mem.ErrWebMapSourceInvalid)
			return true
		}
		items, err := mem.ListWebGraphCandidatesIn(r.Context(), dir, offset)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, items)
		return true
	}
	if len(p) == 9 && p[6] == "nodes" && p[8] == "attachment" && r.Method == "POST" {
		revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil {
			a.mapError(w, mem.ErrWebMapSourceInvalid)
			return true
		}
		r.Body = http.MaxBytesReader(w, r.Body, mem.MaxMapAttachmentBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, 413, "Вложение должно быть не больше 8 МиБ")
			return true
		}
		result, err := mem.AttachWebMapFileIn(r.Context(), dir, actor, p[5], p[7], mem.WebMapDelete{Revision: revision, RequestID: r.URL.Query().Get("request_id")}, r.URL.Query().Get("name"), data)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, result)
		return true
	}
	if len(p) == 9 && p[6] == "nodes" && p[8] == "graph-source" && r.Method == "POST" {
		var in struct {
			Revision  int64  `json:"revision"`
			RequestID string `json:"request_id"`
			GraphID   string `json:"graph_id"`
		}
		if !decode(w, r, &in) {
			return true
		}
		result, err := mem.AttachWebMapGraphNodeIn(r.Context(), dir, actor, p[5], p[7], mem.WebMapDelete{Revision: in.Revision, RequestID: in.RequestID}, in.GraphID)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		writeJSON(w, 200, result)
		return true
	}
	if len(p) == 11 && p[6] == "nodes" && p[8] == "sources" && p[10] == "attachment" && r.Method == "GET" {
		revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil {
			a.mapError(w, mem.ErrWebMapSourceInvalid)
			return true
		}
		name, data, err := mem.ReadWebMapAttachmentIn(r.Context(), dir, p[5], p[7], p[9], revision)
		if err != nil {
			a.mapError(w, err)
			return true
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
		return true
	}
	return false
}
