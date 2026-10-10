package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func handleAddFile(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleAddFile")(&applogResult0)

	if err := validateSingleDocumentCommandArgs("add-file", args); err != nil {
		return err
	}
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)

	path := positional[0]
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" || ext == ".djvu" || ext == ".djv" || ext == ".docx" {
		fmt.Printf("[INFO] Формат %s требует извлечения текста и provenance; переключаю на mem import.\n", ext)
		return handleImport(cfg, store, args)
	}
	data, err := sourcefile.ReadText(context.Background(), path)
	if err != nil {
		return fmt.Errorf("чтение файла %s: %w", path, err)
	}

	text := strings.TrimSpace(string(data))
	if text == "" {
		return fmt.Errorf("файл пуст")
	}

	sourcePath, err := mem.CanonicalSourcePath(path)
	if err != nil {
		return fmt.Errorf("путь к файлу: %w", err)
	}
	fmt.Printf("[FILE] Файл: %s (%d символов)\n", sourcePath, len([]rune(text)))

	chunks := ChunkDocument(text, cfg.Chunking.MaxSize, cfg.Chunking.Overlap, cfg.Chunking.Strategy)
	if len(chunks) == 0 {
		return fmt.Errorf("после чанкинга не осталось текста")
	}
	if len(chunks) > 1 {
		fmt.Printf("[CUT] Разбито на %d чанков (max %d символов, стратегия: %s)\n",
			len(chunks), cfg.Chunking.MaxSize, cfg.Chunking.Strategy)
	}
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}

	embeddings := make([][]float32, len(chunks))
	failed := 0
	for i, chunk := range chunks {
		fmt.Printf("   [%d/%d] Эмбеддинг... ", i+1, len(chunks))
		embedding, embedErr := getEmbedding(cfg, chunk.Text)
		if embedErr != nil {
			failed++
			fmt.Printf("[ERR] %v\n", embedErr)
			continue
		}
		embeddings[i] = embedding
		fmt.Printf("вектор %d [OK]\n", len(embedding))
	}
	if failed > 0 {
		return fmt.Errorf("не удалось построить embedding для %d из %d чанков; файл не сохранён", failed, len(chunks))
	}

	fileName := filepath.Base(sourcePath)
	storedChunks := make([]mem.DocumentChunk, len(chunks))
	for i, chunk := range chunks {
		chunkTitle := fileName
		if len(chunks) == 1 && title != "" {
			chunkTitle = title
		} else if chunk.Label != "" {
			chunkTitle = fileName + ": " + chunk.Label
		}
		storedChunks[i] = mem.DocumentChunk{
			Text: chunk.Text, Title: chunkTitle, Tags: tags, Backend: embeddingIdentity.Backend,
			EmbeddingModel: embeddingIdentity.Model, EmbeddingSpace: embeddingIdentity.SpaceID,
			Embedding: embeddings[i], ChunkLabel: chunk.Label,
			ChunkIndex: chunk.Index, TotalChunks: len(chunks), Important: important,
			Provenance: mem.Provenance{SourcePath: sourcePath, OCRConfidence: -1},
		}
	}
	if err := store.ReplaceDocumentChunks(sourcePath, storedChunks); err != nil {
		return fmt.Errorf("файл не обновлён: %w", err)
	}

	if len(chunks) == 1 {
		entries := store.GetBySourceFile(sourcePath)
		if len(entries) != 1 {
			return fmt.Errorf("файл сохранён, но запись отсутствует в кэше")
		}
		mark := ""
		if important {
			mark = " [!]"
		}
		fmt.Printf("[OK] Файл сохранён как запись #%d%s\n", entries[0].ID, mark)
	} else {
		fmt.Printf("[OK] Файл сохранён как %d чанков\n", len(chunks))
	}
	return nil
}

