package ingest

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type pageRenderer func(context.Context, int, string) error

const pyMuPDFRenderScript = `import sys,fitz;d=fitz.open(sys.argv[1]);p=d[int(sys.argv[2])-1];p.get_pixmap(dpi=int(sys.argv[3]),alpha=False).save(sys.argv[4])`

func (e *engine) ocrPDF(ctx context.Context, path string, discoveredCount int) ([]extractedPage, error) {
	first, last, total, err := e.selectedPages(discoveredCount)
	if err != nil {
		return nil, err
	}
	pages := make([]int, 0, total)
	for page := first; page <= last; page++ {
		pages = append(pages, page)
	}
	return e.ocrPDFPages(ctx, path, pages)
}

func (e *engine) ocrPDFPages(ctx context.Context, path string, pages []int) ([]extractedPage, error) {
	if len(pages) == 0 {
		return nil, nil
	}
	var render pageRenderer
	if tool, found, resolveErr := e.optionalTool("pdftoppm", e.options.Tools.PDFToPPM); resolveErr != nil {
		return nil, resolveErr
	} else if found {
		render = func(ctx context.Context, page int, output string) error {
			prefix := strings.TrimSuffix(output, filepath.Ext(output))
			out, runErr := e.runTool(ctx, tool, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-r", strconv.Itoa(e.options.OCR.DPI), "-png", "-singlefile", path, prefix)
			if runErr != nil {
				return commandFailure("pdftoppm", out, runErr)
			}
			return nil
		}
	} else if tool, found, resolveErr := e.optionalTool("mutool", e.options.Tools.MuTool); resolveErr != nil {
		return nil, resolveErr
	} else if found {
		render = func(ctx context.Context, page int, output string) error {
			out, runErr := e.runTool(ctx, tool, "draw", "-r", strconv.Itoa(e.options.OCR.DPI), "-o", output, path, strconv.Itoa(page))
			if runErr != nil {
				return commandFailure("mutool", out, runErr)
			}
			return nil
		}
	} else if tool, found, resolveErr := e.optionalTool("python", e.options.Tools.Python); resolveErr != nil {
		return nil, resolveErr
	} else if found {
		render = func(ctx context.Context, page int, output string) error {
			out, runErr := e.runTool(ctx, tool, "-c", pyMuPDFRenderScript, path, strconv.Itoa(page), strconv.Itoa(e.options.OCR.DPI), output)
			if runErr != nil {
				return commandFailure("python/PyMuPDF", out, runErr)
			}
			return nil
		}
	} else {
		return nil, fmt.Errorf("no PDF OCR renderer found: configure pdftoppm/mutool or Python with PyMuPDF via project config/MEM_PYTHON")
	}
	return e.ocrRenderedPageNumbers(ctx, pages, ".png", render)
}

func (e *engine) selectedPages(discoveredCount int) (first, last, total int, err error) {
	first, last = e.options.Pages.First, e.options.Pages.Last
	if first == 0 {
		first = 1
	}
	if last == 0 {
		last = discoveredCount
	}
	if last == 0 {
		return 0, 0, 0, fmt.Errorf("page count is unknown; configure a page-count tool or an explicit page range")
	}
	if discoveredCount > 0 && last > discoveredCount {
		last = discoveredCount
	}
	if first > last {
		return 0, 0, 0, fmt.Errorf("selected page range %d-%d is outside document page count %d", first, last, discoveredCount)
	}
	return first, last, last - first + 1, nil
}

func (e *engine) ocrRenderedPages(ctx context.Context, first, last, total int, extension string, render pageRenderer) ([]extractedPage, error) {
	pageNumbers := make([]int, 0, total)
	for page := first; page <= last; page++ {
		pageNumbers = append(pageNumbers, page)
	}
	return e.ocrRenderedPageNumbers(ctx, pageNumbers, extension, render)
}

func (e *engine) ocrRenderedPageNumbers(ctx context.Context, pageNumbers []int, extension string, render pageRenderer) ([]extractedPage, error) {
	if len(pageNumbers) == 0 {
		return nil, nil
	}
	tesseract, err := e.resolve("tesseract", e.options.Tools.Tesseract)
	if err != nil {
		return nil, fmt.Errorf("OCR requires Tesseract: %w", err)
	}
	tessdata, err := resolveTessdata(e.options.OCR.TessdataDir, tesseract, e.options.OCR.Languages)
	if err != nil {
		return nil, err
	}
	tempDir, err := e.mkdirTemp(e.options.TempRoot, "mem-ingest-ocr-")
	if err != nil {
		return nil, fmt.Errorf("create OCR temporary directory: %w", err)
	}
	defer e.removeAll(tempDir)

	pages := make([]extractedPage, 0, len(pageNumbers))
	for current, page := range pageNumbers {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("OCR cancelled before page %d: %w", page, err)
		}
		image := filepath.Join(tempDir, fmt.Sprintf("page-%06d%s", page, extension))
		e.progressAt(StageRender, page, current+1, len(pageNumbers), fmt.Sprintf("rendering page %d", page))
		if err := render(ctx, page, image); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("render cancelled on page %d: %w", page, ctxErr)
			}
			pages = append(pages, failedOCRPage(page, fmt.Errorf("render page %d: %w", page, err)))
			continue
		}
		e.progressAt(StageOCR, page, current+1, len(pageNumbers), fmt.Sprintf("OCR page %d", page))
		result, err := e.runTesseract(ctx, tesseract, tessdata, image)
		_ = e.remove(image)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("OCR cancelled on page %d: %w", page, ctxErr)
			}
			pages = append(pages, failedOCRPage(page, fmt.Errorf("OCR page %d: %w", page, err)))
			continue
		}
		result.page = page
		for i := range result.warnings {
			result.warnings[i] = fmt.Sprintf("page %d: %s", page, result.warnings[i])
		}
		if strings.TrimSpace(result.text) == "" {
			result.warnings = append(result.warnings, fmt.Sprintf("page %d: OCR produced no text", page))
		}
		pages = append(pages, result)
	}
	return pages, nil
}

