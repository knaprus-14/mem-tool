package main

import (
	"strings"
)

// MemCompleter реализует автодополнение для REPL.
//
// Правила:
//   - Tab в начале строки или на пустом месте: список всех /-команд
//   - Tab после "/" + префикса: дополнение команды (/se<TAB> → /search)
//   - Tab после команды и пробела: пусто (аргументы пока не дополняем;
//     историю поиска будем подсказывать в REPL-1.3)
type MemCompleter struct {
	cmds []string
}

// replCommandList — список команд, доступных через префикс /.
// Должен совпадать с тем, что обрабатывает dispatchReplLine.
var replCommandList = []string{
	"/search", "/add", "/recent", "/show", "/get", "/view",
	"/important", "/imp", "/tags", "/retag", "/edit",
	"/delete", "/rm", "/stats", "/sources", "/config",
	"/clear", "/help", "/exit", "/quit",
}

// NewMemCompleter создаёт новый комплитер для REPL.
func NewMemCompleter() *MemCompleter {
	return &MemCompleter{cmds: replCommandList}
}

// Do реализует интерфейс readline.AutoCompleter.
//
// line — текущая строка ввода (от начала до курсора).
// pos — позиция курсора (для line == len(line) при нормальном вводе).
//
// Возвращает:
//   - candidates: варианты суффиксов для подстановки (то, что добавится после курсора)
//   - length: сколько символов от конца line нужно заменить
func (c *MemCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos == 0 {
		return nil, 0
	}

	// Берём подстроку до курсора
	prefix := string(line[:pos])

	// Если в строке есть пробел (после команды) — аргументы не дополняем.
	// Здесь в будущем можно подсказывать ID записей или текст из истории.
	if hasUnquotedSpace(prefix) {
		return nil, 0
	}

	// Иначе — пытаемся дополнить команду.
	// Если строка не начинается с "/" — добавляем его ко всем вариантам.
	var candidates []string
	if strings.HasPrefix(prefix, "/") {
		for _, cmd := range c.cmds {
			if strings.HasPrefix(cmd, prefix) {
				candidates = append(candidates, cmd[len(prefix):])
			}
		}
	} else {
		// Префикс без "/" — например, "se" — подставляем "/search".
		for _, cmd := range c.cmds {
			if strings.HasPrefix(cmd[1:], prefix) { // сравниваем без ведущего "/"
				candidates = append(candidates, cmd[1+len(prefix):])
			}
		}
	}

	if len(candidates) == 0 {
		return nil, 0
	}

	// Превращаем []string в [][]rune, как требует интерфейс readline
	out := make([][]rune, len(candidates))
	for i, s := range candidates {
		out[i] = []rune(s)
	}
	return out, 0
}

// hasUnquotedSpace возвращает true, если в строке есть пробел,
// не заключённый в кавычки. Используется для определения,
// вводим ли мы сейчас команду (без пробела) или её аргументы (после пробела).
func hasUnquotedSpace(s string) bool {
	inSingle := false
	inDouble := false
	for _, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ' ':
			if !inSingle && !inDouble {
				return true
			}
		}
	}
	return false
}
