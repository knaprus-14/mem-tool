package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func newTestTUIModel(t *testing.T) tuiModel {
	t.Helper()
	store, err := mem.NewStore(filepath.Join(t.TempDir(), mem.MemDirName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newTuiModel(mem.DefaultLocalConfig(), store)
}

func TestParseTUICommandLinePreservesQuotedWindowsArguments(t *testing.T) {
	cmd, args, err := parseTUICommandLine(`/import "D:\Мои книги\Радио 1971-01.pdf" -tags "Журнал Радио"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`D:\Мои книги\Радио 1971-01.pdf`, "-tags", "Журнал Радио"}
	if cmd != "import" || len(args) != len(want) {
		t.Fatalf("parsed cmd=%q args=%#v", cmd, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestParseTUICommandLineRejectsUnclosedQuote(t *testing.T) {
	if _, _, err := parseTUICommandLine(`/add "незакрытый текст`); err == nil {
		t.Fatal("unclosed quote was accepted")
	}
}

func TestParseTUICommandLinePreservesTrailingWindowsBackslash(t *testing.T) {
	cmd, args, err := parseTUICommandLine(`/index "D:\Мои книги\"`)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "index" || len(args) != 1 || args[0] != `D:\Мои книги\` {
		t.Fatalf("parsed cmd=%q args=%#v", cmd, args)
	}
}

func TestTUICommandMenuCoversCLICommandsAndSubcommands(t *testing.T) {
	items := tuiCommandMenuItems()
	literals := make(map[string]bool, len(items))
	roots := make(map[string]bool, len(items))
	for _, item := range items {
		literal := commandMenuLiteral(item.name)
		literals[literal] = true
		if fields := strings.Fields(literal); len(fields) > 0 {
			roots[fields[0]] = true
		}
	}

	// `repl` is a mode switch, but it is deliberately available too. Aliases
	// such as get/view/rm/imp/current remain accepted without cluttering help.
	for _, command := range []string{
		"init", "version", "help", "open", "add", "search", "ask", "map",
		"recent", "add-file", "import", "config", "stats", "index", "source",
		"sources", "show", "delete", "edit", "retag", "important", "repl", "where",
	} {
		if !roots[command] {
			t.Errorf("TUI menu does not expose CLI command %q", command)
		}
	}

	for _, subcommand := range []string{
		"map build", "map coverage", "map extract", "map extract-runs", "map extract-run",
		"map analyze", "map duplicates", "map merge-node", "map merges",
		"map runs", "map run", "map prune-runs", "map status", "map approve",
		"map approve-batch", "map reviews", "map edits", "map export", "map export-html",
		"config set-backend", "config set-polza-key", "config set-polza-model",
		"config set-ollama-model", "config set-answer-model", "config set-answer-base-url",
		"config set-answer-timeout", "config set-answer-max-tokens",
		"config set-answer-context-chars", "config set-chunk-size",
		"config set-chunk-overlap", "config set-chunk-strategy",
	} {
		if !literals[subcommand] {
			t.Errorf("TUI menu does not expose %q", subcommand)
		}
	}
}

func TestTUIEscapeAlwaysReturnsHomeWithoutQuitting(t *testing.T) {
	model := newTestTUIModel(t)
	model.output = append(model.output, "результат команды")
	model.textarea.SetValue("/search старый запрос")
	model.showPopup = true

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(tuiModel)
	if cmd != nil || got.quitting || got.showPopup || got.textarea.Value() != "" {
		t.Fatalf("Esc state: quitting=%v popup=%v input=%q cmd=%v", got.quitting, got.showPopup, got.textarea.Value(), cmd)
	}
	output := strings.Join(got.output, "\n")
	if strings.Contains(output, "результат команды") || !strings.Contains(output, "Активная база:") {
		t.Fatalf("Esc did not restore home screen: %q", output)
	}
}

func TestTUICtrlDDoesNotExitAndDoubleCtrlCDoes(t *testing.T) {
	model := newTestTUIModel(t)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(tuiModel)
	if cmd != nil || model.quitting {
		t.Fatalf("Ctrl+D unexpectedly exited: quitting=%v cmd=%v", model.quitting, cmd)
	}

	updated, first := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(tuiModel)
	if first == nil || model.quitting || !model.ctrlCPending {
		t.Fatalf("first Ctrl+C state: quitting=%v pending=%v cmd=%v", model.quitting, model.ctrlCPending, first)
	}
	updated, second := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(tuiModel)
	if second == nil || !model.quitting {
		t.Fatalf("second Ctrl+C did not exit: quitting=%v cmd=%v", model.quitting, second)
	}
}

func TestTUIRunsNewCLICommandsAndCapturesStatusStreams(t *testing.T) {
	model := newTestTUIModel(t)
	cmd := model.runCommandAsync("/version")
	if cmd == nil || !model.busy {
		t.Fatal("/version was not scheduled")
	}
	first := cmd()
	if _, ok := first.(commandProgressMsg); !ok {
		t.Fatalf("first version event = %T, want live commandProgressMsg", first)
	}
	output, result := collectTUICommandEvents(t, model.commandEvents, first)
	if result.err != nil || !strings.Contains(output, "mem-tool v") {
		t.Fatalf("version output=%q result=%#v", output, result)
	}

	model.busy = false
	cmd = model.runCommandAsync("/map status --json")
	first = cmd()
	output, result = collectTUICommandEvents(t, model.commandEvents, first)
	if result.err != nil || !strings.Contains(output, `"summary"`) {
		t.Fatalf("map status output=%q result=%#v", output, result)
	}
}

func TestTUIImportStreamsStageAndChunkCounters(t *testing.T) {
	model := newTestTUIModel(t)
	originalImportDocument := importDocument
	defer func() { importDocument = originalImportDocument }()
	importDocument = func(_ context.Context, _ *mem.Config, _ *mem.Store, path string, options mem.ImportOptions) (mem.ImportResult, error) {
		options.Progress(ingest.ProgressEvent{Stage: ingest.StageAnalyze, Message: "discovering PDF text layer"})
		options.Progress(ingest.ProgressEvent{Stage: ingest.StageEmbed, Page: 1, Current: 3, Total: 10, Message: "embedding chunk 3/10"})
		return mem.ImportResult{
			SourcePath: path, DocumentID: "doc-test", DocumentRevision: "sha256:test",
			Blocks: 1, Chunks: 10, Pages: []int{1},
		}, nil
	}

	cmd := model.runCommandAsync(`/import "D:\Books\manual.pdf"`)
	if cmd == nil {
		t.Fatal("/import was not scheduled")
	}
	first := cmd()
	if progress, ok := first.(commandProgressMsg); !ok || !strings.Contains(progress.output, "[IMPORT]") {
		t.Fatalf("first import event = %#v, want immediate import progress", first)
	}
	output, result := collectTUICommandEvents(t, model.commandEvents, first)
	if result.err != nil {
		t.Fatal(result.err)
	}
	for _, want := range []string{"[ANALYZE]", "[EMBED] [3/10] page 1", "chunks=10"} {
		if !strings.Contains(output, want) {
			t.Fatalf("import stream does not contain %q: %q", want, output)
		}
	}
}

func collectTUICommandEvents(t *testing.T, events <-chan tea.Msg, first tea.Msg) (string, execResultMsg) {
	t.Helper()
	var lines []string
	msg := first
	for {
		switch event := msg.(type) {
		case commandProgressMsg:
			lines = append(lines, event.output)
			next := waitForCommandEvent(events)
			if next == nil {
				t.Fatal("missing command event waiter")
			}
			msg = next()
		case execResultMsg:
			return strings.Join(lines, "\n"), event
		default:
			t.Fatalf("unexpected command event %T", msg)
		}
	}
}

func TestCaptureCommandOutputStreamEmitsBeforeCommandCompletes(t *testing.T) {
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	events := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		captureCommandOutputStream(func() {
			fmt.Fprintln(os.Stdout, "first progress")
			<-release
			fmt.Fprintln(os.Stderr, "second progress")
		}, func(line string) { events <- line })
	}()

	select {
	case got := <-events:
		if got != "first progress" {
			t.Fatalf("first progress = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first progress was buffered until command completion")
	}

	select {
	case <-done:
		t.Fatal("command completed before release")
	default:
	}
	close(release)
	released = true
	select {
	case got := <-events:
		if got != "second progress" {
			t.Fatalf("second progress = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second progress was not emitted")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream capture did not finish")
	}
}

func TestTUIAppendsProgressWhileCommandIsBusy(t *testing.T) {
	model := newTestTUIModel(t)
	events := make(chan tea.Msg, 1)
	model.busy = true
	model.commandEvents = events
	model.activeCommand = "import"
	model.commandStarted = time.Now()

	updated, next := model.Update(commandProgressMsg{output: "[EMBED] [3/10] chunk 3"})
	got := updated.(tuiModel)
	if !got.busy || got.progressUpdates != 1 || next == nil {
		t.Fatalf("progress state: busy=%v updates=%d next=%v", got.busy, got.progressUpdates, next)
	}
	if output := strings.Join(got.output, "\n"); !strings.Contains(output, "[3/10]") {
		t.Fatalf("live progress not appended: %q", output)
	}
}

func TestTUILongCommandOutputWrapsWithoutTruncationAndReflowsOnResize(t *testing.T) {
	model := newTestTUIModel(t)
	model.output = nil
	model.width = 42 // viewport content width = 40 cells
	model.height = 40
	model.recomputeSizes()

	chunk := "8  1. Фотография в современном мире. Начнем с массовой любительской фотографии. " +
		"Ее примеры приводить не нужно - они окружают нас повсюду. " +
		"Длинный_технический_идентификатор_тоже_не_должен_обрезаться."
	model.appendBlock(tuiStyles.Result.Render(chunk))

	narrow := model.renderOutput()
	assertTUIWrappedText(t, narrow, chunk, 40)
	narrowLines := strings.Count(narrow, "\n") + 1

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 92, Height: 40})
	model = updated.(tuiModel)
	wide := model.renderOutput()
	assertTUIWrappedText(t, wide, chunk, 90)
	wideLines := strings.Count(wide, "\n") + 1
	if wideLines >= narrowLines {
		t.Fatalf("resize did not reflow output: narrow=%d lines, wide=%d lines", narrowLines, wideLines)
	}
}

func TestTUISeparatorTracksResizedViewportWidth(t *testing.T) {
	model := newTestTUIModel(t)
	model.output = nil
	model.width = 42
	model.recomputeSizes()
	model.appendSeparator()
	if got := ansi.StringWidth(ansi.Strip(model.renderOutput())); got != 40 {
		t.Fatalf("narrow separator width = %d, want 40", got)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 102, Height: 40})
	model = updated.(tuiModel)
	if got := ansi.StringWidth(ansi.Strip(model.renderOutput())); got != 100 {
		t.Fatalf("wide separator width = %d, want 100", got)
	}
}

func assertTUIWrappedText(t *testing.T, rendered, original string, width int) {
	t.Helper()
	plain := ansi.Strip(rendered)
	compact := func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, value)
	}
	if got, want := compact(plain), compact(original); got != want {
		t.Fatalf("wrapped output lost or changed text:\n got: %q\nwant: %q", got, want)
	}
	for lineNo, line := range strings.Split(rendered, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, limit = %d: %q", lineNo+1, got, width, ansi.Strip(line))
		}
	}
}

func TestTUIRedactsPolzaKeyFromVisibleHistory(t *testing.T) {
	got := redactTUICommand("config", []string{"set-polza-key", "very-secret"}, "/config set-polza-key very-secret")
	if strings.Contains(got, "very-secret") || !strings.Contains(got, "***") {
		t.Fatalf("redacted command = %q", got)
	}
}
