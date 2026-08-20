package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type PDFExtractor struct{ options Options }

func NewPDFExtractor() PDFExtractor { return PDFExtractor{options: DefaultOptions()} }

func (p PDFExtractor) Extract(ctx context.Context, path string) (Document, error) {
	return newEngine(p.options).extractPDF(ctx, path)
}

type extractedPage struct {
	page       int
	text       string
	method     string
	confidence float64
	warnings   []string
}

func (e *engine) extractPDF(ctx context.Context, path string) (Document, error) {
	if _, err := e.options.withDefaults(); err != nil {
		return Document{}, err
	}
	canonical, err := validateSource(path, "PDF")
	if err != nil {
		return Document{}, err
	}
	e.progress(StageAnalyze, 0, 0, "discovering PDF text layer")

	pages, pageCount, attempts, err := e.extractPDFText(ctx, canonical)
	if err != nil {
		return Document{}, err
	}
	ocrPageNumbers := pagesNeedingOCR(pages, e.options.Pages.First, pageCount, e.options.OCR.MinTextRunes)
	if len(ocrPageNumbers) == 0 && len(pages) > 0 {
		doc, buildErr := documentFromPages(canonical, FormatPDF, "application/pdf", pages)
		if buildErr == nil {
			e.progress(StageDone, 0, len(pages), "PDF text layer extracted")
		}
		return doc, buildErr
	}
	if len(ocrPageNumbers) == 0 {
		first, last, _, rangeErr := e.selectedPages(pageCount)
		if rangeErr != nil {
			return Document{}, rangeErr
		}
		for page := first; page <= last; page++ {
			ocrPageNumbers = append(ocrPageNumbers, page)
		}
	}
	e.progress(StageOCR, 0, len(ocrPageNumbers), fmt.Sprintf("PDF pages require OCR: %s", formatPageNumbers(ocrPageNumbers)))
	ocrPages, ocrErr := e.ocrPDFPages(ctx, canonical, ocrPageNumbers)
	if ocrErr != nil {
		detail := strings.Join(attempts, "; ")
		if detail == "" {
			if len(pages) > 0 {
				detail = "pages " + formatPageNumbers(ocrPageNumbers) + " have no usable text layer"
			} else {
				detail = "no usable text extractor"
			}
		}
		return Document{}, fmt.Errorf("PDF requires OCR (%s), but OCR fallback is unavailable or failed: %w", detail, ocrErr)
	}
	doc, err := documentFromPages(canonical, FormatPDF, "application/pdf", mergeExtractedPages(pages, ocrPages))
	if err == nil {
		e.progress(StageDone, 0, len(ocrPages), "PDF OCR complete")
	}
	return doc, err
}

func (e *engine) extractPDFText(ctx context.Context, path string) ([]extractedPage, int, []string, error) {
	type candidate struct {
		name, explicit string
		args           func() []string
	}
	first, last := e.options.Pages.First, e.options.Pages.Last
	candidates := []candidate{
		{"pdftotext", e.options.Tools.PDFToText, func() []string {
			args := []string{"-enc", "UTF-8", "-layout"}
			if first > 1 {
				args = append(args, "-f", strconv.Itoa(first))
			}
			if last > 0 {
				args = append(args, "-l", strconv.Itoa(last))
			}
			return append(args, path, "-")
		}},
		{"mutool", e.options.Tools.MuTool, func() []string {
			args := []string{"draw", "-F", "txt", "-o", "-", path}
			if last > 0 {
				args = append(args, fmt.Sprintf("%d-%d", first, last))
			} else if first > 1 {
				args = append(args, fmt.Sprintf("%d-N", first))
			}
			return args
		}},
	}
	var attempts []string
	maxCount := 0
	for _, candidate := range candidates {
		tool, found, resolveErr := e.optionalTool(candidate.name, candidate.explicit)
		if resolveErr != nil {
			return nil, 0, attempts, resolveErr
		}
		if !found {
			continue
		}
		e.progress(StageText, 0, 0, "extracting PDF text with "+candidate.name)
		out, runErr := e.runTool(ctx, tool, candidate.args()...)
		if runErr != nil {
			attempts = append(attempts, commandFailure(candidate.name, out, runErr).Error())
			continue
		}
		parsed, count := pagesFromFormFeed(string(out.stdout), first, "text", -1)
		if count > maxCount {
			maxCount = count
		}
		if richEnough(parsed, e.options.OCR.MinTextRunes) {
			return parsed, count, attempts, nil
		}
		attempts = append(attempts, candidate.name+": no text layer or extracted text was below the quality threshold")
	}
	if last > 0 {
		maxCount = last
	}
	if maxCount == 0 {
		count, countErr := e.pdfPageCount(ctx, path)
		if countErr == nil {
			maxCount = count
		} else {
			attempts = append(attempts, countErr.Error())
		}
	}
	return nil, maxCount, attempts, nil
}

