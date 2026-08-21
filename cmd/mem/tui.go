package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// tuiStyles — lipgloss-стили для элементов TUI.
var tuiStyles = struct {
	Header    lipgloss.Style
	Frame     lipgloss.Style
	Popup     lipgloss.Style
	PopupItem lipgloss.Style
	PopupSel  lipgloss.Style
	Status    lipgloss.Style
	Separator lipgloss.Style
	UserLine  lipgloss.Style
	Result    lipgloss.Style
}{
	Header:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1),
	Frame:     lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")),
	Popup:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(0, 1),
	PopupItem: lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
	PopupSel:  lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Bold(true).Background(lipgloss.Color("63")),
	Status:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true),
	Separator: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	UserLine:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")),
	Result:    lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
}

// busyText — текст в статус-строке, пока крутится спиннер.
const busyText = "выполняю..."

// maxOutputLines — мягкий предел количества блоков в m.output.
// При превышении старые блоки отбрасываются — защита от O(n²) Join при длинных
// сессиях и от memory pressure, если пользователь не делает /clear.
const maxOutputLines = 500

// maxPopupRows keeps the command palette usable even though it exposes every
// CLI command and map/config subcommand.
const maxPopupRows = 10

// tuiSeparatorBlock is stored semantically in the output history. Its visual
// width is calculated on every render so separators grow and shrink together
// with the terminal instead of retaining the width they had when appended.
const tuiSeparatorBlock = "\x00mem-tui-separator\x00"

// tuiModel — состояние TUI.
type tuiModel struct {
	width           int
	height          int
	viewport        viewport.Model
	textarea        textarea.Model
	spinner         spinner.Model
	output          []string
	showPopup       bool
	popupIdx        int
	popupItems      []commandMenuEntry // полный список команд (для /help)
	popupFiltered   []commandMenuEntry // отфильтрованный по префиксу ввода (для popup)
	busy            bool
	cancelled       bool      // пользователь нажал Esc во время busy=true
	ctrlCPending    bool      // первый Ctrl+C уже нажат, ждём второго
	ctrlCTime       time.Time // время последнего Ctrl+C (для проверки окна 2 сек)
	exitAfterBusy   bool      // двойной Ctrl+C во время операции: выйти после её завершения
	commandEvents   <-chan tea.Msg
	activeCommand   string
	commandStarted  time.Time
	progressUpdates int
	cfg             *Config
	store           *Store
	quitting        bool
	openPath        string // project root requested by /open; consumed after TUI exits
	replRequested   bool   // /repl requests the classic interactive mode after TUI cleanup
}

// execResultMsg — результат выполнения команды.
type execResultMsg struct {
	output string
	err    error
	cfg    *Config
}

// commandProgressMsg carries one stdout/stderr line from a running command.
// The event loop appends it immediately instead of waiting for command exit.
type commandProgressMsg struct {
	output string
}

// ctrlCResetMsg — сбрасывает флаг ctrlCPending через 2 секунды после первого нажатия.
// Без этого случайное первое нажатие блокировало бы выход навсегда.
type ctrlCResetMsg struct{}

// newTuiModel создаёт начальную модель TUI.
func newTuiModel(cfg *Config, store *Store) tuiModel {
	ta := textarea.New()
	ta.Placeholder = "Введите запрос или /help"
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Перебиндим InsertNewline на ctrl+j: textarea по умолчанию реагирует на Enter
	// как на newline, и тот же Enter сабмитит команду — это даёт вспышку newline
	// перед выполнением. Для однострочного prompt Enter должен только сабмитить.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))

	vp := viewport.New(80, 20)
	vp.SetContent("")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))

	menuItems := tuiCommandMenuItems()
	m := tuiModel{
		viewport:      vp,
		textarea:      ta,
		spinner:       sp,
		popupItems:    menuItems,
		popupFiltered: menuItems,
		cfg:           cfg,
		store:         store,
	}
	m.printHeader()
	return m
}

