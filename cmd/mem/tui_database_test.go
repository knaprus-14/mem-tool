package main

import (
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestHandleWhereReportsAbsoluteDatabasePath(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, mem.MemDirName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	output := captureStdout(func() { handleWhere(store) })
	if !strings.Contains(output, root) || !strings.Contains(output, store.Path()) {
		t.Fatalf("where output does not identify active database:\n%s", output)
	}
}

func TestTUIHeaderAndOpenUseExplicitLocalDatabase(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Project With Spaces")
	if err := mem.InitMemIn(filepath.Join(root, mem.MemDirName), "test"); err != nil {
		t.Fatal(err)
	}
	store, err := mem.NewStore(filepath.Join(root, mem.MemDirName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	model := newTuiModel(mem.DefaultLocalConfig(), store)
	if header := model.headerLine(); !strings.Contains(header, "Project With Spaces") {
		t.Fatalf("header does not identify database: %q", header)
	}
	if output := strings.Join(model.output, "\n"); !strings.Contains(output, store.Path()) {
		t.Fatalf("welcome output does not contain absolute database path: %q", output)
	}

	cmd := model.runCommandAsync(`/open "` + root + `"`)
	if cmd == nil {
		t.Fatal("/open did not request TUI exit")
	}
	want, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if model.openPath != want || !model.quitting {
		t.Fatalf("/open state = path %q quitting %v, want %q true", model.openPath, model.quitting, want)
	}
}

func TestTUIOpenRejectsMissingLocalDatabase(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, mem.MemDirName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := newTuiModel(mem.DefaultLocalConfig(), store)

	missing := t.TempDir()
	if cmd := model.runCommandAsync(`/open "` + missing + `"`); cmd != nil {
		t.Fatal("invalid /open unexpectedly requested exit")
	}
	if model.quitting || model.openPath != "" {
		t.Fatalf("invalid /open changed state: path %q quitting %v", model.openPath, model.quitting)
	}
	if output := strings.Join(model.output, "\n"); !strings.Contains(output, "локальная база не найдена") {
		t.Fatalf("invalid /open has no actionable error: %q", output)
	}
}

func TestTUIOpenWaitsForBusyOperation(t *testing.T) {
	root := t.TempDir()
	if err := mem.InitMemIn(filepath.Join(root, mem.MemDirName), "current"); err != nil {
		t.Fatal(err)
	}
	store, err := mem.NewStore(filepath.Join(root, mem.MemDirName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target := filepath.Join(t.TempDir(), "target")
	if err := mem.InitMemIn(filepath.Join(target, mem.MemDirName), "target"); err != nil {
		t.Fatal(err)
	}

	model := newTuiModel(mem.DefaultLocalConfig(), store)
	model.busy = true
	if cmd := model.runCommandAsync(`/open "` + target + `"`); cmd != nil {
		t.Fatal("busy /open unexpectedly requested TUI exit")
	}
	if model.quitting || model.openPath != "" {
		t.Fatalf("busy /open changed session: path=%q quitting=%v", model.openPath, model.quitting)
	}
	if output := strings.Join(model.output, "\n"); !strings.Contains(output, "дождитесь завершения") {
		t.Fatalf("busy /open has no actionable message: %q", output)
	}
}
