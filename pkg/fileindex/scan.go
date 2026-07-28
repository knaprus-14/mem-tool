package fileindex

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

// ScanOptions — параметры сканирования каталога.
type ScanOptions struct {
	RootDir  string // абсолютный путь к scan-root
	Enrich   bool   // вызывать ExtractAnnotation?
	Embed    bool   // вызывать mem.GetEmbedding? (false = только метаданные, без векторов)
	DryRun   bool
	Progress bool // печатать прогресс в stdout
}

// ScanReport — итоги сканирования.
type ScanReport struct {
	Added   int
	Updated int
	Stale   int
	Skipped int
	Errors  []string
}

// skipDirNames — директории, которые мы всегда пропускаем.
var skipDirNames = map[string]bool{
	".git":         true,
	".vs":          true,
	".idea":        true,
	".mem":         true,
	".fileindex":   true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".cache":       true,
	"target":       true, // Rust
	"dist":         true,
	"build":        true,
	".gradle":      true,
	".next":        true, // Next.js
	".parcel-cache": true,
}

// shouldSkipDir возвращает true, если директорию нужно пропустить.
func shouldSkipDir(name string) bool {
	if skipDirNames[name] {
		return true
	}
	// Скрытые директории (начинаются с ".").
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

// Scan рекурсивно обходит opts.RootDir, обновляет/добавляет записи в store.
//
// Алгоритм:
//  1. WalkDir по RootDir с пропуском skip-директорий.
//  2. Для каждого файла: stat() → relPath → проверяем существующую запись.
//  3. Если mtime не изменился и не Enrich — skip (быстрый rescan).
//  4. Иначе: parentDir → annotation (если Enrich) → embedding → Upsert.
//  5. После walk: AllPaths() vs посещённые → MarkStale/UnmarkStale.
func Scan(opts ScanOptions, store *Store, cfg *mem.Config) (ScanReport, error) {
	if opts.RootDir == "" {
		return ScanReport{}, fmt.Errorf("Scan: пустой RootDir")
	}
	absRoot, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return ScanReport{}, fmt.Errorf("Scan: абсолютный путь: %w", err)
	}

	report := ScanReport{}
	visited := make(map[string]bool) // relPath → true
	count := 0

	walkErr := filepath.WalkDir(absRoot, func(fullPath string, d fs.DirEntry, err error) error {
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", fullPath, err))
			return nil // продолжаем обход
		}
		if d.IsDir() {
			if fullPath != absRoot && shouldSkipDir(d.Name()) {
				if opts.Progress {
					fmt.Printf("  [skip dir] %s\n", fullPath)
				}
				return filepath.SkipDir
			}
			return nil
		}

		// Только регулярные файлы (не symlinks, не устройства).
		info, err := d.Info()
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("stat %s: %v", fullPath, err))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		count++
		relPath, err := filepath.Rel(absRoot, fullPath)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("relpath %s: %v", fullPath, err))
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		visited[relPath] = true

		// Проверяем существующую запись.
		existing, exists := store.GetByPath(relPath)

		// Быстрый skip: mtime не изменился и не Enrich.
		if exists && !opts.Enrich && existing.Mtime == info.ModTime().Unix() {
			report.Skipped++
			if opts.Progress && count%100 == 0 {
				fmt.Printf("  [%d files] skipped=%d added=%d updated=%d\n",
					count, report.Skipped, report.Added, report.Updated)
			}
			return nil
		}

		// Собираем метаданные.
		entry := &FileEntry{
			Path:    relPath,
			Name:    filepath.Base(fullPath),
			Ext:     strings.ToLower(filepath.Ext(fullPath)),
			Size:    info.Size(),
			Mtime:   info.ModTime().Unix(),
			Backend: cfg.Backend,
		}
		entry.ParentDir = parentDirChain(absRoot, fullPath, 2)

		// Аннотация (если Enrich).
		if opts.Enrich {
			ann, annErr := ExtractAnnotation(fullPath, entry.Ext)
			if annErr != nil {
				report.Errors = append(report.Errors,
					fmt.Sprintf("annotation %s: %v", fullPath, annErr))
			}
			entry.Annotation = ann
		}

		// Embedding (всегда, если Embed=true, или если записи нет).
		if opts.Embed || !exists {
			searchText := buildSearchText(entry)
			vec, embErr := mem.GetEmbedding(cfg, searchText)
			if embErr != nil {
				report.Errors = append(report.Errors,
					fmt.Sprintf("embedding %s: %v", fullPath, embErr))
				// Без embedding запись бесполезна для поиска — пропускаем.
				return nil
			}
			entry.Embedding = vec
			entry.Dims = len(vec)
		} else {
			// Сохраняем старый embedding при обновлении метаданных без Enrich.
			entry.Embedding = existing.Embedding
			entry.Dims = existing.Dims
		}

		if opts.DryRun {
			if !exists {
				report.Added++
			} else {
				report.Updated++
			}
			return nil
		}

		if err := store.Upsert(entry); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("upsert %s: %v", fullPath, err))
			return nil
		}

		if !exists {
			report.Added++
		} else {
			report.Updated++
		}
		if opts.Progress && count%100 == 0 {
			fmt.Printf("  [%d files] skipped=%d added=%d updated=%d\n",
				count, report.Skipped, report.Added, report.Updated)
		}
		return nil
	})
	if walkErr != nil {
		return report, fmt.Errorf("walk: %w", walkErr)
	}

	// Reconciling: что было в БД, но не найдено на диске.
	allPaths, err := store.AllPaths()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("AllPaths: %v", err))
		return report, nil
	}
	var missing []string
	for _, p := range allPaths {
		if !visited[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		// Разделяем на stale (ранее не stale) и unmark (ранее stale, но мы их пере-не посетили —
		// но это уже случай выше: visited=false). Так что всё missing → MarkStale.
		// Для UnmarkStale — мы бы хотели снять stale с записей, которые мы сейчас посетили
		// и они уже есть. Это делается автоматически в Upsert (stale=0).
		if err := store.MarkStale(missing); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("MarkStale: %v", err))
		} else {
			report.Stale = len(missing)
		}
	}

	return report, nil
}

// parentDirChain возвращает последние N уровней parent_dir_chain через "/".
// Пример: parentDirChain("/root", "/root/Books/Programming/Go", 2) = "Books/Programming".
func parentDirChain(root, fullPath string, levels int) string {
	rel, err := filepath.Rel(root, fullPath)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "/" {
		return ""
	}
	parts := strings.Split(dir, "/")
	if len(parts) <= levels {
		return dir
	}
	return strings.Join(parts[len(parts)-levels:], "/")
}

// buildSearchText собирает строку для embedding из метаданных файла.
// Формат: "name ext parent_dir annotation" (annotation может быть пустой).
func buildSearchText(e *FileEntry) string {
	var b strings.Builder
	b.WriteString(e.Name)
	if e.Ext != "" {
		b.WriteString(" ")
		b.WriteString(e.Ext)
	}
	if e.ParentDir != "" {
		b.WriteString(" ")
		b.WriteString(e.ParentDir)
	}
	if e.Annotation != "" {
		b.WriteString(" ")
		b.WriteString(e.Annotation)
	}
	return b.String()
}