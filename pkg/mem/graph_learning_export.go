package mem

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	KnowledgeLearningExportVersion       = 1
	KnowledgeLearningExportAnki          = "anki-tsv"
	KnowledgeLearningExportMarkdown      = "learning-markdown"
	KnowledgeLearningExportCSV           = "learning-csv"
	MaxKnowledgeLearningExportTitleRunes = 256
)

type KnowledgeLearningExportRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	ExpectedRouteDigest    string                    `json:"expected_route_digest"`
	Format                 string                    `json:"format"`
	Title                  string                    `json:"title,omitempty"`
}

type KnowledgeLearningExport struct {
	Filename    string
	ContentType string
	Content     []byte
}

type knowledgeLearningExportItem struct {
	Route   KnowledgeLearningRouteItem
	History KnowledgeLearningHistoryItem
}

type knowledgeLearningExportData struct {
	Title     string
	Generated time.Time
	Route     KnowledgeLearningRoute
	History   KnowledgeLearningHistory
	Items     []knowledgeLearningExportItem
}

// ExportKnowledgeLearning produces a read-only artifact from one exact,
// reviewed learning route. It never invokes a model and does not modify the
// graph or the local scheduler.
func (s *Store) ExportKnowledgeLearning(request KnowledgeLearningExportRequest) (KnowledgeLearningExport, error) {
	return s.exportKnowledgeLearningAt(request, time.Now().UTC())
}

func (s *Store) exportKnowledgeLearningAt(request KnowledgeLearningExportRequest, now time.Time) (KnowledgeLearningExport, error) {
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" {
		request.Title = "mem-tool"
	}
	if !utf8.ValidString(request.Title) || utf8.RuneCountInString(request.Title) > MaxKnowledgeLearningExportTitleRunes || strings.ContainsAny(request.Title, "\r\n\t") {
		return KnowledgeLearningExport{}, fmt.Errorf("knowledge learning export title must be one line containing 1..%d runes", MaxKnowledgeLearningExportTitleRunes)
	}
	switch request.Format {
	case KnowledgeLearningExportAnki, KnowledgeLearningExportMarkdown, KnowledgeLearningExportCSV:
	default:
		return KnowledgeLearningExport{}, fmt.Errorf("unsupported knowledge learning export format %q", request.Format)
	}
	if strings.TrimSpace(request.ExpectedRouteDigest) == "" {
		return KnowledgeLearningExport{}, fmt.Errorf("knowledge learning export requires a pinned route digest")
	}
	route, err := s.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{
		Selection: request.Selection, ExpectedManifestDigest: request.ExpectedManifestDigest,
	})
	if err != nil {
		return KnowledgeLearningExport{}, err
	}
	if route.Digest != request.ExpectedRouteDigest {
		return KnowledgeLearningExport{}, fmt.Errorf("%w: learning route changed", ErrKnowledgeSelectionChanged)
	}
	if !route.Ready {
		return KnowledgeLearningExport{}, fmt.Errorf("knowledge learning route is not ready for export")
	}
	history, err := s.buildKnowledgeLearningHistoryForRouteAt(route, now)
	if err != nil {
		return KnowledgeLearningExport{}, err
	}
	historyByID := make(map[string]KnowledgeLearningHistoryItem, len(history.Items))
	for _, item := range history.Items {
		historyByID[item.NodeID] = item
	}
	data := knowledgeLearningExportData{Title: request.Title, Generated: now, Route: route, History: history}
	for _, item := range route.Items {
		historyItem, ok := historyByID[item.ID]
		if !ok {
			return KnowledgeLearningExport{}, fmt.Errorf("%w: learning history is missing node %q", ErrKnowledgeSelectionChanged, item.ID)
		}
		data.Items = append(data.Items, knowledgeLearningExportItem{Route: item, History: historyItem})
	}
	result := KnowledgeLearningExport{}
	switch request.Format {
	case KnowledgeLearningExportAnki:
		result.Content, err = renderKnowledgeLearningAnkiTSV(data)
		result.Filename, result.ContentType = "mem-learning-anki.txt", "text/plain; charset=utf-8"
	case KnowledgeLearningExportMarkdown:
		result.Content, err = renderKnowledgeLearningMarkdown(data)
		result.Filename, result.ContentType = "mem-learning.md", "text/markdown; charset=utf-8"
	case KnowledgeLearningExportCSV:
		result.Content, err = renderKnowledgeLearningCSV(data)
		result.Filename, result.ContentType = "mem-learning.csv", "text/csv; charset=utf-8"
	}
	if err != nil {
		return KnowledgeLearningExport{}, err
	}
	return result, nil
}

func renderKnowledgeLearningAnkiTSV(data knowledgeLearningExportData) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("#separator:Tab\n#html:true\n#deck:" + data.Title + "\n#columns:Front\tBack\tTags\n#tags column:3\n")
	writer := csv.NewWriter(&buffer)
	writer.Comma = '\t'
	writer.UseCRLF = false
	for _, item := range data.Items {
		// A hidden stable ID keeps otherwise identical prompts distinct during
		// Anki's first-field duplicate detection without changing the visible
		// front of a standard Basic card.
		front := `<span style="display:none">mem-tool:` + html.EscapeString(item.Route.ID) + `</span>` + html.EscapeString(item.Route.Prompt)
		back := "<div>" + learningHTMLText(item.Route.Answer) + "</div><hr><small>" +
			html.EscapeString(knowledgeLearningScheduleText(item.History)) + "<br>" +
			html.EscapeString(knowledgeLearningSourcesText(item.Route.Sources)) + "<br>mem-tool ID: " + html.EscapeString(item.Route.ID) + "</small>"
		tags := strings.Join([]string{"mem_tool", "mem_tool::" + string(item.Route.Kind), "mem_tool::level_" + strconv.Itoa(item.Route.Level+1), "mem_tool::node_" + knowledgeLearningAnkiTag(item.Route.ID)}, " ")
		if err := writer.Write([]string{front, back, tags}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("encode Anki learning export: %w", err)
	}
	return buffer.Bytes(), nil
}

