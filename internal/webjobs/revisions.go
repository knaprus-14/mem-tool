package webjobs

import (
	"context"
	"database/sql"
	"errors"
	"github.com/knaprus-14/mem-tool/internal/applog"
	"io"
	"path/filepath"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

type Revision struct {
	DocumentKey string   `json:"document_key"`
	Filename    string   `json:"filename"`
	State       string   `json:"state"`
	Title       string   `json:"title"`
	Tags        []string `json:"tags"`
	Important   bool     `json:"important"`
	replacement mem.DocumentReplacement
}

// Revision resolves ownership through uploaded sources, then pins the actual
// current document in store.db. Queue completion may lag behind a core commit.
func (q *Queue) Revision(ctx context.Context, actor workspace.Actor, key string) (Revision, error) {
	if !actor.Valid() || len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" {
		return Revision{}, workspace.ErrNotFound
	}
	column := "document_key"
	if q.version < 2 {
		column = "id"
	}
	var exists int
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE owner=? AND `+column+`=?`, actor.ID, key).Scan(&exists); err != nil {
		return Revision{}, err
	}
	if exists == 0 {
		return Revision{}, workspace.ErrNotFound
	}
	state, err := mem.ReadDocumentUpdateStateIn(ctx, q.dir, "web:"+key)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, workspace.ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	return Revision{DocumentKey: key, Filename: filepath.Base(state.SourcePath), State: state.State, Title: state.Title, Tags: state.Tags, Important: state.Important, replacement: state.DocumentReplacement}, nil
}

func (q *Queue) EnqueueRevision(ctx context.Context, actor workspace.Actor, key, state, name string, body io.Reader, metadata Metadata) (job Job, duplicate bool, logErr error) {
	defer applog.Track("jobs.EnqueueRevision")(&logErr)
	defer func() {
		if logErr == nil {
			applog.Event("INFO", "jobs.queued", applog.Fields{"job_id": job.ID, "duplicate": duplicate})
		}
	}()

	current, err := q.Revision(ctx, actor, key)
	if err != nil {
		return Job{}, false, err
	}
	if current.State != state {
		return Job{}, false, mem.ErrDocumentUpdateStateChanged
	}
	return q.enqueue(ctx, actor, name, body, metadata, key, &current.replacement)
}
