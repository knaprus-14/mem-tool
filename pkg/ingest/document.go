// Package ingest extracts source documents into canonical Markdown and
// provenance-bearing text blocks. It deliberately does not know about vector
// stores or embedding backends.
package ingest

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
)

type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatPDF      Format = "pdf"
	FormatDjVu     Format = "djvu"
)

// Document is the staged representation passed to the indexing layer.
type Document struct {
	managedIdentity   bool
	ID                string
	Revision          string
	SourceFileHash    string
	SourcePath        string
	Format            Format
	MediaType         string
	Title             string
	Markdown          string
	Blocks            []Block
	Warnings          []string
	PhysicalPageCount int
	SelectedPageFirst int
	SelectedPageLast  int
	PageManifest      []PageRecord
}

type PageStatus string

const (
	PageStatusStored PageStatus = "stored"
	PageStatusEmpty  PageStatus = "empty"
	PageStatusFailed PageStatus = "failed"
)

// PageRecord records the extraction outcome for one selected physical page,
// including pages that produced no chunk. Failed is reserved for extractors
// that can continue after a page-local failure.
type PageRecord struct {
	Page          int
	Status        PageStatus
	Extraction    string
	TextRunes     int
	OCRConfidence float64
	Warnings      []string
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
	for _, ext := range []string{".txt", ".csv", ".tsv", ".json", ".xml"} {
		r.Register(ext, TextExtractor{})
	}
	r.Register(".docx", DOCXExtractor{})
	return r
}

func (r *Registry) Register(extension string, extractor Extractor) {
	if r.extractors == nil {
		r.extractors = make(map[string]Extractor)
	}
	r.extractors[strings.ToLower(extension)] = extractor
}

func (r *Registry) Extract(ctx context.Context, path string) (applogResult0 Document, applogResult1 error) {
	defer applog.Track("pkg.ingest.Registry.Extract")(&applogResult1)

	ext := strings.ToLower(filepath.Ext(path))
	extractor, ok := r.extractors[ext]
	if !ok {
		return Document{}, fmt.Errorf("unsupported document format %q: mem import accepts Markdown, PDF, DjVu, DOCX and UTF-8 text (TXT, CSV, TSV, JSON, XML)", ext)
	}
	doc, err := extractor.Extract(ctx, path)
	if err != nil {
		return doc, err
	}
	if err := ValidateDocument(doc); err != nil {
		return Document{}, fmt.Errorf("invalid extracted document %s: %w", path, err)
	}
	return doc, nil
}

func Extract(ctx context.Context, path string) (applogResult0 Document, applogResult1 error) {
	defer applog.Track("pkg.ingest.Extract")(&applogResult1)

	return ExtractWithOptions(ctx, path, DefaultOptions())
}

// ExtractWithOptions is the configurable entry point used by mem import and
// bounded extraction-only smoke tests. It never writes to a source document.
func ExtractWithOptions(ctx context.Context, path string, options Options) (applogResult0 Document, applogResult1 error) {
	defer applog.Track("pkg.ingest.ExtractWithOptions")(&applogResult1)

	if ctx == nil {
		ctx = context.Background()
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown", ".pdf", ".djvu", ".djv", ".docx", ".txt", ".csv", ".tsv", ".json", ".xml":
	default:
		return Document{}, fmt.Errorf("unsupported document format %q: mem import accepts Markdown, PDF, DjVu, DOCX and UTF-8 text (TXT, CSV, TSV, JSON, XML)", ext)
	}
	fileHash, err := sourcefile.Hash(ctx, path)
	if err != nil {
		return Document{}, fmt.Errorf("read source fingerprint: %w", err)
	}
	var (
		doc Document
	)
	switch ext {
	case ".docx":
		doc, err = DOCXExtractor{}.Extract(ctx, path)
	case ".txt", ".csv", ".tsv", ".json", ".xml":
		doc, err = TextExtractor{}.Extract(ctx, path)
	case ".md", ".markdown":
		doc, err = MarkdownExtractor{}.Extract(ctx, path)
	case ".pdf":
		doc, err = newEngine(options).extractPDF(ctx, path)
	case ".djvu", ".djv":
		doc, err = newEngine(options).extractDjVu(ctx, path)
	default:
		return Document{}, fmt.Errorf("unsupported document format %q: mem import accepts Markdown, PDF, DjVu, DOCX and UTF-8 text (TXT, CSV, TSV, JSON, XML)", ext)
	}
	if err != nil {
		return doc, err
	}
	if err := ValidateDocument(doc); err != nil {
		return Document{}, fmt.Errorf("invalid extracted document %s: %w", path, err)
	}
	currentHash, err := sourcefile.Hash(ctx, path)
	if err != nil {
		return Document{}, err
	}
	if currentHash != fileHash {
		return Document{}, fmt.Errorf("исходный файл изменился во время извлечения; повторите импорт")
	}
	doc.SourceFileHash = fileHash
	doc.Revision = ContentRevision(doc)
	return doc, nil
}

