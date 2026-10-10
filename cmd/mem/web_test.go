package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebOptionsRejectInvalidArgumentsWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	original := os.Args
	defer func() { os.Args = original }()
	for _, args := range [][]string{{"gui", "--port", "-1"}, {"gui", "--port", "65536"}, {"gui", "--port", "0", "--port", "1"}, {"gui", "unexpected"}, {"serve"}, {"serve", "--no-browser"}, {"serve", "--config"}} {
		os.Args = append([]string{"mem"}, args...)
		if run() == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".mem")); !os.IsNotExist(err) {
		t.Fatal("invalid web command created database")
	}
	if o, err := parseWebOptions("gui", []string{"--port", "0", "--no-browser", "--state-dir", root}); err != nil || !o.NoBrowser || o.StateDir != root {
		t.Fatalf("valid options: %+v %v", o, err)
	}
}

func TestServeRejectsLocalDirFlagsBeforeChangingWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	other := t.TempDir()
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"mem", "--dir", other, "serve", "--config", "missing.json"}
	if run() == 0 {
		t.Fatal("server accepted --dir")
	}
	cwd, _ := os.Getwd()
	if cwd != root {
		t.Fatalf("changed cwd: %s", cwd)
	}
}
