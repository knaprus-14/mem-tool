package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/buildinfo"
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
	AnswerConfig = mem.AnswerConfig
	PolzaConfig  = mem.PolzaConfig
	ChunkConfig  = mem.ChunkConfig
)

// Алиасы функций
var (
	loadConfig          = mem.LoadConfig
	saveConfig          = mem.SaveConfig
	newStore            = mem.NewStore
	ChunkDocument       = mem.ChunkDocument
	IndexDirectory      = mem.IndexDirectory
	IndexFile           = mem.IndexFile
	IndexSummary        = mem.IndexSummary
	getEmbedding        = mem.GetEmbedding
	getEmbeddingContext = mem.GetEmbeddingContext
	cosineSimilarity    = mem.CosineSimilarity
	configPath          = mem.ConfigPath
	memExists           = mem.MemExists
	memDir              = mem.MemDir
	initMem             = mem.InitMem
	ensureMem           = mem.EnsureMem
	defaultLocalConfig  = mem.DefaultLocalConfig
	defaultConfig       = mem.DefaultConfig
	newAnswerProvider   = func(cfg mem.AnswerConfig) (mem.AnswerProvider, error) {
		return mem.NewOllamaAnswerProvider(cfg)
	}
	importDocument = mem.ImportDocument
)

const memDirName = mem.MemDirName

// cmdRequiresDB — команды, для работы которых нужна локальная база .mem/
var cmdRequiresDB = map[string]bool{
	"add": true, "add-file": true, "import": true, "index": true,
	"config": true,
	"search": true, "ask": true, "map": true, "recent": true, "stats": true,
	"source": true, "sources": true,
	"show": true, "get": true, "view": true,
	"delete": true, "rm": true,
	"edit": true, "retag": true,
	"important": true, "imp": true,
	"repl":  true,
	"where": true, "current": true,
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
			return runTuiSession()
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
	case "open":
		return handleOpenDatabase(args)
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
	case "ask":
		if err := handleAsk(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "map":
		if err := handleMap(cfg, store, args); err != nil {
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
	case "where", "current":
		handleWhere(store)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", cmd)
		printUsage()
		return 1
	}
	return 0
}

func databasePathArg(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("укажи каталог проекта или путь к .mem\nПример: mem open \"D:\\Knowledge\\ProjectA\"")
	}
	path := strings.TrimSpace(strings.Join(args, " "))
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		path = path[1 : len(path)-1]
	}
	if path == "" {
		return "", fmt.Errorf("путь к базе пуст")
	}
	return path, nil
}

func resolveDatabaseRootArg(args []string) (string, error) {
	path, err := databasePathArg(args)
	if err != nil {
		return "", err
	}
	root, err := mem.ResolveDatabaseRoot(path)
	if err != nil {
		return "", err
	}
	probe, err := newStore(filepath.Join(root, memDirName))
	if err != nil {
		return "", fmt.Errorf("локальная база %s не открывается: %w", filepath.Join(root, memDirName), err)
	}
	if err := probe.Close(); err != nil {
		return "", fmt.Errorf("проверка закрытия локальной базы %s: %w", probe.Path(), err)
	}
	return root, nil
}

func handleOpenDatabase(args []string) int {
	root, err := resolveDatabaseRootArg(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		return 1
	}
	if err := os.Chdir(root); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка перехода в %s: %v\n", root, err)
		return 1
	}
	return runTuiSession()
}

// runTuiSession owns the currently opened Store. A /open command returns a new
// project root; the old SQLite connection is closed before changing cwd and
// opening the selected local database.
func runTuiSession() int {
	for {
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
		tuiResult, tuiErr := runTui(cfg, store)
		if tuiErr == nil && tuiResult.replRequested {
			runRepl(cfg, store)
		}
		closeErr := store.Close()
		if tuiErr != nil {
			fmt.Fprintf(os.Stderr, "Ошибка TUI: %v\n", tuiErr)
			return 1
		}
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "Ошибка закрытия базы %s: %v\n", store.Path(), closeErr)
			return 1
		}
		if tuiResult.replRequested || tuiResult.openPath == "" {
			return 0
		}
		root, err := mem.ResolveDatabaseRoot(tuiResult.openPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка открытия базы: %v\n", err)
			return 1
		}
		if err := os.Chdir(root); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка перехода в %s: %v\n", root, err)
			return 1
		}
	}
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
	fmt.Printf("mem-tool v%s\n", buildinfo.Version)
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
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Printf(">> Эмбеддинг через %s... ", cfg.Backend)

	embedding, err := getEmbedding(cfg, text)
	if err != nil {
		return fmt.Errorf("ошибка эмбеддинга: %w", err)
	}
	fmt.Printf("получен вектор %d измерений\n", len(embedding))

	entry, err := store.AddWithEmbeddingIdentity(text, title, tags, embeddingIdentity, embedding, important)
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
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Printf(">> Поиск через %s... ", cfg.Backend)

	queryVec, err := getEmbedding(cfg, query)
	if err != nil {
		return fmt.Errorf("ошибка эмбеддинга: %w", err)
	}
	fmt.Printf("вектор %d измерений\n", len(queryVec))

	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: query, QueryVector: queryVec, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
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

func handleAsk(cfg *Config, store *Store, args []string) error {
	searchArgs, contextOverride, err := parseAskArgs(args)
	if err != nil {
		return err
	}
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(searchArgs)
	if len(positional) == 0 {
		return fmt.Errorf("укажи вопрос\nПример: mem ask \"где описан порядок запуска?\" -limit 5")
	}
	question := strings.Join(positional, " ")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	answerCfg := cfg.Answer.WithDefaults()
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "[ASK] retrieval: строю embedding и ищу evidence...")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := mem.AnswerContext(ctx, answerCfg)
	defer cancel()
	queryVector, err := getEmbeddingContext(ctx, cfg, question)
	if err != nil {
		return fmt.Errorf("ask retrieval: %w", err)
	}
	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: question, QueryVector: queryVector, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
		Tags: tags, TagFilter: tagFilter, From: from, To: to, VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("ask retrieval: %w", err)
	}
	if len(results) > 0 {
		reRankResults(results, question)
		sortSearchResults(results)
	}
	if minScore > 0 {
		filtered := results[:0]
		for _, result := range results {
			if result.Score >= minScore {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]
	if len(results) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: поиск не вернул подходящих фрагментов.")
		return nil
	}

	contextBudget := answerCfg.ContextChars
	if contextOverride > 0 {
		contextBudget = contextOverride
	}
	prompt, err := mem.BuildGroundedPromptWithOptions(question, results, contextBudget, cfg.Ingest.LowConfidence)
	if err != nil {
		return err
	}
	if len(prompt.Evidence) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: evidence не помещается в заданный context budget.")
		return nil
	}
	for _, evidence := range prompt.Evidence {
		for _, warning := range evidence.Warnings {
			fmt.Fprintf(os.Stderr, "[ASK] warning %s: %s\n", evidence.CitationID, warning)
		}
	}
	fmt.Fprintf(os.Stderr, "[ASK] evidence: %d фрагм.; generation через %s...\n", len(prompt.Evidence), answerCfg.Model)
	rawAnswer, err := provider.Generate(ctx, mem.AnswerRequest{
		Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
		MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
	})
	if err != nil {
		return fmt.Errorf("ask generation: %w", err)
	}
	validated := mem.ValidateGroundedAnswer(rawAnswer, prompt.Evidence)
	if validated.Rejected {
		if len(validated.UnknownIDs) > 0 {
			return fmt.Errorf("grounded answer rejected: %s (%s)", validated.Reason, strings.Join(validated.UnknownIDs, ", "))
		}
		return fmt.Errorf("grounded answer rejected: %s", validated.Reason)
	}
	fmt.Fprintln(os.Stdout, validated.Answer)
	if len(validated.Used) > 0 {
		printGroundedSources(os.Stdout, validated.Used)
	}
	return nil
}

