package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDjVuEmbeddedTextWinsBeforeOCR(t *testing.T) {
	path := fixtureFile(t, "book.djvu")
	e := newEngine(Options{OCR: OCRConfig{MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "djvutxt" {
			return name, nil
		}
		return "", errors.New("unexpected OCR tool")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte("page one text\fpage two text\f")}, nil
	}
	doc, err := e.extractDjVu(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[1].Page != 2 || doc.Blocks[0].Extraction != "text" {
		t.Fatalf("unexpected embedded result: %#v", doc.Blocks)
	}
}

func TestDjVuOCRRangeHasPhysicalPages(t *testing.T) {
	path := fixtureFile(t, "scan.djvu")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{Pages: PageRange{First: 2, Last: 3}, OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) { return name, nil }
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "djvutxt":
			return commandOutput{}, nil
		case "ddjvu":
			if err := os.WriteFile(args[len(args)-1], []byte("image"), 0o600); err != nil {
				return commandOutput{}, err
			}
			return commandOutput{}, nil
		case "tesseract":
			return commandOutput{stdout: []byte(fakeTSV)}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	doc, err := e.extractDjVu(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Page != 2 || doc.Blocks[1].Page != 3 {
		t.Fatalf("physical pages lost: %#v", doc.Blocks)
	}
	if doc.Blocks[0].OCRConfidence != 86 || doc.Blocks[0].Extraction != "ocr" {
		t.Fatalf("OCR provenance lost: %#v", doc.Blocks[0])
	}
}

func TestDjVuHybridEmbeddedTextOCRsOnlyMissingPage(t *testing.T) {
	path := fixtureFile(t, "hybrid.djvu")
	tessdata := t.TempDir()
	if err := os.WriteFile(filepath.Join(tessdata, "eng.traineddata"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(Options{Pages: PageRange{First: 1, Last: 2}, OCR: OCRConfig{Languages: "eng", TessdataDir: tessdata, MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) { return name, nil }
	var renderedPages []string
	e.run = func(_ context.Context, name string, args ...string) (commandOutput, error) {
		switch name {
		case "djvutxt":
			return commandOutput{stdout: []byte("embedded page text\f\f")}, nil
		case "ddjvu":
			renderedPages = append(renderedPages, args[1])
			if err := os.WriteFile(args[len(args)-1], []byte("image"), 0o600); err != nil {
				return commandOutput{}, err
			}
			return commandOutput{}, nil
		case "tesseract":
			return commandOutput{stdout: []byte(fakeTSV)}, nil
		default:
			return commandOutput{}, errors.New("unexpected tool")
		}
	}
	doc, err := e.extractDjVu(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 2 || doc.Blocks[0].Extraction != "text" || doc.Blocks[1].Page != 2 || doc.Blocks[1].Extraction != "ocr" {
		t.Fatalf("hybrid provenance is wrong: %#v", doc.Blocks)
	}
	if len(renderedPages) != 1 || renderedPages[0] != "-page=2" {
		t.Fatalf("rendered pages = %#v, want only page 2", renderedPages)
	}
}
