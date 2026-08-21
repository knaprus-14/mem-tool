package mem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDatabaseRootAcceptsProjectAndMemDirectory(t *testing.T) {
	root := t.TempDir()
	memPath := filepath.Join(root, MemDirName)
	if err := InitMemIn(memPath, "test"); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{root, memPath} {
		got, err := ResolveDatabaseRoot(input)
		if err != nil {
			t.Fatalf("ResolveDatabaseRoot(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ResolveDatabaseRoot(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveDatabaseRootRejectsDirectoryWithoutDatabase(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveDatabaseRoot(root); err == nil {
		t.Fatal("ResolveDatabaseRoot accepted a directory without .mem")
	}
}

func TestResolveDatabaseRootRejectsBareOrCorruptMemDirectory(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		root := t.TempDir()
		memPath := filepath.Join(root, MemDirName)
		if err := os.Mkdir(memPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if corrupt {
			if err := os.WriteFile(filepath.Join(memPath, "config.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ResolveDatabaseRoot(root); err == nil {
			t.Fatalf("ResolveDatabaseRoot accepted invalid .mem (corrupt=%v)", corrupt)
		}
	}
}

func TestStoreReportsAbsoluteDatabasePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), MemDirName)
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want, err := filepath.Abs(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	if got := store.Stats()["store_location"]; got != want {
		t.Fatalf("Stats store_location = %v, want %q", got, want)
	}
}
