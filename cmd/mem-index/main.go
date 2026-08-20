// mem-index — отдельный бинарь для семантического каталога файлов.
//
// Хранит метаданные файлов (путь, имя, размер, mtime, hash, аннотация) и
// embedding строки (name + parent_dirs + annotation) в .fileindex/SQLite-базе.
// Поиск — косинусом по embedding.
//
// Использование:
//
//	mem-index init <dir>            — создать .fileindex/, первый scan
//	mem-index scan <dir>            — инкрементальный rescan
//	mem-index enrich <dir>          — обновить аннотации (extract text из PDF/FB2/EPUB/...)
//	mem-index find "запрос"         — семантический поиск
//	mem-index list [-limit N]       — последние записи
//	mem-index show <id>             — одна запись целиком
//	mem-index stats                 — статистика
//	mem-index rm <id>               — удалить запись
//
// Глобальные флаги: --global, --dir <path>, --color/--no-color.
//
// Версия: 1.16.0 (новый бинарь, новая функциональность).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/pkg/fileindex"
	"github.com/knaprus-14/mem-tool/pkg/mem"
	"github.com/knaprus-14/mem-tool/pkg/ui"
)

const version = "1.16.0"

func main() {
	os.Exit(run())
}

func run() int {
	args0 := os.Args[1:]

	// Глобальные флаги --global / --dir.
	useGlobal, customDir, args0 := mem.ParseGlobalFlag(args0)
	if err := mem.ApplyDirSwitch(useGlobal, customDir); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	// Цвета.
	colorMode, args0 := mem.ParseColorFlag(args0)
	ui.Init(colorMode)

	if len(args0) == 0 {
		printUsage()
		return 0
	}

	cmd := args0[0]
	args := args0[1:]

	switch cmd {
	case "init":
		return handleInit(args)
	case "scan":
		return handleScan(args)
	case "enrich":
		return handleEnrich(args)
	case "find":
		return handleFind(args)
	case "list":
		return handleList(args)
	case "show":
		return handleShow(args)
	case "stats":
		return handleStats(args)
	case "rm":
		return handleRm(args)
	case "version":
		mem.PrintVersion("mem-index", version)
		return 0
	case "help":
		printUsage()
		return 0
	}

	fmt.Fprintf(os.Stderr, "неизвестная команда: %s\n", cmd)
	printUsage()
	return 1
}

// parseFileIndexFlags парсит флаги mem-index. Отдельная функция от mem.parseFlags,
// потому что набор флагов другой.
func parseFileIndexFlags(args []string) (positional []string, enrich, noEmbed, includeStale bool, limit int, format string) {
	limit = 10
	format = "text"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-enrich":
			enrich = true
		case "-no-embed":
			noEmbed = true
		case "-include-stale":
			includeStale = true
		case "-limit":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil && n > 0 {
					limit = n
				}
			}
		case "-format":
			if i+1 < len(args) {
				i++
				format = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}
	return
}

// resolveRootDir возвращает абсолютный путь к scan-root.
// Если positional пуст — берём cwd.
func resolveRootDir(positional []string) (string, error) {
	if len(positional) == 0 {
		return os.Getwd()
	}
	root := positional[0]
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("не удалось разрезолвить путь %s: %w", root, err)
	}
	return abs, nil
}

// === handleInit ===

func handleInit(args []string) int {
	root, err := resolveRootDir(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	dir := filepath.Join(root, fileindex.FileIndexDirName)

	if fileindex.FileIndexExistsIn(dir) {
		fmt.Fprintf(os.Stderr, ".fileindex/ уже существует в %s\n", root)
		return 1
	}

	name := filepath.Base(root)
	if err := fileindex.InitFileIndexIn(dir, name); err != nil {
		fmt.Fprintf(os.Stderr, "ошибка init: %v\n", err)
		return 1
	}

	fmt.Printf("%s .fileindex/ создан в %s\n", ui.Mark("ok"), root)
	fmt.Println(ui.Tag("Запускаю первый scan..."))

	cfg, err := fileindex.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки config: %v\n", err)
		return 1
	}

	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	report, err := fileindex.Scan(fileindex.ScanOptions{
		RootDir:  root,
		Enrich:   false,
		Embed:    true,
		Progress: true,
	}, store, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка scan: %v\n", err)
		return 1
	}
	printReport(report)
	if len(report.Errors) > 0 {
		return 1
	}
	fmt.Println()
	fmt.Printf("Готово. Аннотации пока пустые — запустите: mem-index enrich %s\n", root)
	return 0
}

