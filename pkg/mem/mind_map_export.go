package mem

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ClassicMindMapExportVersion       = 1
	MaxClassicMindMapExportNodes      = 10000
	MaxClassicMindMapExportInputBytes = 16 << 20
	MaxClassicMindMapExportBytes      = 128 << 20
)

type ClassicMindMapExportFormat string

const (
	ClassicMindMapExportHTML     ClassicMindMapExportFormat = "html"
	ClassicMindMapExportSVG      ClassicMindMapExportFormat = "svg"
	ClassicMindMapExportPNG      ClassicMindMapExportFormat = "png"
	ClassicMindMapExportJSON     ClassicMindMapExportFormat = "json"
	ClassicMindMapExportOPML     ClassicMindMapExportFormat = "opml"
	ClassicMindMapExportMarkdown ClassicMindMapExportFormat = "markdown"
	ClassicMindMapExportMermaid  ClassicMindMapExportFormat = "mermaid"
	ClassicMindMapExportObsidian ClassicMindMapExportFormat = "obsidian"
)

type ClassicMindMapExportRequest struct {
	MapRef              string                     `json:"map_ref"`
	Format              ClassicMindMapExportFormat `json:"format"`
	ExpectedRevision    int64                      `json:"expected_revision,omitempty"`
	ExpectedDigest      string                     `json:"expected_digest,omitempty"`
	ExpectedStateDigest string                     `json:"expected_state_digest,omitempty"`
}

// ClassicMindMapExportArtifact is a pure in-memory result. Callers own file
// overwrite policy, HTTP attachment headers and destination permissions.
type ClassicMindMapExportArtifact struct {
	Format      ClassicMindMapExportFormat `json:"format"`
	Filename    string                     `json:"filename"`
	MediaType   string                     `json:"media_type"`
	Data        []byte                     `json:"-"`
	MapID       string                     `json:"map_id"`
	Revision    int64                      `json:"revision"`
	Digest      string                     `json:"digest"`
	StateDigest string                     `json:"state_digest"`
	NodeCount   int                        `json:"node_count"`
	SourceCount int                        `json:"source_count"`
}

var ErrClassicMindMapExportChanged = errors.New("classic mind map changed before export")

type classicMindMapPortableDocument struct {
	ExportVersion int                    `json:"export_version"`
	Format        string                 `json:"format"`
	MapDigest     string                 `json:"map_digest"`
	StateDigest   string                 `json:"state_digest"`
	NodeCount     int                    `json:"node_count"`
	SourceCount   int                    `json:"source_count"`
	Document      ClassicMindMapDocument `json:"document"`
}

type classicMindMapExportNode struct {
	Node     ClassicMindMapNode
	Depth    int
	X        int
	Y        int
	Children []*classicMindMapExportNode
}

type classicMindMapExportModel struct {
	Portable classicMindMapPortableDocument
	Root     *classicMindMapExportNode
	Ordered  []*classicMindMapExportNode
	Width    int
	Height   int
}

const (
	classicMindMapExportMargin     = 48
	classicMindMapExportHeader     = 94
	classicMindMapExportNodeWidth  = 320
	classicMindMapExportNodeHeight = 70
	classicMindMapExportGapX       = 84
	classicMindMapExportGapY       = 24
)

