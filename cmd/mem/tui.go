package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

// tuiModel — состояние TUI.
type tuiModel struct {
	width      int
	height     int
	viewport   viewport.Model
	textarea   textarea.Model
	output     []string
	showPopup  bool
	popupIdx   int
	popupItems []commandMenuEntry
	cfg        *Config
	store      *Store
	quitting   bool
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

	m := tuiModel{
		viewport:   vp,
		textarea:   ta,
		popupItems: commandMenu,
		cfg:        cfg,
		store:      store,
	}
	m.printHeader()
	return m
}

// printHeader добавляет приветствие в вывод TUI.
func (m *tuiModel) printHeader() {
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
	m.appendBlock(tuiStyles.Header.Render(
		fmt.Sprintf("mem · поисковая база · %d записей · backend: %s · %s", total, backend, model)))
	m.appendBlock(tuiStyles.Status.Render("Введите запрос, /help для списка команд, Esc/Ctrl-D для выхода."))
	m.appendBlock(tuiStyles.Separator.Render(strings.Repeat("─", 60)))
}

// Init — инициализация модели.
func (m tuiModel) Init() tea.Cmd {
	return textarea.Blink
}

// Update — обработка сообщений.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	prevVal := m.textarea.Value()
	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recomputeSizes()
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
			// Если popup открыт — выбор команды
			if m.showPopup {
				cmd := m.popupItems[m.popupIdx].name
				m.textarea.SetValue("/" + cmd + " ")
				m.textarea.CursorEnd()
				m.showPopup = false
				return m, nil
			}
			// Enter без popup — выполнить
			line := strings.TrimSpace(m.textarea.Value())
			if line == "" {
				return m, nil
			}
			m.executeCommand(line)
			m.textarea.Reset()
			return m, nil

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
		_ = prevVal
		if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
			m.showPopup = true
			m.popupIdx = 0
		} else {
			m.showPopup = false
		}
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

// executeCommand выполняет одну строку ввода: парсит /-команду или текст
// (как сокращение для /search), вызывает соответствующий хендлер,
// перехватывая его вывод в stdout, и добавляет результат в viewport.
func (m *tuiModel) executeCommand(line string) {
	m.appendBlock(tuiStyles.UserLine.Render("> " + line))

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

	var result string
	switch cmd {
	case "search":
		result = captureStdout(func() { handleSearch(m.cfg, m.store, args) })
	case "add":
		result = captureStdout(func() { handleAdd(m.cfg, m.store, args) })
	case "recent":
		result = captureStdout(func() { handleRecent(m.store, args) })
	case "show", "get", "view", "source":
		result = captureStdout(func() { handleShow(m.store, args) })
	case "important", "imp":
		result = captureStdout(func() { handleImportant(m.store, args) })
	case "tags", "retag":
		result = captureStdout(func() { handleRetag(m.store, args) })
	case "edit":
		result = captureStdout(func() { handleEdit(m.cfg, m.store, args) })
	case "delete", "rm":
		result = captureStdout(func() { handleDelete(m.store, args) })
	case "stats":
		result = captureStdout(func() { handleStats(m.store) })
	case "sources":
		result = captureStdout(func() { handleSources(m.store) })
	case "config":
		result = captureStdout(func() {
			if err := handleConfig(args); err != nil {
				fmt.Fprintf(os.Stderr, "Ошибка: %v\n", err)
			}
		})
	case "clear":
		m.output = nil
		m.viewport.SetContent("")
		m.printHeader()
		return
	case "help", "?":
		m.printHelp()
		return
	case "exit", "quit", "q":
		m.quitting = true
		// Возвращаем через View, не сразу
		return
	default:
		m.appendBlock(tuiStyles.Status.Render(fmt.Sprintf("Неизвестная команда: /%s. Введите /help.", cmd)))
		return
	}

	if result != "" {
		m.appendBlock(result)
	}
	m.appendBlock(tuiStyles.Separator.Render(strings.Repeat("─", 60)))
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

	header := tuiStyles.Header.Render("mem · TUI")
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
		reserved += len(m.popupItems) + 3
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
	for i, c := range m.popupItems {
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
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = oldStdout
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
