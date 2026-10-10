package mem

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var outlineMDHeading = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)\s*$`)
var outlineMDList = regexp.MustCompile(`^([ \t]*)(?:[-+*]|[0-9]{1,9}[.)])[ \t]+(.+)$`)
var outlineMDFence = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})(.*)$")

// Markdown import is an outline reader, not a Markdown renderer. Headings and
// list items become nodes; ordinary text and fenced blocks remain text in
// the current node. The original bytes are independently archived by the caller.
func parseOutlineMarkdown(data []byte, filename string) (OutlineImportDraft, error) {
	var out OutlineImportDraft
	if len(data) > MaxOutlineImportBytes {
		return out, ErrOutlineImportLimit
	}
	if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return out, ErrOutlineImportInvalid
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 65536 {
		return out, ErrOutlineImportLimit
	}
	type frame struct{ index, level int }
	var headings, lists []frame
	bodies := map[int]*strings.Builder{}
	bodyRunes := map[int]int{}
	current := -1
	fence := ""
	add := func(label string, parent int) (int, error) {
		label = strings.TrimSpace(label)
		if label == "" {
			return 0, ErrOutlineImportInvalid
		}
		if len(out.Nodes) >= MaxOutlineImportNodes || utf8.RuneCountInString(label) > 512 {
			return 0, ErrOutlineImportLimit
		}
		i := len(out.Nodes)
		n := ClassicMindMapNodeDraft{Ref: fmt.Sprintf("n%d", i+1), Label: label}
		if parent >= 0 {
			n.ParentRef = out.Nodes[parent].Ref
		}
		out.Nodes = append(out.Nodes, n)
		return i, nil
	}
	ensureRoot := func() error {
		if len(out.Nodes) > 0 {
			return nil
		}
		label := strings.TrimSuffix(filename, filepath.Ext(filename))
		if strings.TrimSpace(label) == "" {
			label = "Импорт Markdown"
		}
		i, err := add(label, -1)
		current = i
		return err
	}
	appendBody := func(line string) error {
		if err := ensureRoot(); err != nil {
			return err
		}
		body := bodies[current]
		if body == nil {
			body = &strings.Builder{}
			bodies[current] = body
		}
		separator := 0
		if body.Len() > 0 {
			separator = 1
		}
		count := bodyRunes[current] + separator + utf8.RuneCountInString(line)
		if body.Len()+separator+len(line) > 4*65536 || count > 65536 {
			return ErrOutlineImportLimit
		}
		if separator == 1 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
		bodyRunes[current] = count
		return nil
	}
	for _, line := range lines {
		fm := outlineMDFence.FindStringSubmatch(line)
		if fence != "" {
			if err := appendBody(line); err != nil {
				return out, err
			}
			if fm != nil && fm[1][0] == fence[0] && len(fm[1]) >= len(fence) && strings.TrimSpace(fm[2]) == "" {
				fence = ""
			}
			continue
		}
		if fm != nil {
			fence = fm[1]
			if err := appendBody(line); err != nil {
				return out, err
			}
			continue
		}
		if h := outlineMDHeading.FindStringSubmatch(line); h != nil {
			level := len(h[1])
			label := h[2]
			// Optional closing hashes are syntax only when preceded by whitespace.
			trimmed := strings.TrimRight(label, "#")
			if len(trimmed) < len(label) && len(trimmed) > 0 && (trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t') {
				label = strings.TrimSpace(trimmed)
			}
			if len(out.Nodes) == 0 && level == 1 {
				i, err := add(label, -1)
				if err != nil {
					return out, err
				}
				current = i
				headings = []frame{{i, level}}
				lists = nil
				continue
			}
			if err := ensureRoot(); err != nil {
				return out, err
			}
			for len(headings) > 0 && headings[len(headings)-1].level >= level {
				headings = headings[:len(headings)-1]
			}
			parent := 0
			if len(headings) > 0 {
				parent = headings[len(headings)-1].index
			}
			i, err := add(label, parent)
			if err != nil {
				return out, err
			}
			current = i
			headings = append(headings, frame{i, level})
			lists = nil
			continue
		}
		if m := outlineMDList.FindStringSubmatch(line); m != nil {
			if err := ensureRoot(); err != nil {
				return out, err
			}
			indent := 0
			for _, r := range m[1] {
				if r == '\t' {
					indent += 4 - indent%4
				} else {
					indent++
				}
			}
			for len(lists) > 0 && lists[len(lists)-1].level >= indent {
				lists = lists[:len(lists)-1]
			}
			parent := 0
			if len(headings) > 0 {
				parent = headings[len(headings)-1].index
			}
			if len(lists) > 0 {
				parent = lists[len(lists)-1].index
			}
			i, err := add(m[2], parent)
			if err != nil {
				return out, err
			}
			current = i
			lists = append(lists, frame{i, indent})
			if len(lists)+len(headings) > MaxOutlineImportDepth+1 {
				return out, ErrOutlineImportLimit
			}
			continue
		}
		if strings.TrimSpace(line) == "" && len(out.Nodes) == 0 {
			continue
		}
		if err := appendBody(line); err != nil {
			return out, err
		}
	}
	if len(out.Nodes) == 0 {
		return out, ErrOutlineImportInvalid
	}
	out.Title = out.Nodes[0].Label
	for i, b := range bodies {
		out.Nodes[i].BodyMarkdown = b.String()
	}
	out.Warnings = []string{"Markdown: узлы строятся из заголовков #–###### и пунктов списков; отступ задаёт вложенность, табуляция равна четырём позициям. Прочий текст относится к последнему узлу.", "Markdown: форматирование, ссылки, HTML, таблицы, отметки задач и блоки кода остаются текстом. Они не создают основания, вложения или учебный прогресс; заголовки Setext и YAML не интерпретируются."}
	if fence != "" {
		out.Warnings = append(out.Warnings, "Незакрытый блок кода сохранён как текст до конца файла.")
	}
	return out, nil
}
