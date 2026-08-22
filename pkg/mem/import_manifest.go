package mem

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const documentImportManifestSchema = `
CREATE TABLE IF NOT EXISTS document_import_manifests (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    source_path TEXT NOT NULL,
    media_type TEXT NOT NULL,
    format TEXT NOT NULL,
    physical_page_count INTEGER NOT NULL,
    selected_page_first INTEGER NOT NULL,
    selected_page_last INTEGER NOT NULL,
    stored_pages INTEGER NOT NULL,
    empty_pages INTEGER NOT NULL,
    failed_pages INTEGER NOT NULL,
    blocks INTEGER NOT NULL,
    chunks INTEGER NOT NULL,
    warnings TEXT NOT NULL DEFAULT '[]',
    imported_at TEXT NOT NULL,
    PRIMARY KEY (document_id, document_revision)
);

CREATE INDEX IF NOT EXISTS idx_document_import_manifests_source
    ON document_import_manifests(source_path, imported_at);

CREATE TABLE IF NOT EXISTS document_import_pages (
    document_id TEXT NOT NULL,
    document_revision TEXT NOT NULL,
    page INTEGER NOT NULL,
    status TEXT NOT NULL,
    extraction_method TEXT NOT NULL DEFAULT '',
    text_runes INTEGER NOT NULL DEFAULT 0,
    ocr_confidence REAL NOT NULL DEFAULT -1,
    block_count INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    warnings TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (document_id, document_revision, page)
);
`

const (
	DocumentImportPageStored = "stored"
	DocumentImportPageEmpty  = "empty"
	DocumentImportPageFailed = "failed"
)

type DocumentImportPage struct {
	Page             int      `json:"page"`
	Status           string   `json:"status"`
	ExtractionMethod string   `json:"extraction_method,omitempty"`
	TextRunes        int      `json:"text_runes"`
	OCRConfidence    float64  `json:"ocr_confidence,omitempty"`
	BlockCount       int      `json:"block_count"`
	ChunkCount       int      `json:"chunk_count"`
	Warnings         []string `json:"warnings,omitempty"`
}

type DocumentImportManifest struct {
	Available         bool                 `json:"available"`
	DocumentID        string               `json:"document_id"`
	DocumentRevision  string               `json:"document_revision"`
	SourcePath        string               `json:"source_path"`
	MediaType         string               `json:"media_type"`
	Format            string               `json:"format,omitempty"`
	PhysicalPageCount int                  `json:"physical_page_count"`
	SelectedPageFirst int                  `json:"selected_page_first"`
	SelectedPageLast  int                  `json:"selected_page_last"`
	StoredPages       int                  `json:"stored_pages"`
	EmptyPages        int                  `json:"empty_pages"`
	FailedPages       int                  `json:"failed_pages"`
	Blocks            int                  `json:"blocks"`
	Chunks            int                  `json:"chunks"`
	Warnings          []string             `json:"warnings,omitempty"`
	ImportedAt        string               `json:"imported_at,omitempty"`
	Pages             []DocumentImportPage `json:"pages,omitempty"`
}