// === handleScan ===

func handleScan(args []string) int {
	positional, enrich, noEmbed, _, _, _ := parseFileIndexFlags(args)
	root, err := resolveRootDir(positional)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	dir := filepath.Join(root, fileindex.FileIndexDirName)
	if !fileindex.FileIndexExistsIn(dir) {
		fmt.Fprintf(os.Stderr, ".fileindex/ не найден в %s — сначала запустите: mem-index init %s\n", root, root)
		return 1
	}

	cfg, err := fileindex.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки config: %v\n", err)
		return 1
	}
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	report, err := fileindex.Scan(fileindex.ScanOptions{
		RootDir:  root,
		Enrich:   enrich,
		Embed:    !noEmbed,
		Progress: true,
	}, store, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка scan: %v\n", err)
		return 1
	}
	printReport(report)
	if len(report.Errors) > 0 {
		return 1
	}
	return 0
}

// === handleEnrich ===

func handleEnrich(args []string) int {
	positional, _, _, _, _, _ := parseFileIndexFlags(args)
	root, err := resolveRootDir(positional)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	dir := filepath.Join(root, fileindex.FileIndexDirName)
	if !fileindex.FileIndexExistsIn(dir) {
		fmt.Fprintf(os.Stderr, ".fileindex/ не найден в %s — сначала запустите: mem-index init %s\n", root, root)
		return 1
	}

	cfg, err := fileindex.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки config: %v\n", err)
		return 1
	}
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	fmt.Println(ui.Tag("Извлекаю аннотации (FB2/PDF/EPUB/DjVu/TXT/MD)..."))
	report, err := fileindex.Scan(fileindex.ScanOptions{
		RootDir:  root,
		Enrich:   true,
		Embed:    true,
		Progress: true,
	}, store, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка enrich: %v\n", err)
		return 1
	}
	printReport(report)
	if len(report.Errors) > 0 {
		return 1
	}
	return 0
}

// === handleFind ===

func handleFind(args []string) int {
	positional, _, _, _, limit, _ := parseFileIndexFlags(args)
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "укажи поисковый запрос\nПример: mem-index find \"книга про роботов\"")
		return 1
	}
	query := strings.Join(positional, " ")

	dir := fileindex.FileIndexDir()
	if !fileindex.FileIndexExists() {
		fmt.Fprintln(os.Stderr, ".fileindex/ не найден — сначала запустите: mem-index init <dir>")
		return 1
	}
	cfg, err := fileindex.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки config: %v\n", err)
		return 1
	}
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	fmt.Printf(">> Поиск через %s... ", cfg.Backend)
	queryVec, err := mem.GetEmbedding(cfg, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка эмбеддинга: %v\n", err)
		return 1
	}
	fmt.Printf("вектор %d измерений\n", len(queryVec))

	results, err := store.Search(queryVec, cfg.Backend, limit, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка поиска: %v\n", err)
		return 1
	}
	if len(results) == 0 {
		fmt.Println(ui.Warn("Ничего не найдено. Попробуй: mem-index enrich . — обновить аннотации."))
		return 0
	}

	fmt.Println(ui.Header(fmt.Sprintf("Найдено: %d (показано %d)", len(results), limit)))
	fmt.Println()
	for i, r := range results {
		e := r.Entry
		fmt.Printf("[%d] %s %s  %.1f%%\n",
			i+1,
			ui.ID(fmt.Sprintf("#%d", e.ID)),
			ui.Tag(e.Path),
			r.Score*100)
		if e.Annotation != "" {
			snippet := e.Annotation
			if len([]rune(snippet)) > 200 {
				snippet = string([]rune(snippet)[:200]) + "..."
			}
			fmt.Printf("    %s\n", snippet)
		}
		if i < len(results)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return 0
}

