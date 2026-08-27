package mem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestCreateMemDirectoryRemovesPartialInitializationFiles(t *testing.T) {
	original := writeMemInitializationFile
	defer func() { writeMemInitializationFile = original }()

	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("write-%d", failAt), func(t *testing.T) {
			calls := 0
			writeMemInitializationFile = func(path string, data []byte, perm os.FileMode) error {
				calls++
				if calls != failAt {
					return os.WriteFile(path, data, perm)
				}
				if err := os.WriteFile(path, data[:1], perm); err != nil {
					return err
				}
				return errors.New("injected partial write")
			}
			dir := filepath.Join(t.TempDir(), ".mem")
			if err := createMemDirectory(dir, []byte("config"), []byte("meta")); err == nil {
				t.Fatal("partial initialization write succeeded")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed initialization left directory behind: %v", err)
			}
		})
	}
}

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

func TestSourcePathComparisonMatchesHostCaseSemantics(t *testing.T) {
	upper := filepath.Join(t.TempDir(), "A.md")
	lower := filepath.Join(filepath.Dir(upper), "a.md")
	equal := coveragePathsEqual(upper, lower)
	if runtime.GOOS == "windows" && !equal {
		t.Fatal("Windows source paths should be compared case-insensitively")
	}
	if runtime.GOOS != "windows" && equal {
		t.Fatal("case-distinct source paths were conflated on a case-sensitive host")
	}
	predicate := sourcePathSQLPredicate("source_path")
	if runtime.GOOS == "windows" && !strings.Contains(predicate, "LOWER") {
		t.Fatalf("Windows SQL predicate is case-sensitive: %q", predicate)
	}
	if runtime.GOOS != "windows" && strings.Contains(predicate, "LOWER") {
		t.Fatalf("case-sensitive SQL predicate folds source paths: %q", predicate)
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

func TestInitMemInIsExclusiveUnderConcurrentInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), MemDirName)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			errs <- InitMemIn(dir, "concurrent")
		}()
	}
	ready.Wait()
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful initializers=%d, want exactly one", successes)
	}
	for _, name := range []string{"config.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("initialized %s missing: %v", name, err)
		}
	}
}