func validateDocumentImportManifest(manifest DocumentImportManifest, sourcePath string, chunks []DocumentChunk) error {
	if len(chunks) == 0 {
		return fmt.Errorf("import manifest has no document chunks")
	}
	firstChunk := chunks[0].Provenance
	if manifest.DocumentID != firstChunk.DocumentID || manifest.DocumentRevision != firstChunk.DocumentRevision ||
		manifest.SourcePath != sourcePath || manifest.SourcePath != firstChunk.SourcePath || manifest.MediaType != firstChunk.MediaType {
		return fmt.Errorf("import manifest identity does not match document chunks")
	}
	if strings.TrimSpace(manifest.Format) == "" || manifest.PhysicalPageCount <= 0 || manifest.SelectedPageFirst <= 0 ||
		manifest.SelectedPageLast < manifest.SelectedPageFirst || manifest.SelectedPageLast > manifest.PhysicalPageCount {
		return fmt.Errorf("import manifest has invalid physical page scope %d-%d of %d", manifest.SelectedPageFirst, manifest.SelectedPageLast, manifest.PhysicalPageCount)
	}
	if len(manifest.Pages) != manifest.SelectedPageLast-manifest.SelectedPageFirst+1 {
		return fmt.Errorf("import manifest has %d page records for range %d-%d", len(manifest.Pages), manifest.SelectedPageFirst, manifest.SelectedPageLast)
	}
	stored, empty, failed, chunksOnPages := 0, 0, 0, 0
	for index, page := range manifest.Pages {
		if page.Page != manifest.SelectedPageFirst+index || page.TextRunes < 0 || page.BlockCount < 0 || page.ChunkCount < 0 ||
			page.OCRConfidence < -1 || page.OCRConfidence > 100 {
			return fmt.Errorf("import manifest page record %d is invalid", index)
		}
		switch page.Status {
		case DocumentImportPageStored:
			stored++
			if page.TextRunes == 0 || page.BlockCount == 0 || page.ChunkCount == 0 ||
				(page.ExtractionMethod != "text" && page.ExtractionMethod != "ocr") {
				return fmt.Errorf("stored import page %d has incomplete metadata", page.Page)
			}
		case DocumentImportPageEmpty:
			empty++
			if page.TextRunes != 0 || page.BlockCount != 0 || page.ChunkCount != 0 {
				return fmt.Errorf("empty import page %d reports stored content", page.Page)
			}
		case DocumentImportPageFailed:
			failed++
			if page.TextRunes != 0 || page.BlockCount != 0 || page.ChunkCount != 0 || len(page.Warnings) == 0 {
				return fmt.Errorf("failed import page %d has no failure reason", page.Page)
			}
		default:
			return fmt.Errorf("import page %d has unsupported status %q", page.Page, page.Status)
		}
		if page.ExtractionMethod != "ocr" && page.OCRConfidence != -1 {
			return fmt.Errorf("import page %d has OCR confidence for %q extraction", page.Page, page.ExtractionMethod)
		}
		chunksOnPages += page.ChunkCount
	}
	locatedChunks := 0
	for _, chunk := range chunks {
		if chunk.Provenance.Page > 0 {
			locatedChunks++
		}
	}
	if stored != manifest.StoredPages || empty != manifest.EmptyPages || failed != manifest.FailedPages ||
		manifest.Blocks <= 0 || manifest.Chunks != len(chunks) || chunksOnPages != locatedChunks {
		return fmt.Errorf("import manifest summary does not match page records or chunks")
	}
	return nil
}