func printGroundedSources(w io.Writer, evidence []mem.GroundedEvidence) {
	fmt.Fprintln(w, "\nИсточники:")
	for i, item := range evidence {
		number := i + 1
		sourcePath := strings.TrimSpace(item.SourcePath)
		name := filepath.Base(sourcePath)
		if sourcePath == "" || name == "." {
			name = strings.TrimSpace(item.CitationLabel)
		}
		if strings.HasPrefix(item.CitationID, "entry-") {
			name = "Запись базы #" + strings.TrimPrefix(item.CitationID, "entry-")
		}
		fmt.Fprintf(w, "[%d] %s\n", number, name)
		if sourcePath != "" {
			fmt.Fprintf(w, "    Файл: %s\n", sourcePath)
		}

		location := make([]string, 0, 4)
		if item.Page > 0 {
			location = append(location, fmt.Sprintf("страница %d", item.Page))
		}
		if item.DocumentID != "" || item.Page > 0 || item.BlockMarker != "" || item.BlockTotalChunks > 0 {
			location = append(location, fmt.Sprintf("блок %d", item.BlockIndex+1))
		}
		if item.BlockChunk != "" {
			location = append(location, "фрагмент "+item.BlockChunk)
		} else if item.Chunk != "" {
			location = append(location, "фрагмент "+item.Chunk)
		}
		marker := humanSectionMarker(item.BlockMarker)
		if marker != "" {
			location = append(location, "раздел "+marker)
		}
		if len(location) > 0 {
			fmt.Fprintln(w, "    Место: "+strings.Join(location, " · "))
		} else {
			fmt.Fprintln(w, "    Место: номер страницы отсутствует")
		}
	}
}

func humanSectionMarker(marker string) string {
	marker = strings.TrimSpace(marker)
	normalized := strings.ToLower(marker)
	if strings.HasPrefix(normalized, "page:") || strings.HasPrefix(normalized, "page ") || strings.HasPrefix(normalized, "<!-- page:") {
		return ""
	}
	return marker
}

func parseAskArgs(args []string) ([]string, int, error) {
	contextBudget := 0
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "-context-chars" {
			out = append(out, args[i])
			continue
		}
		if i+1 >= len(args) {
			return nil, 0, errors.New("использование: -context-chars <положительное число>")
		}
		i++
		n, parseErr := strconv.Atoi(args[i])
		if parseErr != nil || n <= 0 || n > mem.MaxAnswerContextChars {
			return nil, 0, fmt.Errorf("-context-chars должен быть от 1 до %d", mem.MaxAnswerContextChars)
		}
		contextBudget = n
	}
	return out, contextBudget, nil
}

func handleMap(cfg *Config, store *Store, args []string) error {
	if len(args) == 0 {
		return errors.New("использование: mem map <open|build|analyze|duplicates|merge-node|merges|runs|run|prune-runs|status|approve|approve-batch|reviews|export|export-html>\n  mem map open [--port N] [--title <текст>] [--no-browser]\n  mem map build <фокус> [-limit N] [-context-chars N]\n  mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]\n  mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]\n  mem map merge-node <manifest.json>\n  mem map merges [--json] [-limit N]\n  mem map runs [--json] [-limit N] [-status running|completed]\n  mem map run <run-id> [--json]\n  mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]\n  mem map status [--json]\n  mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>]\n  mem map approve-batch <manifest.json>\n  mem map reviews [--json] [-limit N]\n  mem map export\n  mem map export-html <output.html> [--title <текст>] [--force]")
	}
	switch args[0] {
	case "open":
		return handleMapOpen(store, args[1:])
	case "export":
		if len(args) != 1 {
			return errors.New("использование: mem map export")
		}
		graph, err := store.LoadKnowledgeGraph()
		if err != nil {
			return fmt.Errorf("map export: %w", err)
		}
		encoded, err := json.MarshalIndent(graph, "", "  ")
		if err != nil {
			return fmt.Errorf("map export: encode graph: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	case "export-html":
		return handleMapExportHTML(store, args[1:])
	case "status":
		return handleMapStatus(store, args[1:])
	case "approve":
		return handleMapApprove(store, args[1:])
	case "approve-batch":
		return handleMapApproveBatch(store, args[1:])
	case "reviews":
		return handleMapReviews(store, args[1:])
	case "duplicates":
		return handleMapDuplicates(cfg, store, args[1:])
	case "merge-node":
		return handleMapMergeNode(store, args[1:])
	case "merges":
		return handleMapMerges(store, args[1:])
	case "runs":
		return handleMapRuns(store, args[1:])
	case "run":
		return handleMapRun(store, args[1:])
	case "prune-runs":
		return handleMapPruneRuns(store, args[1:])
	case "analyze":
		return handleMapAnalyze(cfg, store, args[1:])
	case "build":
		return handleMapBuild(cfg, store, args[1:])
	default:
		return fmt.Errorf("неизвестная подкоманда map: %s (доступны open, build, analyze, duplicates, merge-node, merges, runs, run, prune-runs, status, approve, approve-batch, reviews, export, export-html)", args[0])
	}
}

func handleMapExportHTML(store *Store, args []string) error {
	if len(args) == 0 {
		return errors.New("использование: mem map export-html <output.html> [--title <текст>] [--force]")
	}
	outputPath := strings.TrimSpace(args[0])
	if outputPath == "" || strings.HasPrefix(outputPath, "-") {
		return errors.New("map export-html: укажи путь к output.html первым аргументом")
	}
	title, force := "", false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		case "--title":
			if i+1 >= len(args) {
				return errors.New("использование: mem map export-html <output.html> [--title <текст>] [--force]")
			}
			i++
			title = args[i]
		default:
			return fmt.Errorf("неизвестный аргумент map export-html: %s", args[i])
		}
	}
	if !strings.EqualFold(filepath.Ext(outputPath), ".html") && !strings.EqualFold(filepath.Ext(outputPath), ".htm") {
		return errors.New("map export-html: выходной файл должен иметь расширение .html или .htm")
	}
	if info, err := os.Stat(outputPath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("map export-html: %s является папкой", outputPath)
		}
		if !force {
			return fmt.Errorf("map export-html: файл %s уже существует; используй --force для перезаписи", outputPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("map export-html: inspect output: %w", err)
	}
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		return fmt.Errorf("map export-html: %w", err)
	}
	var output bytes.Buffer
	if err := mem.WriteKnowledgeMapHTML(&output, title, data); err != nil {
		return fmt.Errorf("map export-html: %w", err)
	}
	if err := os.WriteFile(outputPath, output.Bytes(), 0o600); err != nil {
		return fmt.Errorf("map export-html: write output: %w", err)
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		absolute = outputPath
	}
	fmt.Fprintf(os.Stdout, "Интерактивная карта сохранена: %s (nodes=%d edges=%d merges=%d)\n",
		absolute, len(data.Graph.Nodes), len(data.Graph.Edges), len(data.Merges))
	return nil
}

