package mem

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"reflect"
	"strings"
	"testing"
)

type xmindTestMember struct {
	name string
	data string
	mode os.FileMode
}

func xmindTestZIP(t *testing.T, members ...xmindTestMember) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, member := range members {
		header := &zip.FileHeader{Name: member.name, Method: zip.Deflate}
		if member.mode != 0 {
			header.SetMode(member.mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(member.name, "/") {
			if _, err := entry.Write([]byte(member.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

const xmindTestSimple = `[{"id":"sheet1","title":"Лист 😀","rootTopic":{"id":"root","title":"Давление"}}]`

func xmindTestJSON(t *testing.T, content string) (OutlineImportDraft, error) {
	t.Helper()
	return parseOutlineXMind(xmindTestZIP(t, xmindTestMember{name: "content.json", data: content}))
}

func TestOutlineXMindModernSheetsHierarchyAndLossWarnings(t *testing.T) {
	// Shape follows the official xmind-viewer/example/content.json and model
	// schemas, with our own Unicode text and every supported children group.
	content := `[
	 {"id":"sheet1","title":"Гидравлика 😀","theme":{"centralTopic":{}},"relationships":[{},{}],
	  "rootTopic":{"id":"root1","title":"Давление","href":"https://example.org/spec?q=1#page=2",
	   "notes":{"plain":{"content":"Первая строка\nДавление 5 бар 😀"},"html":{"content":{}}},
	   "labels":["важно","расчёт"],"markers":[{},{}],"image":{"src":"xap:resources/a.png"},"style":{},"extensions":[{}],
	   "children":{"attached":[{"id":"child1","title":"Насос","href":"xmind:#root1"}],
	    "detached":[{"id":"floating","title":"Отдельная","children":{"attached":[{"id":"nested","title":"Вложенная"}]}}],
	    "summary":[{"id":"sum","title":"Итог"}],"callout":[{"id":"call","title":"Пояснение"}]}}},
	 {"id":"sheet2","title":"Второй лист","rootTopic":{"id":"root2","title":"Температура","href":"file:///C:/private.txt","unknownOption":true}}
	]`
	data := xmindTestZIP(t, xmindTestMember{name: "content.json", data: content}, xmindTestMember{name: "resources/a.png", data: "opaque embedded bytes"})
	draft, err := parseOutlineXMind(data)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "Книга XMind" || len(draft.Nodes) != 8 {
		t.Fatalf("unexpected workbook: %+v", draft)
	}
	wantLabels := []string{"Книга XMind", "Давление", "Насос", "Отдельная", "Вложенная", "Итог", "Пояснение", "Температура"}
	wantParents := []string{"", "n1", "n2", "n2", "n4", "n2", "n2", "n1"}
	for i, node := range draft.Nodes {
		if node.Ref != fmt.Sprintf("n%d", i+1) || node.Label != wantLabels[i] || node.ParentRef != wantParents[i] || node.Origin != ClassicMindMapNodeImported {
			t.Fatalf("node %d: %+v", i, node)
		}
		if i != 1 && len(node.Sources) != 0 {
			t.Fatalf("unsafe source activated: %+v", node)
		}
	}
	root := draft.Nodes[1]
	if !strings.HasPrefix(root.BodyMarkdown, "Первая строка\nДавление 5 бар 😀") || !strings.Contains(root.BodyMarkdown, "Метки: важно, расчёт") || !strings.Contains(root.BodyMarkdown, "Лист XMind: Гидравлика 😀") {
		t.Fatalf("notes/labels/sheet lost: %+v", root)
	}
	if len(root.Sources) != 1 || root.Sources[0].Kind != ClassicMindMapSourceURL || root.Sources[0].URL != "https://example.org/spec?q=1#page=2" {
		t.Fatalf("ordinary URL not retained: %+v", root.Sources)
	}
	warnings := strings.Join(draft.Warnings, "\n")
	for _, category := range []string{"archive_assets — 1", "relationships — 2", "styles — 2", "markers — 2", "images — 1", "inactive_links — 2", "detached — 1", "summary — 1", "callout — 1", "rich_notes — 1", "unknown_properties — 1", "extensions — 1"} {
		if !strings.Contains(warnings, category) {
			t.Errorf("missing warning %q: %s", category, warnings)
		}
	}
	// Stable conversion and digest despite Go map iteration order.
	input := OutlineImportInput{Format: "xmind", Filename: "Workbook.xmind", ContentBase64: base64.StdEncoding.EncodeToString(data)}
	first, err := PreviewMindMapOutline(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PreviewMindMapOutline(input)
	if err != nil || !reflect.DeepEqual(first, second) || first.NodeCount != 8 || first.SourceCount != 1 {
		t.Fatalf("unstable/common preview: %+v / %+v / %v", first, second, err)
	}
}

func TestOutlineXMindLegacyXMLPreservesNotesAndDetached(t *testing.T) {
	content := `<?xml version="1.0" encoding="UTF-8"?>
	<xmap-content xmlns="urn:xmind:xmap:xmlns:content:2.0" xmlns:xlink="http://www.w3.org/1999/xlink">
	 <sheet id="s1"><title>Схема</title><topic id="root" xlink:href="https://example.org/doc" style-id="s9">
	  <title>Узел 😀</title><notes><plain><![CDATA[Текст <как есть>\n5 бар]]></plain><html><p>rich</p></html></notes>
	  <labels><label>метка</label></labels><marker-refs><marker-ref marker-id="priority-1"/></marker-refs>
	  <children><topics type="attached"><topic id="attached"><title>Дочерняя</title></topic></topics>
	   <topics type="detached"><topic id="floating"><title>Отдельная</title></topic></topics></children>
	 </topic><relationships><relationship id="rel" end1="root" end2="attached"/></relationships></sheet>
	 <sheet id="s2"><title>Другой лист</title><topic id="root2"><title>Вторая</title></topic></sheet>
	</xmap-content>`
	draft, err := parseOutlineXMind(xmindTestZIP(t, xmindTestMember{name: "content.xml", data: content}, xmindTestMember{name: "META-INF/manifest.xml", data: "<manifest/>"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Nodes) != 5 || draft.Nodes[1].Label != "Узел 😀" || draft.Nodes[2].ParentRef != "n2" || draft.Nodes[3].ParentRef != "n2" || draft.Nodes[4].ParentRef != "n1" {
		t.Fatalf("legacy hierarchy lost: %+v", draft.Nodes)
	}
	if !strings.HasPrefix(draft.Nodes[1].BodyMarkdown, `Текст <как есть>\n5 бар`) || len(draft.Nodes[1].Sources) != 1 {
		t.Fatalf("legacy content lost: %+v", draft.Nodes[1])
	}
	for _, category := range []string{"detached — 1", "markers — 1", "relationships — 1", "rich_notes — 1", "styles — 1"} {
		if !strings.Contains(strings.Join(draft.Warnings, "\n"), category) {
			t.Errorf("missing %s: %v", category, draft.Warnings)
		}
	}
}

func TestOutlineXMindRejectsAmbiguousAndUnsafeZIP(t *testing.T) {
	for _, name := range []string{"../secret", "/absolute", `a\b`, "C:/drive", "a/../b", "a//b", "a/./b", "trailing. /b", "dir./x", "NUL.txt", "name\x00x", "name\nx", "a//", "./content.json"} {
		t.Run(fmt.Sprintf("path_%q", name), func(t *testing.T) {
			_, err := parseOutlineXMind(xmindTestZIP(t, xmindTestMember{name: "content.json", data: xmindTestSimple}, xmindTestMember{name: name, data: "x"}))
			if !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("unsafe path accepted %q: %v", name, err)
			}
		})
	}
	cases := map[string][]xmindTestMember{
		"duplicate":            {{name: "content.json", data: xmindTestSimple}, {name: "content.json", data: xmindTestSimple}},
		"case_alias":           {{name: "content.json", data: xmindTestSimple}, {name: "CONTENT.JSON", data: xmindTestSimple}},
		"two_formats":          {{name: "content.json", data: xmindTestSimple}, {name: "content.xml", data: "<xmap-content/>"}},
		"file_directory_alias": {{name: "content.json", data: xmindTestSimple}, {name: "A", data: "file"}, {name: "a/b", data: "x"}},
		"symlink":              {{name: "content.json", data: xmindTestSimple}, {name: "link", data: "/private", mode: os.ModeSymlink | 0777}},
		"missing":              {{name: "other.json", data: xmindTestSimple}},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOutlineXMind(xmindTestZIP(t, members...)); !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name          string
		flags, method uint16
	}{
		{"encryption", 1, zip.Store}, {"strong_encryption", 64, zip.Store}, {"unsupported_method", 0, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := xmindTestRawZIP(t, zip.FileHeader{Name: "content.json", Flags: tc.flags, Method: tc.method, UncompressedSize64: uint64(len(xmindTestSimple)), CompressedSize64: uint64(len(xmindTestSimple)), CRC32: crc32.ChecksumIEEE([]byte(xmindTestSimple))}, []byte(xmindTestSimple))
			if _, err := parseOutlineXMind(data); !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}

func xmindTestRawZIP(t *testing.T, header zip.FileHeader, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.CreateRaw(&header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestOutlineXMindEnforcesRawExpandedAndEntryBudgets(t *testing.T) {
	if _, err := parseOutlineXMind(make([]byte, MaxOutlineImportBytes+1)); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("raw limit: %v", err)
	}
	for _, tc := range []struct {
		name string
		size uint64
	}{
		{"content.json", MaxOutlineImportBytes + 1}, {"asset", xmindMaxExpanded + 1},
	} {
		data := xmindTestRawZIP(t, zip.FileHeader{Name: tc.name, Method: zip.Deflate, UncompressedSize64: tc.size}, nil)
		if _, err := parseOutlineXMind(data); !errors.Is(err, ErrOutlineImportLimit) {
			t.Fatalf("declared limit %s: %v", tc.name, err)
		}
	}
	members := []xmindTestMember{{name: "content.json", data: xmindTestSimple}}
	for i := 0; i < xmindMaxEntries; i++ {
		members = append(members, xmindTestMember{name: fmt.Sprintf("assets/%d", i)})
	}
	if _, err := parseOutlineXMind(xmindTestZIP(t, members...)); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("entry limit: %v", err)
	}
	// Highly compressible data is small on wire, but still exceeds content and
	// aggregate expanded budgets. No allocation is based on its declared size.
	large := strings.Repeat(" ", MaxOutlineImportBytes+1)
	if _, err := xmindTestJSON(t, large); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("compressed content bomb: %v", err)
	}
	members = []xmindTestMember{{name: "content.json", data: xmindTestSimple}}
	for i := 0; i < 4; i++ {
		members = append(members, xmindTestMember{name: fmt.Sprintf("asset-%d", i), data: large})
	}
	if _, err := parseOutlineXMind(xmindTestZIP(t, members...)); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("aggregate bomb: %v", err)
	}
}

func TestOutlineXMindVerifiesCRCEvenForIgnoredAssets(t *testing.T) {
	for _, target := range []string{"content.json", "resources/image.bin"} {
		t.Run(target, func(t *testing.T) {
			data := xmindTestZIP(t, xmindTestMember{name: "content.json", data: xmindTestSimple}, xmindTestMember{name: "resources/image.bin", data: "attachment"})
			// Corrupt the advertised central CRC of the selected member. The
			// content bytes remain valid, exposing readers which stop early.
			for pos := 0; pos+46 <= len(data); pos++ {
				if binary.LittleEndian.Uint32(data[pos:]) != 0x02014b50 {
					continue
				}
				n := int(binary.LittleEndian.Uint16(data[pos+28:]))
				if pos+46+n > len(data) {
					continue
				}
				if string(data[pos+46:pos+46+n]) == target {
					data[pos+16] ^= 0x80
					break
				}
			}
			if _, err := parseOutlineXMind(data); !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("CRC corruption accepted: %v", err)
			}
		})
	}
	// Declaring fewer expanded bytes does not bypass the bounded reader.
	data := xmindTestRawZIP(t, zip.FileHeader{Name: "content.json", Method: zip.Store, UncompressedSize64: 1, CompressedSize64: uint64(len(xmindTestSimple)), CRC32: crc32.ChecksumIEEE([]byte(xmindTestSimple))}, []byte(xmindTestSimple))
	if _, err := parseOutlineXMind(data); !errors.Is(err, ErrOutlineImportInvalid) {
		t.Fatalf("lying length accepted: %v", err)
	}
}

func TestOutlineXMindRejectsMalformedUnicodeAndStructures(t *testing.T) {
	cases := []string{
		`[]`, `{}`, `null`, `[null]`, `[{"rootTopic":null}]`, `[{"rootTopic":{"id":"x","title":null}}]`,
		`[{"rootTopic":{"id":"x","title":"one","title":"two"}}]`,
		`[{"rootTopic":{"id":"x","title":"\ud800"}}]`, `[{"rootTopic":{"id":"x","title":"\udc00"}}]`,
		`[{"rootTopic":{"id":"x","title":"bad\u0000"}}]`, `[{"rootTopic":{"id":"x","title":"bad\u0001"}}]`,
		`[{"rootTopic":{"id":"x","title":42}}]`, `[{"rootTopic":{"id":"x","children":{"attached":{}}}}]`,
		`[{"rootTopic":{"id":"x","notes":{"plain":[]}}}]`, `[{"rootTopic":{"id":"x","labels":[42]}}]`,
		`[{"rootTopic":{"id":"x","children":{"attached":[{"id":"x"}]}}}]`,
		xmindTestSimple + ` true`, strings.Replace(xmindTestSimple, "Давление", string([]byte{0xff}), 1),
	}
	for i, data := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := xmindTestJSON(t, data); !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("accepted %s: %v", data, err)
			}
		})
	}
	draft, err := xmindTestJSON(t, `[{"rootTopic":{"id":"x","title":"\ud83d\ude00","labels":"строка","children":{"custom":[{"id":"y","title":"дочерняя"}]}}}]`)
	if err != nil || draft.Nodes[0].Label != "😀" || len(draft.Nodes) != 2 || !strings.Contains(strings.Join(draft.Warnings, "\n"), "other_children — 1") {
		t.Fatalf("valid surrogate/unknown group: %+v %v", draft, err)
	}
	for _, href := range []string{"javascript:alert(1)", "file:///secret", "xap:resources/a", "../file", "https://user:pass@example.org", "https://example.org\\private"} {
		node := map[string]any{"id": "x", "title": "safe", "href": href}
		raw, _ := json.Marshal([]any{map[string]any{"rootTopic": node}})
		draft, err := xmindTestJSON(t, string(raw))
		if err != nil || len(draft.Nodes[0].Sources) != 0 || !strings.Contains(strings.Join(draft.Warnings, "\n"), "inactive_links — 1") {
			t.Fatalf("active unsafe URL %q: %+v %v", href, draft, err)
		}
	}
}

func TestOutlineXMindEnforcesNodeDepthAndTextLimits(t *testing.T) {
	makeTree := func(depth int) string {
		var node any = map[string]any{"id": fmt.Sprint(depth), "title": "Последняя"}
		for i := depth - 1; i >= 0; i-- {
			node = map[string]any{"id": fmt.Sprint(i), "children": map[string]any{"attached": []any{node}}}
		}
		data, _ := json.Marshal([]any{map[string]any{"rootTopic": node}})
		return string(data)
	}
	if draft, err := xmindTestJSON(t, makeTree(MaxOutlineImportDepth-1)); err != nil || len(draft.Nodes) != MaxOutlineImportDepth {
		t.Fatalf("allowed depth: %d %v", len(draft.Nodes), err)
	}
	if _, err := xmindTestJSON(t, makeTree(MaxOutlineImportDepth)); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("depth limit: %v", err)
	}
	children := []any{}
	for i := 0; i < MaxOutlineImportNodes; i++ {
		children = append(children, map[string]any{"id": fmt.Sprintf("child%d", i), "title": "Дочерняя"})
	}
	data, _ := json.Marshal([]any{map[string]any{"rootTopic": map[string]any{"id": "root", "children": map[string]any{"attached": children}}}})
	if _, err := xmindTestJSON(t, string(data)); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("node limit: %v", err)
	}
	for _, node := range []map[string]any{
		{"id": "x", "title": strings.Repeat("😀", MaxClassicMindMapTitleRunes+1)},
		{"id": "x", "title": "short", "notes": map[string]any{"plain": map[string]any{"content": strings.Repeat("я", MaxClassicMindMapTextRunes+1)}}},
	} {
		data, _ := json.Marshal([]any{map[string]any{"rootTopic": node}})
		if _, err := xmindTestJSON(t, string(data)); !errors.Is(err, ErrOutlineImportLimit) {
			t.Fatalf("text limit: %v", err)
		}
	}
	metadata := `[{"rootTopic":{"id":"x"},"unknown":[` + strings.Repeat(`{},`, xmindMaxTokens) + `{}]}]`
	if _, err := xmindTestJSON(t, metadata); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatalf("metadata token budget: %v", err)
	}
}