// ExportClassicMindMap renders the complete current tree, regardless of any
// browser-only collapsed state. It never writes files or mutates the map.
func (s *Store) ExportClassicMindMap(request ClassicMindMapExportRequest) (ClassicMindMapExportArtifact, error) {
	request.MapRef = strings.TrimSpace(request.MapRef)
	request.ExpectedDigest = strings.TrimSpace(request.ExpectedDigest)
	request.ExpectedStateDigest = strings.TrimSpace(request.ExpectedStateDigest)
	if request.MapRef == "" {
		return ClassicMindMapExportArtifact{}, errors.New("classic mind map export requires map_ref")
	}
	if !validClassicMindMapExportFormat(request.Format) {
		return ClassicMindMapExportArtifact{}, fmt.Errorf("unsupported classic mind map export format %q", request.Format)
	}
	if request.ExpectedRevision < 0 {
		return ClassicMindMapExportArtifact{}, errors.New("classic mind map export expected revision must not be negative")
	}
	doc, err := s.LoadClassicMindMap(request.MapRef)
	if err != nil {
		return ClassicMindMapExportArtifact{}, err
	}
	if request.ExpectedRevision > 0 && doc.Map.Revision != request.ExpectedRevision {
		return ClassicMindMapExportArtifact{}, fmt.Errorf("%w: expected revision %d, current %d", ErrClassicMindMapExportChanged, request.ExpectedRevision, doc.Map.Revision)
	}
	if request.ExpectedDigest != "" && doc.Digest != request.ExpectedDigest {
		return ClassicMindMapExportArtifact{}, fmt.Errorf("%w: expected digest %s, current %s", ErrClassicMindMapExportChanged, request.ExpectedDigest, doc.Digest)
	}
	if request.ExpectedStateDigest != "" && doc.StateDigest != request.ExpectedStateDigest {
		return ClassicMindMapExportArtifact{}, fmt.Errorf("%w: expected state digest %s, current %s", ErrClassicMindMapExportChanged, request.ExpectedStateDigest, doc.StateDigest)
	}
	model, err := buildClassicMindMapExportModel(doc, string(request.Format))
	if err != nil {
		return ClassicMindMapExportArtifact{}, err
	}
	var data []byte
	var mediaType string
	switch request.Format {
	case ClassicMindMapExportHTML:
		data, err = renderClassicMindMapHTML(model)
		mediaType = "text/html; charset=utf-8"
	case ClassicMindMapExportSVG:
		data, err = renderClassicMindMapSVG(model)
		mediaType = "image/svg+xml; charset=utf-8"
	case ClassicMindMapExportPNG:
		data, err = renderClassicMindMapPNG(model)
		mediaType = "image/png"
	case ClassicMindMapExportJSON:
		data, err = renderClassicMindMapJSON(model)
		mediaType = "application/json; charset=utf-8"
	case ClassicMindMapExportOPML:
		data, err = renderClassicMindMapOPML(model)
		mediaType = "text/x-opml; charset=utf-8"
	case ClassicMindMapExportMarkdown:
		data, err = renderClassicMindMapMarkdown(model, false)
		mediaType = "text/markdown; charset=utf-8"
	case ClassicMindMapExportMermaid:
		data, err = renderClassicMindMapMermaid(model)
		mediaType = "text/plain; charset=utf-8"
	case ClassicMindMapExportObsidian:
		data, err = renderClassicMindMapMarkdown(model, true)
		mediaType = "text/markdown; charset=utf-8"
	}
	if err != nil {
		return ClassicMindMapExportArtifact{}, err
	}
	if len(data) == 0 || len(data) > MaxClassicMindMapExportBytes {
		return ClassicMindMapExportArtifact{}, fmt.Errorf("classic mind map %s export size must be 1..%d bytes, got %d", request.Format, MaxClassicMindMapExportBytes, len(data))
	}
	return ClassicMindMapExportArtifact{
		Format: request.Format, Filename: classicMindMapExportFilename(doc.Map.Title, request.Format),
		MediaType: mediaType, Data: data, MapID: doc.Map.ID, Revision: doc.Map.Revision,
		Digest: doc.Digest, StateDigest: doc.StateDigest, NodeCount: len(doc.Nodes), SourceCount: model.Portable.SourceCount,
	}, nil
}

func validClassicMindMapExportFormat(format ClassicMindMapExportFormat) bool {
	switch format {
	case ClassicMindMapExportHTML, ClassicMindMapExportSVG, ClassicMindMapExportPNG, ClassicMindMapExportJSON, ClassicMindMapExportOPML,
		ClassicMindMapExportMarkdown, ClassicMindMapExportMermaid, ClassicMindMapExportObsidian:
		return true
	default:
		return false
	}
}