func handleMapStatus(store *Store, args []string) error {
	jsonOutput := false
	if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
		return errors.New("использование: mem map status [--json]")
	}
	if len(args) == 1 {
		jsonOutput = true
	}
	report, err := store.ReviewKnowledgeGraph()
	if err != nil {
		return fmt.Errorf("map status: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map status: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Карта: total=%d draft=%d active=%d resolved=%d ready=%d\n",
		report.Summary.Total, report.Summary.Draft, report.Summary.Active, report.Summary.Resolved, report.Summary.Ready)
	fmt.Fprintf(os.Stdout, "Evidence: current=%d stale=%d missing=%d\n",
		report.Summary.CurrentEvidence, report.Summary.StaleEvidence, report.Summary.MissingEvidence)
	for _, item := range report.Items {
		ready := ""
		if item.ReadyForApproval {
			ready = " ready"
		}
		label := strings.Join(strings.Fields(item.Label), " ")
		fmt.Fprintf(os.Stdout, "- %s %s [%s/%s evidence=%s%s] %s\n",
			item.ObjectType, item.ID, item.Kind, item.Status, item.EvidenceState, ready, label)
		fmt.Fprintf(os.Stdout, "  digest %s\n", item.EvidenceDigest)
		for _, evidence := range item.Evidence {
			fmt.Fprintf(os.Stdout, "  %s %s\n", evidence.State, evidence.Anchor.CitationID)
		}
	}
	return nil
}

func handleMapApprove(store *Store, args []string) error {
	if len(args) < 4 {
		return errors.New("использование: mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>]")
	}
	objectType := mem.KnowledgeObjectType(args[0])
	if objectType != mem.KnowledgeObjectNode && objectType != mem.KnowledgeObjectEdge {
		return errors.New("тип объекта должен быть node или edge")
	}
	request := mem.KnowledgeApprovalRequest{ObjectType: objectType, ID: args[1]}
	for i := 2; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("для %s требуется значение", args[i])
		}
		value := args[i+1]
		switch args[i] {
		case "--reviewer":
			request.Reviewer = value
		case "--comment":
			request.Comment = value
		case "--evidence-digest":
			request.ExpectedEvidenceDigest = value
		default:
			return fmt.Errorf("неизвестный флаг map approve: %s", args[i])
		}
		i++
	}
	approved, err := store.ApproveKnowledgeObjectWithReview(request)
	if err != nil {
		return fmt.Errorf("map approve: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Подтверждено: %s %s %s->%s evidence=%d review=%d digest=%s\n",
		approved.ObjectType, approved.ID, approved.PreviousStatus, approved.Status,
		len(approved.Evidence), approved.Review.ID, approved.EvidenceDigest)
	return nil
}

