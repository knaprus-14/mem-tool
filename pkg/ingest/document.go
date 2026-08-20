// Package ingest extracts source documents into canonical Markdown and
// provenance-bearing text blocks. It deliberately does not know about vector
// stores or embedding backends.
package ingest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatPDF      Format = "pdf"
	FormatDjVu     Format = "djvu"
)

// Document is the staged representation passed to the indexing layer.
type Document struct {
	ID         string
	SourcePath string
	Format     Format
	MediaType  string
	Title      string
	Markdown   string
	Blocks     []Block
	Warnings   []string
}

// Block is a source-local unit. Page is zero when the source provides no page
// information; Index is always zero-based and stable for one extraction.
type Block struct {
	Index   int
	Page    int
	Marker  string
	Heading string
	Text    string
	// Extraction is "text" for an embedded text layer and "ocr" for
	// Tesseract output. OCRConfidence is -1 when it is not applicable.
	Extraction    string
	OCRConfidence float64
	Warnings      []string
}

// Extractor is the extension point for future DjVu/OCR or custom converters.
type Extractor interface {
	Extract(context.Context, string) (Document, error)
}

// Registry dispatches extraction by lowercase filename extension.
type Registry struct {
	extractors map[string]Extractor
}

func NewRegistry() *Registry {
	r := &Registry{extractors: make(map[string]Extractor)}
	r.Register(".md", MarkdownExtractor{})
	r.Register(".markdown", MarkdownExtractor{})
	r.Register(".pdf", NewPDFExtractor())
	r.Register(".djvu", NewDjVuExtractor())
	r.Register(".djv", NewDjVuExtractor())
	return r
}

func (r *Registry) Register(extension string, extractor Extractor) {
	if r.extractors == nil {
		r.extractors = make(map[string]Extractor)
	}
	r.extractors[strings.ToLower(extension)] = extractor
}

func (r *Registry) Extract(ctx context.Context, path string) (Document, error) {
	ext := strings.ToLower(filepath.Ext(path))
	extractor, ok := r.extractors[ext]
	if !ok {
		return Document{}, fmt.Errorf("unsupported document format %q: mem import accepts Markdown (.md, .markdown), PDF (.pdf), and DjVu (.djvu, .djv)", ext)
	}
	doc, err := extractor.Extract(ctx, path)
	if err != nil {
		return Document{}, err
	}
	if len(doc.Blocks) == 0 {
		return Document{}, fmt.Errorf("document %s produced no non-empty text blocks", path)
	}
	return doc, nil
}

func Extract(ctx context.Context, path string) (Document, error) {
	return NewRegistry().Extract(ctx, path)
}

// ExtractWithOptions is the configurable entry point used by mem import and
// bounded extraction-only smoke tests. It never writes to a source document.
func ExtractWithOptions(ctx context.Context, path string, options Options) (Document, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown":
		return MarkdownExtractor{}.Extract(ctx, path)
	case ".pdf":
		return newEngine(options).extractPDF(ctx, path)
	case ".djvu", ".djv":
		return newEngine(options).extractDjVu(ctx, path)
	default:
		return Document{}, fmt.Errorf("unsupported document format %q: mem import accepts Markdown, PDF, and DjVu", ext)
	}
}

func canonicalSourcePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func documentID(sourcePath string) string {
	identity := sourcePath
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("doc-%x", sum[:12])
}