func buildClassicMindMapExportModel(doc ClassicMindMapDocument, format string) (classicMindMapExportModel, error) {
	if len(doc.Nodes) == 0 || len(doc.Nodes) > MaxClassicMindMapExportNodes {
		return classicMindMapExportModel{}, fmt.Errorf("classic mind map export supports 1..%d nodes, got %d", MaxClassicMindMapExportNodes, len(doc.Nodes))
	}
	inputSize := len(doc.Map.Title) + len(doc.Map.Description)
	sourceCount := 0
	byID := make(map[string]ClassicMindMapNode, len(doc.Nodes))
	children := make(map[string][]ClassicMindMapNode)
	for _, node := range doc.Nodes {
		if node.ID == "" || byID[node.ID].ID != "" {
			return classicMindMapExportModel{}, errors.New("classic mind map export encountered an empty or duplicate node ID")
		}
		byID[node.ID] = node
		children[node.ParentID] = append(children[node.ParentID], node)
		inputSize += len(node.Label) + len(node.Summary) + len(node.BodyMarkdown) + len(node.Style)
		for _, source := range node.Sources {
			sourceCount++
			inputSize += len(source.Title) + len(source.Locator) + len(source.URL) + len(source.KnowledgeNodeID)
			if source.Evidence != nil {
				inputSize += len(source.Evidence.SourcePath) + len(source.Evidence.Excerpt) + 320
			}
		}
	}
	if inputSize > MaxClassicMindMapExportInputBytes {
		return classicMindMapExportModel{}, fmt.Errorf("classic mind map export input exceeds %d bytes; export a smaller map", MaxClassicMindMapExportInputBytes)
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			if children[parent][i].Position != children[parent][j].Position {
				return children[parent][i].Position < children[parent][j].Position
			}
			return children[parent][i].ID < children[parent][j].ID
		})
	}
	rootNode, ok := byID[doc.Map.RootNodeID]
	if !ok || rootNode.ParentID != "" {
		return classicMindMapExportModel{}, errors.New("classic mind map export root is missing or has a parent")
	}
	model := classicMindMapExportModel{Portable: classicMindMapPortableDocument{
		ExportVersion: ClassicMindMapExportVersion, Format: format, MapDigest: doc.Digest, StateDigest: doc.StateDigest,
		NodeCount: len(doc.Nodes), SourceCount: sourceCount, Document: doc,
	}}
	visiting := make(map[string]bool, len(doc.Nodes))
	visited := make(map[string]bool, len(doc.Nodes))
	leaf := 0
	maxDepth := 0
	var walk func(ClassicMindMapNode, int) (*classicMindMapExportNode, error)
	walk = func(node ClassicMindMapNode, depth int) (*classicMindMapExportNode, error) {
		if visiting[node.ID] || visited[node.ID] {
			return nil, fmt.Errorf("classic mind map export tree contains a cycle or repeated node %q", node.ID)
		}
		visiting[node.ID] = true
		item := &classicMindMapExportNode{Node: node, Depth: depth, X: classicMindMapExportMargin + depth*(classicMindMapExportNodeWidth+classicMindMapExportGapX)}
		if depth > maxDepth {
			maxDepth = depth
		}
		model.Ordered = append(model.Ordered, item)
		for _, child := range children[node.ID] {
			childItem, err := walk(child, depth+1)
			if err != nil {
				return nil, err
			}
			item.Children = append(item.Children, childItem)
		}
		if len(item.Children) == 0 {
			item.Y = classicMindMapExportHeader + classicMindMapExportMargin + leaf*(classicMindMapExportNodeHeight+classicMindMapExportGapY)
			leaf++
		} else {
			first, last := item.Children[0], item.Children[len(item.Children)-1]
			item.Y = (first.Y + last.Y) / 2
		}
		visiting[node.ID] = false
		visited[node.ID] = true
		return item, nil
	}
	root, err := walk(rootNode, 0)
	if err != nil {
		return classicMindMapExportModel{}, err
	}
	if len(visited) != len(doc.Nodes) {
		return classicMindMapExportModel{}, fmt.Errorf("classic mind map export tree has %d unreachable nodes", len(doc.Nodes)-len(visited))
	}
	model.Root = root
	model.Width = classicMindMapExportMargin*2 + (maxDepth+1)*classicMindMapExportNodeWidth + maxDepth*classicMindMapExportGapX
	if leaf < 1 {
		leaf = 1
	}
	model.Height = classicMindMapExportHeader + classicMindMapExportMargin*2 + leaf*classicMindMapExportNodeHeight + (leaf-1)*classicMindMapExportGapY
	return model, nil
}

func renderClassicMindMapJSON(model classicMindMapExportModel) ([]byte, error) {
	data, err := json.MarshalIndent(model.Portable, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map JSON export: %w", err)
	}
	return append(data, '\n'), nil
}