func handleMapApproveBatch(store *Store, args []string) error {
	if len(args) != 1 {
		return errors.New("использование: mem map approve-batch <manifest.json>")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("map approve-batch: read manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("map approve-batch: read manifest: %w", err)
	}
	if len(data) > 1<<20 {
		return errors.New("map approve-batch: manifest exceeds 1 MiB")
	}
	var manifest mem.KnowledgeApprovalManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("map approve-batch: decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("map approve-batch: decode manifest: %w", err)
	}
	requests := make([]mem.KnowledgeApprovalRequest, 0, len(manifest.Objects))
	for _, object := range manifest.Objects {
		comment := object.Comment
		if comment == "" {
			comment = manifest.Comment
		}
		requests = append(requests, mem.KnowledgeApprovalRequest{
			ObjectType: object.ObjectType, ID: object.ID, Reviewer: manifest.Reviewer,
			Comment: comment, ExpectedEvidenceDigest: object.ExpectedEvidenceDigest,
		})
	}
	result, err := store.ApproveKnowledgeObjects(requests)
	if err != nil {
		return fmt.Errorf("map approve-batch: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Пакет подтверждён: objects=%d reviewer=%s\n", len(result.Approved), strings.TrimSpace(manifest.Reviewer))
	for _, approved := range result.Approved {
		fmt.Fprintf(os.Stdout, "- %s %s review=%d digest=%s\n", approved.ObjectType, approved.ID, approved.Review.ID, approved.EvidenceDigest)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("expected one JSON object")
		}
		return err
	}
	return nil
}

func handleMapReviews(store *Store, args []string) error {
	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map reviews [--json] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map reviews: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map reviews: %s", args[i])
		}
	}
	reviews, err := store.ListKnowledgeReviews(limit)
	if err != nil {
		return fmt.Errorf("map reviews: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(reviews, "", "  ")
		if err != nil {
			return fmt.Errorf("map reviews: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Review history: records=%d limit=%d\n", len(reviews), limit)
	for _, review := range reviews {
		fmt.Fprintf(os.Stdout, "- #%d %s %s %s->%s reviewer=%s created=%s\n",
			review.ID, review.ObjectType, review.ObjectID, review.PreviousStatus,
			review.NewStatus, review.Reviewer, review.Created)
		if review.Comment != "" {
			fmt.Fprintf(os.Stdout, "  comment %s\n", strings.Join(strings.Fields(review.Comment), " "))
		}
		fmt.Fprintf(os.Stdout, "  digest %s\n", review.EvidenceDigest)
	}
	return nil
}

func handleMapDuplicates(cfg *Config, store *Store, args []string) error {
	jsonOutput := false
	threshold := mem.DefaultKnowledgeDuplicateThreshold
	limit, nodeLimit := 100, 200
	var kind mem.KnowledgeNodeKind
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-threshold":
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			parsed, err := strconv.ParseFloat(args[i], 64)
			if err != nil || parsed < 0 || parsed > 1 {
				return errors.New("map duplicates: -threshold должен быть от 0 до 1")
			}
			threshold = parsed
		case "-limit", "-nodes":
			flag := args[i]
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return fmt.Errorf("map duplicates: %s должен быть от 1 до 1000", flag)
			}
			if flag == "-limit" {
				limit = parsed
			} else {
				nodeLimit = parsed
			}
		case "-kind":
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			kind = mem.KnowledgeNodeKind(strings.TrimSpace(args[i]))
			if !cliKnowledgeNodeKind(kind) {
				return fmt.Errorf("map duplicates: неподдерживаемый kind %q", kind)
			}
		default:
			return fmt.Errorf("неизвестный аргумент map duplicates: %s", args[i])
		}
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	type pendingNode struct {
		node   mem.KnowledgeNode
		digest string
	}
	pending := make([]pendingNode, 0)
	for _, node := range graph.Nodes {
		if node.Status == mem.KnowledgeStatusResolved || (kind != "" && node.Kind != kind) {
			continue
		}
		current := true
		for _, anchor := range node.Evidence {
			if store.ResolveEvidenceAnchor(anchor).State != mem.EvidenceCurrent {
				current = false
				break
			}
		}
		if !current {
			continue
		}
		digest, err := mem.KnowledgeNodeContentDigest(node)
		if err != nil {
			return fmt.Errorf("map duplicates: digest node %s: %w", node.ID, err)
		}
		pending = append(pending, pendingNode{node: node, digest: digest})
		if len(pending) == nodeLimit {
			break
		}
	}
	identity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	vectors := make([]mem.KnowledgeNodeVector, 0, len(pending))
	if len(pending) >= 2 {
		rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := mem.AnswerContext(rootCtx, cfg.Answer.WithDefaults())
		defer cancel()
		for i, item := range pending {
			text := strings.TrimSpace(item.node.Label)
			if body := strings.TrimSpace(item.node.Body); body != "" {
				text += "\n\n" + body
			}
			fmt.Fprintf(os.Stderr, "[MAP DUPLICATES] embedding node=%d/%d id=%s\n", i+1, len(pending), item.node.ID)
			vector, err := getEmbeddingContext(ctx, cfg, text)
			if err != nil {
				return fmt.Errorf("map duplicates: embed node %s: %w", item.node.ID, err)
			}
			vectors = append(vectors, mem.KnowledgeNodeVector{NodeID: item.node.ID, NodeDigest: item.digest, Embedding: vector})
		}
	}
	report, err := store.DetectKnowledgeNodeDuplicates(vectors, identity.SpaceID, threshold, limit, kind)
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map duplicates: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Duplicate candidates: pairs=%d threshold=%.4f eligible=%d scanned=%d embedded=%d\n",
		len(report.Candidates), report.Threshold, report.EligibleNodes, report.ScannedNodes, len(vectors))
	fmt.Fprintf(os.Stdout, "Skipped: resolved=%d non_current=%d changed=%d no_vector=%d\n",
		report.SkippedResolved, report.SkippedNonCurrent, report.SkippedChanged, report.SkippedNoVector)
	for _, candidate := range report.Candidates {
		fmt.Fprintf(os.Stdout, "- similarity=%.6f kind=%s %s <> %s\n",
			candidate.Similarity, candidate.Left.Kind, candidate.Left.ID, candidate.Right.ID)
		fmt.Fprintf(os.Stdout, "  %s <> %s\n", candidate.Left.Label, candidate.Right.Label)
		if candidate.SuggestedSource != "" {
			fmt.Fprintf(os.Stdout, "  merge suggestion: source=%s target=%s\n", candidate.SuggestedSource, candidate.SuggestedTarget)
		}
		fmt.Fprintf(os.Stdout, "  left_node=%s left_evidence=%s\n", candidate.Left.NodeDigest, candidate.Left.EvidenceDigest)
		fmt.Fprintf(os.Stdout, "  right_node=%s right_evidence=%s\n", candidate.Right.NodeDigest, candidate.Right.EvidenceDigest)
	}
	return nil
}

func cliKnowledgeNodeKind(kind mem.KnowledgeNodeKind) bool {
	switch kind {
	case mem.KnowledgeNodeDocument, mem.KnowledgeNodeTopic, mem.KnowledgeNodeClaim, mem.KnowledgeNodeNote,
		mem.KnowledgeNodeQuestion, mem.KnowledgeNodeCard, mem.KnowledgeNodeContradiction, mem.KnowledgeNodeGap:
		return true
	default:
		return false
	}
}

