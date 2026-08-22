package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFTextLayerKeepsPhysicalPageNumbers(t *testing.T) {
	path := fixtureFile(t, "book.pdf")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" || name == "pdftoppm" || name == "tesseract" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "pdftotext":
			return commandOutput{stdout: []byte("first page text\f\fthird page text\f")}, nil
		case "pdftoppm":
			return commandOutput{}, os.WriteFile(args[len(args)-1]+".png", []byte("image"), 0o600)
		case "tesseract":
			return commandOutput{stdout: []byte("level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n")}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Page != 1 || doc.Blocks[1].Page != 3 {
		t.Fatalf("physical PDF pages were renumbered: %#v", doc.Blocks)
	}
	if doc.Blocks[0].Extraction != "text" || doc.MediaType != "application/pdf" {
		t.Fatalf("unexpected PDF metadata: %#v", doc)
	}
	if len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "page 2") {
		t.Fatalf("blank physical page warning was lost: %#v", doc.Warnings)
	}
	if doc.PhysicalPageCount != 3 || doc.SelectedPageFirst != 1 || doc.SelectedPageLast != 3 || len(doc.PageManifest) != 3 {
		t.Fatalf("physical page manifest is incomplete: %#v", doc.PageManifest)
	}
	if doc.PageManifest[1].Status != PageStatusEmpty || doc.PageManifest[1].Extraction != "ocr" ||
		len(doc.PageManifest[1].Warnings) != 1 {
		t.Fatalf("blank OCR page was not classified: %#v", doc.PageManifest[1])
	}
}

func TestPDFHybridTextLayerOCRsOnlyMissingPage(t *testing.T) {
	path := fixtureFile(t, "hybrid.pdf")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var progress []ProgressEvent
	e := newEngine(Options{Pages: PageRange{First: 1, Last: 2}, OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 5}, Progress: func(event ProgressEvent) { progress = append(progress, event) }})
	e.resolve = func(name, explicit string) (string, error) { return name, nil }
	var renderedPages []string
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "pdftotext":
			return commandOutput{stdout: []byte("embedded page text\f\f")}, nil
		case "pdftoppm":
			renderedPages = append(renderedPages, args[1])
			return commandOutput{}, os.WriteFile(args[len(args)-1]+".png", []byte("image"), 0o600)
		case "tesseract":
			return commandOutput{stdout: []byte(fakeTSV)}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Page != 1 || doc.Blocks[0].Extraction != "text" || doc.Blocks[1].Page != 2 || doc.Blocks[1].Extraction != "ocr" {
		t.Fatalf("hybrid provenance is wrong: %#v", doc.Blocks)
	}
	if len(renderedPages) != 1 || renderedPages[0] != "2" {
		t.Fatalf("rendered pages = %#v, want only page 2", renderedPages)
	}
	if len(doc.PageManifest) != 2 || doc.PageManifest[0].Status != PageStatusStored ||
		doc.PageManifest[1].Status != PageStatusStored || doc.PageManifest[1].Extraction != "ocr" {
		t.Fatalf("hybrid page manifest is wrong: %#v", doc.PageManifest)
	}
	foundSparseProgress := false
	for _, event := range progress {
		if event.Stage == StageRender && event.Page == 2 && event.Current == 1 && event.Total == 1 {
			foundSparseProgress = true
		}
	}
	if !foundSparseProgress {
		t.Fatalf("sparse OCR progress is misleading: %#v", progress)
	}
}

func TestPDFKeepsSuccessfulPagesWhenAnotherOCRPageFails(t *testing.T) {
	path := fixtureFile(t, "partial-scan.pdf")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{
		Pages: PageRange{First: 1, Last: 2},
		OCR:   OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 5},
	})
	e.resolve = func(name, explicit string) (string, error) { return name, nil }
	tesseractCalls := 0
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "pdftotext":
			return commandOutput{stdout: []byte("\f\f")}, nil
		case "pdftoppm":
			return commandOutput{}, os.WriteFile(args[len(args)-1]+".png", []byte("image"), 0o600)
		case "tesseract":
			tesseractCalls++
			if tesseractCalls == 2 {
				return commandOutput{stderr: []byte("damaged page image")}, errors.New("exit 1")
			}
			return commandOutput{stdout: []byte(fakeTSV)}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 1 || doc.Blocks[0].Page != 1 || len(doc.PageManifest) != 2 {
		t.Fatalf("partial OCR document is incomplete: %#v", doc)
	}
	failed := doc.PageManifest[1]
	if failed.Status != PageStatusFailed || failed.Extraction != "ocr" ||
		len(failed.Warnings) != 1 || !strings.Contains(failed.Warnings[0], "damaged page image") {
		t.Fatalf("failed physical page was not preserved: %#v", failed)
	}
	if err := ValidateDocument(doc); err != nil {
		t.Fatalf("partial OCR document is invalid: %v", err)
	}
}

func TestFailedOnlyPhysicalPagesRemainAvailableWithExtractionError(t *testing.T) {
	source := filepath.Join(t.TempDir(), "broken.pdf")
	doc, err := buildDocumentFromPages(source, FormatPDF, "application/pdf", []extractedPage{
		failedOCRPage(1, errors.New("render page 1: damaged PDF")),
		failedOCRPage(2, errors.New("OCR page 2: unreadable image")),
	}, 2, 1, 2)
	if err == nil || !strings.Contains(err.Error(), "no non-empty pages") {
		t.Fatalf("all-failed extraction did not fail: %v", err)
	}
	if doc.SourcePath != source || len(doc.PageManifest) != 2 ||
		doc.PageManifest[0].Status != PageStatusFailed || doc.PageManifest[1].Status != PageStatusFailed {
		t.Fatalf("all-failed extraction lost page diagnostics: %#v", doc)
	}
}

