package mem

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	KnowledgeSelectionExportVersion       = 1
	KnowledgeSelectionExportBranch        = "branch-json"
	KnowledgeSelectionExportTable         = "table-csv"
	KnowledgeSelectionExportReport        = "report-markdown"
	KnowledgeSelectionExportPlan          = "plan-markdown"
	KnowledgeSelectionExportChecklist     = "checklist-markdown"
	MaxKnowledgeSelectionExportTitleRunes = 256
)

type KnowledgeSelectionExportRequest struct {
	Selection              KnowledgeSelectionRequest `json:"selection"`
	ExpectedManifestDigest string                    `json:"expected_manifest_digest"`
	Format                 string                    `json:"format"`
	Title                  string                    `json:"title,omitempty"`
}

type KnowledgeSelectionExport struct {
	Filename    string
	ContentType string
	Content     []byte
}

type knowledgeSelectionBranchExport struct {
	Version   int                        `json:"version"`
	Title     string                     `json:"title"`
	Selection KnowledgeSelectionRequest  `json:"selection"`
	Manifest  KnowledgeSelectionManifest `json:"manifest"`
	Analysis  KnowledgeSelectionAnalysis `json:"analysis"`
	Graph     KnowledgeGraph             `json:"graph"`
}

type knowledgeSelectionExportData struct {
	title     string
	selection KnowledgeSelectionRequest
	manifest  KnowledgeSelectionManifest
	analysis  KnowledgeSelectionAnalysis
	graph     KnowledgeGraph
	nodes     map[string]KnowledgeNode
	edges     map[string]KnowledgeEdge
}

// ExportKnowledgeSelection produces a portable, host-rendered artifact from
// one exact pinned selection. It never invokes a model and never mutates the
// graph. Stale/missing evidence remains visible in the export instead of being
// silently discarded.
func (s *Store) ExportKnowledgeSelection(request KnowledgeSelectionExportRequest) (KnowledgeSelectionExport, error) {
	request.Format = strings.ToLower(strings.TrimSpace(request.Format))
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" {
		request.Title = "Выбранная область карты знаний"
	}
	if !utf8.ValidString(request.Title) || utf8.RuneCountInString(request.Title) > MaxKnowledgeSelectionExportTitleRunes {
		return KnowledgeSelectionExport{}, fmt.Errorf("knowledge selection export title must contain 1..%d runes", MaxKnowledgeSelectionExportTitleRunes)
	}
	switch request.Format {
	case KnowledgeSelectionExportBranch, KnowledgeSelectionExportTable, KnowledgeSelectionExportReport,
		KnowledgeSelectionExportPlan, KnowledgeSelectionExportChecklist:
	default:
		return KnowledgeSelectionExport{}, fmt.Errorf("unsupported knowledge selection export format %q", request.Format)
	}

	nodeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.NodeIDs, MaxKnowledgeSelectionNodes, "node")
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	edgeIDs, err := normalizeKnowledgeSelectionIDs(request.Selection.EdgeIDs, MaxKnowledgeSelectionEdges, "edge")
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	request.Selection = KnowledgeSelectionRequest{NodeIDs: nodeIDs, EdgeIDs: edgeIDs}
	analysis, err := s.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{
		Selection: request.Selection, ExpectedManifestDigest: request.ExpectedManifestDigest,
	})
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	manifest, err := s.BuildKnowledgeSelectionManifest(request.Selection)
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	if manifest.Digest != analysis.ManifestDigest {
		return KnowledgeSelectionExport{}, fmt.Errorf("%w: selection changed while export was being prepared", ErrKnowledgeSelectionChanged)
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	data := buildKnowledgeSelectionExportData(request.Title, request.Selection, manifest, analysis, graph)

	var content []byte
	result := KnowledgeSelectionExport{}
	switch request.Format {
	case KnowledgeSelectionExportBranch:
		content, err = renderKnowledgeSelectionBranchJSON(data)
		result.Filename, result.ContentType = "mem-selection-branch.json", "application/json; charset=utf-8"
	case KnowledgeSelectionExportTable:
		content, err = renderKnowledgeSelectionTableCSV(data)
		result.Filename, result.ContentType = "mem-selection-table.csv", "text/csv; charset=utf-8"
	case KnowledgeSelectionExportReport:
		content, err = renderKnowledgeSelectionReportMarkdown(data)
		result.Filename, result.ContentType = "mem-selection-report.md", "text/markdown; charset=utf-8"
	case KnowledgeSelectionExportPlan:
		content, err = renderKnowledgeSelectionPlanMarkdown(data, false)
		result.Filename, result.ContentType = "mem-selection-plan.md", "text/markdown; charset=utf-8"
	case KnowledgeSelectionExportChecklist:
		content, err = renderKnowledgeSelectionPlanMarkdown(data, true)
		result.Filename, result.ContentType = "mem-selection-checklist.md", "text/markdown; charset=utf-8"
	}
	if err != nil {
		return KnowledgeSelectionExport{}, err
	}
	result.Content = content
	return result, nil
}

