package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeTSV = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
	"5\t1\t1\t1\t1\t1\t0\t0\t10\t10\t91.0\tПривет\n" +
	"5\t1\t1\t1\t1\t2\t0\t0\t10\t10\t81.0\tworld\n"

func TestTesseractTSVTextAndConfidence(t *testing.T) {
	text, confidence, err := parseTesseractTSV(fakeTSV)
	if err != nil || text != "Привет world" || confidence != 86 {
		t.Fatalf("got %q %.1f %v", text, confidence, err)
	}
}

func TestRunTesseractEmitsLowConfidenceWarning(t *testing.T) {
	e, _ := fakeOCREngine(t)
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte(strings.ReplaceAll(strings.ReplaceAll(fakeTSV, "91.0", "11.0"), "81.0", "21.0"))}, nil
	}
	page, err := e.runTesseract(context.Background(), "tesseract", e.options.OCR.TessdataDir, "fixture.png")
	if err != nil {
		t.Fatal(err)
	}
	if page.confidence != 16 || len(page.warnings) != 1 || !strings.Contains(page.warnings[0], "below") {
		t.Fatalf("low-confidence warning missing: %#v", page)
	}
}

func TestMissingOCRLanguageIsActionable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveTessdata(dir, filepath.Join(dir, "tesseract.exe"), "rus+eng")
	if err == nil || !strings.Contains(err.Error(), "rus") || !strings.Contains(err.Error(), "MEM_TESSDATA_DIR") {
		t.Fatalf("missing language error is not actionable: %v", err)
	}
}

func TestMissingTesseractIsActionableBeforeRendering(t *testing.T) {
	e := newEngine(Options{OCR: OCRConfig{Languages: "eng"}})
	e.resolve = func(name, explicit string) (string, error) {
		return "", errors.New("not found in config, PATH, or standard locations")
	}
	rendered := false
	_, err := e.ocrRenderedPages(context.Background(), 1, 1, 1, ".png", func(context.Context, int, string) error { rendered = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "Tesseract") || rendered {
		t.Fatalf("missing Tesseract was not checked before rendering: err=%v rendered=%v", err, rendered)
	}
}

func TestOCRCleanupOnPartialFailure(t *testing.T) {
	e, tempParent := fakeOCREngine(t)
	created := ""
	e.mkdirTemp = func(root, pattern string) (string, error) {
		var err error
		created, err = os.MkdirTemp(tempParent, pattern)
		return created, err
	}
	calls := 0
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		calls++
		if calls == 2 {
			return commandOutput{stderr: []byte("synthetic failure")}, errors.New("exit 1")
		}
		return commandOutput{stdout: []byte(fakeTSV)}, nil
	}
	render := func(_ context.Context, page int, output string) error {
		return os.WriteFile(output, []byte(fmt.Sprint(page)), 0o600)
	}
	_, err := e.ocrRenderedPages(context.Background(), 1, 2, 2, ".png", render)
	if err == nil || !strings.Contains(err.Error(), "page 2") {
		t.Fatalf("partial OCR failure hidden: %v", err)
	}
	if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
		t.Fatalf("temporary directory was not cleaned: %v", statErr)
	}
}

func TestOCRCancellationCleansTempDirectory(t *testing.T) {
	e, tempParent := fakeOCREngine(t)
	created := ""
	e.mkdirTemp = func(root, pattern string) (string, error) {
		var err error
		created, err = os.MkdirTemp(tempParent, pattern)
		return created, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.ocrRenderedPages(ctx, 1, 2, 2, ".png", func(context.Context, int, string) error { t.Fatal("render called after cancellation"); return nil })
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancellation not reported: %v", err)
	}
	if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
		t.Fatalf("temporary directory was not cleaned: %v", statErr)
	}
}

func fakeOCREngine(t *testing.T) (*engine, string) {
	t.Helper()
	temp := t.TempDir()
	tessdata := filepath.Join(temp, "tessdata")
	if err := os.Mkdir(tessdata, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, LowConfidence: 65}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "tesseract" {
			return filepath.Join(temp, "tesseract.exe"), nil
		}
		return "", errors.New("not found")
	}
	return e, temp
}
