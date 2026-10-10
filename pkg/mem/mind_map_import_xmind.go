package mem

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	xmindMaxEntries  = 256
	xmindMaxExpanded = 32 << 20
	xmindMaxTokens   = 131072
)

// XMind's official model describes sheet/rootTopic and the four children groups:
// https://github.com/xmindltd/xmind-model/blob/master/schemas/topic.json
// https://github.com/xmindltd/xmind-viewer/blob/master/src/xmindLoader.ts
// The importer deliberately makes an outline, not an XMind rendering. The
// caller retains the original archive; conversion losses are counted below.
func parseOutlineXMind(data []byte) (OutlineImportDraft, error) {
	if len(data) > MaxOutlineImportBytes {
		return OutlineImportDraft{}, xmindLimit("размер ZIP превышает 8 MiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return OutlineImportDraft{}, xmindInvalid("некорректный ZIP")
	}
	if len(archive.File) > xmindMaxEntries {
		return OutlineImportDraft{}, xmindLimit("больше 256 элементов ZIP")
	}
	seen := make(map[string]bool)
	var declared uint64
	contentName := ""
	for _, file := range archive.File {
		name, ok := xmindArchiveName(file.Name)
		if !ok || seen[name] {
			return OutlineImportDraft{}, xmindInvalid("небезопасные или повторяющиеся имена элементов ZIP")
		}
		seen[name] = true
		if file.Flags&(1|64) != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) ||
			(file.Mode().Type() != 0 && file.Mode().Type() != fs.ModeDir) {
			return OutlineImportDraft{}, xmindInvalid("шифрование, метод сжатия или тип элемента ZIP не поддерживается")
		}
		if file.UncompressedSize64 > xmindMaxExpanded-declared {
			return OutlineImportDraft{}, xmindLimit("распакованный ZIP превышает 32 MiB")
		}
		declared += file.UncompressedSize64
		if file.Name == "content.json" || file.Name == "content.xml" {
			if contentName != "" || file.Mode().IsDir() {
				return OutlineImportDraft{}, xmindInvalid("неоднозначное содержимое XMind: ожидается один content.json или content.xml")
			}
			if file.UncompressedSize64 > MaxOutlineImportBytes {
				return OutlineImportDraft{}, xmindLimit("содержимое XMind превышает 8 MiB")
			}
			contentName = file.Name
		}
	}
	// A file and a directory must not share an ancestor spelling (even on
	// case-insensitive platforms). We never extract, but reject ambiguous ZIPs.
	files := make(map[string]bool)
	for _, file := range archive.File {
		name, _ := xmindArchiveName(file.Name)
		if !file.Mode().IsDir() {
			files[name] = true
		}
	}
	for name := range seen {
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if files[ancestor] {
				return OutlineImportDraft{}, xmindInvalid("файл ZIP использован как каталог")
			}
		}
	}
	if contentName == "" {
		return OutlineImportDraft{}, xmindInvalid("нет корневого content.json или content.xml")
	}
	var content []byte
	var expanded int64
	p := xmindOutlineParser{counts: make(map[string]int), ids: make(map[string]bool)}
	// Read every member to EOF, including ignored assets: limits alone do not
	// establish CRC integrity. No archive member is extracted or dereferenced.
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			return OutlineImportDraft{}, xmindInvalid("не удалось прочитать элемент ZIP")
		}
		limit := int64(xmindMaxExpanded) - expanded
		if file.Name == contentName && limit > MaxOutlineImportBytes {
			limit = MaxOutlineImportBytes
		}
		var n int64
		if file.Name == contentName {
			content, err = io.ReadAll(io.LimitReader(reader, limit+1))
			n = int64(len(content))
		} else {
			n, err = io.Copy(io.Discard, io.LimitReader(reader, limit+1))
		}
		closeErr := reader.Close()
		if n > limit {
			return OutlineImportDraft{}, xmindLimit("превышен бюджет распаковки XMind")
		}
		if err != nil || closeErr != nil || uint64(n) != file.UncompressedSize64 {
			return OutlineImportDraft{}, xmindInvalid("повреждён элемент ZIP, размер или CRC")
		}
		expanded += n
		if file.Name != contentName && !file.Mode().IsDir() {
			p.counts["archive_assets"]++
		}
	}
	if !utf8.Valid(content) {
		return OutlineImportDraft{}, xmindInvalid("содержимое должно быть корректным UTF-8")
	}
	if contentName == "content.json" {
		err = p.parseJSON(content)
	} else {
		err = p.parseXML(content)
	}
	if err != nil {
		return OutlineImportDraft{}, err
	}
	p.draft.Warnings = []string{}
	keys := make([]string, 0, len(p.counts))
	for key, count := range p.counts {
		if count > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.draft.Warnings = append(p.draft.Warnings, fmt.Sprintf("XMind: %s — %d; %s", key, p.counts[key], xmindWarningDescription(key)))
	}
	return p.draft, nil
}

