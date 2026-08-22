package mem

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const documentImportRunSchema = `
CREATE TABLE IF NOT EXISTS document_import_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    requested_path TEXT NOT NULL,
    source_path TEXT NOT NULL DEFAULT '',
    document_id TEXT NOT NULL DEFAULT '',
    document_revision TEXT NOT NULL DEFAULT '',
    format TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    owner_pid INTEGER NOT NULL DEFAULT 0,
    final_stage TEXT NOT NULL DEFAULT '',
    document_updated INTEGER NOT NULL DEFAULT 0,
    physical_page_count INTEGER NOT NULL DEFAULT 0,
    selected_page_first INTEGER NOT NULL DEFAULT 0,
    selected_page_last INTEGER NOT NULL DEFAULT 0,
    stored_pages INTEGER NOT NULL DEFAULT 0,
    empty_pages INTEGER NOT NULL DEFAULT 0,
    failed_pages INTEGER NOT NULL DEFAULT 0,
    blocks INTEGER NOT NULL DEFAULT 0,
    chunks INTEGER NOT NULL DEFAULT 0,
    warnings TEXT NOT NULL DEFAULT '[]',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_document_import_runs_started
    ON document_import_runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_document_import_runs_source
    ON document_import_runs(source_path, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_document_import_runs_status
    ON document_import_runs(status, started_at DESC);

CREATE TABLE IF NOT EXISTS document_import_run_pages (
    run_id INTEGER NOT NULL,
    page INTEGER NOT NULL,
    status TEXT NOT NULL,
    extraction_method TEXT NOT NULL DEFAULT '',
    text_runes INTEGER NOT NULL DEFAULT 0,
    ocr_confidence REAL NOT NULL DEFAULT -1,
    block_count INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    warnings TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (run_id, page)
);

CREATE TRIGGER IF NOT EXISTS protect_final_document_import_runs
BEFORE UPDATE ON document_import_runs WHEN OLD.status != 'running'
BEGIN SELECT RAISE(ABORT, 'final import runs are immutable'); END;

CREATE TRIGGER IF NOT EXISTS protect_document_import_runs_delete
BEFORE DELETE ON document_import_runs
BEGIN SELECT RAISE(ABORT, 'import runs are append-only'); END;

CREATE TRIGGER IF NOT EXISTS protect_document_import_run_pages_update
BEFORE UPDATE ON document_import_run_pages
BEGIN SELECT RAISE(ABORT, 'import run pages are immutable'); END;

CREATE TRIGGER IF NOT EXISTS protect_document_import_run_pages_delete
BEFORE DELETE ON document_import_run_pages
BEGIN SELECT RAISE(ABORT, 'import run pages are append-only'); END;
`

const (
	DocumentImportRunRunning     = "running"
	DocumentImportRunSucceeded   = "succeeded"
	DocumentImportRunPartial     = "partial"
	DocumentImportRunFailed      = "failed"
	DocumentImportRunCancelled   = "cancelled"
	DocumentImportRunInterrupted = "interrupted"
)

type DocumentImportRun struct {
	ID                int64                `json:"id"`
	RequestedPath     string               `json:"requested_path"`
	SourcePath        string               `json:"source_path,omitempty"`
	DocumentID        string               `json:"document_id,omitempty"`
	DocumentRevision  string               `json:"document_revision,omitempty"`
	Format            string               `json:"format,omitempty"`
	MediaType         string               `json:"media_type,omitempty"`
	Status            string               `json:"status"`
	FinalStage        string               `json:"final_stage,omitempty"`
	DocumentUpdated   bool                 `json:"document_updated"`
	PhysicalPageCount int                  `json:"physical_page_count"`
	SelectedPageFirst int                  `json:"selected_page_first"`
	SelectedPageLast  int                  `json:"selected_page_last"`
	StoredPages       int                  `json:"stored_pages"`
	EmptyPages        int                  `json:"empty_pages"`
	FailedPages       int                  `json:"failed_pages"`
	Blocks            int                  `json:"blocks"`
	Chunks            int                  `json:"chunks"`
	Warnings          []string             `json:"warnings,omitempty"`
	ErrorMessage      string               `json:"error_message,omitempty"`
	StartedAt         string               `json:"started_at"`
	CompletedAt       string               `json:"completed_at,omitempty"`
	Pages             []DocumentImportPage `json:"pages,omitempty"`
}

