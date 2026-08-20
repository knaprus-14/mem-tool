package ingest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMarkdownPageMarkers(t *testing.T) {
	input := "# Preface\nBefore pagination.\n\n<!-- page: 12 -->\n\n# Chapter\nPage twelve.\n\n<!-- PAGE: 13 -->\nPage thirteen."
	doc, err := ParseMarkdown(filepath.Join(t.TempDir(), "book.md"), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3: %#v", len(doc.Blocks), doc.Blocks)
	}
	if doc.Blocks[0].Page != 0 || doc.Blocks[1].Page != 12 || doc.Blocks[2].Page != 13 {
		t.Fatalf("unexpected pages: %d, %d, %d", doc.Blocks[0].Page, doc.Blocks[1].Page, doc.Blocks[2].Page)
	}
	if doc.Blocks[1].Marker != "<!-- page: 12 -->" || doc.Blocks[1].Heading != "Chapter" {
		t.Fatalf("page provenance lost: %#v", doc.Blocks[1])
	}
	if !strings.Contains(doc.Markdown, "<!-- page: 12 -->") {
		t.Fatal("canonical Markdown did not preserve the page marker")
	}
	if doc.ID == "" || !filepath.IsAbs(doc.SourcePath) {
		t.Fatalf("missing document identity: %#v", doc)
	}
}

func TestParseMarkdownRejectsEmptyPageOnlyDocument(t *testing.T) {
	_, err := ParseMarkdown(filepath.Join(t.TempDir(), "empty.md"), "<!-- page: 1 -->")
	if err == nil {
		t.Fatal("page markers without text were accepted")
	}
}
