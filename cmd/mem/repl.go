package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chzyer/readline"

	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

// replCommands — список команд, доступных в REPL через префикс /.
// Полный список + алиасы поддерживаются в dispatchRepl().
var replCommands = []string{
	"search", "add", "recent", "show", "get", "view",
	"important", "imp", "tags", "retag", "edit",
	"delete", "rm", "stats", "sources", "config",
	"clear", "help", "exit", "quit",
}

// runRepl запускает интерактивный режим mem.
// Использование: `mem` без аргументов или `mem repl`.
//
// Цикл:
//   - читаем строку через readline (Up/Down — история)
//   - если строка начинается с "/" — это команда (например, /search IP)
//   - иначе — это сокращение для /search <строка>
//   - пустая строка — игнорируется
//   - EOF / Ctrl-D — выход
func runRepl(cfg *Config, store *Store) {
	printReplHeader(cfg, store)
	fmt.Println()

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "mem> ",
		HistoryFile:     memHistoryPath(),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		AutoComplete:    NewMemCompleter(),
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

		dispatchReplLine(cfg, store, line)
		fmt.Println()
	}
}

// memHistoryPath возвращает путь к файлу истории REPL: .mem/history.txt.
// Создаётся при первом запуске REPL, используется всеми сессиями в этом проекте.
func memHistoryPath() string {
	return memDir() + "/history.txt"
}

// dispatchReplLine выполняет одну строку ввода REPL.
func dispatchReplLine(cfg *Config, store *Store, line string) {
	var cmd string
	var args []string

	if strings.HasPrefix(line, "/") {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			return
		}
		cmd = strings.ToLower(strings.TrimPrefix(parts[0], "/"))
		args = parts[1:]
	} else {
		cmd = "search"
		args = []string{line}
	}

	switch cmd {
	case "search":
		handleSearch(cfg, store, args)
	case "add":
		handleAdd(cfg, store, args)
	case "recent":
		handleRecent(store, args)
	case "show", "get", "view", "source":
		handleShow(store, args)
	case "important", "imp":
		handleImportant(store, args)
	case "tags", "retag":
		handleRetag(store, args)
	case "edit":
		handleEdit(cfg, store, args)
	case "delete", "rm":
		handleDelete(store, args)
	case "stats":
		handleStats(store)
	case "sources":
		handleSources(store)
	case "config":
		if err := handleConfig(args); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
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
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Неизвестная команда: /%s\n", cmd)
		fmt.Fprintln(os.Stderr, "Введите /help для списка команд")
	}
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
	fmt.Printf("  %s          Список документов\n", ui.ID("/sources"))
	fmt.Printf("  %s           Конфигурация\n", ui.ID("/config"))
	fmt.Printf("  %s            Очистить экран\n", ui.ID("/clear"))
	fmt.Printf("  %s        Очистить историю ввода\n", ui.ID("/clear-history"))
	fmt.Printf("  %s            Помощь\n", ui.ID("/help"))
	fmt.Printf("  %s            Выйти (Ctrl-D тоже)\n", ui.ID("/exit"))
	fmt.Println()
	fmt.Println(ui.Tag("Tab дополняет команды и подставляет из истории."))
}
