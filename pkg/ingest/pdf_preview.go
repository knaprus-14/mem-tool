package ingest

import (
	"context"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const pdfPreviewSide = 2000

// RenderPDFPage renders original PDF bytes without OCR or mathematical inference.
// The caller supplies a verified private snapshot, never a request-supplied path.
func RenderPDFPage(ctx context.Context, snapshot string, page int, tools ToolConfig) ([]byte, error) {
	return newEngine(Options{Tools: tools, ToolTimeout: 30 * time.Second}).renderPDFPage(ctx, snapshot, page)
}

func (e *engine) renderPDFPage(ctx context.Context, snapshot string, page int) ([]byte, error) {
	if page < 1 {
		return nil, fmt.Errorf("physical PDF page must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := e.mkdirTemp(e.options.TempRoot, "mem-pdf-preview-*")
	if err != nil {
		return nil, err
	}
	defer e.removeAll(dir)
	output := filepath.Join(dir, "page.png")
	var tool, name string
	var args []string
	for _, candidate := range []struct{ name, path string }{{"pdftoppm", e.options.Tools.PDFToPPM}, {"mutool", e.options.Tools.MuTool}, {"python", e.options.Tools.Python}} {
		resolved, found, resolveErr := e.optionalTool(candidate.name, candidate.path)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if found {
			tool, name = resolved, candidate.name
			break
		}
	}
	p, side := strconv.Itoa(page), strconv.Itoa(pdfPreviewSide)
	switch name {
	case "pdftoppm":
		args = []string{"-f", p, "-l", p, "-scale-to", side, "-png", "-singlefile", snapshot, filepath.Join(dir, "page")}
	case "mutool":
		args = []string{"draw", "-w", side, "-h", side, "-o", output, snapshot, p}
	case "python":
		const script = `import sys,fitz;d=fitz.open(sys.argv[1]);p=d[int(sys.argv[2])-1];s=int(sys.argv[3])/max(p.rect.width,p.rect.height);p.get_pixmap(matrix=fitz.Matrix(s,s),alpha=False).save(sys.argv[4])`
		args = []string{"-c", script, snapshot, p, side, output}
	default:
		return nil, fmt.Errorf("для изображения PDF настройте pdftoppm, mutool или Python с PyMuPDF в параметрах импорта")
	}
	out, err := e.runTool(ctx, tool, args...)
	if err != nil {
		return nil, commandFailure(name, out, err)
	}
	f, err := os.Open(output)
	if err != nil {
		return nil, fmt.Errorf("PDF renderer did not produce a page: %w", err)
	}
	defer f.Close()
	config, err := png.DecodeConfig(f)
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > pdfPreviewSide+1 || config.Height > pdfPreviewSide+1 {
		return nil, fmt.Errorf("PDF renderer produced an invalid or oversized PNG")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	const maxBytes = 20 << 20
	result, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(result) > maxBytes {
		return nil, fmt.Errorf("PDF preview exceeds the image size limit or could not be read")
	}
	return result, nil
}