func handleImport(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleImport")(&applogResult0)

	if err := validateSingleDocumentCommandArgs("import", args); err != nil {
		return err
	}
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("[IMPORT] Анализ документа: %s\n", positional[0])
	result, err := importDocument(ctx, cfg, store, positional[0], mem.ImportOptions{
		Title: title, Tags: tags, Important: important,
		Progress: func(event ingest.ProgressEvent) {
			if event.Stage == ingest.StageEmbed && event.Current > 0 && !shouldReportImportProgress(event.Current, event.Total) {
				return
			}
			if event.Page > 0 {
				fmt.Printf("[%s] [%d/%d] page %d: %s\n", strings.ToUpper(event.Stage), event.Current, event.Total, event.Page, event.Message)
			} else {
				fmt.Printf("[%s] %s\n", strings.ToUpper(event.Stage), event.Message)
			}
		},
	})
	if err != nil {
		if result.RunID > 0 {
			return fmt.Errorf("запуск импорта #%d завершён со статусом %s: %w; детали: mem import-run %d",
				result.RunID, importRunStatusLabel(result.Status), err, result.RunID)
		}
		return err
	}
	pageSummary := "без известных страниц"
	if len(result.Pages) > 0 {
		pageSummary = fmt.Sprintf("%d страниц с текстом", len(result.Pages))
	}
	fmt.Printf("[OK] Документ импортирован: %s\n", result.SourcePath)
	if result.RunID > 0 {
		fmt.Printf("     запуск #%d · статус: %s\n", result.RunID, importRunStatusLabel(result.Status))
	}
	fmt.Printf("     document=%s | blocks=%d | chunks=%d | %s\n",
		result.DocumentID, result.Blocks, result.Chunks, pageSummary)
	if result.PhysicalPages > 0 {
		fmt.Printf("     physical pages=%d | stored=%d | empty=%d | failed=%d\n",
			result.PhysicalPages, result.StoredPages, result.EmptyPages, result.FailedPages)
	}
	fmt.Printf("     revision=%s\n", result.DocumentRevision)
	for _, warning := range result.Warnings {
		fmt.Printf("[WARN] %s\n", warning)
	}
	return nil
}

func handleImportStatus(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleImportStatus")(&applogResult0)

	selector := ""
	jsonOutput := false
	allPages := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--document":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return fmt.Errorf("import-status: --document требует путь или document-id")
			}
			selector = args[index+1]
			index++
		case "--json":
			jsonOutput = true
		case "--all-pages":
			allPages = true
		default:
			return fmt.Errorf("использование: mem import-status [--document <путь|document-id>] [--all-pages] [--json]")
		}
	}
	manifests, err := store.CurrentDocumentImportManifests(selector)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(manifests)
	}
	if len(manifests) == 0 {
		fmt.Fprintln(os.Stdout, "В текущей базе нет импортированных versioned-документов.")
		return nil
	}
	fmt.Fprintln(os.Stdout, "Манифесты импорта физических страниц")
	fmt.Fprintln(os.Stdout, "-------------------------------------")
	for _, manifest := range manifests {
		fmt.Fprintf(os.Stdout, "\n%s\n", manifest.SourcePath)
		if !manifest.Available {
			fmt.Fprintln(os.Stdout, "  Манифест отсутствует: документ импортирован старой версией; для точного отчёта импортируйте его заново.")
			continue
		}
		fmt.Fprintf(os.Stdout, "  Формат: %s · импорт: %s\n", manifest.Format, manifest.ImportedAt)
		fmt.Fprintf(os.Stdout, "  Физические страницы: %d · сохранено: %d · пусто: %d · ошибок: %d\n",
			manifest.PhysicalPageCount, manifest.StoredPages, manifest.EmptyPages, manifest.FailedPages)
		fmt.Fprintf(os.Stdout, "  Блоки: %d · чанки: %d · диапазон: %d-%d\n",
			manifest.Blocks, manifest.Chunks, manifest.SelectedPageFirst, manifest.SelectedPageLast)
		shown := 0
		for _, page := range manifest.Pages {
			if !allPages && page.Status == mem.DocumentImportPageStored && len(page.Warnings) == 0 {
				continue
			}
			method := page.ExtractionMethod
			if method == "" {
				method = "нет текста"
			}
			fmt.Fprintf(os.Stdout, "  - стр. %d: %s · метод: %s · символов: %d · чанков: %d\n",
				page.Page, page.Status, method, page.TextRunes, page.ChunkCount)
			for _, warning := range page.Warnings {
				fmt.Fprintf(os.Stdout, "      причина: %s\n", warning)
			}
			shown++
		}
		if shown == 0 && !allPages {
			fmt.Fprintln(os.Stdout, "  Проблемных страниц нет. Для полного списка используйте --all-pages.")
		}
	}
	return nil
}

