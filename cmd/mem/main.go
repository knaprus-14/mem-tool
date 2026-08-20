package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

// === Алиасы для удобной работы с библиотекой mem ===
// Type aliases — позволяют писать Config, Store, Entry вместо mem.Config и т.д.
type (
	Config       = mem.Config
	Store        = mem.Store
	Entry        = mem.Entry
	IndexResult  = mem.IndexResult
	OllamaConfig = mem.OllamaConfig
	PolzaConfig  = mem.PolzaConfig
	ChunkConfig  = mem.ChunkConfig
)

// Алиасы функций
var (
	loadConfig         = mem.LoadConfig
	saveConfig         = mem.SaveConfig
	newStore           = mem.NewStore
	ChunkDocument      = mem.ChunkDocument
	IndexDirectory     = mem.IndexDirectory
	IndexFile          = mem.IndexFile
	IndexSummary       = mem.IndexSummary
	getEmbedding       = mem.GetEmbedding
	cosineSimilarity   = mem.CosineSimilarity
	configPath         = mem.ConfigPath
	memExists          = mem.MemExists
	memDir             = mem.MemDir
	initMem            = mem.InitMem
	ensureMem          = mem.EnsureMem
	defaultLocalConfig = mem.DefaultLocalConfig
	defaultConfig      = mem.DefaultConfig
)

const memDirName = mem.MemDirName

func init() {
	// На Windows переключаем консоль в UTF-8, чтобы русские буквы не крокозябрились
	if runtime.GOOS == "windows" {
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		if setCP := kernel32.NewProc("SetConsoleOutputCP"); setCP != nil {
			setCP.Call(65001) // CP_UTF8
		}
		if setCP := kernel32.NewProc("SetConsoleCP"); setCP != nil {
			setCP.Call(65001) // CP_UTF8
		}
	}
}

const version = "1.15.13"

// cmdRequiresDB — команды, для работы которых нужна локальная база .mem/
var cmdRequiresDB = map[string]bool{
	"add": true, "add-file": true, "import": true, "index": true,
	"config": true,
	"search": true, "recent": true, "stats": true,
	"source": true, "sources": true,
	"show": true, "get": true, "view": true,
	"delete": true, "rm": true,
	"edit": true, "retag": true,
	"important": true, "imp": true,
	"repl": true,
}

// cmdCanAutocreate — команды, которые могут автоматически создать .mem/
var cmdCanAutocreate = map[string]bool{
	"add": true, "add-file": true, "import": true, "index": true, "config": true,
}

// Парсинг --global, --dir и --color реализован в pkg/mem/cliutil.go (ParseGlobalFlag,
// ApplyDirSwitch, ParseColorFlag). Экспортированы в v1.16.0 — раньше жили здесь
// как приватные функции; теперь переиспользуются cmd/mem-index/main.go.

func main() {
	// Вся логика в run() int — main() просто транслирует код возврата в os.Exit.
	// Это позволяет defer в run() корректно срабатывать (os.Exit не запускает defer).
	os.Exit(run())
}