func buildKnowledgeSelectionExportData(title string, selection KnowledgeSelectionRequest, manifest KnowledgeSelectionManifest, analysis KnowledgeSelectionAnalysis, graph KnowledgeGraph) knowledgeSelectionExportData {
	nodes := make(map[string]KnowledgeNode, len(graph.Nodes))
	edges := make(map[string]KnowledgeEdge, len(graph.Edges))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range graph.Edges {
		edges[edge.ID] = edge
	}
	selectedNodes := make(map[string]bool, len(selection.NodeIDs))
	selectedEdges := make(map[string]bool, len(selection.EdgeIDs)+len(analysis.Internal))
	for _, id := range selection.NodeIDs {
		selectedNodes[id] = true
	}
	for _, id := range selection.EdgeIDs {
		selectedEdges[id] = true
		if edge, ok := edges[id]; ok {
			selectedNodes[edge.From], selectedNodes[edge.To] = true, true
		}
	}
	for _, relation := range analysis.Internal {
		selectedEdges[relation.ID] = true
		selectedNodes[relation.From.ID], selectedNodes[relation.To.ID] = true, true
	}
	branch := KnowledgeGraph{}
	for id := range selectedNodes {
		if node, ok := nodes[id]; ok {
			branch.Nodes = append(branch.Nodes, node)
		}
	}
	for id := range selectedEdges {
		if edge, ok := edges[id]; ok {
			branch.Edges = append(branch.Edges, edge)
		}
	}
	sort.Slice(branch.Nodes, func(i, j int) bool { return branch.Nodes[i].ID < branch.Nodes[j].ID })
	sort.Slice(branch.Edges, func(i, j int) bool { return branch.Edges[i].ID < branch.Edges[j].ID })
	return knowledgeSelectionExportData{title: title, selection: selection, manifest: manifest, analysis: analysis, graph: branch, nodes: nodes, edges: edges}
}

func renderKnowledgeSelectionBranchJSON(data knowledgeSelectionExportData) ([]byte, error) {
	payload := knowledgeSelectionBranchExport{
		Version: KnowledgeSelectionExportVersion, Title: data.title, Selection: data.selection,
		Manifest: data.manifest, Analysis: data.analysis, Graph: data.graph,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode knowledge selection branch: %w", err)
	}
	return append(encoded, '\n'), nil
}

func renderKnowledgeSelectionTableCSV(data knowledgeSelectionExportData) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(&buffer)
	writer.Comma = ';'
	if err := writer.Write([]string{"Тип объекта", "Название", "Тип знания/связи", "Статус", "Происхождение", "Состояние источника", "Содержание", "Источник", "Страницы", "Координаты"}); err != nil {
		return nil, err
	}
	for _, object := range data.analysis.Objects {
		content := ""
		if object.ObjectType == KnowledgeObjectNode {
			content = data.nodes[object.ID].Body
		} else if edge, ok := data.edges[object.ID]; ok {
			content = knowledgeSelectionRelationText(edge, data.nodes)
		}
		paths, pages, coordinates := knowledgeSelectionExportSources(object.Sources)
		row := []string{string(object.ObjectType), object.Label, object.Kind, string(object.Status), string(object.Origin), string(object.EvidenceState), content, strings.Join(paths, " | "), strings.Join(pages, ", "), strings.Join(coordinates, " | ")}
		for i := range row {
			row[i] = safeSpreadsheetCell(row[i])
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("encode knowledge selection table: %w", err)
	}
	return buffer.Bytes(), nil
}