func handleImportRuns(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleImportRuns")(&applogResult0)

	selector := ""
	status := ""
	limit := 20
	jsonOutput := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--document":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return fmt.Errorf("import-runs: --document требует путь или document-id")
			}
			selector = args[index+1]
			index++
		case "--status":
			if index+1 >= len(args) {
				return fmt.Errorf("import-runs: --status требует значение")
			}
			status = strings.ToLower(args[index+1])
			index++
		case "-limit", "--limit":
			if index+1 >= len(args) {
				return fmt.Errorf("import-runs: -limit требует число")
			}
			value, err := strconv.Atoi(args[index+1])
			if err != nil || value <= 0 {
				return fmt.Errorf("import-runs: limit должен быть положительным числом")
			}
			limit = value
			index++
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("использование: mem import-runs [--document <путь|document-id>] [--status <статус>] [-limit N] [--json]")
		}
	}
	runs, err := store.DocumentImportRuns(selector, status, limit)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(runs)
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stdout, "Запусков импорта по заданному фильтру нет.")
		return nil
	}
	fmt.Fprintln(os.Stdout, "Журнал попыток импорта")
	fmt.Fprintln(os.Stdout, "------------------------")
	for _, run := range runs {
		path := run.SourcePath
		if path == "" {
			path = run.RequestedPath
		}
		fmt.Fprintf(os.Stdout, "#%d · %s · %s\n", run.ID, importRunStatusLabel(run.Status), path)
		fmt.Fprintf(os.Stdout, "  начало: %s · длительность: %s · этап: %s\n",
			formatImportRunTime(run.StartedAt), importRunDuration(run), importRunStageLabel(run.FinalStage))
		if run.Status == mem.DocumentImportRunRunning {
			fmt.Fprintln(os.Stdout, "  база обновлена: ещё нет; импорт продолжается")
		} else if run.DocumentUpdated {
			fmt.Fprintf(os.Stdout, "  база обновлена: да · блоки: %d · чанки: %d\n", run.Blocks, run.Chunks)
		} else {
			fmt.Fprintln(os.Stdout, "  база обновлена: нет; предыдущая успешная версия сохранена")
		}
		if run.PhysicalPageCount > 0 {
			fmt.Fprintf(os.Stdout, "  страницы: сохранено %d · пусто %d · ошибок %d\n",
				run.StoredPages, run.EmptyPages, run.FailedPages)
		}
		if run.ErrorMessage != "" {
			fmt.Fprintf(os.Stdout, "  причина: %s\n", run.ErrorMessage)
		}
	}
	return nil
}