func handleMapMergeNode(store *Store, args []string) error {
	if len(args) != 1 {
		return errors.New("использование: mem map merge-node <manifest.json>")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("map merge-node: open manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var request mem.KnowledgeNodeMergeRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("map merge-node: decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("map merge-node: decode manifest: %w", err)
	}
	result, err := store.MergeKnowledgeDuplicate(request)
	if err != nil {
		return fmt.Errorf("map merge-node: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Duplicate node merged: source=%s target=%s kind=%s similarity=%.6f resolved_edges=%d merge=%d review=%d\n",
		result.Merge.SourceID, result.Merge.TargetID, result.Merge.Kind, result.Merge.Similarity,
		result.ResolvedEdges, result.Merge.ID, result.Review.ID)
	return nil
}

func handleMapMerges(store *Store, args []string) error {
	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map merges [--json] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map merges: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map merges: %s", args[i])
		}
	}
	records, err := store.ListKnowledgeNodeMerges(limit)
	if err != nil {
		return fmt.Errorf("map merges: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return fmt.Errorf("map merges: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Node merge history: records=%d limit=%d\n", len(records), limit)
	for _, record := range records {
		state := "stale"
		if record.Current {
			state = "current"
		}
		fmt.Fprintf(os.Stdout, "- #%d [%s] %s -> %s kind=%s similarity=%.6f reviewer=%s created=%s\n",
			record.ID, state, record.SourceID, record.TargetID, record.Kind, record.Similarity, record.Reviewer, record.Created)
		if record.StateReason != "" {
			fmt.Fprintf(os.Stdout, "  reason %s\n", record.StateReason)
		}
		if record.Comment != "" {
			fmt.Fprintf(os.Stdout, "  comment %s\n", strings.Join(strings.Fields(record.Comment), " "))
		}
	}
	return nil
}

func handleMapRuns(store *Store, args []string) error {
	jsonOutput, limit := false, 100
	var status mem.CorpusAnalysisRunStatus
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map runs [--json] [-limit N] [-status running|completed]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map runs: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		case "-status":
			if i+1 >= len(args) {
				return errors.New("использование: mem map runs [--json] [-limit N] [-status running|completed]")
			}
			i++
			status = mem.CorpusAnalysisRunStatus(strings.TrimSpace(args[i]))
			if status != mem.CorpusAnalysisRunRunning && status != mem.CorpusAnalysisRunCompleted {
				return errors.New("map runs: -status должен быть running или completed")
			}
		default:
			return fmt.Errorf("неизвестный аргумент map runs: %s", args[i])
		}
	}
	runs, err := store.ListCorpusAnalysisRuns(limit, status)
	if err != nil {
		return fmt.Errorf("map runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(runs, "", "  ")
		if err != nil {
			return fmt.Errorf("map runs: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis runs: records=%d limit=%d", len(runs), limit)
	if status != "" {
		fmt.Fprintf(os.Stdout, " status=%s", status)
	}
	fmt.Fprintln(os.Stdout)
	for _, run := range runs {
		fmt.Fprintf(os.Stdout, "- %s [%s] batches=%d completed=%d insufficient=%d failed=%d pending=%d updated=%s\n",
			run.ID, run.Status, run.BatchCount, run.CompletedBatches, run.InsufficientBatches,
			run.FailedBatches, run.PendingBatches, run.Updated)
		fmt.Fprintf(os.Stdout, "  focus %s\n", strings.Join(strings.Fields(run.Focus), " "))
		fmt.Fprintf(os.Stdout, "  coverage claims=%d/%d documents=%d/%d context=%d max_batches=%d\n",
			run.CoveredClaims, run.EligibleClaims, run.CoveredDocuments, run.EligibleDocuments,
			run.ContextChars, run.MaxBatches)
	}
	return nil
}

func handleMapRun(store *Store, args []string) error {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return errors.New("использование: mem map run <run-id> [--json]")
	}
	run, err := store.LoadCorpusAnalysisRun(args[0])
	if err != nil {
		return fmt.Errorf("map run: %w", err)
	}
	if len(args) == 2 {
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return fmt.Errorf("map run: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis run %s [%s]\n", run.ID, run.Status)
	fmt.Fprintf(os.Stdout, "Focus: %s\n", strings.Join(strings.Fields(run.Focus), " "))
	fmt.Fprintf(os.Stdout, "Coverage: claims=%d/%d documents=%d/%d context=%d max_batches=%d\n",
		run.CoveredClaims, run.EligibleClaims, run.CoveredDocuments, run.EligibleDocuments,
		run.ContextChars, run.MaxBatches)
	fmt.Fprintf(os.Stdout, "Created: %s\nUpdated: %s\n", run.Created, run.Updated)
	for _, batch := range run.Batches {
		fmt.Fprintf(os.Stdout, "- batch %d %s [%s] findings=%d relations=%d updated=%s\n",
			batch.Ordinal+1, batch.BatchID, batch.Status, len(batch.Graph.Nodes), len(batch.Graph.Edges), batch.Updated)
		if batch.Reason != "" {
			fmt.Fprintf(os.Stdout, "  reason %s\n", strings.Join(strings.Fields(batch.Reason), " "))
		}
	}
	return nil
}

func parseAnalysisRunRetention(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days < 1 || days > 36500 {
			return 0, errors.New("число дней должно быть от 1 до 36500")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("ожидается положительная duration, например 720h или 30d")
	}
	return duration, nil
}

func handleMapPruneRuns(store *Store, args []string) error {
	var olderThan time.Duration
	keepLatest := 20
	dryRun, yes, jsonOutput := false, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-older-than":
			if i+1 >= len(args) {
				return errors.New("использование: mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]")
			}
			i++
			parsed, err := parseAnalysisRunRetention(args[i])
			if err != nil {
				return fmt.Errorf("map prune-runs: -older-than: %w", err)
			}
			olderThan = parsed
		case "-keep":
			if i+1 >= len(args) {
				return errors.New("использование: mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 0 || parsed > 10000 {
				return errors.New("map prune-runs: -keep должен быть от 0 до 10000")
			}
			keepLatest = parsed
		case "--dry-run":
			dryRun = true
		case "--yes":
			yes = true
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("неизвестный аргумент map prune-runs: %s", args[i])
		}
	}
	if olderThan == 0 {
		return errors.New("map prune-runs: обязателен -older-than, например 30d")
	}
	if dryRun && yes {
		return errors.New("map prune-runs: --dry-run и --yes несовместимы")
	}
	preview := !yes
	result, err := store.PruneCompletedCorpusAnalysisRuns(time.Now().UTC().Add(-olderThan), keepLatest, preview)
	if err != nil {
		return fmt.Errorf("map prune-runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("map prune-runs: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	if preview {
		fmt.Fprintf(os.Stdout, "Analysis run cleanup preview: candidates=%d completed_before=%s keep_latest=%d\n",
			len(result.Runs), result.CompletedBefore, result.KeepLatest)
		for _, run := range result.Runs {
			fmt.Fprintf(os.Stdout, "- %s [completed] batches=%d updated=%s focus=%s\n",
				run.ID, run.BatchCount, run.Updated, strings.Join(strings.Fields(run.Focus), " "))
		}
		fmt.Fprintln(os.Stdout, "Изменений нет. Для удаления повтори команду с --yes.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis run cleanup complete: runs=%d batches=%d completed_before=%s keep_latest=%d\n",
		result.DeletedRuns, result.DeletedBatches, result.CompletedBefore, result.KeepLatest)
	return nil
}

func handleMapAnalyze(cfg *Config, store *Store, args []string) error {
	focusParts := make([]string, 0, len(args))
	contextBudget := cfg.Answer.WithDefaults().ContextChars
	maxBatches := 1
	resumeID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-context-chars":
			if i+1 >= len(args) {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxAnswerContextChars {
				return fmt.Errorf("-context-chars должен быть от 1 до %d", mem.MaxAnswerContextChars)
			}
			contextBudget = parsed
		case "-batches":
			if i+1 >= len(args) {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxCorpusAnalysisBatches {
				return fmt.Errorf("-batches должен быть от 1 до %d", mem.MaxCorpusAnalysisBatches)
			}
			maxBatches = parsed
		case "-resume":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			if resumeID != "" {
				return errors.New("-resume указан более одного раза")
			}
			i++
			resumeID = strings.TrimSpace(args[i])
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("неизвестный аргумент map analyze: %s", args[i])
			}
			focusParts = append(focusParts, args[i])
		}
	}
	if len(focusParts) == 0 {
		return errors.New("укажи фокус междокументного анализа\nПример: mem map analyze \"требования к давлению\"")
	}
	focus := strings.Join(focusParts, " ")
	plan, err := store.BuildCorpusAnalysisPlan(focus, contextBudget, maxBatches)
	if err != nil {
		return fmt.Errorf("map analyze: %w", err)
	}
	if plan.SkippedNonCurrent > 0 {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] пропущено active claims с stale/missing evidence: %d\n", plan.SkippedNonCurrent)
	}
	if len(plan.Batches) == 0 {
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: нужны минимум два active claim с current evidence из разных документов, помещающиеся в context budget (eligible=%d documents=%d).\n",
			plan.EligibleClaims, plan.EligibleDocuments)
		return nil
	}
	if plan.UncoveredClaims > 0 {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] покрытие ограничено: covered=%d eligible=%d uncovered=%d; увеличь -batches или -context-chars\n",
			plan.CoveredClaims, plan.EligibleClaims, plan.UncoveredClaims)
	}
	fmt.Fprintf(os.Stderr, "[MAP ANALYZE] semantic vectors=%d/%d guided_batches=%d fallback_batches=%d\n",
		plan.SemanticClaims, plan.EligibleClaims, plan.SemanticBatches, plan.FallbackBatches)
	configuredAnswerCfg := cfg.Answer.WithDefaults()
	answerCfg := cfg.Answer.WithMapGenerationDefaults()
	if answerCfg.MaxTokens != configuredAnswerCfg.MaxTokens {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] output budget: %d tokens (answer.max_tokens=%d; повышен для структурированного JSON)\n",
			answerCfg.MaxTokens, configuredAnswerCfg.MaxTokens)
	}
	run, err := store.PrepareCorpusAnalysisRun(focus, contextBudget, maxBatches, plan, answerCfg, resumeID)
	if err != nil {
		return fmt.Errorf("map analyze run: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[MAP ANALYZE] run=%s status=%s batches=%d; повтор той же команды безопасно продолжит запуск\n",
		run.ID, run.Status, len(run.Batches))
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	graphs := make([]mem.KnowledgeGraph, 0, len(plan.Batches))
	insufficientBatches := 0
	for i, prompt := range plan.Batches {
		stored := run.Batches[i]
		switch stored.Status {
		case mem.CorpusAnalysisBatchCompleted:
			graphs = append(graphs, stored.Graph)
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: восстановлен проверенный результат\n",
				i+1, len(plan.Batches), prompt.BatchID)
			continue
		case mem.CorpusAnalysisBatchInsufficient:
			insufficientBatches++
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: восстановлен insufficient result (%s)\n",
				i+1, len(plan.Batches), prompt.BatchID, stored.Reason)
			continue
		}
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s claims=%d documents=%d; analysis через %s...\n",
			i+1, len(plan.Batches), prompt.BatchID, len(prompt.Claims), prompt.DocumentCount, answerCfg.Model)
		ctx, cancel := mem.AnswerContext(rootCtx, answerCfg)
		raw, generateErr := provider.Generate(ctx, mem.AnswerRequest{
			Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
			MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
		})
		cancel()
		if generateErr != nil {
			if saveErr := store.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, generateErr); saveErr != nil {
				return fmt.Errorf("map analyze batch %d (%s), run=%s: %v; save failure state: %w", i+1, prompt.BatchID, run.ID, generateErr, saveErr)
			}
			return fmt.Errorf("map analyze batch %d (%s), run=%s: %w", i+1, prompt.BatchID, run.ID, generateErr)
		}
		analyzed, decodeErr := mem.DecodeCorpusAnalysis(raw, prompt.Claims)
		if decodeErr != nil {
			if saveErr := store.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, decodeErr); saveErr != nil {
				return fmt.Errorf("map analyze batch %d (%s) rejected, run=%s: %v; save failure state: %w", i+1, prompt.BatchID, run.ID, decodeErr, saveErr)
			}
			return fmt.Errorf("map analyze batch %d (%s) rejected, run=%s: %w", i+1, prompt.BatchID, run.ID, decodeErr)
		}
		if analyzed.Insufficient {
			if err := store.SaveCorpusAnalysisBatchInsufficient(run.ID, prompt.BatchID, analyzed.Reason); err != nil {
				return fmt.Errorf("map analyze batch %d (%s) checkpoint, run=%s: %w", i+1, prompt.BatchID, run.ID, err)
			}
			insufficientBatches++
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: insufficient evidence (%s)\n",
				i+1, len(plan.Batches), prompt.BatchID, analyzed.Reason)
			continue
		}
		if err := store.SaveCorpusAnalysisBatchGraph(run.ID, prompt.BatchID, analyzed.Graph); err != nil {
			return fmt.Errorf("map analyze batch %d (%s) checkpoint, run=%s: %w", i+1, prompt.BatchID, run.ID, err)
		}
		graphs = append(graphs, analyzed.Graph)
	}
	merged, err := mem.MergeCorpusAnalysisGraphs(graphs...)
	if err != nil {
		return fmt.Errorf("map analyze merge: %w", err)
	}
	if len(merged.Nodes) == 0 {
		if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
			return fmt.Errorf("map analyze complete run %s: %w", run.ID, err)
		}
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: все пакеты завершились без findings (run=%s batches=%d covered=%d/%d semantic=%d fallback=%d).\n",
			run.ID, insufficientBatches, plan.CoveredClaims, plan.EligibleClaims, plan.SemanticBatches, plan.FallbackBatches)
		return nil
	}
	if err := store.UpsertCorpusAnalysisGraph(merged); err != nil {
		return fmt.Errorf("map analyze persistence: %w", err)
	}
	if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
		return fmt.Errorf("map analyze complete run %s: %w", run.ID, err)
	}
	fmt.Fprintf(os.Stdout, "Междокументный анализ сохранён как draft: run=%s findings=%d relations=%d batches=%d insufficient=%d covered=%d/%d documents=%d/%d semantic=%d fallback=%d\n",
		run.ID, len(merged.Nodes), len(merged.Edges), len(plan.Batches), insufficientBatches,
		plan.CoveredClaims, plan.EligibleClaims, plan.CoveredDocuments, plan.EligibleDocuments,
		plan.SemanticBatches, plan.FallbackBatches)
	return nil
}