// ValidateDocument checks the provenance contract shared by extractors and
// the indexing layer. It rejects metadata that could create ambiguous source
// anchors before embeddings or persistent state are changed.
func ValidateDocument(doc Document) (applogResult0 error) {

	if strings.TrimSpace(doc.ID) == "" {
		return fmt.Errorf("missing document identity")
	}
	if strings.TrimSpace(doc.SourcePath) == "" {
		return fmt.Errorf("missing source path")
	}
	if !filepath.IsAbs(doc.SourcePath) || filepath.Clean(doc.SourcePath) != doc.SourcePath {
		return fmt.Errorf("source path is not canonical and absolute: %q", doc.SourcePath)
	}
	if !doc.managedIdentity && doc.ID != documentID(doc.SourcePath) {
		return fmt.Errorf("document identity does not match canonical source path")
	}
	if doc.managedIdentity && !validManagedIdentity(doc.ID) {
		return fmt.Errorf("invalid managed document identity")
	}
	if strings.TrimSpace(string(doc.Format)) == "" || strings.TrimSpace(doc.MediaType) == "" {
		return fmt.Errorf("missing document format or media type")
	}
	if len(doc.Blocks) == 0 {
		return fmt.Errorf("no non-empty text blocks")
	}
	if err := validatePageManifest(doc); err != nil {
		return err
	}

	previousPage := 0
	for i, block := range doc.Blocks {
		if block.Index != i {
			return fmt.Errorf("block %d has index %d; indices must be contiguous and zero-based", i, block.Index)
		}
		if block.Page < 0 {
			return fmt.Errorf("block %d has negative page %d", i, block.Page)
		}
		if block.Page > 0 && previousPage > block.Page {
			return fmt.Errorf("block %d page %d precedes earlier page %d", i, block.Page, previousPage)
		}
		if block.Page > 0 {
			previousPage = block.Page
		}
		if strings.TrimSpace(block.Text) == "" {
			return fmt.Errorf("block %d has empty text", i)
		}
		if block.Extraction != "text" && block.Extraction != "ocr" {
			return fmt.Errorf("block %d has unsupported extraction method %q", i, block.Extraction)
		}
		if math.IsNaN(block.OCRConfidence) || math.IsInf(block.OCRConfidence, 0) || block.OCRConfidence < -1 || block.OCRConfidence > 100 {
			return fmt.Errorf("block %d has invalid OCR confidence %v", i, block.OCRConfidence)
		}
		if block.Extraction != "ocr" && block.OCRConfidence != -1 {
			return fmt.Errorf("block %d has OCR confidence for %q extraction", i, block.Extraction)
		}
		if len(doc.PageManifest) > 0 && block.Page > 0 {
			offset := block.Page - doc.SelectedPageFirst
			if offset < 0 || offset >= len(doc.PageManifest) {
				return fmt.Errorf("block %d page %d is outside selected page scope", i, block.Page)
			}
			page := doc.PageManifest[offset]
			if page.Status != PageStatusStored || page.Extraction != block.Extraction {
				return fmt.Errorf("block %d page %d contradicts page manifest", i, block.Page)
			}
		}
	}
	if doc.SourceFileHash != "" {
		value := strings.TrimPrefix(doc.SourceFileHash, "sha256:")
		decoded, err := hex.DecodeString(value)
		if !strings.HasPrefix(doc.SourceFileHash, "sha256:") || err != nil || len(decoded) != sha256.Size || value != strings.ToLower(value) {
			return fmt.Errorf("invalid physical source fingerprint")
		}
	}
	if strings.TrimSpace(doc.Revision) == "" {
		return fmt.Errorf("missing document content revision")
	}
	if expected := ContentRevision(doc); doc.Revision != expected {
		return fmt.Errorf("document content revision does not match extracted blocks")
	}
	return nil
}

// WithManagedIdentity explicitly opts an extracted upload into path-independent
// identity. Legacy extractor validation continues to require its canonical path.
func WithManagedIdentity(doc Document, id string) (applogResult0 Document, applogResult1 error) {

	if !validManagedIdentity(id) {
		return doc, fmt.Errorf("invalid managed document identity")
	}
	doc.ID = id
	doc.managedIdentity = true
	return doc, nil
}

