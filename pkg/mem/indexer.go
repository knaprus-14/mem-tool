package mem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IndexResult — результат индексации одного файла
type IndexResult struct {
	FilePath string
	Chunks   int
	Err      error
	Skipped  bool
}

// supportedExts — расширения файлов, которые умеем обрабатывать
var supportedExts = map[string]bool{
	".txt":  true,
	".md":   true,
	".csv":  true,
	".json": true,
	".pdf":  true,
}

// IndexDirectory индексирует все поддерживаемые файлы в директории
func IndexDirectory(cfg *Config, store *Store, dirPath string) ([]IndexResult, error) {
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("неверный путь: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("путь не найден: %w", err)
	}

	if !info.IsDir() {
		// Это файл, а не папка
		result, err := IndexFile(cfg, store, absPath)
		if err != nil {
			return []IndexResult{{FilePath: absPath, Err: err}}, nil
		}
		return []IndexResult{result}, nil
	}

	fmt.Printf("[DIR] Сканирую: %s\n\n", absPath)

	var results []IndexResult
	totalChunks := 0
	totalFiles := 0
	skippedFiles := 0

	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Пропускаем скрытые папки
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExts[ext] {
			return nil
		}

		result, iErr := IndexFile(cfg, store, path)
		if iErr != nil {
			result.Err = iErr
		}
		results = append(results, result)

		if result.Err != nil {
			fmt.Printf("  [ERR] %s — ошибка: %v\n", filepath.Base(path), result.Err)
		} else {
			prefix := "[FILE]"
			skipped := ""
			if result.Skipped {
				prefix = "[SKIP]"
				skipped = " (пропущен — уже в базе)"
				skippedFiles++
			}
			fmt.Printf("  %s %s — %d чанков%s\n", prefix, filepath.Base(path), result.Chunks, skipped)
		}
		totalFiles++
		totalChunks += result.Chunks

		return nil
	})

	if err != nil {
		return results, err
	}

	fmt.Printf("\n[STATS] Результат: %d файлов, %d чанков", totalFiles, totalChunks)
	if skippedFiles > 0 {
		fmt.Printf(", %d пропущено", skippedFiles)
	}
	fmt.Println()

	return results, nil
}

// IndexFile индексирует один файл: читает, чанкует, эмбеддит, сохраняет
func IndexFile(cfg *Config, store *Store, filePath string) (IndexResult, error) {
	result := IndexResult{FilePath: filePath}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		result.Err = err
		return result, err
	}

	// Читаем содержимое файла
	text, err := readFile(absPath)
	if err != nil {
		result.Err = err
		return result, err
	}

	text = strings.TrimSpace(text)
	if text == "" {
		result.Skipped = true
		return result, nil
	}

	// Разбиваем на чанки
	chunks := ChunkDocument(text, cfg.Chunking.MaxSize, cfg.Chunking.Overlap, cfg.Chunking.Strategy)
	if len(chunks) == 0 {
		result.Skipped = true
		return result, nil
	}

	// Формируем теги из пути
	tags := []string{filepath.Ext(absPath)}
	if parent := filepath.Base(filepath.Dir(absPath)); parent != "." {
		tags = append(tags, parent)
	}

	// Создаём чанки
	fileName := filepath.Base(absPath)
	for _, chunk := range chunks {
		fmt.Printf("  [%d/%d] Эмбеддинг... ", chunk.Index+1, len(chunks))

		embedding, err := GetEmbedding(cfg, chunk.Text)
		if err != nil {
			fmt.Printf("[ERR] %v\n", err)
			continue
		}
		fmt.Printf("вектор %d\n", len(embedding))

		_, err = store.AddChunk(chunk.Text, fileName, tags, cfg.Backend, embedding,
			fileName, chunk.Label, chunk.Index, len(chunks), false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [ERR] Ошибка сохранения: %v\n", err)
			continue
		}
		result.Chunks++
	}

	return result, nil
}

// readFile читает содержимое файла, поддерживая разные форматы
func readFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	absPath, _ := filepath.Abs(path)

	switch ext {
	case ".txt", ".md", ".csv", ".json":
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", err
		}
		return string(data), nil

	case ".pdf":
		return readPDF(absPath)

	default:
		return "", fmt.Errorf("неподдерживаемый формат: %s", ext)
	}
}

// readPDF пытается извлечь текст из PDF через pdftotext
func readPDF(path string) (string, error) {
	// Пробуем pdftotext
	if _, err := exec.LookPath("pdftotext"); err == nil {
		cmd := exec.Command("pdftotext", "-layout", path, "-")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}

	// Пробуем python с pdfminer
	if _, err := exec.LookPath("python"); err == nil {
		script := `
	import sys, io, pdfminer.high_level
	sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')
	pdfminer.high_level.extract_text(sys.argv[1])
	`
		cmd := exec.Command("python", "-c", script, path)
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}

	return "", fmt.Errorf("не удалось прочитать PDF. Установи pdftotext (poppler-utils) или python pdfminer")
}

// IndexSummary возвращает список файлов в директории с подсчётом
func IndexSummary(store *Store) error {
	sources := store.SourceFiles()
	if len(sources) == 0 {
		fmt.Println("[FILES] Нет проиндексированных документов")
		return nil
	}

	fmt.Println("[FILES] Проиндексированные документы:")
	fmt.Println(strings.Repeat("-", 50))
	total := 0
	for path, count := range sources {
		fmt.Printf("  %s (%d чанков)\n", path, count)
		total += count
	}
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("  Всего: %d документов, %d чанков\n", len(sources), total)
	return nil
}
