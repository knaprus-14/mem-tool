package mem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

// IndexResult — результат индексации одного файла
type IndexResult struct {
	FilePath string
	Chunks   int
	Failed   int
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
			result.Err = err
		}
		return []IndexResult{result}, nil
	}

	fmt.Printf("[DIR] Сканирую: %s\n\n", absPath)

	var results []IndexResult
	totalChunks := 0
	totalFiles := 0
	skippedFiles := 0
	failedFiles := 0

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
			failedFiles++
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
	if failedFiles > 0 {
		fmt.Printf(", %d с ошибками", failedFiles)
	}
	fmt.Println()

	return results, nil
}

// IndexFile индексирует один файл: читает, чанкует, эмбеддит, сохраняет
func IndexFile(cfg *Config, store *Store, filePath string) (IndexResult, error) {
	return indexFileWithEmbedder(cfg, store, filePath, GetEmbedding)
}

type embeddingFunc func(*Config, string) ([]float32, error)

func indexFileWithEmbedder(cfg *Config, store *Store, filePath string, embed embeddingFunc) (IndexResult, error) {
	result := IndexResult{FilePath: filePath}

	absPath, err := CanonicalSourcePath(filePath)
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

	// Сначала строим все embeddings. Если хотя бы один не получен, существующая
	// версия документа остаётся нетронутой, а результат не маскируется под успех.
	embeddings := make([][]float32, len(chunks))
	for i, chunk := range chunks {
		fmt.Printf("  [%d/%d] Эмбеддинг... ", chunk.Index+1, len(chunks))

		embedding, embedErr := embed(cfg, chunk.Text)
		if embedErr != nil {
			result.Failed++
			fmt.Printf("[ERR] %v\n", embedErr)
			continue
		}
		embeddings[i] = embedding
		fmt.Printf("вектор %d\n", len(embedding))
	}
	if result.Failed > 0 {
		err := fmt.Errorf("не удалось построить embedding для %d из %d чанков; документ не обновлён", result.Failed, len(chunks))
		result.Err = err
		return result, err
	}

	// Сохраняем чанки под каноническим абсолютным путём: одинаковые имена
	// файлов в разных каталогах становятся разными документами.
	fileName := filepath.Base(absPath)
	storedChunks := make([]DocumentChunk, len(chunks))
	for i, chunk := range chunks {
		storedChunks[i] = DocumentChunk{
			Text: chunk.Text, Title: fileName, Tags: tags, Backend: cfg.Backend,
			Embedding: embeddings[i], ChunkLabel: chunk.Label,
			ChunkIndex: chunk.Index, TotalChunks: len(chunks),
			Provenance: Provenance{SourcePath: absPath},
		}
	}
	if err = store.ReplaceDocumentChunks(absPath, storedChunks); err != nil {
		result.Failed = len(chunks)
		err = fmt.Errorf("документ не обновлён: %w", err)
		result.Err = err
		return result, err
	}
	result.Chunks = len(chunks)

	return result, nil
}

// CanonicalSourcePath возвращает устойчивую identity документа. В базе хранится
// полный очищенный путь; старые записи с basename остаются нетронутыми, потому
// что автоматически сопоставить их с одним из одноимённых файлов небезопасно.
func CanonicalSourcePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
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

// readPDF keeps the legacy mem index path working while sharing the staged PDF
// extractor and its actionable errors with mem import.
func readPDF(path string) (string, error) {
	doc, err := ingest.Extract(context.Background(), path)
	if err != nil {
		return "", err
	}
	texts := make([]string, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		texts = append(texts, block.Text)
	}
	return strings.Join(texts, "\n\n"), nil
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