func renderKnowledgeSelectionReportMarkdown(data knowledgeSelectionExportData) ([]byte, error) {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n", markdownInline(data.title))
	s := data.analysis.Summary
	fmt.Fprintf(&body, "Выбрано объектов: %d; документов: %d; внутренних связей: %d; связей за пределами выбора: %d.\n\n", s.Objects, s.Documents, s.InternalRelations, s.BoundaryRelations)
	fmt.Fprintf(&body, "Сравнения: %d; зависимости: %d; противоречия: %d; пробелы: %d.\n\n", s.Comparisons, s.Dependencies, s.Contradictions, s.Gaps)
	appendKnowledgeSelectionWarnings(&body, data)
	body.WriteString("## Выбранные объекты\n\n")
	for _, object := range data.analysis.Objects {
		fmt.Fprintf(&body, "### %s\n\n", markdownInline(firstNonEmpty(object.Label, object.Kind)))
		fmt.Fprintf(&body, "Тип: `%s`; статус: `%s`; происхождение: `%s`; источник: `%s`.\n\n", object.Kind, object.Status, object.Origin, object.EvidenceState)
		if object.ObjectType == KnowledgeObjectNode {
			appendMarkdownQuote(&body, data.nodes[object.ID].Body)
		} else if edge, ok := data.edges[object.ID]; ok {
			fmt.Fprintf(&body, "%s\n\n", markdownInline(knowledgeSelectionRelationText(edge, data.nodes)))
		}
		appendKnowledgeSelectionSources(&body, object.Sources)
	}
	appendKnowledgeSelectionRelations(&body, "Внутренние связи", data.analysis.Internal)
	appendKnowledgeSelectionRelations(&body, "Связи за пределами выбора", data.analysis.Boundary)
	body.WriteString("## Ограничение интерпретации\n\nОтчёт перечисляет только объекты и типизированные связи, уже сохранённые в карте. Нулевой счётчик не доказывает отсутствие свойства в исходном документе.\n\n")
	appendKnowledgeSelectionTechnical(&body, data)
	return []byte(body.String()), nil
}

func renderKnowledgeSelectionPlanMarkdown(data knowledgeSelectionExportData, checklist bool) ([]byte, error) {
	var body strings.Builder
	title := "План: " + data.title
	if checklist {
		title = "Чек-лист: " + data.title
	}
	fmt.Fprintf(&body, "# %s\n\n", markdownInline(title))
	appendKnowledgeSelectionWarnings(&body, data)
	ordered, orderedByRelations, cycle := knowledgeSelectionPlanOrder(data)
	if orderedByRelations {
		body.WriteString("Порядок построен по явно сохранённым связям `prerequisite`, `depends_on`, `precedes` и `happens_before`.\n\n")
	} else {
		body.WriteString("В выборе нет явных связей порядка; пункты расположены детерминированно по названию.\n\n")
	}
	if cycle {
		body.WriteString("> Внимание: в связях порядка найден цикл. Остаток списка добавлен в стабильном алфавитном порядке; цикл требует ручной проверки.\n\n")
	}
	for i, node := range ordered {
		prefix := fmt.Sprintf("%d.", i+1)
		if checklist {
			prefix = "- [ ]"
		}
		fmt.Fprintf(&body, "%s **%s** — `%s`, статус `%s`\n", prefix, markdownInline(node.Label), node.Kind, node.Status)
		if strings.TrimSpace(node.Body) != "" {
			fmt.Fprintf(&body, "   %s\n", markdownInline(singleLine(node.Body)))
		}
		refs := knowledgeSelectionSourceRefs(node.Evidence)
		if len(refs) > 0 {
			fmt.Fprintf(&body, "   Источник: %s\n", markdownInline(formatKnowledgeSelectionSourceRef(refs[0])))
		}
	}
	if len(ordered) == 0 {
		body.WriteString("Выбранные узлы отсутствуют. Выберите узлы карты, а не только связи.\n")
	}
	body.WriteString("\n## Связи и основания\n\n")
	for _, relation := range data.analysis.Internal {
		fmt.Fprintf(&body, "- %s\n", markdownInline(formatKnowledgeSelectionAnalysisRelation(relation)))
		if len(relation.Sources) > 0 {
			fmt.Fprintf(&body, "  Источник: %s\n", markdownInline(formatKnowledgeSelectionSourceRef(relation.Sources[0])))
		}
	}
	if len(data.analysis.Internal) == 0 {
		body.WriteString("- Явные внутренние связи отсутствуют.\n")
	}
	body.WriteString("\n")
	appendKnowledgeSelectionTechnical(&body, data)
	return []byte(body.String()), nil
}

