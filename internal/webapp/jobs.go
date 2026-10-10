package webapp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/webjobs"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) jobRoutes(w http.ResponseWriter, r *http.Request, actor workspace.Actor, parts []string) {
	base, err := a.manager.Resolve(r.Context(), actor, parts[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	if len(parts) == 5 && parts[4] == "uploads" && r.Method == "POST" {
		a.upload(w, r, actor, base, "")
		return
	}
	if len(parts) == 6 && parts[4] == "revisions" && (r.Method == "GET" || r.Method == "POST") {
		q, e := webjobs.Open(r.Context(), base, false)
		if os.IsNotExist(e) {
			e = workspace.ErrNotFound
		}
		if e != nil {
			a.fail(w, e)
			return
		}
		current, e := q.Revision(r.Context(), actor, parts[5])
		q.Close()
		if e != nil {
			a.revisionError(w, e)
			return
		}
		if r.Method == "GET" {
			writeJSON(w, 200, current)
		} else {
			a.upload(w, r, actor, base, parts[5])
		}
		return
	}
	if parts[4] != "jobs" {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	if r.Method == "GET" && (len(parts) == 7 && parts[6] == "history" || len(parts) == 9 && parts[6] == "attempts" && parts[8] == "pages") {
		q, e := webjobs.Open(r.Context(), base, false)
		if os.IsNotExist(e) {
			e = workspace.ErrNotFound
		}
		if e != nil {
			a.fail(w, e)
			return
		}
		defer q.Close()
		if len(parts) == 7 {
			items, e := q.History(r.Context(), actor, parts[5])
			if e != nil {
				a.fail(w, e)
				return
			}
			writeJSON(w, 200, items)
			return
		}
		attempt, e := strconv.Atoi(parts[7])
		after := 0
		values := r.URL.Query()
		if e != nil || len(values) > 1 || len(values["after"]) > 1 {
			a.fail(w, webjobs.ErrInvalid)
			return
		}
		for key := range values {
			if key != "after" {
				a.fail(w, webjobs.ErrInvalid)
				return
			}
		}
		if values.Has("after") {
			after, e = strconv.Atoi(values.Get("after"))
			if e != nil || after < 0 {
				a.fail(w, webjobs.ErrInvalid)
				return
			}
		}
		page, e := q.AttemptPages(r.Context(), actor, parts[5], attempt, after)
		if e != nil {
			a.fail(w, e)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if r.Method == "GET" && (len(parts) == 5 || len(parts) == 6) {
		q, err := webjobs.Open(r.Context(), base, false)
		if os.IsNotExist(err) {
			if len(parts) == 5 {
				writeJSON(w, 200, []webjobs.Job{})
			} else {
				writeError(w, 404, "Задание не найдено")
			}
			return
		}
		if err != nil {
			a.fail(w, err)
			return
		}
		defer q.Close()
		if len(parts) == 5 {
			jobs, e := q.List(r.Context(), actor)
			if e != nil {
				a.fail(w, e)
				return
			}
			writeJSON(w, 200, jobs)
		} else {
			job, e := q.Get(r.Context(), actor, parts[5])
			if e != nil {
				a.fail(w, e)
				return
			}
			writeJSON(w, 200, job)
		}
		return
	}
	if r.Method == "POST" && len(parts) == 7 && (parts[6] == "cancel" || parts[6] == "retry") {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		if _, err = os.Lstat(filepath.Join(base.Root, ".mem", "web-jobs.db")); os.IsNotExist(err) {
			writeError(w, 404, "Задание не найдено")
			return
		}
		q, err := webjobs.Open(r.Context(), base, true)
		if err != nil {
			a.fail(w, err)
			return
		}
		defer q.Close()
		job, err := q.Change(r.Context(), actor, parts[5], parts[6])
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, 200, job)
		return
	}
	writeError(w, 405, "Метод не поддерживается")
}

func (a *App) upload(w http.ResponseWriter, r *http.Request, actor workspace.Actor, base workspace.Workspace, documentKey string) {
	select {
	case a.uploads <- struct{}{}:
		defer func() { <-a.uploads }()
	default:
		writeError(w, 429, "Уже загружаются файлы. Повторите через несколько секунд.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, webjobs.MaxFileBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.fail(w, webjobs.ErrLimit)
		} else {
			writeError(w, 400, "Не удалось прочитать загрузку файла")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 {
		writeError(w, 400, "Загружайте по одному файлу на запрос")
		return
	}
	if files[0].Size > webjobs.MaxFileBytes {
		a.fail(w, webjobs.ErrLimit)
		return
	}
	var metadata webjobs.Metadata
	if len(r.MultipartForm.Value) > 2 || len(r.MultipartForm.Value["metadata"]) > 1 || len(r.MultipartForm.Value["state"]) > 1 {
		a.fail(w, webjobs.ErrInvalid)
		return
	}
	for key := range r.MultipartForm.Value {
		if key != "metadata" && !(key == "state" && documentKey != "") {
			a.fail(w, webjobs.ErrInvalid)
			return
		}
	}
	if values := r.MultipartForm.Value["metadata"]; len(values) == 1 {
		if len(values[0]) > 4096 {
			a.fail(w, webjobs.ErrInvalid)
			return
		}
		d := json.NewDecoder(strings.NewReader(values[0]))
		d.DisallowUnknownFields()
		if err := d.Decode(&metadata); err != nil {
			a.fail(w, webjobs.ErrInvalid)
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			a.fail(w, webjobs.ErrInvalid)
			return
		}
	}
	f, err := files[0].Open()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer f.Close()
	q, err := webjobs.Open(r.Context(), base, true)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer q.Close()
	var job webjobs.Job
	var duplicate bool
	if documentKey != "" {
		job, duplicate, err = q.EnqueueRevision(r.Context(), actor, documentKey, r.FormValue("state"), files[0].Filename, f, metadata)
	} else {
		job, duplicate, err = q.Enqueue(r.Context(), actor, files[0].Filename, f, metadata)
	}
	if err != nil {
		a.revisionError(w, err)
		return
	}
	status := http.StatusAccepted
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, struct {
		Job       webjobs.Job `json:"job"`
		Duplicate bool        `json:"duplicate"`
	}{job, duplicate})
}

func (a *App) revisionError(w http.ResponseWriter, err error) {
	if errors.Is(err, mem.ErrDocumentUpdateStateChanged) {
		writeError(w, 409, "Документ изменился. Закройте окно, обновите список и выберите новую редакцию ещё раз.")
		return
	}
	a.fail(w, err)
}
