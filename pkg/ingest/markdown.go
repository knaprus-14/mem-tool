package ingest

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
)

var (
	pageMarkerLine = regexp.MustCompile(`(?i)^\s*<!--\s*page:\s*([0-9]+)\s*-->\s*$`)
	headingLine    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
)

type MarkdownExtractor struct{}

func (MarkdownExtractor) Extract(ctx context.Context, path string) (applogResult0 Document, applogResult1 error) {
	defer applog.Track("pkg.ingest.MarkdownExtractor.Extract")(&applogResult1)

	data, err := sourcefile.ReadText(ctx, path)
	if err != nil {
		return Document{}, fmt.Errorf("read Markdown %s: %w", path, err)
	}
	return ParseMarkdown(path, string(data))
}

// ParseMarkdown preserves page marker lines in Document.Markdown and attaches
// the active marker/page number to every emitted block.
func ParseMarkdown(sourcePath, markdown string) (applogResult0 Document, applogResult1 error) {
	defer applog.Track("pkg.ingest.ParseMarkdown")(&applogResult1)

	canonicalPath, err := canonicalSourcePath(sourcePath)
	if err != nil {
		return Document{}, fmt.Errorf("canonical source path: %w", err)
	}

	markdown = strings.TrimPrefix(markdown, "\ufeff")
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return Document{}, fmt.Errorf("Markdown document is empty: %s", canonicalPath)
	}

	doc := Document{
		ID:         documentID(canonicalPath),
		SourcePath: canonicalPath,
		Format:     FormatMarkdown,
		MediaType:  "text/markdown",
		Title:      filepath.Base(canonicalPath),
		Markdown:   markdown,
	}

	var lines []string
	page := 0
	marker := ""
	flush := func() {
		text := strings.TrimSpace(strings.Join(lines, "\n"))
		lines = lines[:0]
		if text == "" {
			return
		}
		heading := ""
		if match := headingLine.FindStringSubmatch(text); len(match) == 2 {
			heading = strings.TrimSpace(match[1])
			if doc.Title == filepath.Base(canonicalPath) {
				doc.Title = heading
			}
		}
		doc.Blocks = append(doc.Blocks, Block{
			Index: len(doc.Blocks), Page: page, Marker: marker,
			Heading: heading, Text: text, Extraction: "text", OCRConfidence: -1,
		})
	}

	for _, line := range strings.Split(markdown, "\n") {
		match := pageMarkerLine.FindStringSubmatch(line)
		if len(match) != 2 {
			lines = append(lines, line)
			continue
		}
		flush()
		page, err = strconv.Atoi(match[1])
		if err != nil || page <= 0 {
			return Document{}, fmt.Errorf("invalid page marker %q in %s", line, canonicalPath)
		}
		marker = fmt.Sprintf("<!-- page: %d -->", page)
	}
	flush()
	if len(doc.Blocks) == 0 {
		return Document{}, fmt.Errorf("Markdown document contains no text blocks: %s", canonicalPath)
	}
	doc.Revision = ContentRevision(doc)
	return doc, nil
}
