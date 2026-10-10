package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

func handleSource(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleSource")(&applogResult0)

	positionals, err := exactCommandPositionals("source", args, 1, "mem source <id>")
	if err != nil {
		return err
	}
	id, err := parseEntryID(positionals[0])
	if err != nil {
		return err
	}

	entry, err := store.GetByID(id)
	if err != nil {
		return err
	}

	impMark := ""
	if entry.Important {
		impMark = " [!]"
	}
	title := entry.Title
	if title == "" {
		title = "(без заголовка)"
	}
	fmt.Printf("[NOTE] Запись #%d: %s%s\n", entry.ID, title, impMark)
	fmt.Println(strings.Repeat("---", 20))

	if entry.SourceFile != "" {
		sourcePath := entry.SourceFile
		if entry.SourcePath != "" {
			sourcePath = entry.SourcePath
		}
		fmt.Printf("[FILE] Файл:     %s\n", sourcePath)
		if entry.DocumentID != "" {
			fmt.Printf("[DOC]  Документ: %s\n", entry.DocumentID)
		}
		if entry.Page > 0 {
			fmt.Printf("[PAGE] Страница: %d\n", entry.Page)
			fmt.Printf("[BLOCK] Блок:    %d (%s)\n", entry.BlockIndex+1, entry.BlockMarker)
		}
		if entry.ExtractionMethod != "" {
			fmt.Printf("[EXTRACT] Метод:   %s", entry.ExtractionMethod)
			if entry.OCRConfidence >= 0 && entry.ExtractionMethod == "ocr" {
				fmt.Printf(" (confidence %.1f)", entry.OCRConfidence)
			}
			fmt.Println()
		}
		for _, warning := range entry.Warnings {
			fmt.Printf("[WARN] %s\n", warning)
		}
		if entry.ChunkLabel != "" {
			fmt.Printf("[SEC]  Раздел:   %s\n", entry.ChunkLabel)
		}
		if entry.TotalChunks > 0 {
			fmt.Printf("[CHUNK] Чанк:     %d/%d\n", entry.ChunkIndex+1, entry.TotalChunks)
		}
		fmt.Println(strings.Repeat("---", 20))
	}

	fmt.Printf("\n%s\n\n", entry.Text)

	if len(entry.Tags) > 0 {
		fmt.Printf("[TAG]  Теги: %s\n", strings.Join(entry.Tags, ", "))
	}
	fmt.Printf("[DATE] Дата: %s\n", entry.Created)
	fmt.Printf("[CFG]  Бэкенд: %s (%d измерений)\n", entry.Backend, entry.Dims)
	if entry.EmbeddingModel != "" {
		fmt.Printf("[EMBED] Модель: %s; space: %s\n", entry.EmbeddingModel, entry.EmbeddingSpace)
	} else {
		fmt.Println("[EMBED] Модель: неизвестна (legacy; требуется переиндексация)")
	}
	return nil
}

// handleShow выводит одну запись полностью или все чанки одного файла.
//
// Использование:
//
//	mem show <id>           — одна запись (например, mem show 50 или mem show #50)
//	mem show --from-file <path> — все чанки документа с данным SourceFile
//
// Алиасы: get, view.
type showRequest struct {
	idArg    string
	fromFile string
}

func parseShowRequest(args []string) (showRequest, error) {
	var request showRequest
	positionals := make([]string, 0, 1)
	parseOptions := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if parseOptions && a == "--" {
			parseOptions = false
			continue
		}
		if !parseOptions {
			positionals = append(positionals, a)
			continue
		}
		switch {
		case a == "--from-file" || a == "--file" || a == "-f":
			if request.fromFile != "" {
				return showRequest{}, fmt.Errorf("флаг %s указан несколько раз", a)
			}
			if i+1 >= len(args) || args[i+1] == "--" || strings.TrimSpace(args[i+1]) == "" {
				return showRequest{}, fmt.Errorf("флаг %s требует непустой путь", a)
			}
			request.fromFile = args[i+1]
			i++
		case strings.HasPrefix(a, "--from-file="):
			if request.fromFile != "" {
				return showRequest{}, fmt.Errorf("флаг --from-file указан несколько раз")
			}
			request.fromFile = strings.TrimPrefix(a, "--from-file=")
			if strings.TrimSpace(request.fromFile) == "" {
				return showRequest{}, fmt.Errorf("флаг --from-file требует непустой путь")
			}
		case strings.HasPrefix(a, "--file="):
			if request.fromFile != "" {
				return showRequest{}, fmt.Errorf("флаг --file указан несколько раз")
			}
			request.fromFile = strings.TrimPrefix(a, "--file=")
			if strings.TrimSpace(request.fromFile) == "" {
				return showRequest{}, fmt.Errorf("флаг --file требует непустой путь")
			}
		default:
			positionals = append(positionals, a)
		}
	}
	if request.fromFile != "" {
		if len(positionals) != 0 {
			return showRequest{}, fmt.Errorf("mem show --from-file принимает ровно один путь и не принимает ID")
		}
		return request, nil
	}
	if len(positionals) != 1 {
		return showRequest{}, fmt.Errorf("укажи ровно один ID записи или --from-file <путь>\nПримеры:\n  mem show 50\n  mem show #50\n  mem show --from-file docs/architecture.md")
	}
	request.idArg = positionals[0]
	if _, err := parseEntryID(request.idArg); err != nil {
		return showRequest{}, err
	}
	return request, nil
}

