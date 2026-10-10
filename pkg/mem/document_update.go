package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

// ErrDocumentUpdateStateChanged prevents a queued revision from overwriting an
// intervening edit, deletion, restore or reindex performed by another client.
var ErrDocumentUpdateStateChanged = errors.New("document changed after update preview")

type sourceIdentityQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Include retained originals and empty/deleted current documents, not just the
// present chunks. Conflicting historical identities must never be chosen with
// an arbitrary LIMIT 1.
func managedDocumentIDForSourcePath(ctx context.Context, q sourceIdentityQuerier, path string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT document_id FROM (
SELECT document_id FROM entries WHERE `+sourcePathSQLPredicate("source_file")+`
UNION SELECT document_id FROM document_source_files WHERE `+sourcePathSQLPredicate("source_path")+`
UNION SELECT document_id FROM document_current_tombstones WHERE `+sourcePathSQLPredicate("source_path")+`
) WHERE document_id LIKE 'web:%' LIMIT 2`, path, path, path)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var id string
	for rows.Next() {
		if id != "" {
			return "", ErrDocumentUpdateStateChanged
		}
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
	}
	return id, rows.Err()
}

type DocumentReplacement struct {
	SourcePath string `json:"source_path"`
	State      string `json:"state"`
}

type DocumentUpdateState struct {
	DocumentReplacement
	Title     string
	Tags      []string
	Important bool
}

// ReadDocumentUpdateStateIn reads only the selected document in one snapshot.
// The pin includes chunk text, metadata and vectors; unrelated documents do not
// invalidate it. Paths stay inside the application service, never in its API.
func ReadDocumentUpdateStateIn(ctx context.Context, dir, documentID string) (applogResult0 DocumentUpdateState, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadDocumentUpdateStateIn")(&applogResult1)

	db, err := sqliteutil.OpenReadOnly(ctx, StorePathIn(dir))
	if err != nil {
		return DocumentUpdateState{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentUpdateState{}, err
	}
	defer tx.Rollback()
	entries, _, err := loadEntriesFromQuery(tx, `SELECT `+storedEntryColumns+` FROM entries WHERE document_id=? ORDER BY chunk_index`, documentID)
	if err != nil {
		return DocumentUpdateState{}, err
	}
	if len(entries) == 0 {
		return DocumentUpdateState{}, sql.ErrNoRows
	}
	path := entries[0].SourceFile
	if path == "" {
		return DocumentUpdateState{}, ErrDocumentUpdateStateChanged
	}
	result := DocumentUpdateState{DocumentReplacement: DocumentReplacement{SourcePath: path}, Title: entries[0].Title, Tags: []string{}}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !coveragePathsEqual(entry.SourceFile, path) {
			return DocumentUpdateState{}, ErrDocumentUpdateStateChanged
		}
		result.Important = result.Important || entry.Important
		for _, tag := range entry.Tags {
			if !seen[tag] {
				seen[tag] = true
				result.Tags = append(result.Tags, tag)
			}
		}
	}
	result.State, err = documentRestoreCurrentStateDigest(entries, "")
	if err != nil {
		return DocumentUpdateState{}, err
	}
	return result, tx.Commit()
}

// validateDocumentReplacementTx also guards ordinary CLI imports of a retained
// old original: one managed identity must never acquire two current paths.
func validateDocumentReplacementTx(tx *sql.Tx, documentID, incomingPath, oldPath string, old []Entry, replacement *DocumentReplacement) error {
	managedID, err := managedDocumentIDForSourcePath(context.Background(), tx, oldPath)
	if err != nil {
		return err
	}
	if managedID != "" && managedID != documentID {
		return ErrDocumentUpdateStateChanged
	}
	if replacement != nil {
		if !validSHA256Digest(replacement.State) || len(old) == 0 {
			return ErrDocumentUpdateStateChanged
		}
		for _, e := range old {
			if e.DocumentID != documentID {
				return ErrDocumentUpdateStateChanged
			}
		}
		state, err := documentRestoreCurrentStateDigest(old, "")
		if err != nil {
			return err
		}
		if state != replacement.State {
			return ErrDocumentUpdateStateChanged
		}
		if !coveragePathsEqual(oldPath, incomingPath) {
			incoming, err := loadEntriesBySource(tx, incomingPath)
			if err != nil {
				return err
			}
			if len(incoming) > 0 {
				return ErrDocumentUpdateStateChanged
			}
		}
	}
	if strings.HasPrefix(documentID, "web:") {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM entries WHERE document_id=? AND NOT (`+sourcePathSQLPredicate("source_file")+`)`, documentID, oldPath).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrDocumentUpdateStateChanged
		}
	}
	return nil
}