// headerLine возвращает динамическую строку заголовка (статистика, бэкенд, модель).
func (m *tuiModel) headerLine() string {
	stats := m.store.Stats()
	total := stats["total_entries"]
	backend := m.cfg.Backend
	model := m.cfg.Ollama.Model
	if m.cfg.Backend == "polza" {
		model = m.cfg.Polza.Model
	}
	if model == "" {
		model = "(по умолчанию)"
	}
	databaseName := filepath.Base(filepath.Dir(filepath.Dir(m.store.Path())))
	return tuiStyles.Header.Render(fmt.Sprintf(
		"mem · база: %s · %d записей · backend: %s · %s", databaseName, total, backend, model))
}

// printHeader добавляет приветствие в viewport (для /clear и стартового экрана).
// Динамический заголовок (headerLine) НЕ выводится — он уже отрисован в View() сверху.
func (m *tuiModel) printHeader() {
	m.appendBlock(tuiStyles.Status.Render("Активная база: " + m.store.Path()))
	m.appendBlock(tuiStyles.Status.Render("Введите запрос или /help. Esc — на главный экран, Ctrl+C×2 — выход."))
	m.appendSeparator()
}

// returnHome возвращает TUI в исходное состояние без завершения процесса.
// Если команда ещё выполняется, её результат будет проигнорирован: обработчик
// доработает безопасно, а Store не будет закрыт из-под фоновой горутины.
func (m *tuiModel) returnHome() {
	m.showPopup = false
	m.popupIdx = 0
	m.popupFiltered = m.popupItems
	m.textarea.Reset()
	m.textarea.Focus()
	m.ctrlCPending = false
	m.output = nil
	m.viewport.SetContent("")
	m.printHeader()
}

// Init — инициализация модели.
// Spinner НЕ тикает сразу — он стартует только когда m.busy=true
// (в Update, в ветке "enter" при запуске runCommandAsync).
// Раньше тут был deprecated spinner.Tick — он тикал 10 FPS всё время,
// тратя CPU даже когда TUI простаивает.
func (m tuiModel) Init() tea.Cmd {
	return textarea.Blink
}