func knowledgeSelectionPlanOrder(data knowledgeSelectionExportData) ([]KnowledgeNode, bool, bool) {
	selected := make(map[string]KnowledgeNode, len(data.selection.NodeIDs))
	for _, id := range data.selection.NodeIDs {
		if node, ok := data.nodes[id]; ok {
			selected[id] = node
		}
	}
	adjacency := make(map[string]map[string]bool)
	indegree := make(map[string]int, len(selected))
	for id := range selected {
		indegree[id] = 0
	}
	hasOrder := false
	for _, relation := range data.analysis.Internal {
		before, after := "", ""
		switch relation.Kind {
		case KnowledgeRelationPrerequisite, KnowledgeRelationPrecedes, KnowledgeRelationHappensBefore:
			before, after = relation.From.ID, relation.To.ID
		case KnowledgeRelationDependsOn:
			before, after = relation.To.ID, relation.From.ID
		default:
			continue
		}
		if _, ok := selected[before]; !ok {
			continue
		}
		if _, ok := selected[after]; !ok || before == after {
			continue
		}
		if adjacency[before] == nil {
			adjacency[before] = make(map[string]bool)
		}
		if !adjacency[before][after] {
			adjacency[before][after] = true
			indegree[after]++
			hasOrder = true
		}
	}
	less := func(a, b KnowledgeNode) bool {
		al, bl := strings.ToLower(a.Label), strings.ToLower(b.Label)
		if al != bl {
			return al < bl
		}
		return a.ID < b.ID
	}
	ready := make([]KnowledgeNode, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, selected[id])
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
	ordered := make([]KnowledgeNode, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		if seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		ordered = append(ordered, node)
		for next := range adjacency[node.ID] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, selected[next])
			}
		}
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
	}
	cycle := len(ordered) != len(selected)
	if cycle {
		remaining := make([]KnowledgeNode, 0, len(selected)-len(ordered))
		for id, node := range selected {
			if !seen[id] {
				remaining = append(remaining, node)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return less(remaining[i], remaining[j]) })
		ordered = append(ordered, remaining...)
	}
	return ordered, hasOrder, cycle
}

func appendKnowledgeSelectionWarnings(body *strings.Builder, data knowledgeSelectionExportData) {
	if len(data.manifest.Blockers) == 0 {
		return
	}
	body.WriteString("> Состояние источников требует внимания:\n>\n")
	for _, blocker := range data.manifest.Blockers {
		fmt.Fprintf(body, "> - %s\n", markdownInline(singleLine(blocker.Message)))
	}
	body.WriteString("\n")
}

func appendKnowledgeSelectionSources(body *strings.Builder, refs []KnowledgeSelectionSourceRef) {
	if len(refs) == 0 {
		body.WriteString("Источник: не прикреплён.\n\n")
		return
	}
	body.WriteString("Источники:\n\n")
	for _, ref := range refs {
		fmt.Fprintf(body, "- %s\n", markdownInline(formatKnowledgeSelectionSourceRef(ref)))
	}
	body.WriteString("\n")
}