func handleMapBuild(cfg *Config, store *Store, args []string) error {
	searchArgs, contextOverride, err := parseAskArgs(args)
	if err != nil {
		return err
	}
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(searchArgs)
	if len(positional) == 0 {
		return errors.New("укажи фокус карты\nПример: mem map build \"архитектура импорта\" -limit 10")
	}
	focus := strings.Join(positional, " ")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	configuredAnswerCfg := cfg.Answer.WithDefaults()
	answerCfg := cfg.Answer.WithMapGenerationDefaults()

	fmt.Fprintln(os.Stderr, "[MAP] retrieval: строю embedding и ищу versioned evidence...")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := mem.AnswerContext(ctx, answerCfg)
	defer cancel()
	queryVector, err := getEmbeddingContext(ctx, cfg, focus)
	if err != nil {
		return fmt.Errorf("map retrieval: %w", err)
	}
	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: focus, QueryVector: queryVector, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
		Tags: tags, TagFilter: tagFilter, From: from, To: to, VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("map retrieval: %w", err)
	}
	if len(results) > 0 {
		reRankResults(results, focus)
		sortSearchResults(results)
	}
	if minScore > 0 {
		filtered := results[:0]
		for _, result := range results {
			if result.Score >= minScore {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]
	if len(results) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: поиск не вернул подходящих фрагментов.")
		return nil
	}

	contextBudget := answerCfg.ContextChars
	if contextOverride > 0 {
		contextBudget = contextOverride
	}
	prompt, err := mem.BuildKnowledgeExtractionPrompt(focus, results, contextBudget, cfg.Ingest.LowConfidence)
	if err != nil {
		return err
	}
	if len(prompt.Evidence) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: среди результатов нет versioned document evidence, помещающегося в context budget.")
		return nil
	}
	for _, evidence := range prompt.Evidence {
		for _, warning := range evidence.Warnings {
			fmt.Fprintf(os.Stderr, "[MAP] warning %s: %s\n", evidence.CitationID, warning)
		}
	}
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	if answerCfg.MaxTokens != configuredAnswerCfg.MaxTokens {
		fmt.Fprintf(os.Stderr, "[MAP] output budget: %d tokens (answer.max_tokens=%d; повышен для структурированного JSON)\n",
			answerCfg.MaxTokens, configuredAnswerCfg.MaxTokens)
	}
	fmt.Fprintf(os.Stderr, "[MAP] evidence: %d фрагм.; extraction через %s...\n", len(prompt.Evidence), answerCfg.Model)
	raw, err := provider.Generate(ctx, mem.AnswerRequest{
		Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
		MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
	})
	if err != nil {
		return fmt.Errorf("map extraction: %w", err)
	}
	extracted, err := mem.DecodeKnowledgeExtraction(raw, prompt.Evidence)
	if err != nil {
		return fmt.Errorf("map extraction rejected: %w", err)
	}
	if extracted.Insufficient {
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: %s\n", extracted.Reason)
		return nil
	}
	if err := store.UpsertCurrentKnowledgeGraph(extracted.Graph); err != nil {
		return fmt.Errorf("map persistence: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Карта обновлена: nodes=%d edges=%d evidence=%d\n",
		len(extracted.Graph.Nodes), len(extracted.Graph.Edges), len(prompt.Evidence))
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
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" || ext == ".djvu" || ext == ".djv" {
		fmt.Printf("[INFO] Формат %s требует извлечения текста и provenance; переключаю на mem import.\n", ext)
		return handleImport(cfg, store, args)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("чтение файла %s: %w", path, err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fmt.Errorf("файл %s не является UTF-8 текстом; для PDF/DjVu используй mem import", path)
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
		return err
	}
	pageSummary := "без известных страниц"
	if len(result.Pages) > 0 {
		pageSummary = fmt.Sprintf("%d страниц с текстом", len(result.Pages))
	}
	fmt.Printf("[OK] Документ импортирован: %s\n", result.SourcePath)
	fmt.Printf("     document=%s | blocks=%d | chunks=%d | %s\n",
		result.DocumentID, result.Blocks, result.Chunks, pageSummary)
	fmt.Printf("     revision=%s\n", result.DocumentRevision)
	for _, warning := range result.Warnings {
		fmt.Printf("[WARN] %s\n", warning)
	}
	return nil
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
	if entry.EmbeddingModel != "" {
		fmt.Printf("[EMBED] Модель: %s; space: %s\n", entry.EmbeddingModel, entry.EmbeddingSpace)
	} else {
		fmt.Println("[EMBED] Модель: неизвестна (legacy; требуется переиндексация)")
	}
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

func handleSources(store *Store) error {
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

  mem ask <вопрос> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only] [-context-chars N]
      Сформировать локальный ответ только по найденным evidence-фрагментам.
      Каждый тезис требует точного citation ID; статусы идут в stderr,
      проверенный ответ и источники — в stdout. mem search не меняется.

  mem map build <фокус> [-limit N] [-tags "тег1,тег2"] [-tag "категория"] [-from 2026-01-01] [-to 2026-07-01] [-min-score 0.5] [-vector-only] [-context-chars N]
      Извлечь типизированные узлы и связи только из versioned document evidence.
      Citation, координаты, ревизии, хеши и постоянные ID назначаются самим mem;
      невалидный ответ модели отклоняется целиком без частичной записи.

  mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]
      Сравнить active claim с current evidence из разных документов и предложить
      draft-узлы contradiction/gap. Endpoints, anchors и связи назначает mem;
      -batches (1..32, по умолчанию 1) включает детерминированную пакетную обработку.
      Проверенные batch-результаты сохраняются как checkpoints; повтор команды
      продолжает run. Knowledge graph изменяется только после завершения всех пакетов.

  mem map open [--port N] [--title <текст>] [--no-browser]
      Открыть актуальную карту через локальное HTTP-рабочее пространство только на
      127.0.0.1. По умолчанию выбирается свободный порт и запускается браузер;
      обновление страницы перечитывает граф из активной базы. Раскладка закреплённых
      узлов, масштаб и положение сохраняются в базе автоматически. Ctrl+C
      останавливает сервер. Для ручного открытия адреса используй --no-browser.

  mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]
      Найти близкие по смыслу узлы одного kind. Эмбеддится точный label+body,
      stale/missing/resolved узлы исключаются; результат ничего не объединяет.

  mem map merge-node <manifest.json>
      После ручной проверки пометить generated draft как resolved в пользу active
      канонического узла. Манифест закрепляет node/evidence digests из duplicates.

  mem map merges [--json] [-limit N]
      Показать append-only историю объединений и их текущее current/stale состояние.

  mem map runs [--json] [-limit N] [-status running|completed]
      Показать историю analysis runs, покрытие и состояния batch-checkpoints.

  mem map run <run-id> [--json]
      Показать один analysis run со всеми batch-checkpoints и причинами ошибок.

  mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]
      Найти завершённые runs старше duration (например 30d или 720h), сохранив
      последние N (по умолчанию 20). Без --yes выводит preview и ничего не удаляет;
      running runs никогда не удаляются.

  mem map status [--json]
      Показать draft/active/resolved, состояние current/stale/missing для каждого
      evidence anchor и очередь объектов, готовых к подтверждению.

  mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>]
      Подтвердить один draft-объект и записать автора/комментарий в append-only
      журнал. Все evidence должны оставаться current; digest можно закрепить.

  mem map approve-batch <manifest.json>
      Атомарно подтвердить явный пакет объектов. Каждый объект обязан содержать
      expected_evidence_digest из map status --json; частичный успех невозможен.

  mem map reviews [--json] [-limit N]
      Показать локальный журнал review-решений, новые записи первыми.

  mem map export
      Вывести сохранённый граф знаний в JSON (stdout).

  mem map export-html <output.html> [--title <текст>] [--force]
      Создать автономную офлайн HTML-карту с force-layout, pan/zoom/drag,
      поиском, фильтрами и provenance-панелью. Существующий файл не меняется
      без явного --force.

  mem recent [-limit N]
      Показать последние записи

  mem show <id> | mem show --from-file <путь>
      Алиасы: get, view. Также mem source <id>.
      Показать одну запись целиком (полный текст + метаданные)
      или все чанки одного документа.

  mem add-file <путь_к_файлу> [-tags "тег1,тег2"] [-important]
      Сохранить UTF-8 текстовый файл в базу (с чанкингом).
      PDF/DjVu автоматически перенаправляются в безопасный mem import;
      бинарные данные никогда не эмбеддятся как текст.

	mem import <document.md|document.pdf|document.djvu> [-title "Название"] [-tags "тег1,тег2"] [-important]
	  Импортировать Markdown, PDF или DjVu с постраничным provenance документа.
	  Markdown-маркеры <!-- page: N --> сохраняются как номера страниц.
	  Для сканов используется локальный Tesseract; инструменты не устанавливаются автоматически.
	  Все chunks фиксируются атомарно только после успешного завершения embeddings.

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

  mem config set-answer-model <model>
      Установить отдельную локальную chat/instruct модель для mem ask

  mem config set-answer-base-url <url>
      Установить loopback URL Ollama для mem ask

  mem config set-answer-timeout <секунды>
      Таймаут retrieval и генерации ответа

  mem config set-answer-max-tokens <число>
      Максимум токенов ответа

  mem config set-answer-context-chars <число>
      Бюджет сериализованного evidence-контекста

  mem stats
      Статистика базы

  mem where | mem current
      Показать абсолютные пути активного проекта, .mem и SQLite-файла.

  mem open <каталог_проекта|путь_к_.mem>
      Открыть существующую локальную базу и запустить TUI.
      Пример: mem open "D:\Knowledge\ProjectA"

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
                              Путь к установленному mem.exe на выбор базы не влияет.