// Update — обработка сообщений.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	// spinner обновляется только в своей ветке case spinner.TickMsg ниже.
	// Безусловный outer-вызов ломал цепочку тиков (v1.15.11 regression):
	// при TickMsg сначала outer обрабатывал его (инкрементировал m.tag и
	// возвращал tick), потом case делал второй Update, который видел
	// tag mismatch и возвращал nil — цепочка обрывалась после 2 тиков.

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recomputeSizes()
		return m, nil

	case spinner.TickMsg:
		// Тикаем спиннер только пока busy=true. Когда busy=false (результат пришёл)
		// — возвращаем nil, цепочка обрывается, CPU не тратится.
		// Раньше spinner.Update вызывался безусловно для каждого msg в начале Update,
		// и цепочка TickMsg терялась при return внутри switch — спиннер зависал.
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		if m.busy {
			return m, spCmd
		}
		return m, nil

	case commandProgressMsg:
		// Even after Esc, keep draining the worker channel so a verbose command
		// cannot block while it finishes. Its late output stays hidden because
		// Esc has already returned the viewport to the home screen.
		m.progressUpdates++
		if !m.cancelled {
			m.appendBlock(msg.output)
		}
		return m, waitForCommandEvent(m.commandEvents)

	case execResultMsg:
		// Результат выполнения команды
		duration := time.Duration(0)
		if !m.commandStarted.IsZero() {
			duration = time.Since(m.commandStarted)
		}
		updates := m.progressUpdates
		m.busy = false
		m.commandEvents = nil
		m.activeCommand = ""
		m.commandStarted = time.Time{}
		m.progressUpdates = 0
		if msg.cfg != nil {
			m.cfg = msg.cfg
		}
		if m.exitAfterBusy {
			m.exitAfterBusy = false
			m.quitting = true
			return m, tea.Quit
		}
		if m.cancelled {
			// Esc уже показал главный экран. Горутина безопасно доработала, но
			// её результат не должен снова увести пользователя с главного экрана.
			m.cancelled = false
			return m, nil
		}
		if msg.err != nil {
			m.appendBlock(tuiStyles.Status.Render("Ошибка: " + msg.err.Error()))
		}
		if msg.output != "" {
			m.appendBlock(msg.output)
		}
		completion := fmt.Sprintf("Готово за %s · обновлений: %d", formatTUIDuration(duration), updates)
		if msg.err != nil {
			completion = fmt.Sprintf("Завершено с ошибкой за %s · обновлений: %d", formatTUIDuration(duration), updates)
		}
		m.appendBlock(tuiStyles.Status.Render(completion))
		m.appendSeparator()
		return m, nil

	case ctrlCResetMsg:
		// Окно ожидания второго Ctrl+C истекло — сбрасываем флаг.
		m.ctrlCPending = false
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			// Двойной Ctrl+C в течение 2 секунд — выход.
			// Одинарный — показываем предупреждение и ставим таймер на сброс.
			if m.ctrlCPending && time.Since(m.ctrlCTime) < 2*time.Second {
				if m.busy {
					m.cancelled = true
					m.exitAfterBusy = true
					m.returnHome()
					m.appendBlock(tuiStyles.Status.Render("Завершаю текущую операцию и выхожу..."))
					return m, nil
				}
				m.quitting = true
				return m, tea.Quit
			}
			m.ctrlCPending = true
			m.ctrlCTime = time.Now()
			m.appendBlock(tuiStyles.Status.Render(
				"Чтобы выйти, нажмите Ctrl+C ещё раз (в течение 2 секунд)"))
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return ctrlCResetMsg{} })

		case "ctrl+d":
			m.appendBlock(tuiStyles.Status.Render("Ctrl+D не закрывает TUI. Для выхода: /exit или Ctrl+C два раза."))
			return m, nil

		case "esc":
			if m.busy {
				// Esc не закрывает TUI и не закрывает Store из-под обработчика.
				// Возвращаем главный экран, а поздний результат игнорируем.
				m.cancelled = true
			}
			m.returnHome()
			return m, nil

		case "enter":
			if m.busy {
				m.appendBlock(tuiStyles.Status.Render("Дождитесь завершения текущей операции"))
				return m, nil
			}
			// Если popup открыт — выбор команды.
			// Подставляем literal-команду без плейсхолдеров. Для вложенных команд
			// сохраняем подкоманду: «map build <фокус>» → «/map build ».
			if m.showPopup {
				name := commandMenuLiteral(m.popupFiltered[m.popupIdx].name)
				m.textarea.SetValue("/" + name + " ")
				m.textarea.CursorEnd()
				m.showPopup = false
				return m, nil
			}
			// Enter без popup — выполнить
			line := strings.TrimSpace(m.textarea.Value())
			if line == "" {
				return m, nil
			}
			m.textarea.Reset()
			cmd := m.runCommandAsync(line)
			// Если команда async — спиннер должен тикать. Sync-команды (clear/help/exit)
			// возвращают nil cmd и не требуют спиннера.
			if m.busy {
				return m, tea.Batch(cmd, m.spinner.Tick)
			}
			return m, cmd

		case "up":
			if m.showPopup {
				if m.popupIdx > 0 {
					m.popupIdx--
				}
				return m, nil
			}
			m.viewport.LineUp(1)
			return m, nil

		case "down":
			if m.showPopup {
				if m.popupIdx < len(m.popupFiltered)-1 {
					m.popupIdx++
				}
				return m, nil
			}
			m.viewport.LineDown(1)
			return m, nil

		case "pgup":
			m.viewport.HalfViewUp()
			return m, nil

		case "pgdown":
			m.viewport.HalfViewDown()
			return m, nil

		case "home":
			m.viewport.GotoTop()
			return m, nil

		case "end":
			m.viewport.GotoBottom()
			return m, nil
		}

		// Динамический popup: показать/скрыть по содержимому textarea
		val := m.textarea.Value()
		if strings.HasPrefix(val, "/") {
			prefix := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(val, "/")))
			if m.isExactCommand(val) {
				// Пользователь ввёл команду целиком (/clear, /help и т.д.) —
				// popup не нужен, иначе Enter перехватит его и подставит
				// первый элемент (/search) вместо того, что ввёл пользователь.
				m.showPopup = false
			} else {
				// Фильтруем команды по префиксу — автодополнение в стиле bash.
				m.popupFiltered = filterCommands(prefix, m.popupItems)
				if len(m.popupFiltered) == 0 {
					m.showPopup = false
				} else {
					m.showPopup = true
					m.popupIdx = 0
				}
			}
		} else {
			m.showPopup = false
		}
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

