package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

// ImportDocumentIn is an explicit-directory import service for background jobs.
// It does not load the vector corpus, recover other processes' imports, or change
// cwd. Schema changes and writes occur only for an explicitly requested import.
func ImportDocumentIn(ctx context.Context, dir, path string, options ImportOptions) (applogResult0 ImportResult, applogResult1 error) {
	defer applog.Track("pkg.mem.ImportDocumentIn")(&applogResult1)

	info, err := os.Lstat(StorePathIn(dir))
	if err != nil || !info.Mode().IsRegular() {
		return ImportResult{}, fmt.Errorf("база для импорта недоступна")
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return ImportResult{}, err
	}
	db, err := sqliteutil.Open(StorePathIn(dir))
	if err != nil {
		return ImportResult{}, err
	}
	defer db.Close()
	if err = initSchema(db); err != nil {
		return ImportResult{}, err
	}
	s := &Store{db: db, path: StorePathIn(dir), importOnly: true}
	return ImportDocument(ctx, cfg, s, path, options)
}

// ReadDocumentImportRunIn reads a durable completion receipt without migrations.
// Jobs use it to reconcile a crash after document commit but before job completion.
func ReadDocumentImportRunIn(ctx context.Context, dir string, id int64) (applogResult0 DocumentImportRun, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadDocumentImportRunIn")(&applogResult1)

	db, err := sqliteutil.OpenReadOnly(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		return DocumentImportRun{}, err
	}
	defer db.Close()
	return (&Store{db: db}).DocumentImportRun(id)
}