func renderClassicMindMapMarkdown(model classicMindMapExportModel, obsidian bool) ([]byte, error) {
	var body strings.Builder
	if obsidian {
		body.WriteString("---\n")
		fmt.Fprintf(&body, "title: %s\n", classicMindMapYAMLString(model.Portable.Document.Map.Title))
		body.WriteString("aliases: [\"MEM Mind Map\"]\n")
		body.WriteString("tags: [mem-tool, mind-map]\n")
		fmt.Fprintf(&body, "mem_map_id: %s\nmem_revision: %d\nmem_digest: %s\nmem_state_digest: %s\n---\n\n",
			classicMindMapYAMLString(model.Portable.Document.Map.ID), model.Portable.Document.Map.Revision,
			classicMindMapYAMLString(model.Portable.MapDigest), classicMindMapYAMLString(model.Portable.StateDigest))
	} else {
		fmt.Fprintf(&body, "<!-- mem-map-id: %s; revision: %d; digest: %s; state-digest: %s -->\n\n",
			model.Portable.Document.Map.ID, model.Portable.Document.Map.Revision, model.Portable.MapDigest, model.Portable.StateDigest)
	}
	fmt.Fprintf(&body, "# %s\n\n", model.Portable.Document.Map.Title)
	if description := strings.TrimSpace(model.Portable.Document.Map.Description); description != "" {
		body.WriteString(description)
		body.WriteString("\n\n")
	}
	if obsidian {
		body.WriteString("## Интерактивная схема\n\n```mermaid\n")
		mermaid, err := renderClassicMindMapMermaidDiagram(model)
		if err != nil {
			return nil, err
		}
		body.Write(mermaid)
		body.WriteString("```\n\n## Полное содержание\n\n")
	}
	for _, item := range model.Ordered {
		level := item.Depth + 2
		if level > 6 {
			level = 6
		}
		fmt.Fprintf(&body, "%s %s", strings.Repeat("#", level), item.Node.Label)
		if obsidian {
			fmt.Fprintf(&body, " ^%s", classicMindMapObsidianBlockID(item.Node.ID))
		}
		body.WriteString("\n\n")
		fmt.Fprintf(&body, "`%s` · `%s` · %d источников\n\n", item.Node.Kind, item.Node.Origin, len(item.Node.Sources))
		if summary := strings.TrimSpace(item.Node.Summary); summary != "" {
			fmt.Fprintf(&body, "**Кратко:** %s\n\n", summary)
		}
		if content := strings.TrimSpace(item.Node.BodyMarkdown); content != "" {
			body.WriteString(content)
			body.WriteString("\n\n")
		}
		writeClassicMindMapMarkdownSources(&body, item.Node.Sources)
	}
	portable, err := json.Marshal(model.Portable)
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map Markdown provenance: %w", err)
	}
	body.WriteString("<!-- mem-provenance-base64-raw-std-v1:\n")
	encoded := base64.RawStdEncoding.EncodeToString(portable)
	for len(encoded) > 120 {
		body.WriteString(encoded[:120])
		body.WriteByte('\n')
		encoded = encoded[120:]
	}
	body.WriteString(encoded)
	body.WriteString("\n-->\n")
	return []byte(body.String()), nil
}

func renderClassicMindMapMermaid(model classicMindMapExportModel) ([]byte, error) {
	diagram, err := renderClassicMindMapMermaidDiagram(model)
	if err != nil {
		return nil, err
	}
	portable, err := json.Marshal(model.Portable)
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map Mermaid provenance: %w", err)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%%%% MEM map %s · revision %d · digest %s · state %s\n", model.Portable.Document.Map.ID,
		model.Portable.Document.Map.Revision, model.Portable.MapDigest, model.Portable.StateDigest)
	body.WriteString("%% mem-provenance-base64-raw-std-v1:")
	body.WriteString(base64.RawStdEncoding.EncodeToString(portable))
	body.WriteByte('\n')
	body.Write(diagram)
	return []byte(body.String()), nil
}

func renderClassicMindMapMermaidDiagram(model classicMindMapExportModel) ([]byte, error) {
	if model.Root == nil {
		return nil, errors.New("classic mind map Mermaid export has no root")
	}
	var body strings.Builder
	body.WriteString("mindmap\n")
	stack := []*classicMindMapExportNode{model.Root}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		label := classicMindMapMermaidLabel(item.Node.Label)
		fmt.Fprintf(&body, "%s%s[\"%s\"]\n", strings.Repeat("  ", item.Depth+1), classicMindMapMermaidID(item.Node.ID), label)
		for i := len(item.Children) - 1; i >= 0; i-- {
			stack = append(stack, item.Children[i])
		}
	}
	return []byte(body.String()), nil
}

func classicMindMapMermaidID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "n" + hex.EncodeToString(sum[:6])
}

func classicMindMapMermaidLabel(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "`", "'")
	return value
}

func classicMindMapObsidianBlockID(value string) string {
	var result []rune
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			result = append(result, r)
		}
		if len(result) >= 60 {
			break
		}
	}
	if len(result) == 0 {
		return classicMindMapMermaidID(value)
	}
	return string(result)
}

func classicMindMapYAMLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func renderClassicMindMapHTML(model classicMindMapExportModel) ([]byte, error) {
	payload, err := json.Marshal(model.Portable)
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map HTML payload: %w", err)
	}
	// encoding/json escapes '<', '>' and '&', so an adversarial label cannot
	// close this non-executable data script. All rendered content uses textContent.
	pageTitle := html.EscapeString(model.Portable.Document.Map.Title)
	var body strings.Builder
	body.Grow(len(payload) + 12000)
	body.WriteString("<!doctype html><html lang=\"ru\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	body.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'\">")
	body.WriteString("<title>")
	body.WriteString(pageTitle)
	body.WriteString(" · MEM Mind Map</title><style>")
	body.WriteString(classicMindMapExportHTMLCSS)
	body.WriteString("</style></head><body><header><div><small>MEM · PORTABLE MIND MAP</small><h1 id=\"title\"></h1><p id=\"meta\"></p></div><div class=\"tools\"><input id=\"search\" type=\"search\" placeholder=\"Поиск по карте\"><button id=\"expand\">Раскрыть всё</button><button id=\"collapse\">Свернуть ветви</button><button id=\"theme\">Тема</button></div></header><main><section id=\"tree\" aria-label=\"Полная карта мыслей\"></section><aside id=\"details\"><h2>Выберите узел</h2><p>Здесь появятся содержание и проверяемые источники.</p></aside></main><footer id=\"footer\"></footer><script id=\"mem-data\" type=\"application/json\">")
	body.Write(payload)
	body.WriteString("</script><script>")
	body.WriteString(classicMindMapExportHTMLJS)
	body.WriteString("</script></body></html>\n")
	return []byte(body.String()), nil
}

const classicMindMapExportHTMLCSS = `
:root{color-scheme:light;--bg:#f5f7fb;--panel:#fff;--ink:#162033;--muted:#60708a;--line:#cbd5e1;--accent:#3659d9;--source:#0f8b6d}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif}body.dark{color-scheme:dark;--bg:#0d1119;--panel:#151c28;--ink:#edf2ff;--muted:#9aa9c2;--line:#344157;--accent:#8ea7ff;--source:#58d6b2}header{display:flex;gap:20px;align-items:center;padding:18px 24px;background:var(--panel);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:3}h1{font-size:24px;margin:2px 0}small{font-weight:700;letter-spacing:.12em;color:var(--accent)}#meta,footer{color:var(--muted);margin:0}.tools{margin-left:auto;display:flex;gap:8px;flex-wrap:wrap}.tools input,.tools button{border:1px solid var(--line);background:var(--panel);color:var(--ink);padding:8px 10px;border-radius:7px}main{display:grid;grid-template-columns:minmax(520px,1fr) minmax(290px,380px);min-height:calc(100vh - 130px)}#tree{overflow:auto;padding:24px}.branch,.branch ul{list-style:none;margin:0;padding-left:25px}.branch{padding-left:0}.branch li{margin:9px 0}.row{display:flex;align-items:flex-start;gap:7px}.toggle{width:28px;height:28px;border:1px solid var(--line);background:var(--panel);color:var(--ink);border-radius:6px}.toggle.blank{visibility:hidden}.node{max-width:720px;text-align:left;border:1px solid var(--line);border-left:4px solid var(--accent);background:var(--panel);color:var(--ink);padding:9px 12px;border-radius:8px;cursor:pointer}.node strong{display:block}.node span{color:var(--muted);font-size:12px}.hidden{display:none!important}.match>.row>.node{outline:3px solid color-mix(in srgb,var(--accent) 35%,transparent)}#details{border-left:1px solid var(--line);background:var(--panel);padding:22px;overflow:auto}#details h2{margin-top:0}.content{white-space:pre-wrap}.source{border-left:3px solid var(--source);padding:8px 10px;margin:9px 0;background:color-mix(in srgb,var(--source) 7%,transparent);overflow-wrap:anywhere}.source small{display:block;letter-spacing:0;color:var(--muted);font-weight:400}.excerpt{white-space:pre-wrap;margin-top:6px}footer{padding:13px 24px;border-top:1px solid var(--line);background:var(--panel)}@media(max-width:850px){header{align-items:flex-start;flex-direction:column}.tools{margin-left:0}main{grid-template-columns:1fr}#details{border-left:0;border-top:1px solid var(--line)}}
`

