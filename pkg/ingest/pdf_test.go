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
	e := newEngine(Options{OCR: OCRConfig{MinTextRunes: 5}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte("first page text\f\fthird page text\f")}, nil
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
}

func TestPDFPoorTextIsClassifiedAsOCRRequired(t *testing.T) {
	path := fixtureFile(t, "scan.pdf")
	e := newEngine(Options{OCR: OCRConfig{MinTextRunes: 40}, Pages: PageRange{First: 1, Last: 1}})
	e.resolve = func(name, explicit string) (string, error) {
		if name == "pdftotext" {
			return name, nil
		}
		return "", errors.New("not found")
	}
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		return commandOutput{stdout: []byte("x\f")}, nil
	}
	_, err := e.extractPDF(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "requires OCR") || !strings.Contains(err.Error(), "renderer") {
		t.Fatalf("poor text was not reported as actionable OCR-required: %v", err)
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
