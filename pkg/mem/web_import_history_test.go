package mem

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebImportHistoryPagesBoundedPrivateAndPinned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mem")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.startDocumentImportRun(filepath.Join(dir, "private-original.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 121; page++ {
		status := "stored"
		if page == 2 {
			status = "empty"
		}
		if page == 3 {
			status = "failed"
		}
		if _, err = s.db.Exec(`INSERT INTO document_import_run_pages(run_id,page,status,extraction_method,text_runes,ocr_confidence,block_count,chunk_count,warnings) VALUES(?,?,?,'ocr',40,87.5,1,2,?)`, id, page, status, `["private-original.pdf SECRET-TOKEN"]`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`UPDATE document_import_runs SET status='partial',physical_page_count=121,stored_pages=119,empty_pages=1,failed_pages=1,chunks=238,document_updated=1,error_message='SECRET-TOKEN',warnings='["private path"]' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	first, err := ReadWebImportPagesIn(context.Background(), dir, id, 0)
	if err != nil || len(first.Pages) != 50 || first.Next != 50 || first.Pages[1].Status != "empty" || first.Pages[2].Status != "failed" || first.Pages[0].Confidence != 87.5 || !first.HasError || !first.HasWarnings || !first.Updated {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "private") {
		t.Fatalf("private data: %s", data)
	}
	second, err := ReadWebImportPagesIn(context.Background(), dir, id, first.Next)
	if err != nil || second.Pages[0].Page != 51 || second.Next != 100 {
		t.Fatalf("second=%+v %v", second, err)
	}
	last, err := ReadWebImportPagesIn(context.Background(), dir, id, second.Next)
	if err != nil || len(last.Pages) != 21 || last.Next != -1 || last.Pages[20].Page != 121 {
		t.Fatalf("last=%+v %v", last, err)
	}
	// A later attempt for the same file must not change the selected report.
	if _, err = s.startDocumentImportRun(filepath.Join(dir, "private-original.pdf")); err != nil {
		t.Fatal(err)
	}
	again, err := ReadWebImportPagesIn(context.Background(), dir, id, 0)
	if err != nil || again.Status != "partial" || again.Chunks != 238 {
		t.Fatalf("history changed: %+v %v", again, err)
	}
	if _, err = ReadWebImportPagesIn(context.Background(), dir, id, -1); err != ErrQueryInvalid {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ReadWebImportPagesIn(ctx, dir, id, 0); err == nil {
		t.Fatal("cancel ignored")
	}
}
