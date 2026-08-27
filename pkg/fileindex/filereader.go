package fileindex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxAnnotationBytes — максимум байт, читаемых из файла для аннотации.
// 64 KB — первая глава / аннотация / copyright-страница для большинства книг.
const MaxAnnotationBytes = 64 * 1024

// Metadata XML may be larger than the final annotation, but it still needs a
// hard decompression/read boundary. This protects enrich from oversized FB2
// documents and compressed EPUB metadata entries.
const (
	maxMetadataXMLBytes      = 4 * 1024 * 1024
	maxEPUBContainerBytes    = 1024 * 1024
	externalExtractorTimeout = 30 * time.Second
)

// MaxEmbedChars — лимит рун для embedding (тот же, что в pkg/mem/embed.go:15).
const MaxEmbedChars = 2000

// ExtractAnnotation извлекает краткое содержимое файла (аннотацию) для embedding.
// Диспетчер по расширению. Всегда возвращает строку ≤ MaxEmbedChars рун.
//
// Возвращает ("", nil) если формат не поддерживается или внешний инструмент
// не установлен — это нормальный случай, не ошибка. Реальная ошибка только
// если файл не читается вовсе.
//
// Поддерживаемые форматы:
//
//	.txt, .md   — первые MaxAnnotationBytes байт
//	.fb2        — XML <annotation> (или fallback на первые MaxAnnotationBytes)
//	.pdf        — `pdftotext -f 1 -l 2` (если установлен)
//	.epub       — ZIP + XML, <dc:description> из content.opf
//	.djvu       — `djvused -e print-meta` (если установлен), метаданные
//	прочее      — пустая строка
func ExtractAnnotation(absPath, ext string) (string, error) {
	ext = strings.ToLower(ext)
	switch ext {
	case ".txt", ".md":
		return readTextFirst(absPath)
	case ".fb2":
		return readFB2Annotation(absPath)
	case ".pdf":
		return readPDFFirstPages(absPath)
	case ".epub":
		return readEPUBDescription(absPath)
	case ".djvu", ".djv":
		return readDjVuMetadata(absPath)
	default:
		return "", nil
	}
}

// truncateRunes обрезает строку до max рун (безопасно для UTF-8).
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// === TXT / MD ===