func handleImportRun(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleImportRun")(&applogResult0)

	if len(args) == 0 {
		return fmt.Errorf("использование: mem import-run <id> [--all-pages] [--json]")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("import-run: id должен быть положительным числом")
	}
	jsonOutput := false
	allPages := false
	for _, arg := range args[1:] {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--all-pages":
			allPages = true
		default:
			return fmt.Errorf("использование: mem import-run <id> [--all-pages] [--json]")
		}
	}
	run, err := store.DocumentImportRun(id)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(run)
	}
	path := run.SourcePath
	if path == "" {
		path = run.RequestedPath
	}
	fmt.Fprintf(os.Stdout, "Запуск импорта #%d\n", run.ID)
	fmt.Fprintln(os.Stdout, "-----------------")
	fmt.Fprintf(os.Stdout, "Документ: %s\n", path)
	fmt.Fprintf(os.Stdout, "Статус: %s · этап: %s\n", importRunStatusLabel(run.Status), importRunStageLabel(run.FinalStage))
	fmt.Fprintf(os.Stdout, "Начало: %s · завершение: %s · длительность: %s\n",
		formatImportRunTime(run.StartedAt), formatImportRunTime(run.CompletedAt), importRunDuration(run))
	if run.Status == mem.DocumentImportRunRunning {
		fmt.Fprintln(os.Stdout, "База обновлена: ещё нет; импорт продолжается.")
	} else if run.DocumentUpdated {
		fmt.Fprintf(os.Stdout, "База обновлена: да · блоки: %d · чанки: %d\n", run.Blocks, run.Chunks)
	} else {
		fmt.Fprintln(os.Stdout, "База обновлена: нет; предыдущая успешная версия документа не изменена.")
	}
	if run.PhysicalPageCount > 0 {
		fmt.Fprintf(os.Stdout, "Физические страницы: %d · диапазон: %d-%d · сохранено: %d · пусто: %d · ошибок: %d\n",
			run.PhysicalPageCount, run.SelectedPageFirst, run.SelectedPageLast,
			run.StoredPages, run.EmptyPages, run.FailedPages)
	}
	if run.ErrorMessage != "" {
		fmt.Fprintf(os.Stdout, "Причина: %s\n", run.ErrorMessage)
	}
	for _, warning := range run.Warnings {
		fmt.Fprintf(os.Stdout, "Предупреждение: %s\n", warning)
	}
	shown := 0
	for _, page := range run.Pages {
		if !allPages && page.Status == mem.DocumentImportPageStored && len(page.Warnings) == 0 {
			continue
		}
		method := page.ExtractionMethod
		if method == "" {
			method = "нет текста"
		}
		fmt.Fprintf(os.Stdout, "- стр. %d: %s · метод: %s · символов: %d · чанков: %d\n",
			page.Page, page.Status, method, page.TextRunes, page.ChunkCount)
		for _, warning := range page.Warnings {
			fmt.Fprintf(os.Stdout, "    причина: %s\n", warning)
		}
		shown++
	}
	if len(run.Pages) > 0 && shown == 0 && !allPages {
		fmt.Fprintln(os.Stdout, "Проблемных страниц нет. Для полного списка используйте --all-pages.")
	}
	return nil
}

func importRunStatusLabel(status string) string {
	switch status {
	case mem.DocumentImportRunRunning:
		return "выполняется"
	case mem.DocumentImportRunSucceeded:
		return "успешно"
	case mem.DocumentImportRunPartial:
		return "частично: есть ошибки отдельных страниц"
	case mem.DocumentImportRunFailed:
		return "ошибка"
	case mem.DocumentImportRunCancelled:
		return "отменено"
	case mem.DocumentImportRunInterrupted:
		return "прервано аварийным завершением"
	default:
		return status
	}
}

func importRunStageLabel(stage string) string {
	switch stage {
	case ingest.StageAnalyze:
		return "анализ документа"
	case ingest.StageText:
		return "извлечение текстового слоя"
	case ingest.StageRender:
		return "рендеринг страницы"
	case ingest.StageOCR:
		return "OCR"
	case ingest.StageEmbed:
		return "создание embeddings"
	case ingest.StageDone:
		return "фиксация результата"
	case "process":
		return "процесс приложения"
	case "":
		return "не определён"
	default:
		return stage
	}
}

func formatImportRunTime(value string) string {
	if value == "" {
		return "—"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}

func importRunDuration(run mem.DocumentImportRun) string {
	started, startErr := time.Parse(time.RFC3339Nano, run.StartedAt)
	completed, completedErr := time.Parse(time.RFC3339Nano, run.CompletedAt)
	if startErr != nil || completedErr != nil || completed.Before(started) {
		if run.Status == mem.DocumentImportRunRunning {
			return "выполняется"
		}
		return "—"
	}
	return completed.Sub(started).Round(time.Millisecond).String()
}

func shouldReportImportProgress(current, total int) bool {
	if current <= 10 || current == total || total <= 100 {
		return true
	}
	step := total / 100
	if step < 10 {
		step = 10
	}
	return current%step == 0
}

func handleIndex(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleIndex")(&applogResult0)

	positionals, err := exactCommandPositionals("index", args, 1, "mem index <файл|каталог>")
	if err != nil {
		return err
	}
	path := positionals[0]
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("путь для индексации пуст")
	}

	results, err := IndexDirectory(cfg, store, path)
	if err != nil {
		return err
	}

	// Подводим итог
	total := 0
	errors := 0
	for _, r := range results {
		total += r.Chunks
		if r.Err != nil {
			errors++
		}
	}
	if errors > 0 {
		return fmt.Errorf("индексация завершена с ошибками: %d из %d файлов; сохранено %d чанков", errors, len(results), total)
	}
	return nil
}
