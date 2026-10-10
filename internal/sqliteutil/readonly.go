package sqliteutil

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// OpenReadOnly never creates or migrates a database. Unlike Open it also leaves
// import-run recovery to the process that owns those jobs.
func OpenReadOnly(ctx context.Context, path string) (applogResult0 *sql.DB, applogResult1 error) {
	defer applog.Track("internal.sqliteutil.OpenReadOnly")(&applogResult1)

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"mode": {"ro"}, "_pragma": {fmt.Sprintf("busy_timeout(%d)", BusyTimeoutMS), "query_only(1)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
