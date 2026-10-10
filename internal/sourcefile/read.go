// Package sourcefile содержит ограниченное чтение и проверку исходных файлов.
package sourcefile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// MaxTextBytes — предел одного текстового документа до разбиения на чанки.
const MaxTextBytes = 64 << 20

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// ReadText читает обычный UTF-8 файл с проверкой отмены и ограничения памяти.
func ReadText(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("источник не является обычным файлом")
	}
	if info.Size() > MaxTextBytes {
		return nil, fmt.Errorf("текстовый файл превышает лимит %d MiB; разделите документ на части", MaxTextBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, MaxTextBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxTextBytes {
		return nil, fmt.Errorf("текстовый файл превышает лимит %d MiB", MaxTextBytes>>20)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("файл не является UTF-8 текстом; для PDF/DjVu используйте mem import")
	}
	return data, nil
}

// Hash вычисляет SHA-256 физического файла, не загружая его целиком в память.
func Hash(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return HashOpen(ctx, f)
}

// HashOpen проверяет именно открытый дескриптор и возвращает его к началу.
func HashOpen(ctx context.Context, f *os.File) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("источник не является обычным файлом")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 64<<10))
	if err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", fmt.Errorf("исходный файл изменился во время чтения")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Snapshot копирует открытый источник в частный временный файл. Проверяемые
// байты затем можно выдать HTTP-клиенту, даже если оригинал уже перезаписан.
// Успешный вызывающий обязан закрыть и удалить возвращённый файл.
func Snapshot(ctx context.Context, source *os.File) (snapshot *os.File, digest string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	info, err := source.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("источник не является обычным файлом")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	snapshot, err = os.CreateTemp("", "mem-verified-source-*")
	if err != nil {
		return nil, "", err
	}
	tmp := snapshot
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(snapshot, hash), contextReader{ctx, source}, make([]byte, 64<<10))
	if err != nil {
		return nil, "", err
	}
	after, err := source.Stat()
	if err != nil {
		return nil, "", err
	}
	if n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, "", fmt.Errorf("исходный файл изменился во время чтения")
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	return snapshot, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