const classicMindMapExportHTMLJS = `
"use strict";
const p=JSON.parse(document.getElementById("mem-data").textContent),d=p.document,nodes=d.nodes,by=new Map(nodes.map(n=>[n.id,n])),kids=new Map,nodeElements=new Map;
for(const n of nodes){if(!kids.has(n.parent_id||""))kids.set(n.parent_id||"",[]);kids.get(n.parent_id||"").push(n)}
const tree=document.getElementById("tree"),details=document.getElementById("details"),titleElement=document.getElementById("title");
titleElement.textContent=d.map.title;titleElement.title=d.map.title;
document.getElementById("meta").textContent=p.node_count+" узлов · "+p.source_count+" источников · ревизия "+d.map.revision;
document.getElementById("footer").textContent="Карта "+d.map.id+" · "+p.map_digest+" · состояние "+p.state_digest;
function el(tag,cls,text){const x=document.createElement(tag);if(cls)x.className=cls;if(text!==undefined)x.textContent=text;return x}
function show(n){details.textContent="";details.append(el("h2","",n.label),el("div","",n.kind+" · "+n.origin));if(n.summary)details.append(el("h3","","Кратко"),el("div","content",n.summary));if(n.body_markdown)details.append(el("h3","","Описание"),el("div","content",n.body_markdown));details.append(el("h3","","Источники · "+n.sources.length));if(!n.sources.length)details.append(el("p","","Источники не привязаны."));for(const s of n.sources){const c=el("div","source"),a=s.evidence||{},sourceTitle=s.title||a.source_path||s.url||s.locator||"Источник",where=[];if(a.page)where.push("страница "+a.page);if(a.block_index!==undefined)where.push("блок "+(Number(a.block_index)+1));if(a.block_chunk_index!==undefined)where.push("фрагмент "+(Number(a.block_chunk_index)+1));const trace=[s.kind,s.evidence_state,a.source_path,s.knowledge_node_id,where.join(" · "),s.locator,s.url].filter(Boolean);c.append(el("strong","",sourceTitle),el("small","",trace.join(" · ")));if(a.excerpt)c.append(el("div","excerpt",a.excerpt));details.append(c)}}
const root=by.get(d.map.root_node_id),list=el("ul","branch");
if(root){const stack=[{node:root,parent:list}];while(stack.length){const current=stack.pop(),n=current.node,parent=current.parent,li=el("li",""),row=el("div","row"),children=kids.get(n.id)||[],toggle=el("button","toggle"+(children.length?"":" blank"),children.length?"−":""),button=el("button","node");button.dataset.nodeId=n.id;button.title=n.label;nodeElements.set(n.id,button);button.append(el("strong","",n.label),el("span","",n.kind+" · "+children.length+" ветв. · "+n.sources.length+" ист."));button.onclick=()=>show(n);row.append(toggle,button);li.append(row);parent.append(li);if(children.length){const ul=el("ul","");li.append(ul);toggle.onclick=()=>{ul.classList.toggle("hidden");toggle.textContent=ul.classList.contains("hidden")?"+":"−"};for(let i=children.length-1;i>=0;i--)stack.push({node:children[i],parent:ul})}}}
tree.append(list);
document.getElementById("expand").onclick=()=>document.querySelectorAll("#tree ul").forEach(x=>x.classList.remove("hidden"));
document.getElementById("collapse").onclick=()=>document.querySelectorAll("#tree li>ul").forEach(x=>x.classList.add("hidden"));
document.getElementById("theme").onclick=()=>document.body.classList.toggle("dark");
document.getElementById("search").oninput=e=>{const q=e.target.value.trim().toLocaleLowerCase();document.querySelectorAll("#tree li").forEach(x=>x.classList.remove("match"));if(!q)return;for(const n of nodes)if((n.label+" "+n.summary+" "+n.body_markdown).toLocaleLowerCase().includes(q)){const b=nodeElements.get(n.id);if(b){b.closest("li").classList.add("match");let u=b.closest("li").parentElement;while(u&&u.id!=="tree"){u.classList.remove("hidden");u=u.parentElement}}}};
if(root)show(root);
`