func TestPDFEmptyTextIsClassifiedAsOCRRequired(t *testing.T) {
	path := fixtureFile(t, "scan.pdf")
	e := newEngine(Options{OCR: OCRConfig{MinTextRunes: 40}, Pages: PageRange{First: 1, Last: 1}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte("\f")}, nil
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "requires OCR") || !strings.Contains(err.Error(), "renderer") {
		t.Fatalf("poor text was not reported as actionable OCR-required: %v", err)
	}
	if len(doc.PageManifest) != 1 || doc.PageManifest[0].Status != PageStatusFailed ||
		!strings.Contains(doc.PageManifest[0].Warnings[0], "renderer") {
		t.Fatalf("failed OCR setup lost physical-page diagnostics: %#v", doc)
	}
}

func TestPDFRetainsShortWholeDocumentWhenOCRUnavailable(t *testing.T) {
	path := fixtureFile(t, "short.pdf")
	e := newEngine(Options{Pages: PageRange{First: 1, Last: 1}, OCR: OCRConfig{MinTextRunes: 40}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte("short but useful text\f")}, nil
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 1 || doc.Blocks[0].Text != "short but useful text" {
		t.Fatalf("short document text was discarded: %#v", doc.Blocks)
	}
	if len(doc.Blocks[0].Warnings) != 1 || !strings.Contains(doc.Blocks[0].Warnings[0], "OCR unavailable") {
		t.Fatalf("short document uncertainty was not preserved: %#v", doc.Blocks[0].Warnings)
	}
}

func TestPDFDoesNotSwallowOCRCancellation(t *testing.T) {
	path := fixtureFile(t, "cancelled.pdf")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{
		Pages: PageRange{First: 1, Last: 2},
		OCR:   OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 40},
	})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" || name == "pdftoppm" || name == "tesseract" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(_ context.Context, name string, _ ...string) (commandOutput, error) {
		if name == "pdftotext" {
			return commandOutput{stdout: []byte(strings.Repeat("rich text ", 8) + "\fshort page\f")}, nil
		}
		return commandOutput{}, errors.New("unexpected tool invocation after cancellation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.extractPDF(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OCR cancellation was swallowed: %v", err)
	}
}

func TestPDFUsesPythonPyMuPDFFallback(t *testing.T) {
	path := fixtureFile(t, "python-text.pdf")
	e := newEngine(Options{OCR: OCRConfig{MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "python" {
			return "python", nil
		}
		return "", errors.New("not found")
	}
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		if name != "python" || len(args) < 5 || args[0] != "-c" || args[2] != path {
			return commandOutput{}, errors.New("unexpected Python invocation")
		}
		return commandOutput{stdout: []byte("first page text\fsecond page text\f")}, nil
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Page != 1 || doc.Blocks[1].Page != 2 {
		t.Fatalf("PyMuPDF fallback lost physical pages: %#v", doc.Blocks)
	}
}

func TestPDFRetainsNonEmptySparseTextWhenOCRUnavailable(t *testing.T) {
	path := fixtureFile(t, "sparse-text.pdf")
	e := newEngine(Options{Pages: PageRange{First: 1, Last: 2}, OCR: OCRConfig{MinTextRunes: 40}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte(strings.Repeat("rich text ", 8) + "\fshort page heading\f")}, nil
	}
	doc, err := e.extractPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[1].Page != 2 || doc.Blocks[1].Extraction != "text" {
		t.Fatalf("sparse text page was discarded: %#v", doc.Blocks)
	}
	if len(doc.Blocks[1].Warnings) != 1 || !strings.Contains(doc.Blocks[1].Warnings[0], "OCR unavailable") {
		t.Fatalf("sparse text uncertainty was not preserved: %#v", doc.Blocks[1].Warnings)
	}
}

func TestPDFOCRUsesPythonPyMuPDFRenderer(t *testing.T) {
	path := fixtureFile(t, "python-render.pdf")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, DPI: 150}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "python" || name == "tesseract" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "python":
			if len(args) < 6 || args[0] != "-c" || args[2] != path || args[3] != "2" || args[4] != "150" {
				return commandOutput{}, errors.New("unexpected renderer invocation")
			}
			return commandOutput{}, os.WriteFile(args[5], []byte("png"), 0o600)
		case "tesseract":
			return commandOutput{stdout: []byte(fakeTSV)}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	pages, err := e.ocrPDFPages(context.Background(), path, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].page != 2 || pages[0].method != "ocr" {
		t.Fatalf("unexpected OCR result: %#v", pages)
	}
}

func TestParsePDFTextKeepsPhysicalPageNumbers(t *testing.T) {
	doc, err := parsePDFText(filepath.Join(t.TempDir(), "book.pdf"), "first\f\fthird\f")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Page != 1 || doc.Blocks[1].Page != 3 {
		t.Fatalf("physical PDF pages were renumbered: %#v", doc.Blocks)
	}
}

func fixtureFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
