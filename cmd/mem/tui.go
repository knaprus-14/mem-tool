package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
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

// tuiModel — состояние TUI.
type tuiModel struct {
	width      int
	height     int
	viewport   viewport.Model
	textarea   textarea.Model
	spinner    spinner.Model
	output     []string
	showPopup  bool
	popupIdx   int
	popupItems []commandMenuEntry // полный список команд (для /help)
	popupFiltered []commandMenuEntry // отфильтрованный по префиксу ввода (для popup)
	busy       bool
	cfg        *Config
	store      *Store
	quitting   bool
}

// execResultMsg — результат выполнения команды.
type execResultMsg struct {
	output string
	err    error
}

// newTuiModel создаёт начальную модель TUI.
func newTuiModel(cfg *Config, store *Store) tuiModel {
	ta := textarea.New()
	ta.Placeholder = "Введите запрос или /help"
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetEnabled(true)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))

	m := tuiModel{
		viewport:       vp,
		textarea:       ta,
		spinner:        sp,
		popupItems:     commandMenu,
		popupFiltered:  commandMenu,
		cfg:            cfg,
		store:          store,
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
	return tuiStyles.Header.Render(fmt.Sprintf(
		"mem · поисковая база · %d записей · backend: %s · %s", total, backend, model))
}

// printHeader добавляет приветствие в viewport (для /clear и стартового экрана).
// Динамический заголовок (headerLine) НЕ выводится — он уже отрисован в View() сверху.
func (m *tuiModel) printHeader() {
	m.appendBlock(tuiStyles.Status.Render("Введите запрос, /help для списка команд, Esc/Ctrl-D для выхода."))
	m.appendBlock(tuiStyles.Separator.Render(strings.Repeat("─", m.viewportWidth())))
}

// Init — инициализация модели.
func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, spinner.Tick)
}

