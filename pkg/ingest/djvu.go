package ingest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type DjVuExtractor struct{ options Options }

func NewDjVuExtractor() DjVuExtractor { return DjVuExtractor{options: DefaultOptions()} }

func (d DjVuExtractor) Extract(ctx context.Context, path string) (Document, error) {
	return newEngine(d.options).extractDjVu(ctx, path)
}

func (e *engine) extractDjVu(ctx context.Context, path string) (Document, error) {
	if _, err := e.options.withDefaults(); err != nil {
		return Document{}, err
	}
	canonical, err := validateSource(path, "DjVu")
	if err != nil {
		return Document{}, err
	}
	e.progress(StageAnalyze, 0, 0, "checking DjVu embedded text")
	first, last := e.options.Pages.First, e.options.Pages.Last
	textTool, found, resolveErr := e.optionalTool("djvutxt", e.options.Tools.DjVuText)
	if resolveErr != nil {
		return Document{}, resolveErr
	}
	pageCount := 0
	var textPages []extractedPage
	var textAttempt string
	if found {
		args := []string{}
		if last > 0 {
			args = append(args, fmt.Sprintf("-page=%d-%d", first, last))
		} else if first > 1 {
			args = append(args, fmt.Sprintf("-page=%d-$", first))
		}
		// Omitting outputfile sends text to stdout. Passing "-" is not portable:
		// the Windows DjVuLibre build creates a literal file named "-".
		args = append(args, canonical)
		out, runErr := e.runTool(ctx, textTool, args...)
		if runErr == nil {
			pages, count := pagesFromFormFeed(string(out.stdout), first, "text", -1)
			textPages = pages
			pageCount = count
		} else {
			textAttempt = commandFailure("djvutxt", out, runErr).Error()
		}
	}
	if last > 0 {
		pageCount = last
	} else {
		discovered, countErr := e.djvuPageCount(ctx, canonical)
		if countErr == nil {
			pageCount = discovered
		} else if pageCount == 0 {
			return Document{}, fmt.Errorf("DjVu has no usable embedded text and page count is required for OCR: %w", countErr)
		}
	}
	ocrPageNumbers := pagesNeedingOCR(textPages, first, pageCount, e.options.OCR.MinTextRunes)
	if len(ocrPageNumbers) == 0 && len(textPages) > 0 {
		doc, buildErr := documentFromPages(canonical, FormatDjVu, "image/vnd.djvu", textPages)
		if buildErr == nil {
			e.progress(StageDone, 0, len(textPages), "DjVu embedded text extracted")
		}
		return doc, buildErr
	}
	if len(ocrPageNumbers) == 0 {
		first, selectedLast, _, rangeErr := e.selectedPages(pageCount)
		if rangeErr != nil {
			return Document{}, rangeErr
		}
		for page := first; page <= selectedLast; page++ {
			ocrPageNumbers = append(ocrPageNumbers, page)
		}
	}
	e.progress(StageOCR, 0, len(ocrPageNumbers), fmt.Sprintf("DjVu pages require OCR: %s", formatPageNumbers(ocrPageNumbers)))
	pages, err := e.ocrDjVuPages(ctx, canonical, ocrPageNumbers)
	if err != nil {
		if textAttempt != "" {
			return Document{}, fmt.Errorf("DjVu requires OCR after %s, but fallback is unavailable or failed: %w", textAttempt, err)
		}
		return Document{}, fmt.Errorf("DjVu requires OCR, but fallback is unavailable or failed: %w", err)
	}
	doc, err := documentFromPages(canonical, FormatDjVu, "image/vnd.djvu", mergeExtractedPages(textPages, pages))
	if err == nil {
		e.progress(StageDone, 0, len(pages), "DjVu OCR complete")
	}
	return doc, err
}

func (e *engine) djvuPageCount(ctx context.Context, path string) (int, error) {
	tool, err := e.resolve("djvused", e.options.Tools.DjVuUsed)
	if err != nil {
		return 0, err
	}
	out, runErr := e.runTool(ctx, tool, "-e", "n", path)
	if runErr != nil {
		return 0, commandFailure("djvused", out, runErr)
	}
	count, parseErr := strconv.Atoi(strings.TrimSpace(string(out.stdout)))
	if parseErr != nil || count <= 0 {
		return 0, fmt.Errorf("djvused returned invalid page count %q", strings.TrimSpace(string(out.stdout)))
	}
	return count, nil
}

func (e *engine) ocrDjVu(ctx context.Context, path string, discoveredCount int) ([]extractedPage, error) {
	first, last, total, err := e.selectedPages(discoveredCount)
	if err != nil {
		return nil, err
	}
	pageNumbers := make([]int, 0, total)
	for page := first; page <= last; page++ {
		pageNumbers = append(pageNumbers, page)
	}
	return e.ocrDjVuPages(ctx, path, pageNumbers)
}

func (e *engine) ocrDjVuPages(ctx context.Context, path string, pageNumbers []int) ([]extractedPage, error) {
	if len(pageNumbers) == 0 {
		return nil, nil
	}
	renderTool, err := e.resolve("ddjvu", e.options.Tools.DjVuRender)
	if err != nil {
		return nil, fmt.Errorf("DjVu OCR requires DjVuLibre ddjvu: %w", err)
	}
	render := func(ctx context.Context, page int, output string) error {
		out, runErr := e.runTool(ctx, renderTool, "-format=tiff", "-page="+strconv.Itoa(page), path, output)
		if runErr != nil {
			return commandFailure("ddjvu", out, runErr)
		}
		return nil
	}
	return e.ocrRenderedPageNumbers(ctx, pageNumbers, ".tif", render)
}
