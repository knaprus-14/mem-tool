package mem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestSuccessfulImportRunIsFinalizedWithDocumentTransaction(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := ingest.ParseMarkdown(filepath.Join(root, "book.md"), "journaled content")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.startDocumentImportRun(doc.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := importExtractedDocumentWithContextEmbedderForRun(context.Background(),
		testConfig(1000, "paragraph"), store, doc, ImportOptions{},
		func(context.Context, *Config, string) ([]float32, error) { return []float32{1}, nil }, runID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != runID || result.Status != DocumentImportRunSucceeded || result.Chunks != 1 {
		t.Fatalf("unexpected import result: %#v", result)
	}
	run, err := store.DocumentImportRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != DocumentImportRunSucceeded || !run.DocumentUpdated || run.Chunks != 1 || run.CompletedAt == "" {
		t.Fatalf("successful run is incomplete: %#v", run)
	}
	if _, err := store.db.Exec(`UPDATE document_import_runs SET error_message='changed' WHERE id=?`, runID); err == nil {
		t.Fatal("final import run was mutable")
	}
}

func TestPartialPageImportRunKeepsReadableFailureAndUpdatesSuccessfulPages(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := ingest.ParseMarkdown(filepath.Join(root, "scan.pdf"), "<!-- page: 1 -->\n\nReadable page")
	if err != nil {
		t.Fatal(err)
	}
	doc.Format, doc.MediaType = ingest.FormatPDF, "application/pdf"
	doc.PhysicalPageCount, doc.SelectedPageFirst, doc.SelectedPageLast = 2, 1, 2
	doc.PageManifest = []ingest.PageRecord{
		{Page: 1, Status: ingest.PageStatusStored, Extraction: "ocr", TextRunes: 12, OCRConfidence: 88},
		{Page: 2, Status: ingest.PageStatusFailed, Extraction: "ocr", OCRConfidence: -1,
			Warnings: []string{"OCR page 2: damaged image"}},
	}
	doc.Blocks[0].Extraction, doc.Blocks[0].OCRConfidence = "ocr", 88
	doc.Warnings = []string{"OCR page 2: damaged image"}
	doc.Revision = ingest.ContentRevision(doc)
	runID, err := store.startDocumentImportRun(doc.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := importExtractedDocumentWithContextEmbedderForRun(context.Background(),
		testConfig(1000, "paragraph"), store, doc, ImportOptions{},
		func(context.Context, *Config, string) ([]float32, error) { return []float32{1}, nil }, runID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != DocumentImportRunPartial || result.FailedPages != 1 {
		t.Fatalf("partial result was hidden: %#v", result)
	}
	run, err := store.DocumentImportRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != DocumentImportRunPartial || !run.DocumentUpdated || len(run.Pages) != 2 ||
		run.Pages[1].Status != DocumentImportPageFailed || !strings.Contains(run.Pages[1].Warnings[0], "page 2") {
		t.Fatalf("partial run lost physical-page failure: %#v", run)
	}
	if entries := store.GetBySourceFile(doc.SourcePath); len(entries) != 1 || entries[0].Page != 1 {
		t.Fatalf("successful page was not committed: %#v", entries)
	}
}

func TestFailedImportRunDoesNotReplacePreviousDocument(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(root, "book.md")
	stable, err := ingest.ParseMarkdown(source, "stable content")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(testConfig(1000, "paragraph"), store, stable,
		ImportOptions{}, func(*Config, string) ([]float32, error) { return []float32{1}, nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := ingest.ParseMarkdown(source, "replacement content")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.startDocumentImportRun(source)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("embedding backend unavailable")
	result, err := importExtractedDocumentWithContextEmbedderForRun(context.Background(),
		testConfig(1000, "paragraph"), store, updated, ImportOptions{},
		func(context.Context, *Config, string) ([]float32, error) { return nil, cause }, runID)
	if !errors.Is(err, cause) {
		t.Fatalf("embedding failure was hidden: %v", err)
	}
	if finishErr := finishFailedImportRun(store, runID, updated, DocumentImportRunFailed, ingest.StageEmbed, err); !errors.Is(finishErr, cause) {
		t.Fatalf("failed run finalization changed cause: %v", finishErr)
	}
	if result.Chunks != 0 {
		t.Fatalf("failed import reports stored chunks: %#v", result)
	}
	entries := store.GetBySourceFile(stable.SourcePath)
	if len(entries) != 1 || entries[0].Text != "stable content" {
		t.Fatalf("failed import replaced stable document: %#v", entries)
	}
	run, err := store.DocumentImportRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != DocumentImportRunFailed || run.DocumentUpdated || !strings.Contains(run.ErrorMessage, "embedding backend") {
		t.Fatalf("failed run record is incomplete: %#v", run)
	}
}

func TestImportRunCompletionFailureRollsBackDocumentReplacement(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(root, "atomic.md")
	stable, err := ingest.ParseMarkdown(source, "stable atomic content")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(testConfig(1000, "paragraph"), store, stable,
		ImportOptions{}, func(*Config, string) ([]float32, error) { return []float32{1}, nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := ingest.ParseMarkdown(source, "new content must roll back")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.startDocumentImportRun(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_import_run_completion
BEFORE UPDATE ON document_import_runs WHEN OLD.id = ` + fmt.Sprint(runID) + `
BEGIN SELECT RAISE(ABORT, 'synthetic journal failure'); END;`); err != nil {
		t.Fatal(err)
	}
	_, err = importExtractedDocumentWithContextEmbedderForRun(context.Background(),
		testConfig(1000, "paragraph"), store, updated, ImportOptions{},
		func(context.Context, *Config, string) ([]float32, error) { return []float32{2}, nil }, runID)
	if err == nil || !strings.Contains(err.Error(), "synthetic journal failure") {
		t.Fatalf("journal completion failure was hidden: %v", err)
	}
	entries := store.GetBySourceFile(stable.SourcePath)
	if len(entries) != 1 || entries[0].Text != "stable atomic content" {
		t.Fatalf("journal failure allowed document replacement: %#v", entries)
	}
	run, loadErr := store.DocumentImportRun(runID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if run.Status != DocumentImportRunRunning || run.DocumentUpdated {
		t.Fatalf("rolled-back transaction finalized the run: %#v", run)
	}
}

func TestStoreRecoversRunningImportAsInterrupted(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.startDocumentImportRun(filepath.Join(root, "book.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE document_import_runs SET owner_pid=2147483647 WHERE id=?`, runID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	run, err := reopened.DocumentImportRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != DocumentImportRunInterrupted || run.DocumentUpdated || run.CompletedAt == "" {
		t.Fatalf("running import was not recovered honestly: %#v", run)
	}
}

func TestSecondStoreDoesNotInterruptLiveImportProcess(t *testing.T) {
	root := t.TempDir()
	first, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	runID, err := first.startDocumentImportRun(filepath.Join(root, "active.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	run, err := second.DocumentImportRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != DocumentImportRunRunning {
		t.Fatalf("another live mem process interrupted an active import: %#v", run)
	}
}