func validManagedIdentity(id string) bool {
	if !strings.HasPrefix(id, "web:") || len(id) != 36 {
		return false
	}
	for _, r := range id[4:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validatePageManifest(doc Document) error {
	if len(doc.PageManifest) == 0 {
		if doc.PhysicalPageCount != 0 || doc.SelectedPageFirst != 0 || doc.SelectedPageLast != 0 {
			return fmt.Errorf("page scope is present without a page manifest")
		}
		return nil
	}
	if doc.PhysicalPageCount <= 0 {
		return fmt.Errorf("page manifest has no physical page count")
	}
	if doc.SelectedPageFirst <= 0 || doc.SelectedPageLast < doc.SelectedPageFirst || doc.SelectedPageLast > doc.PhysicalPageCount {
		return fmt.Errorf("invalid selected page scope %d-%d of %d", doc.SelectedPageFirst, doc.SelectedPageLast, doc.PhysicalPageCount)
	}
	if len(doc.PageManifest) != doc.SelectedPageLast-doc.SelectedPageFirst+1 {
		return fmt.Errorf("page manifest has %d records for selected range %d-%d", len(doc.PageManifest), doc.SelectedPageFirst, doc.SelectedPageLast)
	}
	for index, page := range doc.PageManifest {
		expectedPage := doc.SelectedPageFirst + index
		if page.Page != expectedPage {
			return fmt.Errorf("page manifest record %d has page %d, want %d", index, page.Page, expectedPage)
		}
		if page.TextRunes < 0 {
			return fmt.Errorf("page %d has negative text rune count", page.Page)
		}
		if math.IsNaN(page.OCRConfidence) || math.IsInf(page.OCRConfidence, 0) || page.OCRConfidence < -1 || page.OCRConfidence > 100 {
			return fmt.Errorf("page %d has invalid OCR confidence %v", page.Page, page.OCRConfidence)
		}
		switch page.Status {
		case PageStatusStored:
			if page.TextRunes == 0 || (page.Extraction != "text" && page.Extraction != "ocr") {
				return fmt.Errorf("stored page %d has no usable extraction metadata", page.Page)
			}
		case PageStatusEmpty:
			if page.TextRunes != 0 {
				return fmt.Errorf("empty page %d reports extracted text", page.Page)
			}
		case PageStatusFailed:
			if page.TextRunes != 0 || len(page.Warnings) == 0 {
				return fmt.Errorf("failed page %d has no failure reason", page.Page)
			}
		default:
			return fmt.Errorf("page %d has unsupported status %q", page.Page, page.Status)
		}
		if page.Extraction != "ocr" && page.OCRConfidence != -1 {
			return fmt.Errorf("page %d has OCR confidence for %q extraction", page.Page, page.Extraction)
		}
	}
	return nil
}

// ContentRevision keeps the legacy text-only revision for documents without a
// physical fingerprint. Verified extraction additionally pins the original's
// bytes and logical locator, so image-only changes and retained uploads at a
// different path cannot reuse an older physical edition. Stored legacy anchors
// are never rewritten or assigned a fingerprint by this function.
func ContentRevision(doc Document) string {
	h := sha256.New()
	version := "mem-tool-document-content-v1"
	if len(doc.PageManifest) > 0 {
		version = "mem-tool-document-content-v2"
	}
	writeRevisionField(h, version)
	for _, block := range doc.Blocks {
		writeRevisionField(h, strconv.Itoa(block.Page))
		writeRevisionField(h, block.Marker)
		writeRevisionField(h, block.Text)
	}
	if len(doc.PageManifest) > 0 {
		writeRevisionField(h, strconv.Itoa(doc.PhysicalPageCount))
		writeRevisionField(h, strconv.Itoa(doc.SelectedPageFirst))
		writeRevisionField(h, strconv.Itoa(doc.SelectedPageLast))
		for _, page := range doc.PageManifest {
			writeRevisionField(h, strconv.Itoa(page.Page))
			writeRevisionField(h, string(page.Status))
		}
	}
	return PhysicalSourceRevision("sha256:"+hex.EncodeToString(h.Sum(nil)), doc.SourcePath, doc.SourceFileHash)
}

// PhysicalSourceRevision binds extracted content to a verified physical source.
// sourcePath is the original logical locator, not a temporary extraction copy or
// a portable archive's relocated path. Empty fingerprints retain legacy IDs.
func PhysicalSourceRevision(contentRevision, sourcePath, fingerprint string) string {
	if fingerprint == "" {
		return contentRevision
	}
	h := sha256.New()
	for _, value := range []string{"mem-tool-physical-source-revision-v1", contentRevision, physicalSourceLocator(sourcePath), fingerprint} {
		writeRevisionField(h, value)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// The stored locator belongs to its originating filesystem, not this process's
// host. A Windows original must keep its digest when rechunked on Linux after a
// portable transfer, including drive case, separators and UNC spelling.
func physicalSourceLocator(locator string) string {
	drive := len(locator) >= 3 && ((locator[0] >= 'A' && locator[0] <= 'Z') || (locator[0] >= 'a' && locator[0] <= 'z')) && locator[1] == ':' && (locator[2] == '\\' || locator[2] == '/')
	if drive || strings.HasPrefix(locator, `\\`) || strings.HasPrefix(locator, "//") {
		return "windows:" + strings.ToLower(path.Clean(strings.ReplaceAll(locator, `\`, "/")))
	}
	return "posix:" + path.Clean(locator)
}

func writeRevisionField(h hash.Hash, value string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(value))
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
