//go:build !windows

package mem

import (
	"os"
	"path/filepath"
)

func replaceAtomicFile(source, target string) error {
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := os.Rename(source, target); err != nil {
		return err
	}
	// The rename has already atomically changed the visible configuration. A
	// directory fsync improves crash durability where supported, but reporting
	// its platform-specific failure as a failed save would be false: callers
	// could retry even though the new file is already live.
	_ = dir.Sync()
	return nil
}
