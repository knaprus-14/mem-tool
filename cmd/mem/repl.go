package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/chzyer/readline"

	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

// replCommands — список команд, доступных в REPL через префикс /.
// Полный список + алиасы поддерживаются в dispatchRepl().
var replCommands = []string{
	"search", "add", "recent", "show", "get", "view",
	"important", "imp", "tags", "retag", "edit",
	"delete", "rm", "stats", "sources", "config",
	"where", "current",
	"clear", "help", "exit", "quit",
}

// runRepl запускает интерактивный режим mem.
// Использование: `mem` без аргументов или `mem repl`.
//
// Цикл:
//   - читаем строку через readline (Up/Down — история)
//   - если строка начинается с "/" — это команда (например, /search IP)
//   - если строка = "/" (одна) — псевдо-popup со списком команд
//   - иначе — это сокращение для /search <строка>
//   - пустая строка — игнорируется
//   - EOF / Ctrl-D — выход
func runRepl(cfg *Config, store *Store) {
	printReplHeader(cfg, store)
	fmt.Println()

	// Prompt: линия сверху (отделяет результат предыдущей команды от ввода)
	// + сам prompt "mem> ". ANSI \x1b[2m — dim (серый), \x1b[0m — сброс.
	// readline v1.5.1 не поддерживает callback для Prompt — только строку,
	// поэтому линия рисуется в самом prompt и появляется перед каждой строкой ввода.
	promptLine := "\x1b[2m" + strings.Repeat("─", 60) + "\x1b[0m\n"
	prompt := promptLine + "mem> "
	historyPath := memHistoryPath()
	if err := sanitizeReplHistory(historyPath); err != nil {
		fmt.Fprintf(os.Stderr, "Предупреждение: небезопасная история REPL отключена: %v\n", err)
		historyPath = ""
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:                 prompt,
		HistoryFile:            historyPath,
		DisableAutoSaveHistory: true,
		InterruptPrompt:        "^C",
		EOFPrompt:              "exit",
		AutoComplete:           NewMemCompleter(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка запуска REPL: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil { // EOF (Ctrl-D) или Ctrl-C
			fmt.Println()
			fmt.Println(ui.Tag("До встречи!"))
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if shouldSaveReplHistory(line) {
			if err := rl.SaveHistory(line); err != nil {
				fmt.Fprintf(os.Stderr, "Предупреждение: история REPL не сохранена: %v\n", err)
			}
		}

		// Псевдо-popup: ввели только "/" — показать список команд
		if line == "/" {
			printCommandMenu()
			continue
		}

		if dispatchReplLine(cfg, store, line) {
			return
		}
		fmt.Println()
	}
}

// memHistoryPath возвращает путь к файлу истории REPL: .mem/history.txt.
// Создаётся при первом запуске REPL, используется всеми сессиями в этом проекте.
func memHistoryPath() string {
	return memDir() + "/history.txt"
}

// shouldSaveReplHistory prevents credentials entered through configuration
// commands from being persisted as plain text. readline auto-history is
// disabled, and only lines accepted by this predicate are saved explicitly.
func shouldSaveReplHistory(line string) bool {
	cmd, args, err := parseReplCommandLine(line)
	if err != nil {
		// A malformed command must not reach dispatch and is not persisted:
		// failing closed also prevents a broken quote from bypassing redaction.
		return false
	}
	if len(args) < 1 {
		return true
	}
	return cmd != "config" || strings.ToLower(args[0]) != "set-polza-key"
}

// sanitizeReplHistory removes credentials written by versions which enabled
// readline auto-history. The replacement is written and flushed before an
// atomic same-directory rename; on failure runRepl disables history entirely.
func sanitizeReplHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if !shouldSaveReplHistory(strings.TrimSuffix(line, "\r")) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".history-sanitize-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(kept, "\n")); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceReplHistoryFile(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// dispatchReplLine выполняет одну строку ввода REPL.
func dispatchReplLine(cfg *Config, store *Store, line string) bool {
	cmd, args, err := parseReplCommandLine(line)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка разбора команды: %v\n", err)
		return false
	}
	if cmd == "" {
		return false
	}
	if err := validateReplCommandArgs(cmd, args); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		return false
	}

	switch cmd {
	case "search":
		if err := handleSearch(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "add":
		if err := handleAdd(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "recent":
		if err := handleRecent(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "show", "get", "view", "source":
		if err := handleShow(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "important", "imp":
		if err := handleImportant(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "tags", "retag":
		if err := handleRetag(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "edit":
		if err := handleEdit(cfg, store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "delete", "rm":
		if err := handleDelete(store, args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		}
	case "stats":
		handleStats(store)
	case "where", "current":
		handleWhere(store)
	case "sources":
		handleSources(store)
	case "config":
		if err := handleConfig(args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
		} else if refreshed, err := loadConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка обновления конфигурации REPL: %v\n", err)
		} else {
			*cfg = *refreshed
		}
	case "clear":
		// \x1b[2J — clear screen, \x1b[H — cursor home
		fmt.Print("\x1b[2J\x1b[H")
		printReplHeader(cfg, store)
	case "clear-history":
		clearReplHistory()
	case "help", "?":
		printReplHelp()
	case "exit", "quit", "q":
		fmt.Println(ui.Tag("До встречи!"))
		return true
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: /%s\n", cmd)
		fmt.Fprintln(os.Stderr, "Введите /help для списка команд")
	}
	return false
}

func validateReplCommandArgs(command string, args []string) error {
	if err := validateTopLevelCommandArgs(command, args); err != nil {
		return err
	}
	switch command {
	case "clear", "clear-history", "help", "?", "exit", "quit", "q":
		_, err := exactCommandPositionals(command, args, 0, "")
		return err
	default:
		return nil
	}
}

// parseReplCommandLine keeps plain text as one search query and tokenizes only
// slash commands. This preserves the REPL shorthand while allowing quoted
// multiword flag values and paths in the same form as the regular CLI.
func parseReplCommandLine(line string) (string, []string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil, nil
	}
	if !strings.HasPrefix(line, "/") {
		return "search", []string{line}, nil
	}
	parts, err := splitReplArguments(strings.TrimPrefix(line, "/"))
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return strings.ToLower(parts[0]), parts[1:], nil
}

// splitReplArguments is a small shell-like tokenizer with no external
// dependencies. Both quote styles preserve whitespace; inside a quote, a
// backslash escapes a matching quote only when that quote is followed by a
// non-space rune. This leaves a quote after a terminal Windows path separator
// available to close the argument. Outside quotes a backslash escapes whitespace
// or a quote. Backslashes before another backslash stay literal so Windows UNC
// paths such as \\\\server\\share are not collapsed.
func splitReplArguments(line string) ([]string, error) {
	var (
		parts        []string
		current      strings.Builder
		quote        rune
		tokenStarted bool
	)
	runes := []rune(line)
	flush := func() {
		if !tokenStarted {
			return
		}
		parts = append(parts, current.String())
		current.Reset()
		tokenStarted = false
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if r == '\\' && i+2 < len(runes) && runes[i+1] == quote &&
				!unicode.IsSpace(runes[i+2]) {
				i++
				current.WriteRune(runes[i])
				tokenStarted = true
				continue
			}
			current.WriteRune(r)
			tokenStarted = true
			continue
		}

		switch {
		case r == '\'' || r == '"':
			quote = r
			tokenStarted = true
		case unicode.IsSpace(r):
			flush()
		case r == '\\' && i+1 < len(runes) &&
			(unicode.IsSpace(runes[i+1]) || runes[i+1] == '\'' || runes[i+1] == '"'):
			i++
			current.WriteRune(runes[i])
			tokenStarted = true
		default:
			current.WriteRune(r)
			tokenStarted = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("незакрытая кавычка %q", string(quote))
	}
	flush()
	return parts, nil
}

// clearReplHistory удаляет файл .mem/history.txt.
// readline автоматически создаст его заново при следующем вводе.
func clearReplHistory() {
	path := memHistoryPath()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, ui.Tag("История уже пуста."))
			return
		}
		fmt.Fprintf(os.Stderr, "Ошибка очистки истории: %v\n", err)
		return
	}
	fmt.Println(ui.Success("История очищена: %s", ui.Tag(path)))
}

// printReplHeader печатает приветствие REPL: статистика + последние 5 записей.
func printReplHeader(cfg *Config, store *Store) {
	stats := store.Stats()
	total := stats["total_entries"]
	backend := cfg.Backend
	model := cfg.Ollama.Model
	if cfg.Backend == "polza" {
		model = cfg.Polza.Model
	}
	if model == "" {
		model = "(по умолчанию)"
	}

	fmt.Println(ui.Header(fmt.Sprintf("mem · поисковая база · %d записей · backend: %s · %s",
		total, backend, model)))

	recent, err := store.Recent(5)
	if err == nil && len(recent) > 0 {
		fmt.Println()
		fmt.Println(ui.Tag("Последние записи:"))
		for _, r := range recent {
			title := r.Title
			if title == "" {
				title = "(без заголовка)"
			}
			dateStr := r.Created
			if t, err := time.Parse(time.RFC3339, r.Created); err == nil {
				dateStr = t.Format("2006-01-02")
			}
			fmt.Printf("  %s %s  %s\n",
				ui.Mark("good"),
				ui.ID(fmt.Sprintf("#%d", r.ID)),
				title,
			)
			fmt.Printf("      %s %s\n", ui.Key("дата"), ui.Date(dateStr))
		}
	}
	fmt.Println(ui.Separator())
	fmt.Println("Введите запрос или /help. Ctrl-D для выхода.")
}

// printReplHelp показывает справку по /-командам REPL.
func printReplHelp() {
	fmt.Println(ui.Header("/-команды mem REPL"))
	fmt.Println()
	fmt.Printf("  %s   Поиск (алиас: текст без /)\n", ui.ID("/search <q>"))
	fmt.Printf("  %s      Добавить запись\n", ui.ID("/add <текст>"))
	fmt.Printf("  %s           Последние N записей\n", ui.ID("/recent"))
	fmt.Printf("  %s        Показать запись целиком\n", ui.ID("/show <id>"))
	fmt.Printf("  %s       Переключить важность\n", ui.ID("/important <id>"))
	fmt.Printf("  %s        Изменить теги\n", ui.ID("/tags <id> -tags ..."))
	fmt.Printf("  %s           Изменить запись\n", ui.ID("/edit <id> <текст>"))
	fmt.Printf("  %s           Удалить запись\n", ui.ID("/delete <id>"))
	fmt.Printf("  %s            Статистика\n", ui.ID("/stats"))
	fmt.Printf("  %s            Активная локальная база\n", ui.ID("/where"))
	fmt.Printf("  %s          Список документов\n", ui.ID("/sources"))
	fmt.Printf("  %s           Конфигурация\n", ui.ID("/config"))
	fmt.Printf("  %s            Очистить экран\n", ui.ID("/clear"))
	fmt.Printf("  %s        Очистить историю ввода\n", ui.ID("/clear-history"))
	fmt.Printf("  %s            Помощь\n", ui.ID("/help"))
	fmt.Printf("  %s            Выйти (Ctrl-D тоже)\n", ui.ID("/exit"))
	fmt.Println()
	fmt.Println(ui.Tag("Tab дополняет команды и подставляет из истории."))
}

// commandMenuEntry — описание одной команды для /-меню.
type commandMenuEntry struct {
	name string
	desc string
}

// commandMenu — список команд для псевдо-popup при вводе "/" + Enter.
var commandMenu = []commandMenuEntry{
	{"search <q>", "поиск (или просто текст)"},
	{"add <текст>", "сохранить новую запись"},
	{"recent", "последние N записей"},
	{"show <id>", "показать запись целиком"},
	{"important <id>", "переключить важность"},
	{"tags <id> ...", "изменить теги"},
	{"edit <id> ...", "изменить запись"},
	{"delete <id>", "удалить запись"},
	{"stats", "статистика базы"},
	{"where", "путь активной локальной базы"},
	{"sources", "список документов"},
	{"config", "конфигурация"},
	{"clear", "очистить экран"},
	{"clear-history", "очистить историю ввода"},
	{"help", "список /-команд"},
	{"exit", "выйти (Ctrl-D тоже)"},
}

// printCommandMenu показывает псевдо-popup со списком доступных команд.
// Вызывается при вводе "/" + Enter.
func printCommandMenu() {
	fmt.Println()
	fmt.Println(ui.Header("/-команды (нажмите /<команда> или Esc+Enter для отмены)"))
	fmt.Println(ui.Separator())

	// Печатаем в две колонки для компактности
	maxName := 0
	for _, e := range commandMenu {
		if len(e.name) > maxName {
			maxName = len(e.name)
		}
	}
	half := (len(commandMenu) + 1) / 2
	for i := 0; i < half; i++ {
		left := commandMenu[i]
		leftStr := fmt.Sprintf("  %s  %s",
			ui.ID("/"+left.name),
			ui.Tag(left.desc),
		)
		var rightStr string
		if i+half < len(commandMenu) {
			right := commandMenu[i+half]
			rightStr = fmt.Sprintf("    %s  %s",
				ui.ID("/"+right.name),
				ui.Tag(right.desc),
			)
		}
		// Выравнивание
		fmt.Printf("%-*s%s\n", maxName+20, leftStr, rightStr)
	}
	fmt.Println(ui.Separator())
	fmt.Println(ui.Tag("Подсказка: Tab дополняет, ↑/↓ история, Ctrl-D выход."))
}
