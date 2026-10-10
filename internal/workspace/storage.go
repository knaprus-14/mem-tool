package workspace

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

var ErrStorageLimit = errors.New("достигнут лимит хранилища; освободите место или увеличьте лимит в конфигурации сервера")

type StorageUsage struct {
	Bytes          int64 `json:"bytes"`
	Files          int   `json:"files"`
	DatabaseBytes  int64 `json:"database_bytes"`
	OriginalBytes  int64 `json:"original_bytes"`
	TemporaryBytes int64 `json:"temporary_bytes"`
}

func MeasureStorage(ctx context.Context, root string) (out StorageUsage, err error) {
	base := filepath.Join(root, ".mem")
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		out.Files++
		if out.Files > 100000 {
			return ErrStorageLimit
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		size := info.Size()
		out.Bytes += size
		rel, e := filepath.Rel(base, path)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "uploads/") || strings.HasPrefix(rel, "sources/") {
			out.OriginalBytes += size
		} else if strings.HasSuffix(rel, ".db") || strings.HasSuffix(rel, ".db-wal") || strings.HasSuffix(rel, ".db-journal") {
			out.DatabaseBytes += size
		} else if strings.HasPrefix(rel, "rechunk-") {
			out.TemporaryBytes += size
		}
		return nil
	})
	return
}