// runCommandAsync запускает навигационные команды сразу, а операции с базой —
// асинхронно в горутине через tea.Cmd.
// Пока асинхронная команда выполняется, в TUI крутится спиннер.
func (m *tuiModel) runCommandAsync(line string) tea.Cmd {
	if m.busy {
		m.appendBlock(tuiStyles.Status.Render("Ошибка: дождитесь завершения текущей операции"))
		m.appendSeparator()
		return nil
	}
	cmd, args, err := parseTUICommandLine(line)
	if err != nil {
		m.appendBlock(tuiStyles.UserLine.Render("> " + line))
		m.appendBlock(tuiStyles.Status.Render("Ошибка: " + err.Error()))
		m.appendSeparator()
		return nil
	}
	if cmd == "" {
		return nil
	}
	m.appendBlock(tuiStyles.UserLine.Render("> " + redactTUICommand(cmd, args, line)))

	// Sync-команды выполняются сразу, без горутины и спиннера
	switch cmd {
	case "clear", "home":
		m.returnHome()
		return nil
	case "help", "?":
		m.printHelp()
		return nil
	case "exit":
		m.quitting = true
		return tea.Quit
	case "repl":
		m.replRequested = true
		m.quitting = true
		return tea.Quit
	case "open", "use":
		root, err := resolveDatabaseRootArg(args)
		if err != nil {
			m.appendBlock(tuiStyles.Status.Render("Ошибка: " + err.Error()))
			m.appendSeparator()
			return nil
		}
		m.openPath = root
		m.quitting = true
		return tea.Quit
	}

	// Асинхронные команды — спиннер + поток stdout/stderr в event loop.
	m.busy = true
	m.activeCommand = cmd
	m.commandStarted = time.Now()
	m.progressUpdates = 0
	m.appendBlock(tuiStyles.Status.Render(fmt.Sprintf("[RUN] /%s запущена", cmd)))

	// Локальные копии указателей (для горутины, чтобы не залипала m целиком)
	cfg := m.cfg
	store := m.store
	events := make(chan tea.Msg, 64)
	m.commandEvents = events

	return func() tea.Msg {
		go runTUICommand(events, cfg, store, cmd, args)
		return <-events
	}
}

func runTUICommand(events chan<- tea.Msg, cfg *Config, store *Store, cmd string, args []string) {
	var (
		commandErr   error
		refreshedCfg *Config
	)
	defer func() {
		if r := recover(); r != nil {
			commandErr = fmt.Errorf("паника в обработчике: %v", r)
		}
		events <- execResultMsg{err: commandErr, cfg: refreshedCfg}
		close(events)
	}()

	captureCommandOutputStream(
		func() { commandErr = executeTUICommand(cfg, store, cmd, args) },
		func(output string) { events <- commandProgressMsg{output: output} },
	)
	if commandErr == nil && cmd == "config" {
		refreshedCfg, commandErr = loadConfig()
	}
}

func waitForCommandEvent(events <-chan tea.Msg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return execResultMsg{err: fmt.Errorf("канал выполнения команды неожиданно закрыт")}
		}
		return msg
	}
}

// executeTUICommand is the shared TUI dispatcher. Every operational command
// exposed by `mem <command>` is routed to the same handler as the CLI.
func executeTUICommand(cfg *Config, store *Store, cmd string, args []string) error {
	switch cmd {
	case "add":
		return handleAdd(cfg, store, args)
	case "search":
		return handleSearch(cfg, store, args)
	case "ask":
		return handleAsk(cfg, store, args)
	case "map":
		if len(args) > 0 && args[0] == "open" {
			return fmt.Errorf("map open управляет сервером до Ctrl+C; на первом этапе запустите `mem map open` из PowerShell")
		}
		return handleMap(cfg, store, args)
	case "recent":
		return handleRecent(store, args)
	case "add-file":
		return handleAddFile(cfg, store, args)
	case "import":
		return handleImport(cfg, store, args)
	case "config":
		return handleConfig(args)
	case "stats":
		return handleStats(store)
	case "index":
		return handleIndex(cfg, store, args)
	case "source", "show", "get", "view":
		return handleShow(store, args)
	case "sources":
		return handleSources(store)
	case "delete", "rm":
		return handleDelete(store, args)
	case "edit":
		return handleEdit(cfg, store, args)
	case "retag", "tags":
		return handleRetag(store, args)
	case "important", "imp":
		return handleImportant(store, args)
	case "where", "current":
		handleWhere(store)
		return nil
	case "version":
		printVersion()
		return nil
	case "init":
		fmt.Printf("[OK] Активная база уже инициализирована: %s\n", filepath.Dir(store.Path()))
		return nil
	default:
		return fmt.Errorf("неизвестная команда: /%s (введите /help)", cmd)
	}
}