func renderClassicMindMapSVG(model classicMindMapExportModel) ([]byte, error) {
	metadata, err := json.Marshal(model.Portable)
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map SVG metadata: %w", err)
	}
	var body strings.Builder
	body.Grow(len(metadata) + len(model.Ordered)*700)
	fmt.Fprintf(&body, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\" role=\"img\">\n", model.Width, model.Height, model.Width, model.Height)
	body.WriteString("<title>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.Title))
	body.WriteString("</title><desc>Portable MEM classic mind map with complete tree and provenance metadata.</desc><metadata id=\"mem-provenance\">")
	body.WriteString(classicMindMapExportXMLEscape(string(metadata)))
	body.WriteString("</metadata><rect width=\"100%\" height=\"100%\" fill=\"#f5f7fb\"/><style>text{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;fill:#162033}.box{fill:#fff;stroke:#aebbd0;stroke-width:1.5}.edge{fill:none;stroke:#8391a8;stroke-width:2}.label{font-size:15px;font-weight:700}.meta{font-size:11px;fill:#60708a}.heading{font-size:24px;font-weight:700}.sub{font-size:12px;fill:#60708a}</style>")
	fmt.Fprintf(&body, "<text x=\"48\" y=\"42\" class=\"heading\">%s</text><text x=\"48\" y=\"67\" class=\"sub\">%s</text>", classicMindMapExportXMLEscape(model.Portable.Document.Map.Title), classicMindMapExportXMLEscape(fmt.Sprintf("%d nodes · %d sources · revision %d · %s", model.Portable.NodeCount, model.Portable.SourceCount, model.Portable.Document.Map.Revision, model.Portable.MapDigest)))
	for _, parent := range model.Ordered {
		for _, child := range parent.Children {
			x1, y1 := parent.X+classicMindMapExportNodeWidth, parent.Y+classicMindMapExportNodeHeight/2
			x2, y2 := child.X, child.Y+classicMindMapExportNodeHeight/2
			mid := (x1 + x2) / 2
			fmt.Fprintf(&body, "<path class=\"edge\" d=\"M%d %d C%d %d %d %d %d %d\"/>", x1, y1, mid, y1, mid, y2, x2, y2)
		}
	}
	for _, item := range model.Ordered {
		color := classicMindMapExportKindColor(item.Node.Kind)
		fmt.Fprintf(&body, "<g data-node-id=\"%s\" data-kind=\"%s\"><title>%s</title><rect class=\"box\" x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" rx=\"8\"/><rect x=\"%d\" y=\"%d\" width=\"5\" height=\"%d\" rx=\"2\" fill=\"%s\"/>", classicMindMapExportXMLAttr(item.Node.ID), classicMindMapExportXMLAttr(string(item.Node.Kind)), classicMindMapExportXMLEscape(item.Node.Label), item.X, item.Y, classicMindMapExportNodeWidth, classicMindMapExportNodeHeight, item.X, item.Y, classicMindMapExportNodeHeight, color)
		lines := classicMindMapExportWrap(item.Node.Label, 38, 2)
		body.WriteString("<text class=\"label\">")
		for line, text := range lines {
			fmt.Fprintf(&body, "<tspan x=\"%d\" y=\"%d\">%s</tspan>", item.X+17, item.Y+25+line*18, classicMindMapExportXMLEscape(text))
		}
		body.WriteString("</text>")
		fmt.Fprintf(&body, "<text x=\"%d\" y=\"%d\" class=\"meta\">%s · %d sources</text></g>", item.X+17, item.Y+classicMindMapExportNodeHeight-10, classicMindMapExportXMLEscape(string(item.Node.Kind)), len(item.Node.Sources))
	}
	body.WriteString("</svg>\n")
	return []byte(body.String()), nil
}

