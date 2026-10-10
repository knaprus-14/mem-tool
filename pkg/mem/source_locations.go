package mem

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// Source locators are part of immutable evidence and its digest. Relocation
// therefore maps a document revision to bytes instead of rewriting old anchors.
const sourceLocationsSchema = `CREATE TABLE IF NOT EXISTS source_locations(document_id TEXT NOT NULL,document_revision TEXT NOT NULL,relative_path TEXT NOT NULL,PRIMARY KEY(document_id,document_revision));`

func sourceLocation(ctx context.Context, db *sql.DB, dir string, a EvidenceAnchor) (string, bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='source_locations'`).Scan(&count); err != nil {
		return "", false, err
	}
	if count == 0 {
		return a.SourcePath, false, nil
	}
	var relative string
	err := db.QueryRowContext(ctx, `SELECT relative_path FROM source_locations WHERE document_id=? AND document_revision=?`, a.DocumentID, a.DocumentRevision).Scan(&relative)
	if errors.Is(err, sql.ErrNoRows) {
		return a.SourcePath, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !filepath.IsLocal(relative) {
		return "", false, ErrSourceFileUnverified
	}
	return filepath.Join(dir, relative), true, nil
}

func openLocatedSource(ctx context.Context, db *sql.DB, dir string, a EvidenceAnchor, local bool) (*os.File, error) {
	path, mapped, err := sourceLocation(ctx, db, dir, a)
	if err != nil {
		return nil, err
	}
	if local && !mapped {
		return os.Open(path)
	}
	base := filepath.Dir(dir)
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, ErrSourceFileUnverified
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(rel)
}