// run — основная логика. Возвращает код выхода: 0 при успехе, 1 при ошибке.
// Использование defer для Close() возможно благодаря тому, что main() вызывает os.Exit(run()).
func run() int {
	args0 := os.Args[1:]

	// Сначала парсим --global / --dir — они переключают cwd до всей остальной логики.
	// После chdir все команды работают с целевой базой как обычно.
	useGlobal, customDir, args0 := mem.ParseGlobalFlag(args0)
	if err := mem.ApplyDirSwitch(useGlobal, customDir); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	// Потом --color/--no-color (влияет только на ui-стили, не на cwd)
	colorMode, args0 := mem.ParseColorFlag(args0)
	ui.Init(colorMode)

	if len(os.Args) < 2 || len(args0) == 0 {
		// mem без аргументов → если .mem/ есть, запускаем TUI; иначе — help
		if memExists() {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Ошибка загрузки конфига: %v\n", err)
				return 1
			}
			store, err := newStore(memDir())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Ошибка открытия хранилища: %v\n", err)
				return 1
			}
			defer store.Close()
			runTui(cfg, store)
			return 0
		}
		printUsage()
		return 0
	}

	cmd := args0[0]
	args := args0[1:]

	// Команды, не требующие базы — обрабатываем сразу
	switch cmd {
	case "init":
		handleInit() // handleInit сам делает os.Exit — это CLI-only путь, defer'ов нет
		return 0
	case "version", "--version", "-v":
		printVersion()
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	}

	// Все остальные команды требуют локальную базу
	if !cmdRequiresDB[cmd] {
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", cmd)
		printUsage()
		return 1
	}

	// Проверяем/создаём базу
	if !memExists() {
		if cmdCanAutocreate[cmd] {
			if err := initMem(); err != nil {
				fmt.Fprintf(os.Stderr, "Ошибка автосоздания .mem/: %v\n", err)
				return 1
			}
			fmt.Println(ui.Mark("ok"), "Создана новая локальная база:", ui.Tag(".mem/"))
		} else {
			fmt.Fprintf(os.Stderr, "Ошибка: .mem/ не найдена в текущей папке\n")
			fmt.Fprintf(os.Stderr, "  Сначала выполните `mem init` или добавьте запись через `mem add`\n")
			return 1
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка загрузки конфига: %v\n", err)
		return 1
	}

	store, err := newStore(memDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка открытия хранилища: %v\n", err)
		return 1
	}
	defer store.Close()

	switch cmd {
	case "add":
		if err := handleAdd(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "search":
		if err := handleSearch(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "recent":
		if err := handleRecent(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "add-file":
		if err := handleAddFile(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "import":
		if err := handleImport(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "config":
		if err := handleConfig(args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "stats":
		handleStats(store)
	case "index":
		if err := handleIndex(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "source":
		handleSource(store, args)
	case "sources":
		handleSources(store)
	case "show", "get", "view":
		if err := handleShow(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "delete", "rm":
		if err := handleDelete(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "edit":
		if err := handleEdit(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "retag":
		if err := handleRetag(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "important", "imp":
		if err := handleImportant(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "repl":
		runRepl(cfg, store)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", cmd)
		printUsage()
		return 1
	}
	return 0
}

// handleInit создаёт локальную базу .mem/ в текущей папке
func handleInit() {
	if memExists() {
		fmt.Fprintf(os.Stderr, "Ошибка: .mem/ уже существует в текущей папке\n")
		os.Exit(1)
	}
	if err := initMem(); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		os.Exit(1)
	}
	cwd, _ := os.Getwd()
	fmt.Println(ui.Success("Создана локальная база %s", ui.Tag(".mem/")))
	fmt.Printf("  ├── %s — настройки (бэкенд, модель, чанкинг)\n", ui.Tag("config.json"))
	fmt.Printf("  ├── %s    — записи (SQLite, создаётся при первом add)\n", ui.Tag("store.db"))
	fmt.Printf("  └── %s   — метаданные (имя базы, дата создания)\n", ui.Tag("meta.json"))
	fmt.Printf("  %s %s\n", ui.Key("Имя базы:"), ui.Value(filepath.Base(cwd)))
	fmt.Println()
	fmt.Println("Теперь можно работать:")
	fmt.Println("  mem add \"текст\"          — добавить запись")
	fmt.Println("  mem search \"запрос\"      — найти")
	fmt.Println("  mem recent                — последние записи")
}

func printVersion() {
	fmt.Printf("mem-tool v%s\n", version)
	fmt.Println("(c) 2026 Кнап Руслан Юрьевич")
	fmt.Println("Векторная база знаний для работы с Claude")
}

func parseFlags(args []string) (positional []string, title string, tags []string, limit int, from, to string, minScore float64, vectorOnly bool, important bool, tagFilter string) {
	limit = 10
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-title":
			if i+1 < len(args) {
				i++
				title = args[i]
			}
		case "-tags":
			if i+1 < len(args) {
				i++
				tags = strings.Split(args[i], ",")
				for j := range tags {
					tags[j] = strings.TrimSpace(tags[j])
				}
			}
		case "-limit":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil && n > 0 {
					limit = n
				}
			}
		case "-from":
			if i+1 < len(args) {
				i++
				from = args[i]
			}
		case "-to":
			if i+1 < len(args) {
				i++
				to = args[i]
			}
		case "-min-score":
			if i+1 < len(args) {
				i++
				if n, err := strconv.ParseFloat(args[i], 64); err == nil && n >= 0 && n <= 1 {
					minScore = n
				}
			}
		case "-vector-only":
			vectorOnly = true
		case "-important":
			important = true
		case "-tag":
			// Точный фильтр по одному тегу (для семантической фильтрации по типу записи:
			// rule, decision, bug, best-practice, project, global и т.д.).
			// -tags (множественное число) оставлен для совместимости — это поиск
			// по тегам ВНУТРИ записей, а -tag — фильтр по КАТЕГОРИИ записи.
			if i+1 < len(args) {
				i++
				tagFilter = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}
	return
}

func handleAdd(cfg *Config, store *Store, args []string) error {
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)
	if len(positional) == 0 {
		return fmt.Errorf("укажи текст для сохранения\nПример: mem add \"какой-то факт\" -title \"Название\" -tags \"термины,проект\" -important")
	}

	text := strings.Join(positional, " ")
	fmt.Printf(">> Эмбеддинг через %s... ", cfg.Backend)

	embedding, err := getEmbedding(cfg, text)
	if err != nil {
		return fmt.Errorf("ошибка эмбеддинга: %w", err)
	}
	fmt.Printf("получен вектор %d измерений\n", len(embedding))

	entry, err := store.Add(text, title, tags, cfg.Backend, embedding, important)
	if err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}

	mark := ""
	if important {
		mark = " " + ui.Mark("warn")
	}
	fmt.Printf("%s Запись %s сохранена%s\n", ui.Mark("ok"), ui.ID(fmt.Sprintf("#%d", entry.ID)), mark)
	return nil
}

func handleSearch(cfg *Config, store *Store, args []string) error {
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(args)
	if len(positional) == 0 {
		return fmt.Errorf("укажи поисковый запрос\nПример: mem search \"IP сервера\" -tags \"инфраструктура\" -from 2026-06-01")
	}

	query := strings.Join(positional, " ")
	fmt.Printf(">> Поиск через %s... ", cfg.Backend)

	queryVec, err := getEmbedding(cfg, query)
	if err != nil {
		return fmt.Errorf("ошибка эмбеддинга: %w", err)
	}
	fmt.Printf("вектор %d измерений\n", len(queryVec))

	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: query, QueryVector: queryVec, Backend: cfg.Backend,
		Tags: tags, TagFilter: tagFilter, From: from, To: to,
		VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("ошибка поиска: %w", err)
	}

	if len(tags) > 0 {
		fmt.Printf("[TAG] Фильтр по тегам: %s\n", strings.Join(tags, ", "))
	}

	if tagFilter != "" {
		fmt.Printf("[TAG-FILTER] Категория: %s\n", tagFilter)
	}

	if from != "" || to != "" {
		dateRange := ""
		if from != "" {
			dateRange = "от " + from
		}
		if to != "" {
			if dateRange != "" {
				dateRange += " "
			}
			dateRange += "до " + to
		}
		fmt.Printf("[DATE] Фильтр по дате: %s\n", dateRange)
	}

	if !vectorOnly && len(results) > 0 {
		lexicalCount := 0
		for _, result := range results {
			if result.LexicalHit {
				lexicalCount++
			}
		}
		fmt.Printf("[HYBRID] Vector %.0f%% + lexical %.0f%% (%s; lexical candidates %d/%d)\n",
			mem.VectorFusionWeight*100, mem.LexicalFusionWeight*100, store.LexicalMode(), lexicalCount, len(results))
	} else if vectorOnly && len(results) > 0 {
		fmt.Printf("[VECTOR] Только векторный поиск\n")
	}

	// Повторное ранжирование: свежесть + совпадение тегов + важность
	if len(results) > 0 {
		rerankCount := reRankResults(results, query)
		if rerankCount > 0 {
			sort.Slice(results, func(i, j int) bool {
				return results[i].Score > results[j].Score
			})
			fmt.Printf("[RERANK] Повторное ранжирование: буст у %d/%d записей\n",
				rerankCount, len(results))
		}
	}
	sortSearchResults(results)

	// Фильтрация по порогу релевантности (после всех бустов)
	if minScore > 0 {
		var filtered []Entry
		for _, r := range results {
			if r.Score >= minScore {
				filtered = append(filtered, r)
			}
		}
		results = filtered
		fmt.Printf("[SCORE] Порог релевантности: >= %.0f%%\n", minScore*100)
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]

	if len(results) == 0 {
		fmt.Println(ui.Warn("Ничего не найдено"))
		return nil
	}

	fmt.Println()
	for i, r := range results {
		pct := int(r.Score * 100)
		var markKind string
		switch {
		case pct > 90:
			markKind = "good"
		case pct > 70:
			markKind = "mid"
		default:
			markKind = "low"
		}
		// Форматируем дату
		dateStr := r.Created
		if t, err := time.Parse(time.RFC3339, r.Created); err == nil {
			dateStr = t.Format("2006-01-02")
		}

		title := r.Title
		if title == "" {
			title = "(без заголовка)"
		}

		impMark := ""
		if r.Important {
			impMark = " " + ui.Mark("warn")
		}
		// Заголовок результата: mark ID [score] title (date) [!]
		header := fmt.Sprintf("%s %s %s %s (%s)%s",
			ui.Mark(markKind),
			ui.ID(fmt.Sprintf("#%d", r.ID)),
			ui.Score(pct),
			title,
			ui.Date(dateStr),
			impMark,
		)
		fmt.Println(header)
		fmt.Printf("   %s\n", r.Text)
		fmt.Printf("   %s vector=%.3f lexical=%.3f fusion=%.3f final=%.3f\n",
			ui.Key("[SCORE]"), r.VectorScore, r.LexicalScore, r.FusionScore, r.Score)

		citationID, citationLabel := r.CitationID, r.CitationLabel
		if citationID == "" || citationLabel == "" {
			citationID, citationLabel = mem.CitationForEntry(r)
		}
		fmt.Printf("   %s %s %s\n", ui.Key("[CITE]"), ui.ID(citationID), ui.Tag(citationLabel))

		if len(r.Tags) > 0 {
			fmt.Printf("   %s %s\n", ui.Key("[TAG]"), ui.Tag(strings.Join(r.Tags, ", ")))
		}
		if i < len(results)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return nil
}

func sourceReference(entry Entry) string {
	_, ref := mem.CitationForEntry(entry)
	return ref
}

func sortSearchResults(results []Entry) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].LexicalScore != results[j].LexicalScore {
			return results[i].LexicalScore > results[j].LexicalScore
		}
		if results[i].VectorScore != results[j].VectorScore {
			return results[i].VectorScore > results[j].VectorScore
		}
		return results[i].ID < results[j].ID
	})
}

// reRankResults применяет повышающие коэффициенты:
// - Freshness: записи младше 7 дней → *1.05
// - TagMatch: теги пересекаются со словами запроса → *1.10
// - Important: важные записи → *1.15
// Возвращает количество записей, получивших буст
func reRankResults(results []Entry, query string) int {
	if len(results) == 0 {
		return 0
	}

	now := time.Now()
	queryWords := strings.Fields(strings.ToLower(query))
	count := 0

	for i := range results {
		boost := 1.0
		hasBoost := false

		// Freshness boost: записи младше 7 дней
		if t, err := time.Parse(time.RFC3339, results[i].Created); err == nil {
			if now.Sub(t) < 7*24*time.Hour {
				boost *= 1.05
				hasBoost = true
			}
		}

		// Tag match boost: совпадение тегов со словами запроса
		for _, tag := range results[i].Tags {
			tagLower := strings.ToLower(tag)
			for _, word := range queryWords {
				if tagLower == word || strings.Contains(tagLower, word) || strings.Contains(word, tagLower) {
					boost *= 1.10
					hasBoost = true
					goto nextEntry
				}
			}
		}

	nextEntry:
		// Important boost: важные записи поднимаем на 15%
		if results[i].Important {
			boost *= 1.15
			hasBoost = true
		}

		if hasBoost {
			newScore := results[i].Score * boost
			if newScore > 1.0 {
				newScore = 1.0
			}
			results[i].Score = newScore
			count++
		}
	}

	return count
}

func handleRecent(store *Store, args []string) error {
	_, _, _, limit, _, _, _, _, _, _ := parseFlags(args)

	entries, err := store.Recent(limit)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	if len(entries) == 0 {
		fmt.Println(ui.Warn("База пуста. Начни с: %s", ui.Tag(`mem add "какой-то факт"`)))
		return nil
	}

	fmt.Println(ui.Header(fmt.Sprintf("Последние %d записей:", len(entries))))
	fmt.Println()
	for i, e := range entries {
		display := e.Title
		if display == "" {
			display = e.Text
			if len([]rune(display)) > 120 {
				display = string([]rune(display)[:120]) + "..."
			}
		}
		ref := ""
		if e.SourceFile != "" {
			ref = " " + ui.Tag(fmt.Sprintf("[%s]", e.SourceFile))
		}
		tagStr := ""
		if len(e.Tags) > 0 {
			tagStr = " " + ui.Tag("("+strings.Join(e.Tags, ", ")+")")
		}
		impMark := ""
		if e.Important {
			impMark = " " + ui.Mark("warn")
		}
		fmt.Printf("  %s%s%s%s  %s\n", ui.ID(fmt.Sprintf("#%d", e.ID)), tagStr, ref, impMark, display)
		if i < len(entries)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return nil
}

func handleAddFile(cfg *Config, store *Store, args []string) error {
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)
	if len(positional) == 0 {
		return fmt.Errorf("укажи путь к файлу (пример: mem add-file ./notes.txt -tags \"документация\")")
	}

	path := positional[0]
	data, err := os.ReadFile(path)
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
			Text: chunk.Text, Title: chunkTitle, Tags: tags, Backend: cfg.Backend,
			Embedding: embeddings[i], ChunkLabel: chunk.Label,
			ChunkIndex: chunk.Index, TotalChunks: len(chunks), Important: important,
			Provenance: mem.Provenance{SourcePath: sourcePath},
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

func handleImport(cfg *Config, store *Store, args []string) error {
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)
	if len(positional) == 0 {
		return fmt.Errorf("укажи Markdown, PDF или DjVu (пример: mem import ./book.djvu)")
	}
	if len(positional) > 1 {
		return fmt.Errorf("mem import принимает один документ за вызов")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("[IMPORT] Анализ документа: %s\n", positional[0])
	result, err := mem.ImportDocument(ctx, cfg, store, positional[0], mem.ImportOptions{
		Title: title, Tags: tags, Important: important,
		Progress: func(event ingest.ProgressEvent) {
			if event.Page > 0 {
				fmt.Printf("[%s] [%d/%d] page %d: %s\n", strings.ToUpper(event.Stage), event.Current, event.Total, event.Page, event.Message)
			} else {
				fmt.Printf("[%s] %s\n", strings.ToUpper(event.Stage), event.Message)
			}
		},
	})
	if err != nil {
		return err
	}
	pageSummary := "без известных страниц"
	if len(result.Pages) > 0 {
		pageSummary = fmt.Sprintf("%d страниц с текстом", len(result.Pages))
	}
	fmt.Printf("[OK] Документ импортирован: %s\n", result.SourcePath)
	fmt.Printf("     document=%s | blocks=%d | chunks=%d | %s\n",
		result.DocumentID, result.Blocks, result.Chunks, pageSummary)
	for _, warning := range result.Warnings {
		fmt.Printf("[WARN] %s\n", warning)
	}
	return nil
}

func handleIndex(cfg *Config, store *Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("укажи путь к файлу или папке (пример: mem index C:\\МоиДокументы\\)")
	}

	path := args[0]
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

func handleSource(store *Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Ошибка: укажи номер записи")
		fmt.Fprintln(os.Stderr, "Пример: mem source 15")
		os.Exit(1)
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: '%s' не число\n", args[0])
		os.Exit(1)
	}

	entry, err := store.GetByID(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		os.Exit(1)
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
}

// handleShow выводит одну запись полностью или все чанки одного файла.
//
// Использование:
//
//	mem show <id>           — одна запись (например, mem show 50 или mem show #50)
//	mem show --from-file <path> — все чанки документа с данным SourceFile
//
// Алиасы: get, view.
func handleShow(store *Store, args []string) error {
	var idArg string
	fromFile := ""
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from-file" || a == "--file" || a == "-f":
			if i+1 < len(args) {
				fromFile = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from-file="):
			fromFile = strings.TrimPrefix(a, "--from-file=")
		case strings.HasPrefix(a, "--file="):
			fromFile = strings.TrimPrefix(a, "--file=")
		default:
			rest = append(rest, a)
		}
	}
	if fromFile != "" {
		rest = nil // --from-file — отдельный режим, без id
	} else {
		if len(rest) == 0 {
			return fmt.Errorf("укажи ID записи или --from-file <путь>\nПримеры:\n  mem show 50\n  mem show #50\n  mem show --from-file docs/architecture.md")
		}
		idArg = rest[0]
	}

	if fromFile != "" {
		return showAllChunksFromFile(store, fromFile)
	}

	// Снимаем префикс # если есть
	idStr := strings.TrimPrefix(idArg, "#")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", idArg)
	}

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

func handleSources(store *Store) error {
	IndexSummary(store)
	return nil
}

func handleStats(store *Store) error {
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
func handleDelete(store *Store, args []string) error {
	if len(args) == 0 {
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
func handleEdit(cfg *Config, store *Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("укажи номер записи и новый текст\nПример: mem edit 15 \"новый текст\"\nПример: mem edit 15 -title \"Новый заголовок\"")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	// Парсим остальные аргументы (после ID)
	rest := args[1:]
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
		fmt.Printf(">> Новый эмбеддинг через %s... ", cfg.Backend)
		embedding, err := getEmbedding(cfg, editText)
		if err != nil {
			return fmt.Errorf("ошибка эмбеддинга: %w", err)
		}
		fmt.Printf("вектор %d измерений\n", len(embedding))

		if err := store.UpdateById(id, editText, editTitle, entry.Tags, embedding); err != nil {
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
func handleRetag(store *Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("укажи номер записи и новые теги\nПример: mem retag 15 -tags \"новый,тег\"")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("'%s' не число", args[0])
	}

	_, _, newTags, _, _, _, _, _, _, _ := parseFlags(args[1:])
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
func handleImportant(store *Store, args []string) error {
	if len(args) == 0 {
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

func printUsage() {
	printVersion()
	fmt.Println()
	fmt.Println(`Каждая команда работает с локальной базой .mem/ в текущей папке.
В разных папках — разные базы. Настройки эмбеддингов тоже per-project.

Использование:
  mem init
      Создать новую локальную базу .mem/ в текущей папке
      (опционально — база создаётся автоматически при первом add/index/config)

  mem version
      Показать версию программы

  mem help
      Показать эту справку

  mem add <текст> [-title "Название"] [-tags "тег1,тег2"] [-important]
      Сохранить новую запись в базу. -important — пометить как важную.
      Если .mem/ нет — создаст автоматически.

  mem search <запрос> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only]
      Найти записи, похожие по смыслу.
      По умолчанию гибридный поиск + реранжирование (свежесть, теги, важность).
      -vector-only — отключить полнотекстовый буст, только векторы.
      -tag "X" — фильтр по КАТЕГОРИИ записи (точное совпадение с одним тегом:
      rule / decision / bug / best-practice). Отличается от -tags (любой из списка).
      Текст каждой записи выводится полностью (без обрезки).

  mem recent [-limit N]
      Показать последние записи

  mem show <id> | mem show --from-file <путь>
      Алиасы: get, view. Также mem source <id>.
      Показать одну запись целиком (полный текст + метаданные)
      или все чанки одного документа.

  mem add-file <путь_к_файлу> [-tags "тег1,тег2"] [-important]
      Сохранить содержимое файла в базу (с чанкингом)

	mem import <document.md|document.pdf|document.djvu> [-title "Название"] [-tags "тег1,тег2"] [-important]
	  Импортировать Markdown, PDF или DjVu с постраничным provenance документа.
	  Markdown-маркеры <!-- page: N --> сохраняются как номера страниц.
	  Для сканов используется локальный Tesseract; инструменты не устанавливаются автоматически.

  mem index <путь_к_папке_или_файлу>
      Проиндексировать все файлы в папке (.txt, .md, .pdf, .csv, .json)

  mem sources
      Список всех проиндексированных документов с числом чанков

  mem delete <id> | mem rm <id>
      Удалить запись из базы

  mem edit <id> <новый текст> [-title "Новый заголовок"]
      Изменить текст и/или заголовок записи

  mem retag <id> -tags "новые,теги"
      Изменить теги записи

  mem important <id> | mem imp <id>
      Переключить флаг важности записи

  mem config
      Показать текущую конфигурацию (локальную, из .mem/config.json)

  mem config set-backend <ollama|polza>
      Переключить бэкенд эмбеддингов (только для текущей базы)

  mem config set-chunk-size <символов>
      Размер чанка (100-10000, умолч. 1000)

  mem config set-chunk-overlap <символов>
      Перекрытие чанков (0-1000, умолч. 100)

  mem config set-chunk-strategy <paragraph|sentence|fixed>
      Стратегия разбивки документа на чанки

  mem config set-polza-key <api_key>
      Установить API ключ Polza AI

  mem config set-polza-model <model>
      Установить модель Polza AI

  mem config set-ollama-model <model>
      Установить модель Ollama (по умолч. bge-m3)

  mem stats
      Статистика базы

  mem
      Без аргументов — запуск TUI (полноценный интерфейс на bubbletea).
      Если .mem/ нет — показывается эта справка.

  mem repl
      Классический readline-REPL (с историей и Tab-дополнением)

Глобальные флаги (перед командой):
  --color=always|never|auto   Включить/выключить цвета в выводе
  --no-color                  То же, что --color=never
  По умолчанию: auto (цвета в TTY, выключены в pipe).
  Управляется также env NO_COLOR=1, MEM_NO_COLOR=1.

  --global                    Переключиться на глобальную базу знаний
                              (по умолчанию ~/global-mem/.mem, путь через
                              env MEM_GLOBAL_DIR). Пример: mem --global stats
  --dir <путь>                Переключиться на базу в указанной директории.
                              Пример: mem --dir "C:/Users/ZMII/global-mem" stats
                              или mem --dir /home/user/projects/foo stats

Примеры:
  cd ~/projects/myapp && mem add "Сервер: 157.22.196.67"
  cd ~/projects/other && mem add "Другой факт"
  mem search "IP сервера" -tags "инфраструктура"
  mem search "tui" -tag rule                 # только правила про TUI
  mem --global search "deadlock"            # поиск в глобальной базе
  mem --global search "архитектура" -tag best-practice
  mem search "архитектура" -from 2026-07-01 -to 2026-07-26
  mem search "сервер" -min-score 0.5 -vector-only
  mem show 50                            # одна запись целиком
  mem show --from-file docs/arch.md      # все чанки документа
  mem add-file ./документация.txt
  mem import ./book.md
  mem index ./проекты/
  mem edit 1 "Обновлённый текст сервера"
  mem retag 5 -tags "сервер,ubuntu,важно"
  mem important 5
  mem delete 12
  mem config set-chunk-size 800
  mem config set-chunk-strategy sentence

Интерактивный REPL:
  Запускается командой mem (без аргументов) или mem repl.
  Внизу — prompt mem>, сверху — горизонтальная линия (рамка).
  Текст без / — сокращение для /search.
  Up/Down — история запросов (хранится в .mem/history.txt).
  Tab — дополнение /-команд (/se<TAB> → /search, /<TAB> — все команды).
  / + Enter — псевдо-popup со списком всех команд.
  Ctrl-D или /exit — выход.
  Полный список /-команд: введите /help в REPL.

Больше информации: README.md и DOCUMENTATION.md`)
}

// handleConfig — CLI-обёртка над mem.SaveConfig / mem.LoadConfig
// Реализует команды `mem config set-backend`, `set-polza-key` и т.д.
func handleConfig(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	cmd := args[0]
	switch cmd {
	case "set-backend":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-backend <ollama|polza>")
		}
		backend := args[1]
		if backend != "ollama" && backend != "polza" {
			return fmt.Errorf("бэкенд должен быть 'ollama' или 'polza'")
		}
		cfg.Backend = backend
		return saveConfig(cfg)

	case "set-polza-key":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-polza-key <api_key>")
		}
		cfg.Polza.APIKey = args[1]
		return saveConfig(cfg)

	case "set-polza-model":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-polza-model <model_name>")
		}
		cfg.Polza.Model = args[1]
		return saveConfig(cfg)

	case "set-ollama-model":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-ollama-model <model_name>")
		}
		cfg.Ollama.Model = args[1]
		return saveConfig(cfg)

	case "set-chunk-size":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-size <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 100 || n > 10000 {
			return fmt.Errorf("размер чанка должен быть от 100 до 10000")
		}
		cfg.Chunking.MaxSize = n
		return saveConfig(cfg)

	case "set-chunk-overlap":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-overlap <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 1000 {
			return fmt.Errorf("перекрытие должно быть от 0 до 1000")
		}
		cfg.Chunking.Overlap = n
		return saveConfig(cfg)

	case "set-chunk-strategy":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-strategy <paragraph|sentence|fixed>")
		}
		strategy := args[1]
		if strategy != "paragraph" && strategy != "sentence" && strategy != "fixed" {
			return fmt.Errorf("стратегия должна быть: paragraph, sentence или fixed")
		}
		cfg.Chunking.Strategy = strategy
		return saveConfig(cfg)

	default:
		return fmt.Errorf("неизвестная команда: %s", cmd)
	}
}
