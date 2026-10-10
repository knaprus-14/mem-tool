package mem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
)

// Отпечатки сохраняются и после замены документа: старые snapshots используют
// прежнюю content-revision. Старым базам отпечатки не приписываются автоматически.
const documentSourceFilesSchema = `
CREATE TABLE IF NOT EXISTS document_source_files (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    source_path TEXT NOT NULL,
    file_hash TEXT NOT NULL,
    PRIMARY KEY (document_id, document_revision)
);`

var ErrSourceFileUnverified = errors.New("отпечаток исходного файла неизвестен; повторите mem import")
var ErrSourceFileChanged = errors.New("исходный файл изменён; повторите mem import перед открытием цитаты")

func publicSourceFileError(err error) string {
	if errors.Is(err, ErrSourceFileUnverified) {
		return ErrSourceFileUnverified.Error()
	}
	if errors.Is(err, ErrSourceFileChanged) {
		return ErrSourceFileChanged.Error()
	}
	return "не удалось проверить исходный файл; повторите импорт перед открытием цитаты"
}

func writeDocumentSourceFileTx(tx *sql.Tx, documentID, revision, path, hash string) error {
	if hash == "" {
		return nil
	}
	if documentID == "" || !isSHA256ContentHash(hash) {
		return fmt.Errorf("invalid physical source fingerprint")
	}
	if _, err := tx.Exec(`INSERT INTO document_source_files(document_id, document_revision, source_path, file_hash)
VALUES (?, ?, ?, ?) ON CONFLICT(document_id, document_revision) DO NOTHING`, documentID, revision, path, hash); err != nil {
		return err
	}
	var originalPath, originalHash string
	if err := tx.QueryRow(`SELECT source_path,file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, documentID, revision).Scan(&originalPath, &originalHash); err != nil {
		return err
	}
	if !coveragePathsEqual(path, originalPath) || hash != originalHash {
		return fmt.Errorf("physical source edition is immutable: %w", ErrSourceFileChanged)
	}
	// A portable legacy database can acquire a new physical revision during an
	// explicit reimport. Reuse its relocated bytes only when the stored logical
	// locator AND fingerprint agree; never guess a missing legacy fingerprint.
	var locations int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='source_locations'`).Scan(&locations); err != nil {
		return err
	}
	if locations == 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO source_locations(document_id,document_revision,relative_path)
SELECT ?,?,l.relative_path FROM source_locations l JOIN document_source_files f
ON f.document_id=l.document_id AND f.document_revision=l.document_revision
WHERE f.document_id=? AND `+sourcePathSQLPredicate("f.source_path")+` AND f.file_hash=?
ORDER BY f.document_revision LIMIT 1 ON CONFLICT(document_id,document_revision) DO NOTHING`, documentID, revision, documentID, path, hash)
	return err
}

// verifiedSourceFile связывает частный снимок выдаваемых байтов с редакцией
// evidence. Это исключает замену содержимого между проверкой и ServeContent.
func (s *Store) verifiedSourceFile(ctx context.Context, anchor EvidenceAnchor, file *os.File) (*os.File, error) {
	var path, expected string
	err := s.db.QueryRowContext(ctx, `SELECT source_path, file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, anchor.DocumentID, anchor.DocumentRevision).Scan(&path, &expected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceFileUnverified
	}
	if err != nil {
		return nil, err
	}
	if !coveragePathsEqual(path, anchor.SourcePath) {
		return nil, ErrSourceFileChanged
	}
	snapshot, actual, err := sourcefile.Snapshot(ctx, file)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		closeSourceSnapshot(snapshot)
		return nil, ErrSourceFileChanged
	}
	return snapshot, nil
}

func closeSourceSnapshot(file *os.File) { file.Close(); os.Remove(file.Name()) }