func renderClassicMindMapOPML(model classicMindMapExportModel) ([]byte, error) {
	var body strings.Builder
	body.Grow(model.Portable.NodeCount * 500)
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<opml version=\"2.0\" xmlns:mem=\"urn:mem-tool:classic-mind-map:1\" mem:export-version=\"1\" mem:format=\"opml\"><head><title>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.Title))
	body.WriteString("</title><ownerName>mem-tool</ownerName><docs>MEM portable classic mind map</docs><mem:mapId>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.ID))
	body.WriteString("</mem:mapId><mem:description>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.Description))
	body.WriteString("</mem:description><mem:mode>")
	body.WriteString(classicMindMapExportXMLEscape(string(model.Portable.Document.Map.Mode)))
	body.WriteString("</mem:mode><mem:status>")
	body.WriteString(classicMindMapExportXMLEscape(string(model.Portable.Document.Map.Status)))
	body.WriteString("</mem:status><mem:rootNodeId>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.RootNodeID))
	body.WriteString("</mem:rootNodeId><mem:revision>")
	body.WriteString(strconv.FormatInt(model.Portable.Document.Map.Revision, 10))
	body.WriteString("</mem:revision><mem:digest>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.MapDigest))
	body.WriteString("</mem:digest><mem:stateDigest>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.StateDigest))
	body.WriteString("</mem:stateDigest><mem:created>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.Created))
	body.WriteString("</mem:created><mem:updated>")
	body.WriteString(classicMindMapExportXMLEscape(model.Portable.Document.Map.Updated))
	body.WriteString("</mem:updated></head><body>")
	var writeNode func(*classicMindMapExportNode) error
	writeNode = func(item *classicMindMapExportNode) error {
		sources, err := json.Marshal(item.Node.Sources)
		if err != nil {
			return err
		}
		style, err := json.Marshal(item.Node.Style)
		if err != nil {
			return err
		}
		note := strings.TrimSpace(strings.TrimSpace(item.Node.Summary) + "\n\n" + strings.TrimSpace(item.Node.BodyMarkdown))
		encode := base64.RawStdEncoding.EncodeToString
		fmt.Fprintf(&body, "<outline text=\"%s\" type=\"%s\" mem:id=\"%s\" mem:parent-id=\"%s\" mem:position=\"%d\" mem:kind=\"%s\" mem:origin=\"%s\" mem:locked=\"%t\" mem:encoding=\"base64-raw-std-v1\" mem:summary=\"%s\" mem:body-markdown=\"%s\" mem:style=\"%s\" mem:created=\"%s\" mem:updated=\"%s\" mem:sources=\"%s\"", classicMindMapExportXMLAttr(item.Node.Label), classicMindMapExportXMLAttr(string(item.Node.Kind)), classicMindMapExportXMLAttr(item.Node.ID), classicMindMapExportXMLAttr(item.Node.ParentID), item.Node.Position, classicMindMapExportXMLAttr(string(item.Node.Kind)), classicMindMapExportXMLAttr(string(item.Node.Origin)), item.Node.Locked, encode([]byte(item.Node.Summary)), encode([]byte(item.Node.BodyMarkdown)), encode(style), classicMindMapExportXMLAttr(item.Node.Created), classicMindMapExportXMLAttr(item.Node.Updated), encode(sources))
		if note != "" {
			fmt.Fprintf(&body, " _note=\"%s\"", classicMindMapExportXMLAttr(note))
		}
		if len(item.Children) == 0 {
			body.WriteString("/>")
			return nil
		}
		body.WriteByte('>')
		for _, child := range item.Children {
			if err := writeNode(child); err != nil {
				return err
			}
		}
		body.WriteString("</outline>")
		return nil
	}
	if err := writeNode(model.Root); err != nil {
		return nil, fmt.Errorf("encode classic mind map OPML sources: %w", err)
	}
	body.WriteString("</body></opml>\n")
	return []byte(body.String()), nil
}

func classicMindMapExportKindColor(kind ClassicMindMapNodeKind) string {
	switch kind {
	case ClassicMindMapNodeTopic:
		return "#3659d9"
	case ClassicMindMapNodeFact:
		return "#0f8b6d"
	case ClassicMindMapNodeQuestion:
		return "#b06b13"
	case ClassicMindMapNodeTask:
		return "#8a4bc2"
	case ClassicMindMapNodeDecision:
		return "#c03d5b"
	case ClassicMindMapNodeQuote:
		return "#53718f"
	default:
		return "#64748b"
	}
}

func classicMindMapExportWrap(value string, columns, maxLines int) []string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return []string{"(empty)"}
	}
	var lines []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			lines = append(lines, string(current))
			current = nil
		}
	}
	for _, word := range strings.Fields(value) {
		wordRunes := []rune(word)
		for len(wordRunes) > columns {
			if len(current) > 0 {
				flush()
			}
			lines = append(lines, string(wordRunes[:columns]))
			wordRunes = wordRunes[columns:]
		}
		needed := len(wordRunes)
		if len(current) > 0 {
			needed++
		}
		if len(current)+needed > columns {
			flush()
		}
		if len(current) > 0 {
			current = append(current, ' ')
		}
		current = append(current, wordRunes...)
	}
	flush()
	if len(lines) <= maxLines {
		return lines
	}
	lines = lines[:maxLines]
	last := []rune(lines[maxLines-1])
	if len(last) > columns-3 {
		last = last[:columns-3]
	}
	lines[maxLines-1] = strings.TrimSpace(string(last)) + "..."
	return lines
}

func classicMindMapExportFilename(title string, format ClassicMindMapExportFormat) string {
	var slug []rune
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			slug = append(slug, r)
			dash = false
		} else if len(slug) > 0 && !dash {
			slug = append(slug, '-')
			dash = true
		}
		if len(slug) >= 72 {
			break
		}
	}
	for len(slug) > 0 && slug[len(slug)-1] == '-' {
		slug = slug[:len(slug)-1]
	}
	if len(slug) == 0 {
		slug = []rune("map")
	}
	extension := string(format)
	switch format {
	case ClassicMindMapExportMarkdown:
		extension = "md"
	case ClassicMindMapExportMermaid:
		extension = "mmd"
	case ClassicMindMapExportObsidian:
		extension = "obsidian.md"
	}
	return "mem-mindmap-" + string(slug) + "." + extension
}

func classicMindMapExportXMLEscape(value string) string {
	var body bytes.Buffer
	_ = xml.EscapeText(&body, []byte(classicMindMapExportXMLSafe(value)))
	return body.String()
}

func classicMindMapExportXMLAttr(value string) string {
	return classicMindMapExportXMLEscape(value)
}

func classicMindMapExportXMLSafe(value string) string {
	if utf8.ValidString(value) {
		var body strings.Builder
		body.Grow(len(value))
		for _, r := range value {
			if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= unicode.MaxRune {
				body.WriteRune(r)
			} else {
				body.WriteRune('\ufffd')
			}
		}
		return body.String()
	}
	return strings.ToValidUTF8(value, "\ufffd")
}
