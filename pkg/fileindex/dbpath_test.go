package fileindex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCreateFileIndexDirectoryRemovesPartialInitializationFiles(t *testing.T) {
	original := writeFileIndexInitializationFile
	defer func() { writeFileIndexInitializationFile = original }()

	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("write-%d", failAt), func(t *testing.T) {
			calls := 0
			writeFileIndexInitializationFile = func(path string, data []byte, perm os.FileMode) error {
				calls++
				if calls != failAt {
					return os.WriteFile(path, data, perm)
				}
				if err := os.WriteFile(path, data[:1], perm); err != nil {
					return err
				}
				return errors.New("injected partial write")
			}
			dir := filepath.Join(t.TempDir(), FileIndexDirName)
			if err := createFileIndexDirectory(dir, []byte("config"), []byte("meta")); err == nil {
				t.Fatal("partial initialization write succeeded")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed initialization left directory behind: %v", err)
			}
		})
	}
}

func TestInitFileIndexInIsExclusiveUnderConcurrentInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FileIndexDirName)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			errs <- InitFileIndexIn(dir, "concurrent")
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