func writeDocumentImportManifestTx(tx *sql.Tx, manifest DocumentImportManifest, importedAt string) error {
	warningsJSON, err := json.Marshal(append([]string(nil), manifest.Warnings...))
	if err != nil {
		return fmt.Errorf("serialize import manifest warnings: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO document_import_manifests
        (document_id, document_revision, source_path, media_type, format,
         physical_page_count, selected_page_first, selected_page_last,
         stored_pages, empty_pages, failed_pages, blocks, chunks, warnings, imported_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(document_id, document_revision) DO UPDATE SET
         source_path=excluded.source_path, media_type=excluded.media_type, format=excluded.format,
         physical_page_count=excluded.physical_page_count,
         selected_page_first=excluded.selected_page_first, selected_page_last=excluded.selected_page_last,
         stored_pages=excluded.stored_pages, empty_pages=excluded.empty_pages, failed_pages=excluded.failed_pages,
         blocks=excluded.blocks, chunks=excluded.chunks, warnings=excluded.warnings, imported_at=excluded.imported_at`,
		manifest.DocumentID, manifest.DocumentRevision, manifest.SourcePath, manifest.MediaType, manifest.Format,
		manifest.PhysicalPageCount, manifest.SelectedPageFirst, manifest.SelectedPageLast,
		manifest.StoredPages, manifest.EmptyPages, manifest.FailedPages, manifest.Blocks, manifest.Chunks,
		string(warningsJSON), importedAt); err != nil {
		return fmt.Errorf("write import manifest: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM document_import_pages WHERE document_id = ? AND document_revision = ?`,
		manifest.DocumentID, manifest.DocumentRevision); err != nil {
		return fmt.Errorf("replace import manifest pages: %w", err)
	}
	for _, page := range manifest.Pages {
		pageWarningsJSON, marshalErr := json.Marshal(append([]string(nil), page.Warnings...))
		if marshalErr != nil {
			return fmt.Errorf("serialize import page %d warnings: %w", page.Page, marshalErr)
		}
		if _, err := tx.Exec(`INSERT INTO document_import_pages
            (document_id, document_revision, page, status, extraction_method, text_runes,
             ocr_confidence, block_count, chunk_count, warnings)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			manifest.DocumentID, manifest.DocumentRevision, page.Page, page.Status, page.ExtractionMethod,
			page.TextRunes, page.OCRConfidence, page.BlockCount, page.ChunkCount, string(pageWarningsJSON)); err != nil {
			return fmt.Errorf("write import manifest page %d: %w", page.Page, err)
		}
	}
	return nil
}

// CurrentDocumentImportManifests returns the manifest matching each current
// document revision. Older databases are represented with Available=false so
// callers can explain that a re-import is required instead of inventing pages.
func (s *Store) CurrentDocumentImportManifests(selector string) ([]DocumentImportManifest, error) {
	selector = strings.TrimSpace(selector)
	s.mu.RLock()
	entries := make([]Entry, len(s.entries))
	for index := range s.entries {
		entries[index] = cloneEntry(s.entries[index])
	}
	s.mu.RUnlock()

	type currentDocument struct{ id, revision, source, media string }
	documents := make(map[string]currentDocument)
	matched := selector == ""
	for _, entry := range entries {
		if entry.DocumentID == "" || entry.DocumentRevision == "" || entry.SourcePath == "" || !coverageEntryMatchesDocument(entry, selector) {
			continue
		}
		matched = true
		key := entry.DocumentID + "\x00" + entry.DocumentRevision
		documents[key] = currentDocument{entry.DocumentID, entry.DocumentRevision, entry.SourcePath, entry.MediaType}
	}
	if !matched {
		return nil, fmt.Errorf("import manifest document %q was not found in current entries", selector)
	}
	keys := make([]string, 0, len(documents))
	for key := range documents {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(documents[keys[i]].source) < strings.ToLower(documents[keys[j]].source)
	})
	result := make([]DocumentImportManifest, 0, len(keys))
	for _, key := range keys {
		document := documents[key]
		manifest, err := s.loadDocumentImportManifest(document.id, document.revision)
		if err == sql.ErrNoRows {
			result = append(result, DocumentImportManifest{
				DocumentID: document.id, DocumentRevision: document.revision,
				SourcePath: document.source, MediaType: document.media,
			})
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, manifest)
	}
	return result, nil
}

func (s *Store) loadDocumentImportManifest(documentID, revision string) (DocumentImportManifest, error) {
	var manifest DocumentImportManifest
	var warningsJSON string
	err := s.db.QueryRow(`SELECT document_id, document_revision, source_path, media_type, format,
        physical_page_count, selected_page_first, selected_page_last,
        stored_pages, empty_pages, failed_pages, blocks, chunks, warnings, imported_at
        FROM document_import_manifests WHERE document_id = ? AND document_revision = ?`, documentID, revision).Scan(
		&manifest.DocumentID, &manifest.DocumentRevision, &manifest.SourcePath, &manifest.MediaType, &manifest.Format,
		&manifest.PhysicalPageCount, &manifest.SelectedPageFirst, &manifest.SelectedPageLast,
		&manifest.StoredPages, &manifest.EmptyPages, &manifest.FailedPages, &manifest.Blocks, &manifest.Chunks,
		&warningsJSON, &manifest.ImportedAt)
	if err != nil {
		return DocumentImportManifest{}, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &manifest.Warnings); err != nil {
		return DocumentImportManifest{}, fmt.Errorf("decode import manifest warnings: %w", err)
	}
	rows, err := s.db.Query(`SELECT page, status, extraction_method, text_runes, ocr_confidence,
        block_count, chunk_count, warnings FROM document_import_pages
        WHERE document_id = ? AND document_revision = ? ORDER BY page`, documentID, revision)
	if err != nil {
		return DocumentImportManifest{}, fmt.Errorf("read import manifest pages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var page DocumentImportPage
		var pageWarningsJSON string
		if err := rows.Scan(&page.Page, &page.Status, &page.ExtractionMethod, &page.TextRunes,
			&page.OCRConfidence, &page.BlockCount, &page.ChunkCount, &pageWarningsJSON); err != nil {
			return DocumentImportManifest{}, fmt.Errorf("scan import manifest page: %w", err)
		}
		if err := json.Unmarshal([]byte(pageWarningsJSON), &page.Warnings); err != nil {
			return DocumentImportManifest{}, fmt.Errorf("decode import page %d warnings: %w", page.Page, err)
		}
		manifest.Pages = append(manifest.Pages, page)
	}
	if err := rows.Err(); err != nil {
		return DocumentImportManifest{}, err
	}
	manifest.Available = true
	return manifest, nil
}