func handleShow(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleShow")(&applogResult0)

	request, err := parseShowRequest(args)
	if err != nil {
		return err
	}
	if request.fromFile != "" {
		return showAllChunksFromFile(store, request.fromFile)
	}

	id, _ := parseEntryID(request.idArg)
	entry, err := store.GetByID(id)
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	showOneEntry(entry)
	return nil
}

// showOneEntry печатает одну запись: заголовок, полный текст, метаданные.
func showOneEntry(entry *Entry) {
	title := entry.Title
	if title == "" {
		title = "(без заголовка)"
	}

	impMark := ""
	if entry.Important {
		impMark = " " + ui.Mark("warn")
	}

	header := fmt.Sprintf("%s %s  %s%s",
		ui.Mark("good"),
		ui.ID(fmt.Sprintf("#%d", entry.ID)),
		title,
		impMark,
	)
	fmt.Println(header)
	fmt.Println(ui.Separator())
	fmt.Println()
	fmt.Println(entry.Text)
	fmt.Println()

	if entry.SourceFile != "" {
		fmt.Printf("   %s %s\n", ui.Key("[FILE]"), ui.Tag(sourceReference(*entry)))
	}
	if len(entry.Tags) > 0 {
		fmt.Printf("   %s %s\n", ui.Key("[TAG]"), ui.Tag(strings.Join(entry.Tags, ", ")))
	}
	dateStr := entry.Created
	if t, err := time.Parse(time.RFC3339, entry.Created); err == nil {
		dateStr = t.Format("2006-01-02 15:04")
	}
	fmt.Printf("   %s %s\n", ui.Key("[DATE]"), ui.Date(dateStr))
	fmt.Printf("   %s %s (%d изм.)\n", ui.Key("[CFG]"), ui.Tag(entry.Backend), entry.Dims)
	if entry.EmbeddingModel != "" {
		fmt.Printf("   %s %s\n", ui.Key("[EMBED]"), ui.Tag(entry.EmbeddingModel))
	} else {
		fmt.Printf("   %s %s\n", ui.Key("[EMBED]"), ui.Tag("unknown legacy space"))
	}
}

// showAllChunksFromFile печатает все чанки одного документа (по SourceFile).
func showAllChunksFromFile(store *Store, sourcePath string) error {
	entries := store.GetBySourceFile(sourcePath)
	if len(entries) == 0 {
		return fmt.Errorf("не найдено записей с SourceFile = %s", sourcePath)
	}

	// Сортируем по chunk_index, чтобы порядок был правильный
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ChunkIndex != entries[j].ChunkIndex {
			return entries[i].ChunkIndex < entries[j].ChunkIndex
		}
		return entries[i].ID < entries[j].ID
	})

	fmt.Println(ui.Header(fmt.Sprintf("Документ: %s", sourcePath)))
	fmt.Println(ui.Header(fmt.Sprintf("Чанков: %d", len(entries))))
	fmt.Println(ui.Separator())

	for i, e := range entries {
		label := e.ChunkLabel
		if label == "" {
			label = fmt.Sprintf("чанк %d/%d", e.ChunkIndex+1, e.TotalChunks)
		}
		fmt.Println()
		fmt.Printf("%s %s  %s\n",
			ui.Mark("mid"),
			ui.ID(fmt.Sprintf("#%d", e.ID)),
			ui.Header(label),
		)
		fmt.Println(ui.Separator())
		fmt.Println(e.Text)
		if i < len(entries)-1 {
			fmt.Println()
			fmt.Println(ui.Separator())
		}
	}
	fmt.Println()
	return nil
}

func handleSources(store *Store) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleSources")(&applogResult0)

	IndexSummary(store)
	return nil
}

func handleWhere(store *Store) {
	storePath := store.Path()
	memPath := filepath.Dir(storePath)
	projectPath := filepath.Dir(memPath)
	stats := store.Stats()
	fmt.Println("[DB] Активная локальная база")
	fmt.Println(strings.Repeat("--", 25))
	fmt.Printf("  Проект:       %s\n", projectPath)
	fmt.Printf("  Каталог базы: %s\n", memPath)
	fmt.Printf("  SQLite:       %s\n", storePath)
	fmt.Printf("  Записей:      %d\n", stats["total_entries"])
}