type documentImportRunCompletion struct {
	RunID             int64
	SourcePath        string
	DocumentID        string
	DocumentRevision  string
	Format            string
	MediaType         string
	Status            string
	FinalStage        string
	DocumentUpdated   bool
	PhysicalPageCount int
	SelectedPageFirst int
	SelectedPageLast  int
	StoredPages       int
	EmptyPages        int
	FailedPages       int
	Blocks            int
	Chunks            int
	Warnings          []string
	ErrorMessage      string
	Pages             []DocumentImportPage
}

func (s *Store) startDocumentImportRun(requestedPath string) (int64, error) {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return 0, fmt.Errorf("start import run: requested path is empty")
	}
	if absolute, err := filepath.Abs(requestedPath); err == nil {
		requestedPath = filepath.Clean(absolute)
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(`INSERT INTO document_import_runs
        (requested_path, status, owner_pid, started_at) VALUES (?, ?, ?, ?)`,
		requestedPath, DocumentImportRunRunning, os.Getpid(), startedAt)
	if err != nil {
		return 0, fmt.Errorf("start import run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read import run id: %w", err)
	}
	return runID, nil
}

func (s *Store) finishDocumentImportRun(completion documentImportRunCompletion) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin import run completion: %w", err)
	}
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := finishDocumentImportRunTx(tx, completion, completedAt); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit import run completion: %w", err)
	}
	return nil
}