func appendKnowledgeSelectionRelations(body *strings.Builder, title string, relations []KnowledgeSelectionAnalysisRelation) {
	if len(relations) == 0 {
		return
	}
	fmt.Fprintf(body, "## %s\n\n", title)
	for _, relation := range relations {
		fmt.Fprintf(body, "- %s\n", markdownInline(formatKnowledgeSelectionAnalysisRelation(relation)))
		if len(relation.Sources) > 0 {
			fmt.Fprintf(body, "  Источник: %s\n", markdownInline(formatKnowledgeSelectionSourceRef(relation.Sources[0])))
		}
	}
	body.WriteString("\n")
}

func appendKnowledgeSelectionTechnical(body *strings.Builder, data knowledgeSelectionExportData) {
	body.WriteString("<details>\n<summary>Технические данные проверки</summary>\n\n")
	fmt.Fprintf(body, "- Версия формата: %d\n- Manifest: `%s`\n- Analysis: `%s`\n", KnowledgeSelectionExportVersion, data.manifest.Digest, data.analysis.Digest)
	body.WriteString("\n</details>\n")
}

func appendMarkdownQuote(body *strings.Builder, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.ReplaceAll(line, "&", "&amp;")
		line = strings.ReplaceAll(line, "<", "&lt;")
		line = strings.ReplaceAll(line, ">", "&gt;")
		fmt.Fprintf(body, "> %s\n", line)
	}
	body.WriteString("\n")
}

func formatKnowledgeSelectionAnalysisRelation(relation KnowledgeSelectionAnalysisRelation) string {
	text := relation.From.Label + " → " + relation.To.Label + " [" + string(relation.Kind) + "]"
	if strings.TrimSpace(relation.Label) != "" {
		text += ": " + relation.Label
	}
	return text
}

func knowledgeSelectionRelationText(edge KnowledgeEdge, nodes map[string]KnowledgeNode) string {
	from, to := nodes[edge.From], nodes[edge.To]
	text := firstNonEmpty(from.Label, edge.From) + " → " + firstNonEmpty(to.Label, edge.To) + " [" + string(edge.Kind) + "]"
	if strings.TrimSpace(edge.Label) != "" {
		text += ": " + edge.Label
	}
	return text
}

func formatKnowledgeSelectionSourceRef(ref KnowledgeSelectionSourceRef) string {
	name := firstNonEmpty(ref.SourcePath, "источник без пути")
	parts := []string{name}
	if ref.Page > 0 {
		parts = append(parts, "стр. "+strconv.Itoa(ref.Page))
	}
	parts = append(parts, "блок "+strconv.Itoa(ref.BlockIndex+1), "фрагмент "+strconv.Itoa(ref.BlockChunkIndex+1))
	return strings.Join(parts, " · ")
}

func knowledgeSelectionExportSources(refs []KnowledgeSelectionSourceRef) ([]string, []string, []string) {
	pathSet, pageSet := map[string]bool{}, map[string]bool{}
	coordinates := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.SourcePath != "" {
			pathSet[ref.SourcePath] = true
		}
		if ref.Page > 0 {
			pageSet[strconv.Itoa(ref.Page)] = true
		}
		coordinates = append(coordinates, formatKnowledgeSelectionSourceRef(ref))
	}
	paths, pages := sortedStringSet(pathSet), sortedNumericStringSet(pageSet)
	return paths, pages, coordinates
}

func sortedStringSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedNumericStringSet(values map[string]bool) []string {
	result := sortedStringSet(values)
	sort.Slice(result, func(i, j int) bool {
		a, _ := strconv.Atoi(result[i])
		b, _ := strconv.Atoi(result[j])
		return a < b
	})
	return result
}

func safeSpreadsheetCell(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	trimmed := strings.TrimLeft(value, " \t\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func markdownInline(value string) string {
	value = singleLine(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	for _, token := range []string{"`", "*", "_", "[", "]", "<", ">", "#", "|"} {
		value = strings.ReplaceAll(value, token, "\\"+token)
	}
	return value
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