// parseTUICommandLine сохраняет пробелы внутри кавычек и Windows-пути с '\\'.
// В отличие от strings.Fields это позволяет вводить, например,
// /import "D:\\Мои книги\\manual.pdf" -tags "Журнал Радио".
func parseTUICommandLine(line string) (string, []string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil, nil
	}
	if !strings.HasPrefix(line, "/") {
		return "search", []string{line}, nil
	}
	parts, err := splitTUIArguments(strings.TrimPrefix(line, "/"))
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return strings.ToLower(parts[0]), parts[1:], nil
}

func splitTUIArguments(line string) ([]string, error) {
	var (
		parts        []string
		current      strings.Builder
		quote        rune
		tokenStarted bool
	)
	runes := []rune(line)
	flush := func() {
		if tokenStarted {
			parts = append(parts, current.String())
			current.Reset()
			tokenStarted = false
		}
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			tokenStarted = true
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			tokenStarted = true
		case ' ', '\t', '\r', '\n':
			flush()
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

func redactTUICommand(cmd string, args []string, original string) string {
	if cmd == "config" && len(args) > 0 && args[0] == "set-polza-key" {
		return "/config set-polza-key ***"
	}
	return original
}

// printHelp показывает список команд.
func (m *tuiModel) printHelp() {
	m.appendBlock(tuiStyles.Header.Render("/-команды mem TUI"))
	for _, c := range m.commandMenu() {
		m.appendBlock(fmt.Sprintf("  %s  %s",
			tuiStyles.Header.Render("/"+c.name),
			c.desc))
	}
	m.appendBlock(tuiStyles.Status.Render("Tab/↑↓: навигация · Enter: выполнить · Esc: главный экран · выход: /exit или Ctrl+C×2"))
}

// commandMenu возвращает список команд для help и popup.
func (m *tuiModel) commandMenu() []commandMenuEntry {
	return m.popupItems
}

func tuiCommandMenuItems() []commandMenuEntry {
	return []commandMenuEntry{
		{"search <запрос> [флаги]", "гибридный поиск (или просто текст без /)"},
		{"ask <вопрос> [флаги]", "ответ только по подтверждённым evidence"},
		{"add <текст> [флаги]", "сохранить новую запись"},
		{"add-file <путь> [флаги]", "добавить UTF-8 файл; PDF/DjVu → import"},
		{"import <путь> [флаги]", "импортировать Markdown/PDF/DjVu с provenance"},
		{"index <путь>", "проиндексировать файл или каталог"},
		{"recent [-limit N]", "последние записи"},
		{"show <id> [--from-file путь]", "показать запись или chunks документа"},
		{"source <id>", "показать запись с данными источника"},
		{"sources", "список проиндексированных документов"},
		{"delete <id>", "удалить запись"},
		{"edit <id> <текст> [флаги]", "изменить запись"},
		{"retag <id> -tags <теги>", "заменить теги записи"},
		{"important <id>", "переключить важность"},
		{"stats", "статистика базы"},
		{"where", "абсолютный путь активной базы"},
		{"config", "показать конфигурацию"},
		{"config set-backend <ollama|polza>", "выбрать embedding-бэкенд"},
		{"config set-polza-key <api_key>", "задать ключ Polza (в TUI скрывается)"},
		{"config set-polza-model <model>", "задать embedding-модель Polza"},
		{"config set-ollama-model <model>", "задать embedding-модель Ollama"},
		{"config set-answer-model <model>", "задать локальную answer-модель"},
		{"config set-answer-base-url <url>", "задать loopback URL answer API"},
		{"config set-answer-timeout <сек>", "задать таймаут ответа"},
		{"config set-answer-max-tokens <N>", "задать предел токенов ответа"},
		{"config set-answer-context-chars <N>", "задать бюджет evidence"},
		{"config set-chunk-size <N>", "задать размер chunk"},
		{"config set-chunk-overlap <N>", "задать перекрытие chunks"},
		{"config set-chunk-strategy <стратегия>", "задать стратегию chunking"},
		{"map build <фокус> [флаги]", "построить draft knowledge graph"},
		{"map analyze <фокус> [флаги]", "найти противоречия и пробелы"},
		{"map open [флаги]", "открыть живую карту (из PowerShell)"},
		{"map duplicates [флаги]", "найти семантические дубли узлов"},
		{"map merge-node <manifest.json>", "подтвердить объединение узла"},
		{"map merges [флаги]", "показать историю объединений"},
		{"map runs [флаги]", "показать analysis runs"},
		{"map run <run-id> [--json]", "показать один analysis run"},
		{"map prune-runs <флаги>", "очистить старые завершённые runs"},
		{"map status [--json]", "review-состояние карты"},
		{"map approve <тип> <id> <флаги>", "подтвердить draft-объект"},
		{"map approve-batch <manifest.json>", "атомарно подтвердить пакет"},
		{"map reviews [флаги]", "журнал review-решений"},
		{"map export", "вывести граф в JSON"},
		{"map export-html <output.html> [флаги]", "создать автономную HTML-карту"},
		{"open <путь>", "открыть другую локальную базу"},
		{"init", "проверить, что активная база инициализирована"},
		{"version", "показать версию программы"},
		{"repl", "переключиться в классический REPL"},
		{"home", "вернуться на главный экран"},
		{"clear", "очистить вывод и открыть главный экран"},
		{"help", "полный список TUI-команд"},
		{"exit", "выйти из TUI"},
	}
}

// isExactCommand возвращает true, если val — это "/<cmd>" где <cmd> — точное
// имя команды из commandMenu (без алиасов и плейсхолдеров вида "<id>").
// В этом случае popup не нужен: пользователь уже ввёл команду целиком,
// Enter должен её выполнить, а не подменять на первый элемент popup'а.
func (m *tuiModel) isExactCommand(val string) bool {
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(val, "/")))
	if name == "" {
		return false
	}
	for _, item := range m.popupItems {
		if strings.EqualFold(commandMenuLiteral(item.name), name) {
			return true
		}
	}
	return false
}