func finishDocumentImportRunTx(tx *sql.Tx, completion documentImportRunCompletion, completedAt string) error {
	if completion.RunID <= 0 {
		return fmt.Errorf("finish import run: invalid run id %d", completion.RunID)
	}
	if !validFinalImportRunStatus(completion.Status) {
		return fmt.Errorf("finish import run %d: invalid status %q", completion.RunID, completion.Status)
	}
	if completion.DocumentUpdated && completion.Status != DocumentImportRunSucceeded && completion.Status != DocumentImportRunPartial {
		return fmt.Errorf("finish import run %d: status %q cannot update a document", completion.RunID, completion.Status)
	}
	if !completion.DocumentUpdated && (completion.Status == DocumentImportRunSucceeded || completion.Status == DocumentImportRunPartial) {
		return fmt.Errorf("finish import run %d: successful status has no document update", completion.RunID)
	}
	if completion.DocumentUpdated && (strings.TrimSpace(completion.SourcePath) == "" || completion.Chunks <= 0) {
		return fmt.Errorf("finish import run %d: updated document metadata is incomplete", completion.RunID)
	}
	if err := validateImportRunPages(completion); err != nil {
		return err
	}
	warningsJSON, err := json.Marshal(boundedImportMessages(completion.Warnings, 200, 2000))
	if err != nil {
		return fmt.Errorf("serialize import run warnings: %w", err)
	}
	result, err := tx.Exec(`UPDATE document_import_runs SET
        source_path=?, document_id=?, document_revision=?, format=?, media_type=?,
        status=?, final_stage=?, document_updated=?, physical_page_count=?,
        selected_page_first=?, selected_page_last=?, stored_pages=?, empty_pages=?,
        failed_pages=?, blocks=?, chunks=?, warnings=?, error_message=?, completed_at=?
        WHERE id=? AND status=?`,
		completion.SourcePath, completion.DocumentID, completion.DocumentRevision,
		completion.Format, completion.MediaType, completion.Status, completion.FinalStage,
		boolToInt(completion.DocumentUpdated), completion.PhysicalPageCount,
		completion.SelectedPageFirst, completion.SelectedPageLast, completion.StoredPages,
		completion.EmptyPages, completion.FailedPages, completion.Blocks, completion.Chunks,
		string(warningsJSON), boundedImportMessage(completion.ErrorMessage, 4000), completedAt,
		completion.RunID, DocumentImportRunRunning)
	if err != nil {
		return fmt.Errorf("finish import run %d: %w", completion.RunID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish import run %d: %w", completion.RunID, err)
	}
	if affected != 1 {
		return fmt.Errorf("finish import run %d: run is missing or already final", completion.RunID)
	}
	for _, page := range completion.Pages {
		pageWarningsJSON, marshalErr := json.Marshal(boundedImportMessages(page.Warnings, 50, 2000))
		if marshalErr != nil {
			return fmt.Errorf("serialize import run %d page %d warnings: %w", completion.RunID, page.Page, marshalErr)
		}
		if _, err := tx.Exec(`INSERT INTO document_import_run_pages
            (run_id, page, status, extraction_method, text_runes, ocr_confidence,
             block_count, chunk_count, warnings) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			completion.RunID, page.Page, page.Status, page.ExtractionMethod, page.TextRunes,
			page.OCRConfidence, page.BlockCount, page.ChunkCount, string(pageWarningsJSON)); err != nil {
			return fmt.Errorf("write import run %d page %d: %w", completion.RunID, page.Page, err)
		}
	}
	return nil
}

func validateImportRunPages(completion documentImportRunCompletion) error {
	if len(completion.Pages) == 0 {
		if completion.SelectedPageFirst != 0 || completion.SelectedPageLast != 0 ||
			completion.StoredPages != 0 || completion.EmptyPages != 0 || completion.FailedPages != 0 {
			return fmt.Errorf("finish import run %d: page summary exists without page records", completion.RunID)
		}
		return nil
	}
	if completion.PhysicalPageCount <= 0 || completion.SelectedPageFirst <= 0 ||
		completion.SelectedPageLast < completion.SelectedPageFirst ||
		completion.SelectedPageLast > completion.PhysicalPageCount ||
		len(completion.Pages) != completion.SelectedPageLast-completion.SelectedPageFirst+1 {
		return fmt.Errorf("finish import run %d: invalid physical page scope", completion.RunID)
	}
	stored, empty, failed := 0, 0, 0
	for index, page := range completion.Pages {
		if page.Page != completion.SelectedPageFirst+index || page.TextRunes < 0 ||
			page.BlockCount < 0 || page.ChunkCount < 0 || math.IsNaN(page.OCRConfidence) ||
			math.IsInf(page.OCRConfidence, 0) || page.OCRConfidence < -1 || page.OCRConfidence > 100 {
			return fmt.Errorf("finish import run %d: non-contiguous page records", completion.RunID)
		}
		switch page.Status {
		case DocumentImportPageStored:
			if page.TextRunes == 0 || page.BlockCount == 0 ||
				(page.ExtractionMethod != "text" && page.ExtractionMethod != "ocr") {
				return fmt.Errorf("finish import run %d: stored page %d has incomplete metadata", completion.RunID, page.Page)
			}
			stored++
		case DocumentImportPageEmpty:
			if page.TextRunes != 0 || page.BlockCount != 0 || page.ChunkCount != 0 {
				return fmt.Errorf("finish import run %d: empty page %d reports content", completion.RunID, page.Page)
			}
			empty++
		case DocumentImportPageFailed:
			if page.TextRunes != 0 || page.BlockCount != 0 || page.ChunkCount != 0 || len(page.Warnings) == 0 {
				return fmt.Errorf("finish import run %d: failed page %d has no failure reason", completion.RunID, page.Page)
			}
			failed++
		default:
			return fmt.Errorf("finish import run %d: invalid page status %q", completion.RunID, page.Status)
		}
		if page.ExtractionMethod != "ocr" && page.OCRConfidence != -1 {
			return fmt.Errorf("finish import run %d: page %d has invalid OCR confidence", completion.RunID, page.Page)
		}
	}
	if stored != completion.StoredPages || empty != completion.EmptyPages || failed != completion.FailedPages {
		return fmt.Errorf("finish import run %d: page totals do not match records", completion.RunID)
	}
	return nil
}

func validFinalImportRunStatus(status string) bool {
	switch status {
	case DocumentImportRunSucceeded, DocumentImportRunPartial, DocumentImportRunFailed,
		DocumentImportRunCancelled, DocumentImportRunInterrupted:
		return true
	default:
		return false
	}
}

func boundedImportMessages(messages []string, maxItems, maxRunes int) []string {
	if len(messages) > maxItems {
		messages = messages[:maxItems]
	}
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		if value := boundedImportMessage(message, maxRunes); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func boundedImportMessage(message string, maxRunes int) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) > maxRunes {
		message = string(runes[:maxRunes]) + "..."
	}
	return message
}

func recoverInterruptedImportRuns(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, owner_pid FROM document_import_runs WHERE status=?`, DocumentImportRunRunning)
	if err != nil {
		return fmt.Errorf("inspect running import runs: %w", err)
	}
	type candidate struct {
		id  int64
		pid int
	}
	var interrupted []candidate
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.pid); err != nil {
			rows.Close()
			return fmt.Errorf("scan running import run: %w", err)
		}
		if !processAlive(value.pid) {
			interrupted = append(interrupted, value)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(interrupted) == 0 {
		return nil
	}
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, value := range interrupted {
		if _, err := db.Exec(`UPDATE document_import_runs SET status=?, final_stage='process',
            error_message=?, completed_at=? WHERE id=? AND status=?`,
			DocumentImportRunInterrupted,
			"предыдущий процесс завершился до фиксации результата; активный документ не изменён",
			completedAt, value.id, DocumentImportRunRunning); err != nil {
			return fmt.Errorf("recover interrupted import run #%d: %w", value.id, err)
		}
	}
	return nil
}

func (s *Store) DocumentImportRuns(selector, status string, limit int) ([]DocumentImportRun, error) {
	selector = strings.TrimSpace(selector)
	status = strings.TrimSpace(status)
	if status != "" && status != DocumentImportRunRunning && !validFinalImportRunStatus(status) {
		return nil, fmt.Errorf("unsupported import run status %q", status)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		return nil, fmt.Errorf("import run limit %d exceeds 500", limit)
	}
	query := `SELECT id, requested_path, source_path, document_id, document_revision,
        format, media_type, status, final_stage, document_updated, physical_page_count,
        selected_page_first, selected_page_last, stored_pages, empty_pages, failed_pages,
        blocks, chunks, warnings, error_message, started_at, completed_at
        FROM document_import_runs`
	var where []string
	var args []any
	if selector != "" {
		absolute := selector
		if value, err := filepath.Abs(selector); err == nil {
			absolute = filepath.Clean(value)
		}
		where = append(where, `(LOWER(requested_path)=LOWER(?) OR LOWER(source_path)=LOWER(?) OR document_id=?)`)
		args = append(args, absolute, absolute, selector)
	}
	if status != "" {
		where = append(where, `status=?`)
		args = append(args, status)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read import runs: %w", err)
	}
	defer rows.Close()
	runs := make([]DocumentImportRun, 0)
	for rows.Next() {
		run, scanErr := scanDocumentImportRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) DocumentImportRun(id int64) (DocumentImportRun, error) {
	if id <= 0 {
		return DocumentImportRun{}, fmt.Errorf("invalid import run id %d", id)
	}
	row := s.db.QueryRow(`SELECT id, requested_path, source_path, document_id, document_revision,
        format, media_type, status, final_stage, document_updated, physical_page_count,
        selected_page_first, selected_page_last, stored_pages, empty_pages, failed_pages,
        blocks, chunks, warnings, error_message, started_at, completed_at
        FROM document_import_runs WHERE id=?`, id)
	run, err := scanDocumentImportRun(row)
	if err == sql.ErrNoRows {
		return DocumentImportRun{}, fmt.Errorf("import run #%d was not found", id)
	}
	if err != nil {
		return DocumentImportRun{}, err
	}
	rows, err := s.db.Query(`SELECT page, status, extraction_method, text_runes,
        ocr_confidence, block_count, chunk_count, warnings
        FROM document_import_run_pages WHERE run_id=? ORDER BY page`, id)
	if err != nil {
		return DocumentImportRun{}, fmt.Errorf("read import run #%d pages: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var page DocumentImportPage
		var warningsJSON string
		if err := rows.Scan(&page.Page, &page.Status, &page.ExtractionMethod, &page.TextRunes,
			&page.OCRConfidence, &page.BlockCount, &page.ChunkCount, &warningsJSON); err != nil {
			return DocumentImportRun{}, fmt.Errorf("scan import run #%d page: %w", id, err)
		}
		if err := json.Unmarshal([]byte(warningsJSON), &page.Warnings); err != nil {
			return DocumentImportRun{}, fmt.Errorf("decode import run #%d page %d warnings: %w", id, page.Page, err)
		}
		run.Pages = append(run.Pages, page)
	}
	return run, rows.Err()
}

type importRunScanner interface{ Scan(...any) error }

func scanDocumentImportRun(scanner importRunScanner) (DocumentImportRun, error) {
	var run DocumentImportRun
	var warningsJSON string
	var documentUpdated int
	if err := scanner.Scan(&run.ID, &run.RequestedPath, &run.SourcePath, &run.DocumentID,
		&run.DocumentRevision, &run.Format, &run.MediaType, &run.Status, &run.FinalStage,
		&documentUpdated, &run.PhysicalPageCount, &run.SelectedPageFirst, &run.SelectedPageLast,
		&run.StoredPages, &run.EmptyPages, &run.FailedPages, &run.Blocks, &run.Chunks,
		&warningsJSON, &run.ErrorMessage, &run.StartedAt, &run.CompletedAt); err != nil {
		return DocumentImportRun{}, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &run.Warnings); err != nil {
		return DocumentImportRun{}, fmt.Errorf("decode import run #%d warnings: %w", run.ID, err)
	}
	run.DocumentUpdated = documentUpdated != 0
	return run, nil
}