func renderKnowledgeLearningMarkdown(data knowledgeLearningExportData) ([]byte, error) {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n", markdownInline(data.Title))
	fmt.Fprintf(&body, "Сформировано: %s. Подтверждённых учебных объектов: %d; новых: %d; назначенных: %d; к повторению сейчас: %d.\n\n", data.Generated.Format(time.RFC3339), len(data.Items), data.History.New, data.History.Scheduled, data.History.Due)
	body.WriteString("> Расписание ниже принадлежит mem-tool. Текстовый импорт Anki переносит вопросы, ответы, источники и это состояние как справочные данные, но не заменяет внутренний планировщик Anki.\n\n")
	currentLevel := -1
	for _, item := range data.Items {
		if item.Route.Level != currentLevel {
			currentLevel = item.Route.Level
			fmt.Fprintf(&body, "## Этап %d\n\n", currentLevel+1)
		}
		fmt.Fprintf(&body, "### %s\n\n", markdownInline(item.Route.Prompt))
		fmt.Fprintf(&body, "%s\n\n", strings.TrimSpace(item.Route.Answer))
		fmt.Fprintf(&body, "- Тип: `%s`\n- Состояние: %s\n- Попыток: %d\n", item.Route.Kind, markdownInline(knowledgeLearningScheduleText(item.History)), item.History.Attempts)
		for _, source := range item.Route.Sources {
			fmt.Fprintf(&body, "- Источник: %s\n", markdownInline(formatKnowledgeSelectionSourceRef(source)))
		}
		fmt.Fprintf(&body, "- ID: `%s`\n\n", item.Route.ID)
	}
	body.WriteString("<details>\n<summary>Техническая привязка</summary>\n\n")
	fmt.Fprintf(&body, "- Версия формата: %d\n- Manifest: `%s`\n- Route: `%s`\n", KnowledgeLearningExportVersion, data.Route.ManifestDigest, data.Route.Digest)
	body.WriteString("\n</details>\n")
	return []byte(body.String()), nil
}

func renderKnowledgeLearningCSV(data knowledgeLearningExportData) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(&buffer)
	writer.Comma = ';'
	header := []string{"ID", "Тип", "Этап", "Вопрос", "Ответ", "Состояние", "Следующее повторение", "Интервал, сек", "Коэффициент", "Повторений", "Ошибок", "Последняя оценка", "Попыток", "Источник", "Страницы", "Координаты"}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, item := range data.Items {
		paths, pages, coordinates := knowledgeSelectionExportSources(item.Route.Sources)
		state := item.History.State
		row := []string{item.Route.ID, string(item.Route.Kind), strconv.Itoa(item.Route.Level + 1), item.Route.Prompt, item.Route.Answer, knowledgeLearningScheduleStatus(item.History), "", "0", "0", "0", "0", "", strconv.Itoa(item.History.Attempts), strings.Join(paths, " | "), strings.Join(pages, ", "), strings.Join(coordinates, " | ")}
		if state != nil {
			row[6], row[7], row[8], row[9], row[10], row[11] = state.DueAt, strconv.FormatInt(state.IntervalSeconds, 10), strconv.FormatFloat(float64(state.EasePermille)/1000, 'f', 3, 64), strconv.Itoa(state.Repetitions), strconv.Itoa(state.Lapses), string(state.LastGrade)
		}
		for index := range row {
			row[index] = safeSpreadsheetCell(row[index])
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("encode learning CSV export: %w", err)
	}
	return buffer.Bytes(), nil
}

func knowledgeLearningScheduleStatus(item KnowledgeLearningHistoryItem) string {
	if item.ScheduleReset {
		return "расписание сброшено после изменения"
	}
	if item.State == nil {
		return "новая"
	}
	if item.Due {
		return "готова к повторению"
	}
	return "назначена"
}

func knowledgeLearningScheduleText(item KnowledgeLearningHistoryItem) string {
	status := knowledgeLearningScheduleStatus(item)
	if item.State == nil {
		return status
	}
	return fmt.Sprintf("%s; следующее повторение %s; интервал %s; повторений %d; ошибок %d", status, item.State.DueAt, knowledgeLearningInterval(item.State.IntervalSeconds), item.State.Repetitions, item.State.Lapses)
}

func knowledgeLearningInterval(seconds int64) string {
	switch {
	case seconds < 3600:
		return strconv.FormatInt(seconds/60, 10) + " мин"
	case seconds < 86400:
		return strconv.FormatInt(seconds/3600, 10) + " ч"
	default:
		return strconv.FormatInt(seconds/86400, 10) + " дн"
	}
}

func knowledgeLearningSourcesText(sources []KnowledgeSelectionSourceRef) string {
	if len(sources) == 0 {
		return "Источник: не прикреплён"
	}
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		values = append(values, formatKnowledgeSelectionSourceRef(source))
	}
	sort.Strings(values)
	return "Источник: " + strings.Join(values, " | ")
}

func learningHTMLText(value string) string {
	return strings.ReplaceAll(html.EscapeString(strings.TrimSpace(value)), "\n", "<br>")
}

func knowledgeLearningAnkiTag(value string) string {
	var result strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "unknown"
	}
	return result.String()
}
