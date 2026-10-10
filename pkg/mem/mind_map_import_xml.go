package mem

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// XML is parsed as data only: no CharsetReader, entity map, include resolver,
// filesystem access or network client is installed. The small bounded tree also
// lets rich XHTML retain text order without importing its executable markup.
const outlineMemNamespace = "urn:mem-tool:classic-mind-map:1"
const outlineXHTMLNamespace = "http://www.w3.org/1999/xhtml"

type outlineXMLPart struct {
	text string
	node *outlineXMLNode
}
type outlineXMLNode struct {
	name  xml.Name
	attrs map[xml.Name]string
	parts []outlineXMLPart
}
type outlineXMLParser struct {
	draft       OutlineImportDraft
	seen        map[string]bool
	sourceCount int
}

func (p *outlineXMLParser) warn(message string) {
	if !p.seen[message] {
		p.seen[message] = true
		p.draft.Warnings = append(p.draft.Warnings, message)
	}
}
func outlineXMLInvalid(message string) error {
	return fmt.Errorf("%w: %s", ErrOutlineImportInvalid, message)
}
func outlineXMLLimit(message string) error {
	return fmt.Errorf("%w: %s", ErrOutlineImportLimit, message)
}
func (n *outlineXMLNode) attr(name string) string { return n.attrs[xml.Name{Local: name}] }
func (n *outlineXMLNode) mem(name string) (string, bool) {
	v, ok := n.attrs[xml.Name{Space: outlineMemNamespace, Local: name}]
	return v, ok
}
func outlineCoreName(name string) bool {
	switch name {
	case "opml", "head", "body", "title", "outline", "map", "node", "richcontent":
		return true
	}
	return false
}
func outlineCoreAttribute(name string) bool {
	switch name {
	case "version", "text", "type", "_note", "url", "xmlUrl", "htmlUrl", "TEXT", "LINK", "TYPE", "href":
		return true
	}
	return false
}
func outlineMemAttribute(name string) bool {
	switch name {
	case "encoding", "summary", "body-markdown", "style", "sources", "kind", "id", "parent-id", "position", "origin", "locked", "created", "updated", "export-version", "format":
		return true
	}
	return false
}
func readOutlineXML(data []byte) (*outlineXMLNode, error) {
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, outlineXMLInvalid("требуется XML в UTF-8")
	}
	if len(data) > MaxOutlineImportBytes {
		return nil, outlineXMLLimit("XML превышает 8 МиБ")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var root *outlineXMLNode
	var stack []*outlineXMLNode
	declaration := false
	elements, outlineNodes := 0, 0
	for tokens := 0; ; tokens++ {
		if tokens > 131072 {
			return nil, outlineXMLLimit("слишком много XML-токенов")
		}
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, outlineXMLInvalid("некорректная структура XML или кодировка")
		}
		switch t := token.(type) {
		case xml.Directive:
			return nil, outlineXMLInvalid("DTD и XML-директивы не поддерживаются")
		case xml.ProcInst:
			if t.Target != "xml" || declaration || root != nil {
				return nil, outlineXMLInvalid("XML processing instructions не поддерживаются")
			}
			declaration = true
		case xml.StartElement:
			elements++
			if t.Name.Space == "" && (t.Name.Local == "node" || t.Name.Local == "outline") {
				outlineNodes++
			}
			if elements > 16384 || outlineNodes > MaxOutlineImportNodes {
				return nil, outlineXMLLimit("слишком много XML-элементов или узлов карты")
			}
			if len(stack) >= MaxOutlineImportDepth+68 || len(t.Attr) > 128 {
				return nil, outlineXMLLimit("превышена глубина XML или число атрибутов")
			}
			if outlineCoreName(t.Name.Local) && t.Name.Space != "" {
				insideRich := false
				for _, ancestor := range stack {
					if ancestor.name == (xml.Name{Local: "richcontent"}) {
						insideRich = true
					}
				}
				if !insideRich || t.Name.Space != outlineXHTMLNamespace || (t.Name.Local != "head" && t.Name.Local != "body" && t.Name.Local != "title") {
					return nil, outlineXMLInvalid("подмена пространства имён структурного XML-элемента")
				}
			}
			n := &outlineXMLNode{name: t.Name, attrs: make(map[xml.Name]string, len(t.Attr))}
			for _, a := range t.Attr {
				if _, exists := n.attrs[a.Name]; exists {
					return nil, outlineXMLInvalid("повтор XML-атрибута")
				}
				if outlineCoreAttribute(a.Name.Local) && a.Name.Space != "" {
					return nil, outlineXMLInvalid("подмена пространства имён поддерживаемого атрибута")
				}
				if outlineMemAttribute(a.Name.Local) && a.Name.Space != "" && a.Name.Space != outlineMemNamespace && a.Name.Space != "xmlns" {
					return nil, outlineXMLInvalid("подмена пространства имён метаданных mem")
				}
				n.attrs[a.Name] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, outlineXMLInvalid("несколько корневых XML-элементов")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.parts = append(parent.parts, outlineXMLPart{node: n})
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1] // Decoder checks matching start/end tags.
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, outlineXMLInvalid("текст вне корневого XML-элемента")
				}
			} else if len(t) != 0 {
				n := stack[len(stack)-1]
				n.parts = append(n.parts, outlineXMLPart{text: string(t)})
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, outlineXMLInvalid("пустой или незаконченный XML")
	}
	return root, nil
}