func xmindInvalid(message string) error {
	return fmt.Errorf("%w: XMind: %s", ErrOutlineImportInvalid, message)
}
func xmindLimit(message string) error {
	return fmt.Errorf("%w: XMind: %s", ErrOutlineImportLimit, message)
}

func xmindArchiveName(name string) (string, bool) {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || path.Clean(name) != name {
		return "", false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." || strings.TrimSpace(component) != component || strings.TrimRight(component, ".") != component {
			return "", false
		}
		base := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", false
		}
	}
	return strings.ToLower(name), true
}

type xmindOutlineParser struct {
	draft  OutlineImportDraft
	counts map[string]int
	ids    map[string]bool
}

func xmindWarningDescription(key string) string {
	switch key {
	case "workbook":
		return "листы сохранены под общим корнем, их названия добавлены к корневым темам"
	case "detached", "summary", "callout", "other_children":
		return "темы сохранены как обычные дочерние; исходное расположение и специальная семантика не перенесены"
	case "labels":
		return "метки сохранены текстом в заметке"
	case "inactive_links":
		return "внутренние, файловые или небезопасные ссылки не активированы"
	case "untitled":
		return "пустым названиям назначено «Без названия»"
	default:
		return "не перенесено в редактируемое дерево; сохранено в исходном архиве"
	}
}

func xmindText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// Decode tokens ourselves so duplicate keys, null and repaired Unicode cannot
// silently change meaning. Token and depth budgets bound ignored metadata too.
func xmindJSONValue(decoder *json.Decoder, depth int, tokens *int) (any, error) {
	*tokens++
	if depth > 1024 || *tokens > xmindMaxTokens {
		return nil, xmindLimit("слишком сложная структура JSON")
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return nil, xmindInvalid("некорректный JSON или null")
	}
	if str, ok := token.(string); ok && !xmindText(str) {
		return nil, xmindInvalid("недопустимые управляющие символы JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || !xmindText(name) {
				return nil, xmindInvalid("некорректное имя поля JSON")
			}
			if _, duplicate := object[name]; duplicate {
				return nil, xmindInvalid("повторяющееся поле JSON")
			}
			value, err := xmindJSONValue(decoder, depth+1, tokens)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, xmindInvalid("некорректный объект JSON")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := xmindJSONValue(decoder, depth+1, tokens)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, xmindInvalid("некорректный массив JSON")
		}
		return array, nil
	default:
		return nil, xmindInvalid("некорректная структура JSON")
	}
}