func (e *engine) pdfPageCount(ctx context.Context, path string) (int, error) {
	if tool, found, err := e.optionalTool("pdfinfo", e.options.Tools.PDFInfo); err != nil {
		return 0, err
	} else if found {
		out, runErr := e.runTool(ctx, tool, path)
		if runErr == nil {
			if count := parsePageCount(string(out.stdout)); count > 0 {
				return count, nil
			}
		}
	}
	if tool, found, err := e.optionalTool("mutool", e.options.Tools.MuTool); err != nil {
		return 0, err
	} else if found {
		out, runErr := e.runTool(ctx, tool, "info", path)
		if runErr == nil {
			if count := parsePageCount(string(out.stdout)); count > 0 {
				return count, nil
			}
		}
	}
	return 0, fmt.Errorf("cannot determine PDF page count for OCR: configure pdfinfo/mutool or use an explicit extractor page range")
}

var pagesPattern = regexp.MustCompile(`(?im)^\s*Pages?\s*:\s*([0-9]+)\s*$`)

func parsePageCount(text string) int {
	match := pagesPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

func normalizePDFText(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func pagesFromFormFeed(text string, first int, method string, confidence float64) ([]extractedPage, int) {
	parts := strings.Split(normalizePDFText(text), "\f")
	count := len(parts)
	if count > 0 && strings.TrimSpace(parts[count-1]) == "" {
		count--
	}
	pages := make([]extractedPage, 0, count)
	for i := 0; i < count; i++ {
		value := strings.TrimSpace(parts[i])
		if value == "" {
			continue
		}
		pages = append(pages, extractedPage{page: first + i, text: value, method: method, confidence: confidence})
	}
	return pages, count + first - 1
}

func richEnough(pages []extractedPage, threshold int) bool {
	count := 0
	for _, page := range pages {
		for _, r := range page.text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				count++
			}
		}
	}
	return count >= threshold
}

func pageRichEnough(page extractedPage, threshold int) bool {
	return richEnough([]extractedPage{page}, threshold)
}

func pagesNeedingOCR(pages []extractedPage, first, last, threshold int) []int {
	if first <= 0 {
		first = 1
	}
	if last < first {
		return nil
	}
	byPage := make(map[int]extractedPage, len(pages))
	for _, page := range pages {
		byPage[page.page] = page
	}
	var result []int
	for page := first; page <= last; page++ {
		if extracted, ok := byPage[page]; !ok || !pageRichEnough(extracted, threshold) {
			result = append(result, page)
		}
	}
	return result
}

func mergeExtractedPages(textPages, ocrPages []extractedPage) []extractedPage {
	merged := make(map[int]extractedPage, len(textPages)+len(ocrPages))
	for _, page := range textPages {
		merged[page.page] = page
	}
	for _, page := range ocrPages {
		merged[page.page] = page
	}
	pageNumbers := make([]int, 0, len(merged))
	for page := range merged {
		pageNumbers = append(pageNumbers, page)
	}
	sort.Ints(pageNumbers)
	result := make([]extractedPage, 0, len(pageNumbers))
	for _, page := range pageNumbers {
		result = append(result, merged[page])
	}
	return result
}

func formatPageNumbers(pages []int) string {
	values := make([]string, len(pages))
	for i, page := range pages {
		values[i] = strconv.Itoa(page)
	}
	return strings.Join(values, ",")
}

func validateSource(path, label string) (string, error) {
	canonical, err := canonicalSourcePath(path)
	if err != nil {
		return "", fmt.Errorf("canonical %s path: %w", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("read %s %s: %w", label, canonical, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s source is a directory: %s", label, canonical)
	}
	return canonical, nil
}

func documentFromPages(source string, format Format, mediaType string, pages []extractedPage) (Document, error) {
	var markdown strings.Builder
	byPage := make(map[int]extractedPage)
	var warnings []string
	for _, page := range pages {
		warnings = append(warnings, page.warnings...)
		if strings.TrimSpace(page.text) == "" {
			continue
		}
		if markdown.Len() > 0 {
			markdown.WriteString("\n\n")
		}
		fmt.Fprintf(&markdown, "<!-- page: %d -->\n\n%s", page.page, strings.TrimSpace(page.text))
		byPage[page.page] = page
	}
	if markdown.Len() == 0 {
		return Document{}, fmt.Errorf("no non-empty pages were extracted from %s", source)
	}
	doc, err := ParseMarkdown(source, markdown.String())
	if err != nil {
		return Document{}, err
	}
	doc.Format, doc.MediaType = format, mediaType
	doc.Warnings = append(doc.Warnings, warnings...)
	for i := range doc.Blocks {
		page := byPage[doc.Blocks[i].Page]
		doc.Blocks[i].Extraction = page.method
		doc.Blocks[i].OCRConfidence = page.confidence
		doc.Blocks[i].Warnings = append([]string(nil), page.warnings...)
	}
	doc.Title = filepath.Base(source)
	return doc, nil
}

// parsePDFText is retained for tests and callers that already have pdftotext
// output. Empty physical pages are skipped without renumbering later pages.
func parsePDFText(sourcePath, text string) (Document, error) {
	pages, _ := pagesFromFormFeed(text, 1, "text", -1)
	return documentFromPages(sourcePath, FormatPDF, "application/pdf", pages)
}