func parseOutlineXML(data []byte, format string) (OutlineImportDraft, error) {
	p := outlineXMLParser{seen: map[string]bool{}}
	root, err := readOutlineXML(data)
	if err != nil {
		return OutlineImportDraft{}, err
	}
	if root.name.Space != "" {
		return OutlineImportDraft{}, outlineXMLInvalid("корень формата должен быть без пространства имён")
	}
	switch format {
	case "opml":
		if root.name.Local != "opml" {
			return OutlineImportDraft{}, outlineXMLInvalid("ожидался корень opml")
		}
		err = p.opml(root)
	case "freemind":
		if root.name.Local != "map" {
			return OutlineImportDraft{}, outlineXMLInvalid("ожидался корень map")
		}
		p.unknownAttrs(root, map[string]bool{"version": true}, "FreeMind: неподдерживаемые атрибуты map исключены.")
		roots := p.children(root, "node", "FreeMind: неподдерживаемые элементы map исключены.")
		if len(roots) != 1 {
			err = outlineXMLInvalid("FreeMind требует ровно один корневой node")
		} else {
			err = p.freeMindNode(roots[0], "", 1)
			if err == nil {
				p.draft.Title = p.draft.Nodes[0].Label
			}
		}
	default:
		err = outlineXMLInvalid("неподдерживаемый XML-формат")
	}
	if err != nil {
		return OutlineImportDraft{}, err
	}
	return p.draft, nil
}