Примеры:
  cd ~/projects/myapp && mem add "Сервер: 157.22.196.67"
  cd ~/projects/other && mem add "Другой факт"
  mem where                              # показать точный store.db
  mem open "D:/Knowledge/ProjectA"       # открыть локальную базу в TUI
  mem --dir "D:/Knowledge/ProjectA" recent
  mem search "IP сервера" -tags "инфраструктура"
  mem search "tui" -tag rule                 # только правила про TUI
  mem --global search "deadlock"            # поиск в глобальной базе
  mem --global search "архитектура" -tag best-practice
  mem search "архитектура" -from 2026-07-01 -to 2026-07-26
  mem search "сервер" -min-score 0.5 -vector-only
  mem ask "каков порядок запуска?" -limit 5
  mem map build "архитектура импорта" -limit 10
  mem map analyze "требования к рабочему давлению"
  mem map analyze "требования к рабочему давлению" -batches 8 -resume kar-0123456789abcdef0123456789abcdef
  mem map duplicates --json -kind claim -threshold 0.92
  mem map merge-node merge-node.json
  mem map merges
  mem map runs -status running
  mem map run kar-0123456789abcdef0123456789abcdef --json
  mem map prune-runs -older-than 30d -keep 20
  mem map status
  mem map approve node kn-0123456789abcdef0123456789abcdef --reviewer "Руслан"
  mem map approve-batch review-manifest.json
  mem map reviews --json
  mem map export
  mem map export-html knowledge-map.html --title "Карта проекта"
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