// === handleList ===

func handleList(args []string) int {
	_, _, _, includeStale, limit, _ := parseFileIndexFlags(args)

	dir := fileindex.FileIndexDir()
	if !fileindex.FileIndexExists() {
		fmt.Fprintln(os.Stderr, ".fileindex/ не найден")
		return 1
	}
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	entries, err := store.List(limit, includeStale)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка list: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Println(ui.Warn("База пуста. Начни с: %s", ui.Tag("mem-index init .")))
		return 0
	}
	fmt.Println(ui.Header(fmt.Sprintf("Последние %d записей:", len(entries))))
	fmt.Println()
	for i, e := range entries {
		staleMark := ""
		if e.Stale {
			staleMark = " " + ui.Mark("warn") + " [STALE]"
		}
		sizeStr := humanSize(e.Size)
		fmt.Printf("  %s %s  %s  %s%s\n",
			ui.ID(fmt.Sprintf("#%d", e.ID)),
			ui.Tag(e.Path),
			ui.Tag(fmt.Sprintf("(%s, %s)", e.Ext, sizeStr)),
			e.Name,
			staleMark)
		if i < len(entries)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return 0
}

// === handleShow ===

func handleShow(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "укажи id записи\nПример: mem-index show 42")
		return 1
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "'%s' не число\n", args[0])
		return 1
	}

	dir := fileindex.FileIndexDir()
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	e, err := store.Get(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	fmt.Println(ui.Header(fmt.Sprintf("Запись #%d", e.ID)))
	fmt.Printf("  Path:        %s\n", e.Path)
	fmt.Printf("  Name:        %s\n", e.Name)
	fmt.Printf("  Ext:         %s\n", e.Ext)
	fmt.Printf("  Size:        %s (%d bytes)\n", humanSize(e.Size), e.Size)
	fmt.Printf("  Mtime:       %s\n", timeFmt(e.Mtime))
	fmt.Printf("  Parent dir:  %s\n", e.ParentDir)
	if e.Hash != "" {
		fmt.Printf("  Hash:        %s\n", e.Hash)
	}
	fmt.Printf("  Backend:     %s (%d dims)\n", e.Backend, e.Dims)
	fmt.Printf("  Stale:       %v\n", e.Stale)
	fmt.Printf("  Last seen:   %s\n", e.LastSeenAt)
	fmt.Println()
	if e.Annotation != "" {
		fmt.Println(ui.Tag("Annotation:"))
		fmt.Println("  " + e.Annotation)
	} else {
		fmt.Println(ui.Warn("Annotation отсутствует. Запустите: mem-index enrich ."))
	}
	return 0
}

// === handleStats ===

func handleStats(args []string) int {
	dir := fileindex.FileIndexDir()
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()

	stats, err := store.Stats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка stats: %v\n", err)
		return 1
	}
	fmt.Println("[STATS] Статистика каталога файлов")
	fmt.Println("--------------------------------------------------")
	fmt.Printf("  Всего записей:  %d\n", stats.Total)
	fmt.Printf("  Активных:       %d\n", stats.NotStale)
	fmt.Printf("  Stale:          %d\n", stats.Stale)
	fmt.Println("  По расширениям:")
	for ext, count := range stats.ByExt {
		fmt.Printf("    %s: %d\n", ext, count)
	}
	fmt.Printf("  Расположение:   %s\n", filepath.Join(dir, "store.db"))
	return 0
}

// === handleRm ===

func handleRm(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "укажи id записи\nПример: mem-index rm 42")
		return 1
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "'%s' не число\n", args[0])
		return 1
	}
	dir := fileindex.FileIndexDir()
	store, err := fileindex.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка открытия store: %v\n", err)
		return 1
	}
	defer store.Close()
	if err := store.Delete(id); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("%s Запись #%d удалена\n", ui.Mark("ok"), id)
	return 0
}

// === Утилиты ===