func (p *outlineXMLParser) unknownAttrs(n *outlineXMLNode, allowed map[string]bool, warning string) {
	for name := range n.attrs {
		if name.Space == "xmlns" || (name.Space == "" && name.Local == "xmlns") {
			continue
		}
		if name.Space != "" || !allowed[name.Local] {
			p.warn(warning)
		}
	}
}
func (p *outlineXMLParser) children(n *outlineXMLNode, name, warning string) []*outlineXMLNode {
	var result []*outlineXMLNode
	for _, part := range n.parts {
		if part.node != nil {
			if part.node.name == (xml.Name{Local: name}) {
				result = append(result, part.node)
			} else {
				p.warn(warning)
			}
		} else if strings.TrimSpace(part.text) != "" {
			p.warn(warning)
		}
	}
	return result
}
func outlinePlainXML(n *outlineXMLNode) (string, error) {
	var out strings.Builder
	for _, part := range n.parts {
		if part.node != nil {
			return "", outlineXMLInvalid("в текстовом поле XML обнаружен вложенный элемент")
		}
		out.WriteString(part.text)
	}
	return strings.TrimSpace(out.String()), nil
}
func outlineTextLimit(value string, limit int) error {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) {
		return outlineXMLInvalid("неподдерживаемые символы в тексте")
	}
	if utf8.RuneCountInString(value) > limit {
		return outlineXMLLimit("текстовое поле превышает допустимый размер")
	}
	return nil
}
func (p *outlineXMLParser) appendNode(node ClassicMindMapNodeDraft, parent string, depth int) (int, error) {
	if len(p.draft.Nodes) >= MaxOutlineImportNodes || depth > MaxOutlineImportDepth {
		return 0, outlineXMLLimit("превышено число узлов или глубина карты")
	}
	if strings.TrimSpace(node.Label) == "" {
		node.Label = "Узел без названия"
		p.warn("Пустые метки заменены на «Узел без названия».")
	}
	if err := outlineTextLimit(node.Label, 512); err != nil {
		return 0, err
	}
	for _, value := range []string{node.Summary, node.BodyMarkdown} {
		if err := outlineTextLimit(value, 65536); err != nil {
			return 0, err
		}
	}
	node.Ref, node.ParentRef = "n"+strconv.Itoa(len(p.draft.Nodes)+1), parent
	node.Origin = ClassicMindMapNodeImported
	if node.Kind == "" {
		node.Kind = ClassicMindMapNodeTopic
	}
	p.draft.Nodes = append(p.draft.Nodes, node)
	return len(p.draft.Nodes) - 1, nil
}
func (p *outlineXMLParser) addURL(node *ClassicMindMapNodeDraft, raw string) error {
	if raw == "" {
		return nil
	}
	value := strings.TrimSpace(raw)
	decoded, e := url.PathUnescape(value)
	_, err := validateClassicMindMapHTTPURL(value)
	if e != nil || err != nil || len(value) > 8192 || !strings.HasPrefix(strings.ToLower(value), "http://") && !strings.HasPrefix(strings.ToLower(value), "https://") || strings.ContainsAny(value, "\\") || strings.ContainsFunc(decoded, unicode.IsControl) || strings.Contains(decoded, "\\") {
		p.warn("Небезопасные, локальные и относительные ссылки исключены; файлы и сеть не открывались.")
		return nil
	}
	for _, old := range node.Sources {
		if old.URL == value {
			return nil
		}
	}
	if p.sourceCount >= 2000 {
		return outlineXMLLimit("более 2000 внешних URL")
	}
	p.sourceCount++
	node.Sources = append(node.Sources, ClassicMindMapSource{Kind: ClassicMindMapSourceURL, URL: value})
	return nil
}