func TestOutlineXMindLegacyRejectsDTDNamespaceSpoofAndDuplicates(t *testing.T) {
	cases := []string{
		`<!DOCTYPE xmap-content [<!ENTITY ext SYSTEM "file:///secret">]><xmap-content><sheet><topic id="x"><title>&ext;</title></topic></sheet></xmap-content>`,
		`<xmap-content><sheet><topic id="x" id="y"/></sheet></xmap-content>`,
		`<xmap-content><sheet><topic id="x"><title>one</title><title>two</title></topic></sheet></xmap-content>`,
		`<xmap-content><sheet><topic id="x"/><topic id="y"/></sheet></xmap-content>`,
		`<xmap-content xmlns="https://attacker.example"><sheet><topic id="x"/></sheet></xmap-content>`,
		`<xmap-content xmlns:e="https://attacker.example"><sheet><e:topic id="x"/></sheet></xmap-content>`,
		`<xmap-content><sheet><topic id="x"><children><topics type="attached"/><topics type="attached"/></children></topic></sheet></xmap-content>`,
		`<xmap-content><sheet><topic id="x"><notes><plain>a</plain><plain>b</plain></notes></topic></sheet></xmap-content>`,
		`<?evil run?><xmap-content/>`, `<xmap-content/>trailing`,
	}
	for i, content := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := parseOutlineXMind(xmindTestZIP(t, xmindTestMember{name: "content.xml", data: content})); !errors.Is(err, ErrOutlineImportInvalid) {
				t.Fatalf("accepted XML: %v", err)
			}
		})
	}
	// XML uses three elements for each tree level; its syntax budget must not
	// accidentally narrow the valid semantic depth or omit the synthetic root.
	makeTree := func(levels int) string {
		var tree strings.Builder
		tree.WriteString(`<xmap-content><sheet id="s"><title>Лист</title>`)
		for i := 0; i < levels; i++ {
			fmt.Fprintf(&tree, `<topic id="n%d"><title>Тема</title>`, i)
			if i+1 < levels {
				tree.WriteString(`<children><topics type="attached">`)
			}
		}
		for i := levels - 1; i >= 0; i-- {
			tree.WriteString(`</topic>`)
			if i > 0 {
				tree.WriteString(`</topics></children>`)
			}
		}
		tree.WriteString(`</sheet></xmap-content>`)
		return tree.String()
	}
	for _, levels := range []int{MaxOutlineImportDepth, MaxOutlineImportDepth + 1} {
		draft, err := parseOutlineXMind(xmindTestZIP(t, xmindTestMember{name: "content.xml", data: makeTree(levels)}))
		if levels == MaxOutlineImportDepth && (err != nil || len(draft.Nodes) != levels) {
			t.Fatalf("valid XML depth: %d %v", len(draft.Nodes), err)
		}
		if levels > MaxOutlineImportDepth && !errors.Is(err, ErrOutlineImportLimit) {
			t.Fatalf("XML depth limit: %v", err)
		}
	}
}