// filterCommands возвращает подмножество команд из items, имена которых
// (без плейсхолдера) начинаются с prefix (регистр игнорируется).
// Используется для автодополнения в popup: ввёл "/cl" → остаются только
// команды, начинающиеся с "cl" (например, "clear").
// При пустом prefix возвращает исходный список.
func filterCommands(prefix string, items []commandMenuEntry) []commandMenuEntry {
	if prefix == "" {
		return items
	}
	p := strings.ToLower(prefix)
	var out []commandMenuEntry
	for _, item := range items {
		cmdName := commandMenuLiteral(item.name)
		if strings.HasPrefix(strings.ToLower(cmdName), p) {
			out = append(out, item)
		}
	}
	return out
}

// commandMenuLiteral removes argument placeholders but retains real
// subcommands: "map build <фокус> [флаги]" becomes "map build".
func commandMenuLiteral(name string) string {
	fields := strings.Fields(name)
	literal := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.HasPrefix(field, "<") || strings.HasPrefix(field, "[") {
			break
		}
		literal = append(literal, field)
	}
	return strings.Join(literal, " ")
}

// appendBlock добавляет блок текста в вывод.
// При превышении maxOutputLines старые блоки отбрасываются — защита от
// неконтролируемого роста буфера в длинных сессиях.
func (m *tuiModel) appendBlock(text string) {
	m.output = append(m.output, text)
	if len(m.output) > maxOutputLines {
		// Отбрасываем самые старые блоки — оставляем только последние maxOutputLines.
		m.output = m.output[len(m.output)-maxOutputLines:]
	}
	m.refreshViewportContent(true)
}