func (p *xmindOutlineParser) parseJSON(data []byte) error {
	if !validAnnotationTransferJSONUnicode(data) {
		return xmindInvalid("некорректные Unicode escape-последовательности JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	tokens := 0
	value, err := xmindJSONValue(decoder, 0, &tokens)
	if err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return xmindInvalid("данные после конца JSON")
	}
	sheets, ok := value.([]any)
	if !ok {
		return xmindInvalid("ожидается непустой массив листов")
	}
	return p.sheets(sheets)
}

func (p *xmindOutlineParser) sheets(sheets []any) error {
	if len(sheets) == 0 {
		return xmindInvalid("ожидается непустой массив листов")
	}
	if len(sheets) > MaxOutlineImportNodes {
		return xmindLimit("слишком много листов")
	}
	parent := ""
	depth := 0
	if len(sheets) > 1 {
		p.draft.Title = "Книга XMind"
		var err error
		parent, err = p.appendNode("", p.draft.Title, "", "", 0)
		if err != nil {
			return err
		}
		p.counts["workbook"] = len(sheets)
		depth = 1
	}
	for _, raw := range sheets {
		sheet, ok := raw.(map[string]any)
		if !ok {
			return xmindInvalid("лист должен быть объектом")
		}
		title, err := xmindString(sheet, "title")
		if err != nil {
			return err
		}
		root, ok := sheet["rootTopic"].(map[string]any)
		if !ok {
			return xmindInvalid("у листа отсутствует корневая тема")
		}
		if len(sheets) == 1 {
			p.draft.Title = title
		}
		p.countIgnored(sheet, "id title rootTopic")
		if err := p.topic(root, parent, depth, title, len(sheets) > 1); err != nil {
			return err
		}
	}
	if strings.TrimSpace(p.draft.Title) == "" {
		p.draft.Title = p.draft.Nodes[0].Label
	}
	return p.titleBounds(p.draft.Title)
}

func xmindString(object map[string]any, key string) (string, error) {
	value, exists := object[key]
	if !exists {
		return "", nil
	}
	str, ok := value.(string)
	if !ok {
		return "", xmindInvalid("ожидается строковое поле " + key)
	}
	return str, nil
}

func (p *xmindOutlineParser) titleBounds(title string) error {
	if utf8.RuneCountInString(title) > MaxClassicMindMapTitleRunes {
		return xmindLimit("название превышает 512 символов")
	}
	return nil
}

func (p *xmindOutlineParser) appendNode(parent, title, body, href string, depth int) (string, error) {
	if len(p.draft.Nodes) >= MaxOutlineImportNodes || depth >= MaxOutlineImportDepth {
		return "", xmindLimit("превышен предел числа тем или глубины дерева")
	}
	if err := p.titleBounds(title); err != nil {
		return "", err
	}
	if utf8.RuneCountInString(body) > MaxClassicMindMapTextRunes {
		return "", xmindLimit("заметка превышает 65536 символов")
	}
	if strings.TrimSpace(title) == "" {
		title = "Без названия"
		p.counts["untitled"]++
	}
	node := ClassicMindMapNodeDraft{Ref: fmt.Sprintf("n%d", len(p.draft.Nodes)+1), ParentRef: parent, Label: title, BodyMarkdown: body, Kind: ClassicMindMapNodeSubtopic, Origin: ClassicMindMapNodeImported}
	if parent == "" {
		node.Kind = ClassicMindMapNodeTopic
	}
	if href != "" {
		// URL controls and backslashes are rejected even if net/url accepts a
		// percent-escaped representation with ambiguous browser semantics.
		if _, err := validateClassicMindMapHTTPURL(href); err == nil && !strings.ContainsAny(href, "\\\r\n\t") {
			node.Sources = []ClassicMindMapSource{{Kind: ClassicMindMapSourceURL, URL: strings.TrimSpace(href)}}
		} else {
			p.counts["inactive_links"]++
		}
	}
	p.draft.Nodes = append(p.draft.Nodes, node)
	return node.Ref, nil
}

func (p *xmindOutlineParser) topic(object map[string]any, parent string, depth int, sheetTitle string, preserveSheet bool) error {
	if depth >= MaxOutlineImportDepth {
		return xmindLimit("глубина тем превышает 256 уровней")
	}
	id, err := xmindString(object, "id")
	if err != nil || strings.TrimSpace(id) == "" || p.ids[id] {
		return xmindInvalid("пустой или повторяющийся идентификатор темы")
	}
	p.ids[id] = true
	title, err := xmindString(object, "title")
	if err != nil {
		return err
	}
	href, err := xmindString(object, "href")
	if err != nil {
		return err
	}
	body := ""
	if raw, exists := object["notes"]; exists {
		notes, ok := raw.(map[string]any)
		if !ok {
			return xmindInvalid("notes должен быть объектом")
		}
		if raw, exists := notes["plain"]; exists {
			plain, ok := raw.(map[string]any)
			if !ok {
				return xmindInvalid("notes.plain должен быть объектом")
			}
			body, err = xmindString(plain, "content")
			if err != nil {
				return err
			}
			p.countIgnored(plain, "content")
		}
		p.countIgnored(notes, "plain")
	}
	if raw, exists := object["labels"]; exists {
		labels := []string{}
		switch value := raw.(type) {
		case string:
			labels = append(labels, value)
		case []any:
			for _, item := range value {
				label, ok := item.(string)
				if !ok {
					return xmindInvalid("метки должны быть строками")
				}
				labels = append(labels, label)
			}
		default:
			return xmindInvalid("некорректный список меток")
		}
		if len(labels) > 0 {
			body = xmindAppendBody(body, "Метки: "+strings.Join(labels, ", "))
			p.counts["labels"] += len(labels)
		}
	}
	if preserveSheet && sheetTitle != "" {
		body = xmindAppendBody(body, "Лист XMind: "+sheetTitle)
	}
	ref, err := p.appendNode(parent, title, body, href, depth)
	if err != nil {
		return err
	}
	p.countIgnored(object, "id title href notes labels children class titleUnedited")
	if raw, exists := object["children"]; exists {
		groups, ok := raw.(map[string]any)
		if !ok {
			return xmindInvalid("children должен быть объектом")
		}
		keys := []string{"attached", "detached", "summary", "callout"}
		extra := []string{}
		for key := range groups {
			if key != "attached" && key != "detached" && key != "summary" && key != "callout" {
				extra = append(extra, key)
			}
		}
		sort.Strings(extra)
		keys = append(keys, extra...)
		for _, key := range keys {
			raw, exists := groups[key]
			if !exists {
				continue
			}
			children, ok := raw.([]any)
			if !ok {
				return xmindInvalid("группа дочерних тем должна быть массивом")
			}
			if key != "attached" {
				category := key
				if key != "detached" && key != "summary" && key != "callout" {
					category = "other_children"
				}
				p.counts[category] += len(children)
			}
			for _, value := range children {
				child, ok := value.(map[string]any)
				if !ok {
					return xmindInvalid("дочерняя тема должна быть объектом")
				}
				if err := p.topic(child, ref, depth+1, "", false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func xmindAppendBody(body, extra string) string {
	if body == "" {
		return extra
	}
	return body + "\n\n" + extra
}

func (p *xmindOutlineParser) countIgnored(object map[string]any, understood string) {
	known := make(map[string]bool)
	for _, key := range strings.Fields(understood) {
		known[key] = true
	}
	for key, value := range object {
		if known[key] {
			continue
		}
		category := "unknown_properties"
		switch key {
		case "relationships", "relations":
			category = "relationships"
		case "style", "theme", "style-id":
			category = "styles"
		case "markers", "marker-refs":
			category = "markers"
		case "image", "images":
			category = "images"
		case "html":
			category = "rich_notes"
		case "extensions", "boundaries", "summaries":
			category = key
		case "position", "structureClass", "structure-class", "topicPositioning", "customWidth", "branch":
			category = "layout"
		}
		count := 1
		if items, ok := value.([]any); ok {
			count = len(items)
		}
		p.counts[category] += count
	}
}

type xmindXMLNode struct {
	name     xml.Name
	attrs    map[string]string
	children []*xmindXMLNode
	text     strings.Builder
}

func (p *xmindOutlineParser) parseXML(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var root *xmindXMLNode
	stack := []*xmindXMLNode{}
	count := 0
	declaration := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return xmindInvalid("некорректный XML")
		}
		switch value := token.(type) {
		case xml.Directive:
			return xmindInvalid("директивы и DTD XML не поддерживаются")
		case xml.ProcInst:
			if value.Target != "xml" || root != nil || declaration {
				return xmindInvalid("инструкции XML не поддерживаются")
			}
			declaration = true
		case xml.StartElement:
			count++
			if count > xmindMaxTokens/4 || len(stack) > 1024 || len(value.Attr) > 128 {
				return xmindLimit("слишком сложная структура XML")
			}
			node := &xmindXMLNode{name: value.Name, attrs: make(map[string]string)}
			seenAttrs := make(map[string]bool)
			for _, attr := range value.Attr {
				if attr.Name.Space == "xmlns" || (attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					continue
				}
				if !xmindText(attr.Value) {
					return xmindInvalid("управляющие символы XML")
				}
				// Reject duplicate local attribute names, rather than allowing a
				// namespace alias to override id, href or a children group type.
				if seenAttrs[attr.Name.Local] {
					return xmindInvalid("повторяющийся атрибут XML")
				}
				seenAttrs[attr.Name.Local] = true
				if attr.Name.Space != "" && !(attr.Name.Space == "http://www.w3.org/1999/xlink" && attr.Name.Local == "href") {
					p.counts["unknown_properties"]++
					continue
				}
				node.attrs[attr.Name.Local] = attr.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return xmindInvalid("несколько корневых элементов XML")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return xmindInvalid("несогласованное дерево XML")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if !xmindText(string(value)) {
				return xmindInvalid("управляющие символы XML")
			}
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(value)
			} else if strings.TrimSpace(string(value)) != "" {
				return xmindInvalid("текст вне корня XML")
			}
		}
	}
	if root == nil || len(stack) != 0 || root.name.Local != "xmap-content" ||
		(root.name.Space != "" && root.name.Space != "urn:xmind:xmap:xmlns:content:2.0" && root.name.Space != "http://www.xmind.net/content/2.0") {
		return xmindInvalid("ожидается xmap-content XMind XML")
	}
	sheets := []any{}
	for _, child := range root.children {
		if child.name.Local != "sheet" || child.name.Space != root.name.Space {
			p.counts["unknown_properties"]++
			continue
		}
		sheet, err := p.xmlObject(child, root.name.Space, false)
		if err != nil {
			return err
		}
		sheets = append(sheets, sheet)
	}
	// Use the same semantic validator without re-encoding text as JSON:
	// XML text containing '<' could otherwise expand sixfold before validation.
	return p.sheets(sheets)
}

func (p *xmindOutlineParser) xmlObject(node *xmindXMLNode, namespace string, topic bool) (map[string]any, error) {
	object := make(map[string]any)
	for key, value := range node.attrs {
		object[key] = value
	}
	for _, child := range node.children {
		name := child.name.Local
		if child.name.Space != namespace {
			p.counts["unknown_properties"]++
			continue
		}
		switch name {
		case "title":
			if _, exists := object["title"]; exists || len(child.children) != 0 {
				return nil, xmindInvalid("неоднозначное название XML")
			}
			object["title"] = child.text.String()
		case "topic":
			if topic || object["rootTopic"] != nil {
				return nil, xmindInvalid("неоднозначная корневая тема XML")
			}
			root, err := p.xmlObject(child, namespace, true)
			if err != nil {
				return nil, err
			}
			object["rootTopic"] = root
		case "children":
			if object["children"] != nil {
				return nil, xmindInvalid("повторяющиеся children XML")
			}
			groups := make(map[string]any)
			for _, group := range child.children {
				if group.name.Local != "topics" || group.name.Space != namespace || group.attrs["type"] == "" {
					return nil, xmindInvalid("некорректная группа дочерних тем XML")
				}
				key := group.attrs["type"]
				if _, exists := groups[key]; exists {
					return nil, xmindInvalid("повторяющаяся группа тем XML")
				}
				items := []any{}
				for _, item := range group.children {
					if item.name.Local != "topic" || item.name.Space != namespace {
						return nil, xmindInvalid("некорректная дочерняя тема XML")
					}
					converted, err := p.xmlObject(item, namespace, true)
					if err != nil {
						return nil, err
					}
					items = append(items, converted)
				}
				groups[key] = items
			}
			object["children"] = groups
		case "notes":
			if object["notes"] != nil {
				return nil, xmindInvalid("повторяющиеся notes XML")
			}
			notes := make(map[string]any)
			for _, note := range child.children {
				if note.name.Local == "plain" && note.name.Space == namespace {
					if notes["plain"] != nil || len(note.children) != 0 {
						return nil, xmindInvalid("неоднозначная простая заметка XML")
					}
					notes["plain"] = map[string]any{"content": note.text.String()}
				} else {
					p.counts["rich_notes"]++
				}
			}
			object["notes"] = notes
		case "labels":
			if object["labels"] != nil {
				return nil, xmindInvalid("повторяющиеся labels XML")
			}
			labels := []any{}
			for _, label := range child.children {
				if label.name.Local != "label" || label.name.Space != namespace || len(label.children) != 0 {
					return nil, xmindInvalid("некорректная метка XML")
				}
				labels = append(labels, label.text.String())
			}
			object["labels"] = labels
		default:
			// Keep only a count of unsupported XML payloads, never reinterpret
			// HTML, resource references, relationship endpoints or styles.
			items := make([]any, len(child.children))
			if len(items) == 0 {
				items = []any{true}
			} else {
				for i := range items {
					items[i] = true
				}
			}
			p.countIgnored(map[string]any{name: items}, "")
		}
	}
	return object, nil
}
