package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	StageAnalyze = "analyze"
	StageText    = "text"
	StageRender  = "render"
	StageOCR     = "ocr"
	StageDone    = "done"
)

type PageRange struct {
	First int
	Last  int
}

type ToolConfig struct {
	PDFToText  string
	MuTool     string
	PDFInfo    string
	PDFToPPM   string
	DjVuText   string
	DjVuUsed   string
	DjVuRender string
	Tesseract  string
}

type OCRConfig struct {
	Languages     string
	TessdataDir   string
	DPI           int
	LowConfidence float64
	MinTextRunes  int
}

type ProgressEvent struct {
	Stage   string
	Page    int
	Current int
	Total   int
	Message string
}

type Options struct {
	Tools       ToolConfig
	OCR         OCRConfig
	Pages       PageRange
	TempRoot    string
	ToolTimeout time.Duration
	Progress    func(ProgressEvent)
}

func DefaultOptions() Options {
	languages := strings.TrimSpace(os.Getenv("MEM_OCR_LANGS"))
	if languages == "" {
		languages = "rus+eng"
	}
	return Options{OCR: OCRConfig{
		Languages: languages, TessdataDir: strings.TrimSpace(os.Getenv("MEM_TESSDATA_DIR")),
		DPI: 300, LowConfidence: 65, MinTextRunes: 40,
	}, ToolTimeout: 5 * time.Minute}
}

func (o Options) withDefaults() (Options, error) {
	defaults := DefaultOptions()
	if o.OCR.Languages == "" {
		o.OCR.Languages = defaults.OCR.Languages
	}
	if o.OCR.TessdataDir == "" {
		o.OCR.TessdataDir = defaults.OCR.TessdataDir
	}
	if o.OCR.DPI <= 0 {
		o.OCR.DPI = defaults.OCR.DPI
	}
	if o.OCR.LowConfidence <= 0 {
		o.OCR.LowConfidence = defaults.OCR.LowConfidence
	}
	if o.OCR.MinTextRunes <= 0 {
		o.OCR.MinTextRunes = defaults.OCR.MinTextRunes
	}
	if o.ToolTimeout <= 0 {
		o.ToolTimeout = defaults.ToolTimeout
	}
	if o.Pages.First < 0 || o.Pages.Last < 0 || (o.Pages.Last > 0 && o.Pages.First > o.Pages.Last) {
		return o, fmt.Errorf("invalid page range %d-%d", o.Pages.First, o.Pages.Last)
	}
	if o.Pages.First == 0 {
		o.Pages.First = 1
	}
	return o, nil
}

type commandOutput struct{ stdout, stderr []byte }
type commandRunner func(context.Context, string, ...string) (commandOutput, error)

type engine struct {
	options   Options
	resolve   func(string, string) (string, error)
	run       commandRunner
	mkdirTemp func(string, string) (string, error)
	removeAll func(string) error
	remove    func(string) error
}

func newEngine(options Options) *engine {
	resolved, _ := options.withDefaults()
	e := &engine{options: resolved}
	e.resolve = defaultToolResolver
	e.run = func(ctx context.Context, name string, args ...string) (commandOutput, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return commandOutput{stdout.Bytes(), stderr.Bytes()}, err
	}
	e.mkdirTemp, e.removeAll, e.remove = os.MkdirTemp, os.RemoveAll, os.Remove
	return e
}

func (e *engine) progress(stage string, page, total int, message string) {
	if e.options.Progress != nil {
		current := 0
		if page > 0 {
			current = page - e.options.Pages.First + 1
		}
		e.options.Progress(ProgressEvent{Stage: stage, Page: page, Current: current, Total: total, Message: message})
	}
}

func (e *engine) runTool(ctx context.Context, tool string, args ...string) (commandOutput, error) {
	runCtx, cancel := context.WithTimeout(ctx, e.options.ToolTimeout)
	defer cancel()
	out, err := e.run(runCtx, tool, args...)
	if runCtx.Err() != nil {
		return out, runCtx.Err()
	}
	return out, err
}

func commandFailure(name string, out commandOutput, err error) error {
	message := strings.TrimSpace(string(out.stderr))
	if len(message) > 600 {
		message = message[:600] + "..."
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	return fmt.Errorf("%s failed: %s", name, message)
}
