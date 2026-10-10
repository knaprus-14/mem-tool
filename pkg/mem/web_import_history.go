package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// WebImportPages is a bounded view of a pinned import attempt, not of the
// document's latest revision. Paths, raw tool errors and warning text stay local.
type WebImportPages struct {
	Status        string          `json:"status"`
	Updated       bool            `json:"document_updated"`
	PhysicalPages int             `json:"physical_pages"`
	StoredPages   int             `json:"stored_pages"`
	EmptyPages    int             `json:"empty_pages"`
	FailedPages   int             `json:"failed_pages"`
	Chunks        int             `json:"chunks"`
	HasWarnings   bool            `json:"has_warnings"`
	HasError      bool            `json:"has_error"`
	Pages         []WebImportPage `json:"pages"`
	Next          int             `json:"next"`
}
type WebImportPage struct {
	Page        int     `json:"page"`
	Status      string  `json:"status"`
	Method      string  `json:"method"`
	TextRunes   int     `json:"text_runes"`
	Confidence  float64 `json:"confidence"`
	Blocks      int     `json:"blocks"`
	Chunks      int     `json:"chunks"`
	HasWarnings bool    `json:"has_warnings"`
}

// Caller must resolve the run ID from the owner's job receipt. No API should
// expose this reader by an arbitrary client-supplied import-run ID.
func ReadWebImportPagesIn(ctx context.Context, dir string, runID int64, after int) (applogResult0 WebImportPages, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebImportPagesIn")(&applogResult1)

	result := WebImportPages{Pages: []WebImportPage{}, Next: -1}
	if runID <= 0 || after < 0 {
		return result, ErrQueryInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT substr(status,1,32),document_updated,physical_page_count,stored_pages,empty_pages,failed_pages,chunks,length(warnings)>2,length(error_message)>0 FROM document_import_runs WHERE id=?`, runID).Scan(&result.Status, &result.Updated, &result.PhysicalPages, &result.StoredPages, &result.EmptyPages, &result.FailedPages, &result.Chunks, &result.HasWarnings, &result.HasError)
	if err != nil {
		return result, err
	}
	switch result.Status {
	case "running", "succeeded", "partial", "failed", "cancelled", "interrupted":
	default:
		result.Status = "unknown"
	}
	rows, err := tx.QueryContext(ctx, `SELECT page,substr(status,1,32),substr(extraction_method,1,16),text_runes,ocr_confidence,block_count,chunk_count,length(warnings)>2 FROM document_import_run_pages WHERE run_id=? AND page>? ORDER BY page LIMIT 51`, runID, after)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var p WebImportPage
		if err = rows.Scan(&p.Page, &p.Status, &p.Method, &p.TextRunes, &p.Confidence, &p.Blocks, &p.Chunks, &p.HasWarnings); err != nil {
			return result, err
		}
		if len(result.Pages) == 50 {
			result.Next = result.Pages[49].Page
			break
		}
		switch p.Status {
		case "stored", "empty", "failed":
		default:
			p.Status = "unknown"
		}
		switch p.Method {
		case "text", "ocr":
		default:
			p.Method = ""
		}
		if math.IsNaN(p.Confidence) || math.IsInf(p.Confidence, 0) || p.Confidence < 0 || p.Confidence > 100 {
			p.Confidence = -1
		}
		result.Pages = append(result.Pages, p)
	}
	return result, rows.Err()
}