func readTextFirst(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	limited := io.LimitReader(f, MaxAnnotationBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	return truncateRunes(string(data), MaxEmbedChars), nil
}

// === FB2 ===

// fb2Description — структура для парсинга <description> FB2.
// Нас интересует <annotation>: описание книги (вступление, о чём).
type fb2Document struct {
	XMLName     xml.Name       `xml:"http://www.gribuser.ru/xml/fictionbook/2.0 FictionBook"`
	Description fb2Description `xml:"description"`
}

type fb2Description struct {
	TitleInfo fb2TitleInfo `xml:"title-info"`
}

type fb2TitleInfo struct {
	Annotation string `xml:"annotation"`
}

func readFB2Annotation(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Decode as a stream so a multi-gigabyte FB2 never has to be materialized.
	// The decoder may inspect up to maxMetadataXMLBytes while looking for the
	// annotation; malformed or unusually structured files fall back to the same
	// bounded plain-text prefix used historically.
	decoder := xml.NewDecoder(io.LimitReader(f, maxMetadataXMLBytes))
	for {
		token, decodeErr := decoder.Token()
		if decodeErr != nil {
			return readTextFirst(absPath)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "annotation" {
			continue
		}
		var annotation string
		if err := decoder.DecodeElement(&annotation, &start); err != nil {
			return readTextFirst(absPath)
		}
		annotation = strings.TrimSpace(annotation)
		if annotation == "" {
			return readTextFirst(absPath)
		}
		return truncateRunes(annotation, MaxEmbedChars), nil
	}
}

// === PDF ===

// readPDFFirstPages использует pdftotext из poppler-utils.
// Если pdftotext не установлен — возвращает ("", nil) (warning на уровне CLI).
func readPDFFirstPages(absPath string) (string, error) {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		// pdftotext недоступен — это не ошибка, просто нет аннотации.
		return "", nil
	}
	out, err := boundedCommandOutput(pdftotext,
		[]string{"-f", "1", "-l", "2", "-layout", absPath, "-"}, MaxAnnotationBytes)
	if err != nil {
		// pdftotext вернул ошибку (например, encrypted PDF) — тоже не критично.
		return "", nil
	}
	return truncateRunes(string(out), MaxEmbedChars), nil
}

// === EPUB ===

// epubContainer — META-INF/container.xml: rootfile path.
type epubContainer struct {
	Rootfiles []epubRootfile `xml:"rootfiles>rootfile"`
}

type epubRootfile struct {
	FullPath string `xml:"full-path,attr"`
}

// epubPackage — content.opf: metadata.
type epubPackage struct {
	Metadata epubMetadata `xml:"metadata"`
}

type epubMetadata struct {
	Title       string   `xml:"title"`
	Creator     []string `xml:"creator"`
	Description string   `xml:"description"`
	Language    string   `xml:"language"`
	Publisher   string   `xml:"publisher"`
}

func readEPUBDescription(absPath string) (string, error) {
	zr, err := zip.OpenReader(absPath)
	if err != nil {
		return "", nil
	}
	defer zr.Close()

	// 1. META-INF/container.xml → full-path to content.opf.
	var container epubContainer
	var opfPath string
	for _, f := range zr.File {
		if f.Name == "META-INF/container.xml" {
			data, ok := readZIPEntryBounded(f, maxEPUBContainerBytes)
			if !ok {
				break
			}
			if err := xml.Unmarshal(data, &container); err == nil {
				if len(container.Rootfiles) > 0 {
					opfPath = container.Rootfiles[0].FullPath
				}
			}
			break
		}
	}
	if opfPath == "" {
		return "", nil
	}

	// 2. content.opf → metadata.
	for _, f := range zr.File {
		if f.Name == opfPath {
			data, ok := readZIPEntryBounded(f, maxMetadataXMLBytes)
			if !ok {
				return "", nil
			}

			var pkg epubPackage
			if err := xml.Unmarshal(data, &pkg); err != nil {
				return "", nil
			}

			var parts []string
			if pkg.Metadata.Title != "" {
				parts = append(parts, "Title: "+pkg.Metadata.Title)
			}
			if len(pkg.Metadata.Creator) > 0 {
				parts = append(parts, "Author: "+strings.Join(pkg.Metadata.Creator, ", "))
			}
			if pkg.Metadata.Description != "" {
				parts = append(parts, pkg.Metadata.Description)
			} else if pkg.Metadata.Publisher != "" {
				parts = append(parts, "Publisher: "+pkg.Metadata.Publisher)
			}
			if pkg.Metadata.Language != "" {
				parts = append(parts, "Language: "+pkg.Metadata.Language)
			}
			return truncateRunes(strings.Join(parts, "\n"), MaxEmbedChars), nil
		}
	}
	return "", nil
}

// === DjVu ===

// readDjVuMetadata использует djvused (из пакета djvulibre) для извлечения метаданных.
// Без OCR — это именно метаданные (title/author/year), не текст страниц.
func readDjVuMetadata(absPath string) (string, error) {
	djvused, err := exec.LookPath("djvused")
	if err != nil {
		return "", nil
	}
	out, err := boundedCommandOutput(djvused, []string{"-e", "print-meta", absPath}, MaxAnnotationBytes)
	if err != nil {
		return "", nil
	}
	// Парсим вывод djvused -e print-meta:
	//   (_meta
	//     (Title "...")
	//     (Author "...")
	//     (Year "...")
	//     ...
	//   )
	var title, author, year string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "(Title ") {
			title = extractDjVuValue(line)
		} else if strings.HasPrefix(line, "(Author ") {
			author = extractDjVuValue(line)
		} else if strings.HasPrefix(line, "(Year ") {
			year = extractDjVuValue(line)
		}
	}
	var parts []string
	if title != "" {
		parts = append(parts, "Title: "+title)
	}
	if author != "" {
		parts = append(parts, "Author: "+author)
	}
	if year != "" {
		parts = append(parts, "Year: "+year)
	}
	return truncateRunes(strings.Join(parts, "; "), MaxEmbedChars), nil
}

func readZIPEntryBounded(file *zip.File, maxBytes int64) ([]byte, bool) {
	if file == nil || maxBytes <= 0 || file.UncompressedSize64 > uint64(maxBytes) {
		return nil, false
	}
	rc, err := file.Open()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, false
	}
	return data, true
}

// cappedBuffer reports every byte as consumed while retaining only a bounded
// prefix. This lets external extractors finish without buffering arbitrary
// output in memory or blocking on a full stdout pipe.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.max - w.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.Buffer.Write(p)
	}
	return written, nil
}

func boundedCommandOutput(command string, args []string, maxBytes int) ([]byte, error) {
	return boundedCommandOutputWithTimeout(command, args, maxBytes, externalExtractorTimeout)
}

func boundedCommandOutputWithTimeout(command string, args []string, maxBytes int, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	output := cappedBuffer{max: maxBytes}
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return output.Bytes(), nil
}

// extractDjVuValue достаёт значение из строки вида (Key "value") или (Key value).
func extractDjVuValue(line string) string {
	// (Key "value with spaces") — взять в кавычках
	start := strings.Index(line, "\"")
	if start >= 0 {
		rest := line[start+1:]
		end := strings.Index(rest, "\"")
		if end >= 0 {
			return rest[:end]
		}
	}
	// (Key value) — взять до закрывающей скобки
	start = strings.Index(line, "(")
	if start < 0 {
		return ""
	}
	rest := line[start+1:]
	// Пропускаем ключ
	space := strings.IndexAny(rest, " \t")
	if space < 0 {
		return ""
	}
	val := strings.TrimSpace(rest[space:])
	// Убираем хвостовую скобку
	if strings.HasSuffix(val, ")") {
		val = val[:len(val)-1]
	}
	return val
}

// Удобство для тестов: получить аннотацию по basename в каталоге.
func extractFromPath(absPath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(absPath))
	return ExtractAnnotation(absPath, ext)
}