func failedOCRPage(page int, cause error) extractedPage {
	return extractedPage{
		page: page, method: "ocr", confidence: -1, failed: true,
		warnings: []string{cause.Error()},
	}
}

func failedOCRPages(pageNumbers []int, cause error) []extractedPage {
	pages := make([]extractedPage, 0, len(pageNumbers))
	for _, page := range pageNumbers {
		pages = append(pages, failedOCRPage(page,
			fmt.Errorf("page %d: OCR unavailable or failed: %w", page, cause)))
	}
	return pages
}

func (e *engine) runTesseract(ctx context.Context, executable, tessdata, image string) (extractedPage, error) {
	args := []string{image, "stdout", "-l", e.options.OCR.Languages}
	if tessdata != "" {
		args = append(args, "--tessdata-dir", tessdata)
	}
	// Set the output variable directly instead of relying on the optional
	// tessdata/configs/tsv file. Isolated tessdata directories often contain
	// only requested *.traineddata files.
	args = append(args, "-c", "tessedit_create_tsv=1")
	out, runErr := e.runTool(ctx, executable, args...)
	if runErr != nil {
		return extractedPage{}, commandFailure("tesseract", out, runErr)
	}
	text, confidence, err := parseTesseractTSV(string(out.stdout))
	if err != nil {
		return extractedPage{}, fmt.Errorf("parse Tesseract TSV: %w", err)
	}
	page := extractedPage{text: text, method: "ocr", confidence: confidence}
	if confidence >= 0 && confidence < e.options.OCR.LowConfidence {
		page.warnings = append(page.warnings, fmt.Sprintf("OCR confidence %.1f is below %.1f", confidence, e.options.OCR.LowConfidence))
	}
	return page, nil
}

func resolveTessdata(explicit, executable, languages string) (string, error) {
	requested := strings.FieldsFunc(languages, func(r rune) bool { return r == '+' || r == ',' || r == ' ' })
	if len(requested) == 0 {
		return "", fmt.Errorf("OCR language list is empty; set ingest.ocr_languages or MEM_OCR_LANGS")
	}
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	} else {
		if env := strings.TrimSpace(os.Getenv("TESSDATA_PREFIX")); env != "" {
			candidates = append(candidates, env, filepath.Join(env, "tessdata"))
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "tessdata"))
	}
	var bestMissing []string
	for _, dir := range candidates {
		missing := missingLanguages(dir, requested)
		if len(missing) == 0 {
			return filepath.Clean(dir), nil
		}
		if bestMissing == nil || len(missing) < len(bestMissing) {
			bestMissing = missing
		}
		if explicit != "" {
			break
		}
	}
	return "", fmt.Errorf("Tesseract language data missing for %s (missing: %s); set ingest.tessdata_dir/MEM_TESSDATA_DIR to one directory containing every requested .traineddata file", languages, strings.Join(bestMissing, ", "))
}

func missingLanguages(dir string, languages []string) []string {
	var missing []string
	for _, language := range languages {
		if _, err := os.Stat(filepath.Join(dir, language+".traineddata")); err != nil {
			missing = append(missing, language)
		}
	}
	return missing
}

func parseTesseractTSV(value string) (string, float64, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(value, "\ufeff")))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return "", -1, err
	}
	if len(records) == 0 {
		return "", -1, nil
	}
	if len(records[0]) < 12 || records[0][0] != "level" || records[0][11] != "text" {
		return "", -1, fmt.Errorf("unexpected output: Tesseract did not emit TSV")
	}
	var output strings.Builder
	lastLine := ""
	totalConfidence := 0.0
	words := 0
	for i, record := range records {
		if i == 0 || len(record) < 12 || record[0] != "5" {
			continue
		}
		word := strings.TrimSpace(record[11])
		if word == "" {
			continue
		}
		line := strings.Join(record[2:5], ":")
		if output.Len() > 0 {
			if line != lastLine {
				output.WriteByte('\n')
			} else {
				output.WriteByte(' ')
			}
		}
		output.WriteString(word)
		lastLine = line
		if confidence, parseErr := strconv.ParseFloat(record[10], 64); parseErr == nil && confidence >= 0 {
			totalConfidence += confidence
			words++
		}
	}
	confidence := -1.0
	if words > 0 {
		confidence = totalConfidence / float64(words)
	}
	return strings.TrimSpace(output.String()), confidence, nil
}