func handleStats(store *Store) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleStats")(&applogResult0)

	stats := store.Stats()

	fmt.Println("[STATS] Статистика базы памяти")
	fmt.Println(strings.Repeat("--", 25))
	fmt.Printf("  Всего записей: %d\n", stats["total_entries"])
	fmt.Printf("  Из них чанков: %d\n", stats["doc_chunks"])
	fmt.Printf("  Расположение:  %s\n", stats["store_location"])
	if byBackend, ok := stats["by_backend"].(map[string]int); ok {
		fmt.Println("  По бэкендам:")
		for backend, count := range byBackend {
			fmt.Printf("    %s: %d\n", backend, count)
		}
	}
	return nil
}

// handleDelete удаляет запись по ID
func handleDelete(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleDelete")(&applogResult0)

	if len(args) != 1 {
		return fmt.Errorf("укажи номер записи для удаления\nПример: mem delete 15")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	if err := store.DeleteById(id); err != nil {
		return fmt.Errorf("%w", err)
	}

	fmt.Printf("[OK] Запись #%d удалена\n", id)
	return nil
}

// handleEdit изменяет текст и/или заголовок записи
func handleEdit(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleEdit")(&applogResult0)

	if len(args) == 0 {
		return fmt.Errorf("укажи номер записи и новый текст\nПример: mem edit 15 \"новый текст\"\nПример: mem edit 15 -title \"Новый заголовок\"")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	// Парсим остальные аргументы (после ID)
	rest := args[1:]
	if err := validateCommonFlags(rest, "-title"); err != nil {
		return err
	}
	positionals, editTitle, _, _, _, _, _, _, _, _ := parseFlags(rest)
	editText := strings.Join(positionals, " ")

	if editText == "" && editTitle == "" {
		return fmt.Errorf("укажи новый текст или заголовок (-title)\nПример: mem edit 15 \"новый текст\"")
	}

	// Получаем текущую запись
	entry, err := store.GetByID(id)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	// Если текст не указан — оставляем старый
	if editText == "" {
		editText = entry.Text
	}
	// Если заголовок не указан — оставляем старый
	if editTitle == "" {
		editTitle = entry.Title
	}

	// Если текст изменился — пересчитываем эмбеддинг
	if editText != entry.Text {
		embeddingIdentity, identityErr := mem.EmbeddingIdentityForConfig(cfg)
		if identityErr != nil {
			return identityErr
		}
		fmt.Printf(">> Новый эмбеддинг через %s... ", cfg.Backend)
		embedding, err := getEmbedding(cfg, editText)
		if err != nil {
			return fmt.Errorf("ошибка эмбеддинга: %w", err)
		}
		fmt.Printf("вектор %d измерений\n", len(embedding))

		if err := store.UpdateByIdWithEmbeddingIdentity(id, editText, editTitle, entry.Tags, embedding, embeddingIdentity); err != nil {
			return fmt.Errorf("%w", err)
		}
	} else {
		// Текст не менялся — эмбеддинг остаётся прежним
		if err := store.UpdateById(id, editText, editTitle, entry.Tags, nil); err != nil {
			return fmt.Errorf("%w", err)
		}
	}

	fmt.Printf("[OK] Запись #%d обновлена\n", id)
	return nil
}

// handleRetag изменяет теги записи
func handleRetag(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleRetag")(&applogResult0)

	if len(args) == 0 {
		return fmt.Errorf("укажи номер записи и новые теги\nПример: mem retag 15 -tags \"новый,тег\"")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	if err := validateCommonFlags(args[1:], "-tags"); err != nil {
		return err
	}
	positional, _, newTags, _, _, _, _, _, _, _ := parseFlags(args[1:])
	if len(positional) != 0 {
		return fmt.Errorf("лишний аргумент: %s", positional[0])
	}
	if len(newTags) == 0 {
		return fmt.Errorf("укажи -tags \"новые,теги\"\nПример: mem retag 15 -tags \"сервер,ubuntu\"")
	}

	// Получаем текущую запись, чтобы сохранить текст и заголовок
	entry, err := store.GetByID(id)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	if err := store.UpdateById(id, entry.Text, entry.Title, newTags, nil); err != nil {
		return fmt.Errorf("%w", err)
	}

	fmt.Printf("[OK] Теги записи #%d обновлены: %s\n", id, strings.Join(newTags, ", "))
	return nil
}

// handleImportant переключает флаг важности записи
func handleImportant(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleImportant")(&applogResult0)

	if len(args) != 1 {
		return fmt.Errorf("укажи номер записи\nПример: mem important 15")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	entry, err := store.ToggleImportant(id)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	status := "⭐ важная"
	if !entry.Important {
		status = "обычная"
	}
	fmt.Printf("[OK] Запись #%d помечена как %s\n", id, status)
	return nil
}
