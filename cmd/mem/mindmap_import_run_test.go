package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMindMapImportRunPreviewNeverOpensDatabase(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	file := filepath.Join(root, "outline.md")
	if err := os.WriteFile(file, []byte("# Root\n- Child"), 0600); err != nil {
		t.Fatal(err)
	}
	originalArgs, originalStore := os.Args, newStore
	defer func() { os.Args = originalArgs; newStore = originalStore }()
	newStore = func(string) (*Store, error) { t.Fatal("preview opened store"); return nil, nil }
	// The same preview works without .mem and with an intentionally unreadable
	// database/config that must not be inspected or migrated at all.
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.Mkdir(filepath.Join(root, ".mem"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".mem", "store.db"), []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".mem", "config.json"), []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		os.Args = []string{"mem", "--dir", root, "mindmap", "import", "--file", "outline.md", "--format", "markdown", "--json"}
		if code := run(); code != 0 {
			t.Fatal("preview failed", code)
		}
		if existing {
			got, err := os.ReadFile(filepath.Join(root, ".mem", "store.db"))
			if err != nil || string(got) != "untouched" {
				t.Fatal("changed db", err)
			}
		} else if _, err := os.Stat(filepath.Join(root, ".mem")); !os.IsNotExist(err) {
			t.Fatal("created db", err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	os.Args = []string{"mem", "--dir", target, "mindmap", "import", "--file", file, "--format", "markdown", "--apply"}
	if code := run(); code == 0 {
		t.Fatal("invalid apply accepted")
	}
	after, _ := os.Getwd()
	if cwd != after {
		t.Fatal("invalid flags changed cwd")
	}
}
