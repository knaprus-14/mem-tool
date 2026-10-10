package ingest

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextDocumentPreservesMarkupWithoutInventingPages(t *testing.T) {
	for _, ext := range []string{"txt", "csv", "tsv", "json", "xml"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source."+ext)
			text := "<!-- page: 99 -->\nТекст, значения 5 бар\t8 м.\n\nКонец."
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			doc, err := Extract(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Markdown != text || doc.PhysicalPageCount != 0 || doc.Blocks[0].Page != 0 || doc.Format != "text" || doc.SourceFileHash == "" {
				t.Fatal(doc)
			}
			if _, err = NewRegistry().Extract(t.Context(), path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writeTestDOCX(t *testing.T, parts map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "document.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(file)
	for name, value := range parts {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDOCXTextTablesNotesAndImageWarning(t *testing.T) {
	const prefix = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	const suffix = `</w:body></w:document>`
	path := writeTestDOCX(t, map[string]string{
		"word/document.xml":     prefix + `<w:p><w:r><w:t>Давление </w:t></w:r><w:r><w:t>5 бар.</w:t></w:r><w:del><w:r><w:delText>9 бар.</w:delText></w:r></w:del></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Столбец 1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Столбец 2</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` + suffix,
		"word/footnotes.xml":    prefix + `<w:p><w:r><w:t>Условие из сноски.</w:t></w:r></w:p>` + suffix,
		"word/media/image1.png": "image placeholder",
	})
	doc, err := Extract(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"Давление 5 бар.", "Столбец 1", "Столбец 2", "Условие из сноски."} {
		if !strings.Contains(doc.Markdown, wanted) {
			t.Fatal("missing", wanted)
		}
	}
	if strings.Contains(doc.Markdown, "9 бар") || len(doc.Warnings) < 2 || doc.PhysicalPageCount != 0 || doc.Format != "docx" {
		t.Fatal(doc)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = Extract(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDOCXInvalidAndImageOnlyHaveExplicitErrors(t *testing.T) {
	for _, parts := range []map[string]string{{"unknown": "data"}, {"word/document.xml": "<broken>"}, {"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`}} {
		if _, err := Extract(t.Context(), writeTestDOCX(t, parts)); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
}
