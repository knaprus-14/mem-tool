// Package sqliteutil задаёт общую политику подключений локальных SQLite-баз.
package sqliteutil

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// BusyTimeoutMS ограничивает ожидание занятого писателя пятью секундами.
const BusyTimeoutMS = 5000

// Open применяет параметры к каждому соединению пула, включая созданные позже.
// IMMEDIATE резервирует writer до чтения изменяемых строк, исключая неустранимую
// повтором отдельного UPDATE гонку повышения read-транзакции до write.
func Open(path string) (applogResult0 *sql.DB, applogResult1 error) {
	defer applog.Track("internal.sqliteutil.Open")(&applogResult1)

	return openPath(path, "")
}

// OpenExisting fails if the database disappears instead of creating an empty one.
func OpenExisting(path string) (applogResult0 *sql.DB, applogResult1 error) {
	defer applog.Track("internal.sqliteutil.OpenExisting")(&applogResult1)

	return openPath(path, "rw")
}

func openPath(path, mode string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{}
	if mode != "" {
		q.Set("mode", mode)
	}
	q.Set("_pragma", fmt.Sprintf("busy_timeout(%d)", BusyTimeoutMS))
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}

// BeginRead открывает согласованный снимок без резервирования SQLite writer.
func BeginRead(db *sql.DB) (applogResult0 *sql.Tx, applogResult1 error) {
	defer applog.Track("internal.sqliteutil.BeginRead")(&applogResult1)

	return db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
}
