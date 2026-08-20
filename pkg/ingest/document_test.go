package ingest

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDocumentRejectsAmbiguousProvenance(t *testing.T) {
	doc, err := ParseMarkdown(filepath.Join(t.TempDir(), "book.md"), "preface\n\n<!-- page: 3 -->\n\nbody")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Document)
		want   string
	}{
		{"missing identity", func(value *Document) { value.ID = "" }, "missing document identity"},
		{"identity mismatch", func(value *Document) { value.ID = "doc-other" }, "identity does not match"},
		{"relative source", func(value *Document) { value.SourcePath = "book.md" }, "not canonical and absolute"},
		{"missing media", func(value *Document) { value.MediaType = "" }, "missing document format or media type"},
		{"block index", func(value *Document) { value.Blocks[1].Index = 8 }, "indices must be contiguous"},
		{"negative page", func(value *Document) { value.Blocks[1].Page = -1 }, "negative page"},
		{"page order", func(value *Document) { value.Blocks[0].Page, value.Blocks[1].Page = 4, 3 }, "precedes earlier page"},
		{"empty text", func(value *Document) { value.Blocks[0].Text = " \n" }, "empty text"},
		{"method", func(value *Document) { value.Blocks[0].Extraction = "guess" }, "unsupported extraction"},
		{"confidence", func(value *Document) { value.Blocks[0].OCRConfidence = math.NaN() }, "invalid OCR confidence"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := doc
			invalid.Blocks = append([]Block(nil), doc.Blocks...)
			tt.mutate(&invalid)
			if err := ValidateDocument(invalid); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateDocumentAcceptsOCRConfidenceSentinel(t *testing.T) {
	doc, err := ParseMarkdown(filepath.Join(t.TempDir(), "scan.md"), "recognized text")
	if err != nil {
		t.Fatal(err)
	}
	doc.Blocks[0].Extraction = "ocr"
	doc.Blocks[0].OCRConfidence = -1
	if err := ValidateDocument(doc); err != nil {
		t.Fatalf("OCR confidence sentinel was rejected: %v", err)
	}
}

func TestContentRevisionTracksCanonicalExtractedContent(t *testing.T) {
	source := filepath.Join(t.TempDir(), "book.md")
	first, err := ParseMarkdown(source, "first\r\n\r\n<!-- page: 2 -->\r\n\r\ntarget")
	if err != nil {
		t.Fatal(err)
	}
	same, err := ParseMarkdown(source, "first\n\n<!-- page: 2 -->\n\ntarget")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ParseMarkdown(source, "changed\n\n<!-- page: 2 -->\n\ntarget")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == "" || first.Revision != same.Revision {
		t.Fatalf("canonical line endings changed revision: %q vs %q", first.Revision, same.Revision)
	}
	if changed.ID != first.ID || changed.Revision == first.Revision {
		t.Fatalf("content edit should preserve identity and change revision: first=%#v changed=%#v", first, changed)
	}

	changed.Revision = first.Revision
	if err := ValidateDocument(changed); err == nil || !strings.Contains(err.Error(), "revision does not match") {
		t.Fatalf("stale content revision was accepted: %v", err)
	}
}
