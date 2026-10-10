// Package filelock holds an OS lock for the lifetime of a background worker.
// The empty lock file remains on disk; process exit releases the lock itself.
package filelock

import (
	"errors"
	"os"
)

var ErrBusy = errors.New("обработка этой базы уже выполняется другим процессом")

func Try(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