func printReport(r fileindex.ScanReport) {
	fmt.Println()
	fmt.Println(ui.Header("Итог сканирования:"))
	fmt.Printf("  Добавлено:    %d\n", r.Added)
	fmt.Printf("  Обновлено:    %d\n", r.Updated)
	fmt.Printf("  Пропущено:    %d (mtime не изменился)\n", r.Skipped)
	fmt.Printf("  Stale:        %d (удалено/перемещено)\n", r.Stale)
	if len(r.Errors) > 0 {
		fmt.Printf("  Ошибок:       %d\n", len(r.Errors))
		for _, e := range r.Errors {
			if len(e) > 200 {
				e = e[:200] + "..."
			}
			fmt.Printf("    - %s\n", e)
		}
	}
}

func humanSize(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func timeFmt(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return fmt.Sprintf("%s (%d)", time.Unix(unix, 0).Format("2006-01-02 15:04"), unix)
}

// === Справка ===

func printUsage() {
	mem.PrintVersion("mem-index", version)
	fmt.Println()
	fmt.Println(`Семантический каталог файлов. mem-index индексирует имена, пути и
(опционально) аннотации файлов, чтобы можно было найти книгу по смыслу —
даже если не помнишь точного названия.

База — в <dir>/.fileindex/ (SQLite + embedding).
Поддерживаемые форматы для аннотаций: TXT, MD, FB2, PDF, EPUB, DjVu.

Использование:
  mem-index init [<dir>]
      Создать .fileindex/ в указанной папке (по умолчанию cwd)
      и сразу сделать первый scan (без аннотаций).
      Пример: mem-index init D:\Books

  mem-index scan [<dir>] [-enrich] [-no-embed]
      Инкрементальный rescan. По умолчанию — без аннотаций, с embedding.
      -enrich    — обновить аннотации (FB2/PDF/EPUB/DjVu/TXT/MD).
      -no-embed  — только метаданные, без embedding (быстрый preview).
      Быстрый skip для файлов с неизменившимся mtime.

  mem-index enrich [<dir>]
      Повторный scan с извлечением аннотаций.
      Полезно после добавления pdftotext/djvused в систему — ранее
      пропущенные аннотации подтянутся.

  mem-index find "запрос" [-limit N]
      Семантический поиск. Гибрид: embedding (bge-m3 / OpenAI)
      + substring boost по имени файла.
      Пример: mem-index find "книга про японскую кухню"
              mem-index find "Азимов роботы"

  mem-index list [-limit N] [-include-stale]
      Последние записи (по last_seen_at).
      По умолчанию stale скрыты. -include-stale — показать.

  mem-index show <id>
      Одна запись целиком: путь, метаданные, аннотация.

  mem-index stats
      Статистика каталога (всего/активных/stale/по расширениям).

  mem-index rm <id>
      Удалить запись из каталога (не с диска!).

  mem-index version
      Показать версию

  mem-index help
      Эта справка

Глобальные флаги (перед командой):
  --global                   Использовать глобальную базу (~/global-mem/.fileindex,
                             если symlink/инициализирована). По умолчанию — локальная.
  --dir <путь>               Использовать базу в <путь>/.fileindex.
  --color=always|never|auto   Цвета (по умолчанию auto).

Примеры:
  mem-index init D:\Books            # создать каталог + первый scan
  mem-index enrich D:\Books          # извлечь аннотации
  mem-index find "роботы"            # найти "Я, робот" Азимова и т.п.
  mem-index list -limit 5            # последние 5 файлов
  mem-index show 42                  # показать запись #42
  mem-index --dir D:\Books stats     # статистика другой базы

Аннотации:
  TXT, MD     — первые 64 KB текста
  FB2         — XML <annotation> (точное описание книги)
  PDF         — первые 2 страницы через pdftotext (флаги -f 1 -l 2)
                (если pdftotext установлен; иначе пустая аннотация)
  EPUB        — <dc:description>/<dc:title>/<dc:creator> из content.opf
  DjVu        — метаданные через djvused (-e print-meta)
                (без OCR — это title/author/year, не текст страниц)

После сканирования с enrich — поиск "книга про X" находит не только по
имени файла, но и по смыслу аннотации.`)
}