func (m *tuiModel) appendSeparator() {
	m.appendBlock(tuiSeparatorBlock)
}

// renderOutput reflows the complete logical history for the current viewport.
// ansi.Wrap preserves colour escape sequences and Unicode cell widths while
// still breaking an unusually long path/token when it cannot fit on one row.
// Keeping m.output unwrapped is essential: after a resize we must wrap from the
// original text, not from line breaks produced for the previous window width.
func (m tuiModel) renderOutput() string {
	width := m.viewportWidth()
	if width < 1 {
		width = 1
	}
	blocks := make([]string, 0, len(m.output))
	for _, block := range m.output {
		if block == tuiSeparatorBlock {
			blocks = append(blocks, tuiStyles.Separator.Render(strings.Repeat("─", width)))
			continue
		}
		blocks = append(blocks, ansi.Wrap(block, width, " \t"))
	}
	return strings.Join(blocks, "\n")
}

func (m *tuiModel) refreshViewportContent(gotoBottom bool) {
	m.viewport.SetContent(m.renderOutput())
	if gotoBottom {
		m.viewport.GotoBottom()
	}
}

// View — отрисовка.
func (m tuiModel) View() string {
	if m.quitting {
		return tuiStyles.Status.Render("До встречи!\n")
	}

	header := m.headerLine()
	viewportView := tuiStyles.Frame.
		Width(m.viewportWidth()).
		Height(m.viewportHeight()).
		Render(m.viewport.View())

	var popupView string
	if m.showPopup {
		popupView = "\n" + m.renderPopup()
	}

	sep := tuiStyles.Separator.Render(strings.Repeat("─", m.viewportWidth()))
	taView := m.textarea.View()
	status := tuiStyles.Status.Render("Enter: выполнить · Esc: главный экран · выход: /exit или Ctrl+C×2 · /: команды")

	if m.busy {
		elapsed := formatTUIDuration(time.Since(m.commandStarted))
		if m.exitAfterBusy {
			status = fmt.Sprintf("%s /%s · %s · обновлений: %d · завершаю перед выходом...",
				m.spinner.View(), m.activeCommand, elapsed, m.progressUpdates)
		} else {
			status = fmt.Sprintf("%s /%s · %s · обновлений: %d · %s",
				m.spinner.View(), m.activeCommand, elapsed, m.progressUpdates, busyText)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Top,
		header,
		sep,
		viewportView+popupView,
		sep,
		taView,
		status,
	)
}

func formatTUIDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}

// recomputeSizes пересчитывает размеры при изменении окна.
func (m *tuiModel) recomputeSizes() {
	if m.width > 4 {
		wasAtBottom := m.viewport.AtBottom()
		m.viewport.Width = m.viewportWidth()
		m.viewport.Height = m.viewportHeight()
		m.textarea.SetWidth(m.viewportWidth())
		m.refreshViewportContent(wasAtBottom)
	}
}

func (m tuiModel) viewportWidth() int {
	if m.width < 10 {
		return 80
	}
	return m.width - 2
}

func (m tuiModel) viewportHeight() int {
	if m.height < 10 {
		return 15
	}
	reserved := 8
	if m.showPopup {
		rows := len(m.popupFiltered)
		if rows > maxPopupRows {
			rows = maxPopupRows
		}
		reserved += rows + 3
	}
	h := m.height - reserved
	if h < 5 {
		h = 5
	}
	return h
}

// renderPopup отрисовывает popup со списком команд.
func (m tuiModel) renderPopup() string {
	var lines []string
	start := 0
	if m.popupIdx >= maxPopupRows {
		start = m.popupIdx - maxPopupRows + 1
	}
	end := start + maxPopupRows
	if end > len(m.popupFiltered) {
		end = len(m.popupFiltered)
	}
	if start > 0 {
		lines = append(lines, tuiStyles.Status.Render(fmt.Sprintf("  ↑ ещё %d", start)))
	}
	for i := start; i < end; i++ {
		c := m.popupFiltered[i]
		var row string
		if i == m.popupIdx {
			row = tuiStyles.PopupSel.Render(fmt.Sprintf("▸ /%s  %s", c.name, c.desc))
		} else {
			row = tuiStyles.PopupItem.Render(fmt.Sprintf("  /%s  %s", c.name, c.desc))
		}
		lines = append(lines, row)
	}
	if end < len(m.popupFiltered) {
		lines = append(lines, tuiStyles.Status.Render(fmt.Sprintf("  ↓ ещё %d", len(m.popupFiltered)-end)))
	}
	return tuiStyles.Popup.
		Width(m.viewportWidth()).
		Render(strings.Join(lines, "\n"))
}