func (p *outlineXMLParser) opml(root *outlineXMLNode) error {
	p.unknownAttrs(root, map[string]bool{"version": true}, "OPML: служебные атрибуты документа исключены.")
	var head, body *outlineXMLNode
	for _, part := range root.parts {
		if part.node == nil {
			if strings.TrimSpace(part.text) != "" {
				p.warn("OPML: текст вне head/body исключён.")
			}
			continue
		}
		n := part.node
		switch n.name {
		case xml.Name{Local: "head"}:
			if head != nil {
				return outlineXMLInvalid("повтор head в OPML")
			}
			head = n
		case xml.Name{Local: "body"}:
			if body != nil {
				return outlineXMLInvalid("повтор body в OPML")
			}
			body = n
		default:
			p.warn("OPML: неподдерживаемые элементы документа исключены.")
		}
	}
	if head == nil || body == nil {
		return outlineXMLInvalid("OPML требует head и body")
	}
	p.unknownAttrs(head, nil, "OPML: атрибуты head исключены.")
	p.unknownAttrs(body, nil, "OPML: атрибуты body исключены.")
	seen := map[xml.Name]bool{}
	for _, part := range head.parts {
		if part.node == nil {
			if strings.TrimSpace(part.text) != "" {
				p.warn("OPML: неподдерживаемое содержимое head исключено.")
			}
			continue
		}
		n := part.node
		if seen[n.name] {
			return outlineXMLInvalid("повтор элемента head в OPML")
		}
		seen[n.name] = true
		if n.name == (xml.Name{Local: "title"}) {
			p.unknownAttrs(n, nil, "OPML: атрибуты title исключены.")
			title, err := outlinePlainXML(n)
			if err != nil {
				return err
			}
			if err = outlineTextLimit(title, 512); err != nil {
				return err
			}
			p.draft.Title = title
		} else {
			p.warn("OPML: метаданные head, кроме title, исключены; ID, ревизия и происхождение создаются заново.")
		}
	}
	roots := p.children(body, "outline", "OPML: неподдерживаемое содержимое body исключено.")
	if len(roots) == 0 {
		return outlineXMLInvalid("в OPML нет outline")
	}
	parent, depth := "", 1
	if len(roots) > 1 {
		if p.draft.Title == "" {
			p.draft.Title = "Импорт OPML"
		}
		if _, err := p.appendNode(ClassicMindMapNodeDraft{Label: p.draft.Title}, "", 1); err != nil {
			return err
		}
		parent, depth = "n1", 2
		p.warn("Для нескольких корней OPML добавлен общий организационный корень.")
	}
	for _, n := range roots {
		if err := p.opmlNode(n, parent, depth); err != nil {
			return err
		}
	}
	if p.draft.Title == "" {
		p.draft.Title = p.draft.Nodes[0].Label
	}
	return nil
}
func (p *outlineXMLParser) opmlNode(n *outlineXMLNode, parent string, depth int) error {
	label, ok := n.attrs[xml.Name{Local: "text"}]
	if !ok {
		return outlineXMLInvalid("outline не содержит обязательный text")
	}
	node := ClassicMindMapNodeDraft{Label: label, BodyMarkdown: n.attr("_note")}
	p.unknownAttrs(n, map[string]bool{"text": true, "type": true, "_note": true, "url": true, "xmlUrl": true, "htmlUrl": true}, "OPML: неподдерживаемые атрибуты outline исключены.")
	if encoding, exists := n.mem("encoding"); exists {
		if encoding != "base64-raw-std-v1" {
			p.warn("OPML: неизвестная кодировка метаданных mem исключена; сохранён обычный _note.")
		} else {
			values := map[string]*string{"summary": &node.Summary, "body-markdown": &node.BodyMarkdown}
			for key, destination := range values {
				if raw, present := n.mem(key); present {
					if len(raw) > base64.RawStdEncoding.EncodedLen(65536*4) {
						return outlineXMLLimit("слишком большое поле метаданных mem")
					}
					decoded, err := base64.RawStdEncoding.Strict().DecodeString(raw)
					if err != nil || !utf8.Valid(decoded) {
						return outlineXMLInvalid("некорректный base64/UTF-8 в метаданных mem")
					}
					*destination = string(decoded)
				}
			}
			_, hasBody := n.mem("body-markdown")
			if hasBody && n.attr("_note") != "" && strings.TrimSpace(n.attr("_note")) != strings.TrimSpace(strings.TrimSpace(node.Summary)+"\n\n"+strings.TrimSpace(node.BodyMarkdown)) {
				node.BodyMarkdown += "\n\n" + n.attr("_note")
				p.warn("OPML: отличающийся _note сохранён дополнительно к mem:body-markdown.")
			}
		}
	}
	if _, present := n.mem("style"); present {
		p.warn("OPML: mem:style исключён; оформление не восстанавливается из внешних метаданных.")
	}
	if _, present := n.mem("sources"); present {
		p.warn("OPML: mem:sources и прежние evidence/anchors исключены; XML не подтверждает связь с локальными документами.")
	}
	if kind, present := n.mem("kind"); present {
		switch ClassicMindMapNodeKind(kind) {
		case ClassicMindMapNodeTopic, ClassicMindMapNodeSubtopic, ClassicMindMapNodeFact, ClassicMindMapNodeNote, ClassicMindMapNodeQuote, ClassicMindMapNodeQuestion, ClassicMindMapNodeTask, ClassicMindMapNodeDecision, ClassicMindMapNodeLink:
			node.Kind = ClassicMindMapNodeKind(kind)
		default:
			p.warn("OPML: неизвестный mem:kind заменён на topic.")
		}
	}
	switch strings.ToLower(n.attr("type")) {
	case "", "link":
	case "include", "rss":
		p.warn("OPML: include/RSS сохранены только как узлы и безопасные URL; включения и подписки не загружались.")
	default:
		p.warn("OPML: семантика нестандартного type не исполняется.")
	}
	for _, key := range []string{"url", "xmlUrl", "htmlUrl"} {
		if err := p.addURL(&node, n.attr(key)); err != nil {
			return err
		}
	}
	index, err := p.appendNode(node, parent, depth)
	if err != nil {
		return err
	}
	ref := p.draft.Nodes[index].Ref
	for _, child := range p.children(n, "outline", "OPML: неподдерживаемое содержимое outline исключено.") {
		if err = p.opmlNode(child, ref, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (p *outlineXMLParser) freeMindNode(n *outlineXMLNode, parent string, depth int) error {
	node := ClassicMindMapNodeDraft{Label: n.attr("TEXT")}
	var bodyParts []string
	bodyRunes := 0
	appendBody := func(value string) error {
		if value == "" {
			return nil
		}
		bodyRunes += utf8.RuneCountInString(value)
		if len(bodyParts) > 0 {
			bodyRunes += 2
		}
		if bodyRunes > 65536 {
			return outlineXMLLimit("слишком большой подробный текст FreeMind")
		}
		bodyParts = append(bodyParts, value)
		return nil
	}
	p.unknownAttrs(n, map[string]bool{"TEXT": true, "LINK": true}, "FreeMind: внешние ID, folding, стили, координаты, даты и прочие атрибуты узла исключены.")
	if err := p.addURL(&node, n.attr("LINK")); err != nil {
		return err
	}
	var children []*outlineXMLNode
	var richNode string
	hasRichNode := false
	for _, part := range n.parts {
		if part.node == nil {
			if strings.TrimSpace(part.text) != "" {
				if err := appendBody(part.text); err != nil {
					return err
				}
				p.warn("FreeMind: текст вне richcontent сохранён в подробном тексте узла.")
			}
			continue
		}
		child := part.node
		switch child.name {
		case xml.Name{Local: "node"}:
			children = append(children, child)
		case xml.Name{Local: "richcontent"}:
			p.unknownAttrs(child, map[string]bool{"TYPE": true}, "FreeMind: атрибуты richcontent, кроме TYPE, исключены.")
			content, err := p.richText(child, &node)
			if err != nil {
				return err
			}
			if child.attr("TYPE") == "NODE" {
				if hasRichNode {
					return outlineXMLInvalid("повтор richcontent TYPE=NODE")
				}
				richNode, hasRichNode = content, true
			} else {
				if child.attr("TYPE") != "NOTE" {
					p.warn("FreeMind: неизвестный TYPE richcontent сохранён как обычный подробный текст.")
				}
				if err := appendBody(content); err != nil {
					return err
				}
			}
		default:
			p.warn("FreeMind: неподдерживаемые элементы (стили, icons, arrows, hooks и другие) исключены.")
		}
	}
	node.BodyMarkdown = strings.Join(bodyParts, "\n\n")
	if hasRichNode && richNode != "" {
		if strings.TrimSpace(node.Label) == "" {
			node.Label = richNode
			if utf8.RuneCountInString(richNode) > 512 {
				node.Label = string([]rune(richNode)[:511]) + "…"
				node.BodyMarkdown = joinOutlineText(richNode, node.BodyMarkdown)
				p.warn("FreeMind: длинная richcontent-метка сокращена; полный текст сохранён в теле узла.")
			}
		} else if strings.TrimSpace(node.Label) != richNode {
			node.BodyMarkdown = joinOutlineText(richNode, node.BodyMarkdown)
			p.warn("FreeMind: различающиеся TEXT и richcontent NODE сохранены как метка и подробный текст.")
		}
	}
	index, err := p.appendNode(node, parent, depth)
	if err != nil {
		return err
	}
	ref := p.draft.Nodes[index].Ref
	for _, child := range children {
		if err = p.freeMindNode(child, ref, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func joinOutlineText(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

// Rich markup is reduced to bounded visible text. Block boundaries become
// line breaks, inline text stays in order, and preformatted text keeps spacing.
func (p *outlineXMLParser) richText(root *outlineXMLNode, draft *ClassicMindMapNodeDraft) (string, error) {
	var out strings.Builder
	var last rune
	pendingSpace := false
	writeRune := func(r rune) error {
		if out.Len()+utf8.RuneLen(r) > 65536*4 {
			return outlineXMLLimit("слишком большой richcontent")
		}
		out.WriteRune(r)
		last = r
		return nil
	}
	line := func() error {
		pendingSpace = false
		if out.Len() != 0 && last != '\n' {
			return writeRune('\n')
		}
		return nil
	}
	var walk func(*outlineXMLNode, int, bool) error
	walk = func(n *outlineXMLNode, depth int, pre bool) error {
		if depth > 64 {
			return outlineXMLLimit("слишком глубокий richcontent")
		}
		tag := strings.ToLower(n.name.Local)
		if n != root && n.name.Space != "" && n.name.Space != outlineXHTMLNamespace {
			p.warn("FreeMind: нестандартная richcontent-разметка преобразована в обычный текст.")
		}
		switch tag {
		case "script", "style", "img", "iframe", "object", "embed", "audio", "video":
			p.warn("FreeMind: изображения, scripts/styles и встроенные внешние объекты richcontent исключены; ресурсы не загружались.")
			return nil
		case "head":
			if len(n.parts) > 0 || len(n.attrs) > 0 {
				p.warn("FreeMind: служебное содержимое HTML head исключено.")
			}
			return nil
		}
		block := false
		switch tag {
		case "p", "div", "li", "tr", "td", "th", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "blockquote":
			block = true
		case "richcontent", "html", "body", "span", "b", "strong", "i", "em", "u", "s", "code", "a", "ul", "ol", "table", "tbody":
		default:
			p.warn("FreeMind: неподдерживаемая richcontent-разметка преобразована в обычный текст.")
		}
		if n != root {
			allowed := map[string]bool{}
			if tag == "a" {
				allowed["href"] = true
				if err := p.addURL(draft, n.attr("href")); err != nil {
					return err
				}
			}
			p.unknownAttrs(n, allowed, "FreeMind: оформление и неподдерживаемые HTML-атрибуты richcontent исключены.")
		}
		if block {
			if err := line(); err != nil {
				return err
			}
		}
		for _, part := range n.parts {
			if part.node != nil {
				if err := walk(part.node, depth+1, pre || tag == "pre"); err != nil {
					return err
				}
				continue
			}
			for _, r := range part.text {
				if !pre && tag != "pre" && unicode.IsSpace(r) {
					pendingSpace = out.Len() != 0
					continue
				}
				if pendingSpace && last != '\n' {
					if err := writeRune(' '); err != nil {
						return err
					}
				}
				pendingSpace = false
				if err := writeRune(r); err != nil {
					return err
				}
			}
		}
		if block {
			return line()
		}
		return nil
	}
	if err := walk(root, 0, false); err != nil {
		return "", err
	}
	value := strings.TrimSpace(out.String())
	if err := outlineTextLimit(value, 65536); err != nil {
		return "", err
	}
	p.warn("FreeMind: richcontent преобразован в обычный текст с переносами; HTML-оформление не сохраняется.")
	return value, nil
}