// Update — обработка сообщений.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	m.spinner, spCmd = m.spinner.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recomputeSizes()
		return m, nil

	case execResultMsg:
		// Результат выполнения команды
		m.busy = false
		if msg.err != nil {
			m.appendBlock(tuiStyles.Status.Render("Ошибка: " + msg.err.Error()))
		}
		if msg.output != "" {
			m.appendBlock(msg.output)
		}
		m.appendBlock(tuiStyles.Separator.Render(strings.Repeat("─", m.viewportWidth())))
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "ctrl+d":
			m.quitting = true
			return m, tea.Quit

		case "esc":
			if m.showPopup {
				m.showPopup = false
				m.textarea.SetValue("")
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit

		case "enter":
			// Если popup открыт — выбор команды.
			// Берём только имя (без плейсхолдера вроде «<id>»/«<текст>»):
			// иначе пользователь допишет аргумент после плейсхолдера и
			// парсер передаст «<id>» в хендлер как ID — будет «не число».
			if m.showPopup {
				name := m.popupFiltered[m.popupIdx].name
				if i := strings.IndexByte(name, ' '); i >= 0 {
					name = name[:i]
				}
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
			return m, m.runCommandAsync(line)

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
				if m.popupIdx < len(m.popupItems)-1 {
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
		if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
			prefix := strings.ToLower(strings.TrimPrefix(val, "/"))
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

	return m, tea.Batch(tiCmd, vpCmd, spCmd)
}

// runCommandAsync запускает команду: sync (/clear, /help, /exit) сразу,
// остальные — асинхронно в горутине через tea.Cmd.
// Пока асинхронная команда выполняется, в TUI крутится спиннер.
func (m *tuiModel) runCommandAsync(line string) tea.Cmd {
	m.appendBlock(tuiStyles.UserLine.Render("> " + line))

	// Парсим команду
	var cmd string
	var args []string
	if strings.HasPrefix(line, "/") {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			return nil
		}
		cmd = strings.ToLower(strings.TrimPrefix(parts[0], "/"))
		args = parts[1:]
	} else {
		cmd = "search"
		args = []string{line}
	}

	// Sync-команды выполняются сразу, без горутины и спиннера
	switch cmd {
	case "clear":
		m.output = nil
		m.viewport.SetContent("")
		m.printHeader()
		return nil
	case "help", "?":
		m.printHelp()
		return nil
	case "exit", "quit", "q":
		m.quitting = true
		return tea.Quit
	}

	// Асинхронные команды — спиннер + горутина
	m.busy = true

	// Локальные копии указателей (для горутины, чтобы не залипала m целиком)
	cfg := m.cfg
	store := m.store

	return func() (msg tea.Msg) {
		// recover превращает панику хендлера в обычный err,
		// иначе TUI залипнет в busy=true навсегда.
		defer func() {
			if r := recover(); r != nil {
				msg = execResultMsg{err: fmt.Errorf("паника в обработчике: %v", r)}
			}
		}()

		var result string
		var err error

		// runWithCapture выполняет хендлер, перехватывая его stdout, и возвращает
		// и вывод, и ошибку хендлера — чтобы TUI мог показать ошибку в viewport,
		// не убивая процесс (раньше хендлер делал os.Exit(1) и TUI умирал).
		runWithCapture := func(fn func() error) {
			var hErr error
			result = captureStdout(func() { hErr = fn() })
			if hErr != nil {
				err = hErr
			}
		}

		switch cmd {
		case "search":
			runWithCapture(func() error { return handleSearch(cfg, store, args) })
		case "add":
			runWithCapture(func() error { return handleAdd(cfg, store, args) })
		case "recent":
			runWithCapture(func() error { return handleRecent(store, args) })
		case "show", "get", "view", "source":
			runWithCapture(func() error { return handleShow(store, args) })
		case "important", "imp":
			runWithCapture(func() error { return handleImportant(store, args) })
		case "tags", "retag":
			runWithCapture(func() error { return handleRetag(store, args) })
		case "edit":
			runWithCapture(func() error { return handleEdit(cfg, store, args) })
		case "delete", "rm":
			runWithCapture(func() error { return handleDelete(store, args) })
		case "stats":
			runWithCapture(func() error { return handleStats(store) })
		case "sources":
			runWithCapture(func() error { return handleSources(store) })
		case "config":
			runWithCapture(func() error { return handleConfig(args) })
		default:
			err = fmt.Errorf("неизвестная команда: /%s", cmd)
		}
		msg = execResultMsg{output: result, err: err}
		return
	}
}

// printHelp показывает список команд.
func (m *tuiModel) printHelp() {
	m.appendBlock(tuiStyles.Header.Render("/-команды mem TUI"))
	for _, c := range m.commandMenu() {
		m.appendBlock(fmt.Sprintf("  %s  %s",
			tuiStyles.Header.Render("/"+c.name),
			c.desc))
	}
	m.appendBlock(tuiStyles.Status.Render("Tab/↑↓: навигация · Enter: выполнить · Esc/Ctrl-D: выход"))
}

// commandMenu возвращает список команд для help и popup.
func (m *tuiModel) commandMenu() []commandMenuEntry {
	return commandMenu
}

// isExactCommand возвращает true, если val — это "/<cmd>" где <cmd> — точное
// имя команды из commandMenu (без алиасов и плейсхолдеров вида "<id>").
// В этом случае popup не нужен: пользователь уже ввёл команду целиком,
// Enter должен её выполнить, а не подменять на первый элемент popup'а.
func (m *tuiModel) isExactCommand(val string) bool {
	parts := strings.Fields(val)
	if len(parts) != 1 {
		return false
	}
	name := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	if name == "" {
		return false
	}
	for _, item := range m.popupItems {
		cmdName := item.name
		if i := strings.IndexByte(cmdName, ' '); i >= 0 {
			cmdName = cmdName[:i]
		}
		if cmdName == name {
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
		cmdName := item.name
		if i := strings.IndexByte(cmdName, ' '); i >= 0 {
			cmdName = cmdName[:i]
		}
		if strings.HasPrefix(strings.ToLower(cmdName), p) {
			out = append(out, item)
		}
	}
	return out
}

// appendBlock добавляет блок текста в вывод.
func (m *tuiModel) appendBlock(text string) {
	m.output = append(m.output, text)
	m.viewport.SetContent(strings.Join(m.output, "\n"))
	m.viewport.GotoBottom()
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
	status := tuiStyles.Status.Render("Enter: выполнить · /<TAB>: команды · Esc/Ctrl-D: выход · ↑/↓/PgUp/PgDn: прокрутка")

	if m.busy {
		status = m.spinner.View() + " " + busyText
	}

	return fmt.Sprintf("%s\n%s\n%s%s\n%s\n%s\n%s",
		header, sep, viewportView, popupView, sep, taView, status)
}

// recomputeSizes пересчитывает размеры при изменении окна.
func (m *tuiModel) recomputeSizes() {
	if m.width > 4 {
		m.viewport.Width = m.viewportWidth()
		m.viewport.Height = m.viewportHeight()
		m.textarea.SetWidth(m.viewportWidth())
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
		reserved += len(m.popupFiltered) + 3
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
	for i, c := range m.popupFiltered {
		var row string
		if i == m.popupIdx {
			row = tuiStyles.PopupSel.Render(fmt.Sprintf("▸ /%s  %s", c.name, c.desc))
		} else {
			row = tuiStyles.PopupItem.Render(fmt.Sprintf("  /%s  %s", c.name, c.desc))
		}
		lines = append(lines, row)
	}
	return tuiStyles.Popup.
		Width(m.viewportWidth()).
		Render(strings.Join(lines, "\n"))
}

// captureStdout выполняет fn, перехватывая её вывод в stdout, и возвращает его строкой.
// Используется, чтобы вывод хендлеров (handleSearch, handleAdd и т.д.) попадал в viewport TUI,
// а не в реальный stdout (где он смешался бы с TUI-рендерингом).
//
// w.Close() и восстановление os.Stdout вынесены в defer — если fn() запаникует,
// pipe всё равно закроется и io.Copy в горутине увидит EOF. Иначе pipe-FD и
// сама горутина утекают (io.Copy блокируется до EOF на read-end).
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	os.Stdout = w

	defer func() {
		_ = w.Close()
		os.Stdout = oldStdout
	}()

	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	return <-done
}

// runTui запускает TUI-режим mem.
func runTui(cfg *Config, store *Store) {
	m := newTuiModel(cfg, store)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Ошибка TUI: %v\n", err)
	}
}