Интерактивные режимы:
  mem без аргументов запускает TUI; mem repl запускает readline-REPL.
  TUI поддерживает все команды выше через /команда, включая ask, import,
  index, map и config. Введите / для палитры; /help показывает полный список.
  Esc возвращает на главный экран. Выход из TUI: /exit или Ctrl+C два раза.
  В REPL текст без / — сокращение для /search, Up/Down — история,
  Tab — дополнение, Ctrl-D или /exit — выход.

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

	case "set-answer-model":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return fmt.Errorf("использование: mem config set-answer-model <model_name>")
		}
		candidate := cfg.Answer
		candidate.Model = strings.TrimSpace(args[1])
		if _, err := mem.NewOllamaAnswerProvider(candidate); err != nil {
			return err
		}
		cfg.Answer.Model = candidate.Model
		return saveConfig(cfg)

	case "set-answer-base-url":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-answer-base-url <local-url>")
		}
		baseURL, err := mem.NormalizeLocalAnswerBaseURL(args[1])
		if err != nil {
			return err
		}
		cfg.Answer.BaseURL = baseURL
		return saveConfig(cfg)

	case "set-answer-timeout":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-answer-timeout <секунды>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerTimeoutSeconds {
			return fmt.Errorf("таймаут ответа должен быть от 1 до %d секунд", mem.MaxAnswerTimeoutSeconds)
		}
		cfg.Answer.TimeoutSeconds = n
		return saveConfig(cfg)

	case "set-answer-max-tokens":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-answer-max-tokens <число>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerTokens {
			return fmt.Errorf("максимум токенов ответа должен быть от 1 до %d", mem.MaxAnswerTokens)
		}
		cfg.Answer.MaxTokens = n
		return saveConfig(cfg)

	case "set-answer-context-chars":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-answer-context-chars <число>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerContextChars {
			return fmt.Errorf("бюджет evidence должен быть от 1 до %d символов", mem.MaxAnswerContextChars)
		}
		cfg.Answer.ContextChars = n
		return saveConfig(cfg)

	case "set-chunk-size":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-size <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 100 || n > mem.MaxEmbeddingChars {
			return fmt.Errorf("размер чанка должен быть от 100 до %d", mem.MaxEmbeddingChars)
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