// captureStdout выполняет fn, перехватывая её вывод в stdout, и возвращает его
// строкой. Она сохранена отдельно для узких вызовов и тестов.
func captureStdout(fn func()) string {
	return captureProcessOutput(fn, false)
}

// captureCommandOutput направляет в viewport и stdout, и stderr. Это важно для
// ask/map/import: их прогресс по CLI-контракту пишется в stderr и без перехвата
// повреждал бы alt-screen TUI.
func captureCommandOutput(fn func()) string {
	return captureProcessOutput(fn, true)
}

// captureCommandOutputStream redirects stdout and stderr while fn runs and
// emits each complete line as soon as it is written. bufio.Reader is used
// instead of Scanner so large JSON/status lines are not limited to 64 KiB.
func captureCommandOutputStream(fn func(), emit func(string)) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return
	}
	os.Stdout = w
	os.Stderr = w

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.Close()
		reader := bufio.NewReader(r)
		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				emit(strings.TrimRight(line, "\r\n"))
			}
			if readErr != nil {
				return
			}
		}
	}()

	// This cleanup also runs during panic, before runTUICommand converts the
	// panic into execResultMsg. Closing the writer first is required for EOF.
	defer func() {
		_ = w.Close()
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		<-done
	}()
	fn()
}

// captureProcessOutput temporarily redirects process streams. TUI serializes
// command execution with m.busy, so only one command uses this capture at once.
//
// ВАЖНО: w.Close() ДОЛЖЕН быть вызван ДО чтения из done, иначе классический deadlock:
// горутина-ридер ждёт EOF на r → EOF наступит только после w.Close() → а return <-done
// вычисляется до defer → циклическая блокировка. Дефер оставлен как страховка от паники
// в fn() (recover в runCommandAsync ловит панику и шлёт execResultMsg{err}).
func captureProcessOutput(fn func(), includeStderr bool) string {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	os.Stdout = w
	if includeStderr {
		os.Stderr = w
	}

	// Страховка от паники в fn(): закрыть pipe и восстановить streams.
	defer func() {
		_ = w.Close()
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		done <- buf.String()
	}()

	fn()
	// Закрываем w явно ДО <-done — иначе deadlock.
	// Горутина-ридер увидит EOF на r, допишет в buf, закроет r,
	// запишет результат в done (буферизованный канал уже будет иметь значение).
	_ = w.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	return <-done
}

type tuiExitResult struct {
	openPath      string
	replRequested bool
}

// runTui запускает TUI-режим mem.
func runTui(cfg *Config, store *Store) (tuiExitResult, error) {
	m := newTuiModel(cfg, store)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	// В этой версии bubbletea (v1.x) нет Program.Release() — alt-screen cleanup
	// делается через Run() автоматически при нормальном завершении. При panic
	// в Cmd терминал может остаться в alt-screen, поэтому ниже — защита:
	// явный ExitAltScreen/RestoreTerminal через Kill() если Run вернул ошибку.
	defer func() {
		if r := recover(); r != nil {
			_ = p.ReleaseTerminal()
			panic(r) // продолжаем распространение после cleanup
		}
	}()
	finalModel, err := p.Run()
	if err != nil {
		_ = p.ReleaseTerminal()
		return tuiExitResult{}, err
	}
	switch final := finalModel.(type) {
	case tuiModel:
		return tuiExitResult{openPath: final.openPath, replRequested: final.replRequested}, nil
	case *tuiModel:
		return tuiExitResult{openPath: final.openPath, replRequested: final.replRequested}, nil
	default:
		return tuiExitResult{}, nil
	}
}
