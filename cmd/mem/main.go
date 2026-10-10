package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/buildinfo"
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
	"add": true, "add-file": true, "import": true, "import-status": true,
	"import-runs": true, "import-run": true, "index": true,
	"config": true,
	"search": true, "ask": true, "map": true, "mindmap": true, "recent": true, "stats": true,
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
	os.Exit(applog.Run("mem", buildinfo.Version, run))
}

// run — основная логика. Возвращает код выхода: 0 при успехе, 1 при ошибке.
// Использование defer для Close() возможно благодаря тому, что main() вызывает os.Exit(run()).
func run() int {
	args0 := os.Args[1:]

	// Сначала только разбираем глобальные флаги. Проверка арности выполняется до
	// смены cwd, автосоздания .mem/ и открытия SQLite, чтобы ошибочная команда не
	// могла оставить никаких следов в текущей или целевой базе.
	useGlobal, customDir, args0, err := mem.ParseGlobalFlagStrict(args0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		return 1
	}
	colorMode, args0, err := mem.ParseColorFlagStrict(args0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		return 1
	}
	if len(args0) > 0 {
		if err := validateTopLevelCommandArgs(args0[0], args0[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	}

	// Web requests carry explicit workspace paths; never change process cwd.
	if len(args0) > 0 && (args0[0] == "gui" || args0[0] == "serve") {
		if err := handleWebCommand(args0[0], args0[1:], useGlobal, customDir); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
		return 0
	}

	if err := mem.ApplyDirSwitch(useGlobal, customDir); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	// --color/--no-color влияет только на оформление, но и его применяем после
	// чистой проверки командной строки.
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
	// Outline preview needs only the source file. Do not open/migrate a store
	// or require an initialized database just to inspect an import.
	if cmd == "mindmap" && len(args) > 0 && args[0] == "import" {
		options, e := parseClassicMindMapImportCLIOptions(args[1:])
		if e != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", e)
			return 1
		}
		if !options.apply {
			if e = handleClassicMindMapImport(nil, args[1:]); e != nil {
				fmt.Fprintf(os.Stderr, "Ошибка: %v\n", e)
				return 1
			}
			return 0
		}
	}

	// Команды, не требующие базы — обрабатываем сразу
	switch cmd {
	case "init":
		return handleInit()
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
	case "mindmap":
		if err := handleMindMap(cfg, store, args); err != nil {
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
	case "import-status":
		if err := handleImportStatus(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "import-runs":
		if err := handleImportRuns(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
	case "import-run":
		if err := handleImportRun(store, args); err != nil {
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
		if err := handleSource(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			return 1
		}
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
		if err := runRepl(cfg, store); err != nil {
			return 1
		}
	case "where", "current":
		handleWhere(store)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: %s\n\n", cmd)
		printUsage()
		return 1
	}
	return 0
}

// commandPositionals removes the first argument terminator while preserving
// every token after it as a literal positional argument. In particular,
// `mem index -- -notes` addresses a path beginning with a dash, while a second
// `--` remains an ordinary argument and is therefore subject to arity checks.
func commandPositionals(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			out := make([]string, 0, len(args)-1)
			out = append(out, args[:i]...)
			out = append(out, args[i+1:]...)
			return out
		}
	}
	return append([]string(nil), args...)
}

func exactCommandPositionals(command string, args []string, want int, usage string) ([]string, error) {
	positionals := commandPositionals(args)
	if len(positionals) != want {
		if usage != "" {
			return nil, fmt.Errorf("использование: %s", usage)
		}
		return nil, fmt.Errorf("команда %s не принимает аргументы", command)
	}
	return positionals, nil
}

// validateTopLevelCommandArgs covers commands whose handlers otherwise have
// no args parameter or historically consumed only args[0]. It intentionally
// runs before any filesystem/database side effect in run(). Argument-consuming
// handlers repeat their structural validation for direct REPL/TUI calls.
func validateTopLevelCommandArgs(command string, args []string) error {
	switch command {
	case "mindmap":
		if len(args) > 0 && args[0] == "import" {
			_, err := parseClassicMindMapImportCLIOptions(args[1:])
			return err
		}
		return nil
	case "gui", "serve":
		_, err := parseWebOptions(command, args)
		return err
	case "init", "version", "--version", "-v", "help", "--help", "-h", "stats", "sources", "repl", "where", "current":
		_, err := exactCommandPositionals(command, args, 0, "")
		return err
	case "index":
		positionals, err := exactCommandPositionals(command, args, 1, "mem index <файл|каталог>")
		if err != nil {
			return err
		}
		if strings.TrimSpace(positionals[0]) == "" {
			return fmt.Errorf("путь для индексации пуст")
		}
		return nil
	case "add":
		return validateAddCommandArgs(args)
	case "add-file", "import":
		return validateSingleDocumentCommandArgs(command, args)
	case "config":
		_, err := parseConfigCommandArgs(args)
		return err
	case "source":
		positionals, err := exactCommandPositionals(command, args, 1, "mem source <id>")
		if err != nil {
			return err
		}
		if _, err := parseEntryID(positionals[0]); err != nil {
			return err
		}
		return nil
	case "show", "get", "view":
		_, err := parseShowRequest(args)
		return err
	default:
		return nil
	}
}

func validateAddCommandArgs(args []string) error {
	if err := validateCommonFlags(args, "-title", "-tags", "-important"); err != nil {
		return err
	}
	positionals, _, _, _, _, _, _, _, _, _ := parseFlags(args)
	if strings.TrimSpace(strings.Join(positionals, " ")) == "" {
		return fmt.Errorf("укажи текст для сохранения\nПример: mem add \"какой-то факт\" -title \"Название\" -tags \"термины,проект\" -important")
	}
	return nil
}

func validateSingleDocumentCommandArgs(command string, args []string) error {
	if err := validateCommonFlags(args, "-title", "-tags", "-important"); err != nil {
		return err
	}
	positionals, _, _, _, _, _, _, _, _, _ := parseFlags(args)
	if len(positionals) == 0 || strings.TrimSpace(positionals[0]) == "" {
		if command == "add-file" {
			return fmt.Errorf("укажи путь к файлу (пример: mem add-file ./notes.txt -tags \"документация\")")
		}
		return fmt.Errorf("укажи Markdown, PDF или DjVu (пример: mem import ./book.djvu)")
	}
	if len(positionals) != 1 {
		if command == "add-file" {
			return fmt.Errorf("mem add-file принимает один файл за вызов")
		}
		return fmt.Errorf("mem import принимает один документ за вызов")
	}
	return nil
}

func parseEntryID(value string) (int64, error) {
	idValue := strings.TrimPrefix(value, "#")
	id, err := strconv.ParseInt(idValue, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("'%s' не является положительным номером записи", value)
	}
	return id, nil
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

func handleOpenDatabase(args []string) (applogResult0 int) {
	defer applog.TrackCode("cmd.mem.handleOpenDatabase")(&applogResult0)

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
func runTuiSession() (applogResult0 int) {
	defer applog.TrackCode("cmd.mem.runTuiSession")(&applogResult0)

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
		var replErr error
		if tuiErr == nil && tuiResult.replRequested {
			replErr = runRepl(cfg, store)
		}
		closeErr := store.Close()
		if replErr != nil {
			fmt.Fprintf(os.Stderr, "Ошибка REPL: %v\n", replErr)
			return 1
		}
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
func handleInit() int {
	if memExists() {
		fmt.Fprintf(os.Stderr, "Ошибка: .mem/ уже существует в текущей папке\n")
		return 1
	}
	if err := initMem(); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		return 1
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
	return 0
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
		case "--":
			positional = append(positional, args[i+1:]...)
			return
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

func validateCommonFlags(args []string, allowedFlags ...string) error {
	valueFlags := map[string]bool{
		"-title": true, "-tags": true, "-limit": true, "-from": true,
		"-to": true, "-min-score": true, "-tag": true,
	}
	boolFlags := map[string]bool{"-vector-only": true, "-important": true}
	allowed := make(map[string]bool, len(allowedFlags))
	for _, flag := range allowedFlags {
		allowed[flag] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if boolFlags[arg] {
			if !allowed[arg] {
				return fmt.Errorf("флаг %s неприменим к этой команде", arg)
			}
			continue
		}
		if valueFlags[arg] {
			if !allowed[arg] {
				return fmt.Errorf("флаг %s неприменим к этой команде", arg)
			}
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return fmt.Errorf("флаг %s требует значение", arg)
			}
			i++
			value := args[i]
			switch arg {
			case "-limit":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return fmt.Errorf("-limit должен быть положительным целым числом")
				}
			case "-min-score":
				n, err := strconv.ParseFloat(value, 64)
				if err != nil || n < 0 || n > 1 {
					return fmt.Errorf("-min-score должен быть числом от 0 до 1")
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("неизвестный флаг: %s", arg)
		}
	}
	return nil
}

func handleAdd(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleAdd")(&applogResult0)

	if err := validateAddCommandArgs(args); err != nil {
		return err
	}
	positional, title, tags, _, _, _, _, _, important, _ := parseFlags(args)

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
