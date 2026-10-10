package mem

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func requireOutlineXML(t *testing.T, raw, format string) OutlineImportDraft {
	t.Helper()
	draft, err := parseOutlineXML([]byte(raw), format)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, node := range draft.Nodes {
		if node.Ref != fmt.Sprintf("n%d", i+1) || i == 0 && node.ParentRef != "" || i > 0 && !seen[node.ParentRef] || node.Origin != ClassicMindMapNodeImported || utf8.RuneCountInString(node.Label) > 512 || utf8.RuneCountInString(node.BodyMarkdown) > 65536 {
			t.Fatalf("invalid draft node %d: %#v", i, node)
		}
		seen[node.Ref] = true
		for _, source := range node.Sources {
			if source.Kind != ClassicMindMapSourceURL || source.Evidence != nil || source.Locator != "" || source.KnowledgeNodeID != "" {
				t.Fatal("untrusted evidence or file became a source", source)
			}
		}
	}
	warnings := map[string]bool{}
	for _, warning := range draft.Warnings {
		if warning == "" || warnings[warning] {
			t.Fatal("empty/duplicate warning", draft.Warnings)
		}
		warnings[warning] = true
	}
	return draft
}
func requireOutlineWarning(t *testing.T, draft OutlineImportDraft, part string) {
	t.Helper()
	if !strings.Contains(strings.Join(draft.Warnings, "\n"), part) {
		t.Fatalf("missing warning %q: %#v", part, draft.Warnings)
	}
}
func TestOutlineXMLOPMLHierarchyNotesAndIncludes(t *testing.T) {
	raw := `<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>Русский 🌡️ план</title><ownerName>Author</ownerName></head><body>
<outline text="Первый" _note="Строка 1&#10;Строка 2"><outline text="Вложенный" type="LINK" url="https://example.org/a?q=1&amp;b=2"/></outline>
<outline text="Второй" type="include" url="http://127.0.0.1:1/not-fetched.opml" custom="not imported"/>
<outline text="Подписка" type="RSS" xmlUrl="https://example.org/feed.xml" htmlUrl="file:///private/source.pdf"/></body></opml>`
	draft := requireOutlineXML(t, raw, "opml")
	if draft.Title != "Русский 🌡️ план" || len(draft.Nodes) != 5 || draft.Nodes[0].Label != draft.Title || draft.Nodes[1].ParentRef != "n1" || draft.Nodes[2].ParentRef != "n2" || draft.Nodes[3].ParentRef != "n1" || draft.Nodes[1].BodyMarkdown != "Строка 1\nСтрока 2" {
		t.Fatal(draft)
	}
	if draft.Nodes[2].Sources[0].URL != "https://example.org/a?q=1&b=2" || len(draft.Nodes[4].Sources) != 1 {
		t.Fatal(draft.Nodes)
	}
	requireOutlineWarning(t, draft, "include/RSS")
	requireOutlineWarning(t, draft, "организационный корень")
	requireOutlineWarning(t, draft, "Небезопасные")
	if strings.Contains(strings.Join(draft.Warnings, " "), "/private/") {
		t.Fatal("warnings must not echo private locators")
	}
	one := requireOutlineXML(t, `<opml version="2.0"><head><title>Title</title></head><body><outline text="Root"/></body></opml>`, "opml")
	if one.Title != "Title" || len(one.Nodes) != 1 || one.Nodes[0].Label != "Root" {
		t.Fatal(one)
	}
	fallback := requireOutlineXML(t, `<opml version="2.0"><head/><body><outline text="A"/><outline text="B"/></body></opml>`, "opml")
	if fallback.Title != "Импорт OPML" || fallback.Nodes[0].Label != fallback.Title {
		t.Fatal(fallback)
	}
}

func TestOutlineXMLOPMLMetadataPreservesTextWithoutTrustingEvidence(t *testing.T) {
	encode := func(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) }
	summary, body := "Обзор 🧭", "**Тело**\n```tex\nx_i = 1\n```"
	raw := fmt.Sprintf(`<opml version="2.0" xmlns:mem="%s"><head/><body><outline text="Root" mem:encoding="base64-raw-std-v1" mem:summary="%s" mem:body-markdown="%s" mem:style="%s" mem:sources="%s" mem:kind="question" mem:id="foreign" mem:locked="true" _note="Обзор 🧭&#10;&#10;**Тело**&#10;`+"```tex"+`&#10;x_i = 1&#10;`+"```"+`"/></body></opml>`, outlineMemNamespace, encode(summary), encode(body), encode(`{"color":"red"}`), encode(`[{"kind":"evidence","evidence":{"source_path":"private.pdf"}}]`))
	draft := requireOutlineXML(t, raw, "opml")
	n := draft.Nodes[0]
	if n.Summary != summary || n.BodyMarkdown != body || n.Kind != ClassicMindMapNodeQuestion || n.Locked || len(n.Style) != 0 || len(n.Sources) != 0 || n.Ref != "n1" {
		t.Fatal(n)
	}
	requireOutlineWarning(t, draft, "mem:style")
	requireOutlineWarning(t, draft, "evidence/anchors")
	changed := strings.Replace(raw, `_note="Обзор 🧭`, `_note="Дополнительная заметка`, 1)
	other := requireOutlineXML(t, changed, "opml")
	if !strings.Contains(other.Nodes[0].BodyMarkdown, "Дополнительная заметка") || !strings.HasPrefix(other.Nodes[0].BodyMarkdown, body) {
		t.Fatal("different note was silently lost", other)
	}
	bad := strings.Replace(raw, `mem:summary="`+encode(summary)+`"`, `mem:summary="%%%"`, 1)
	if _, err := parseOutlineXML([]byte(bad), "opml"); !errors.Is(err, ErrOutlineImportInvalid) {
		t.Fatal(err)
	}
}

func TestOutlineXMLOPMLActualExporterRoundTripTextAndHierarchy(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{Title: "Exported title", Mode: ClassicMindMapModeManual, Status: ClassicMindMapStatusDraft, Nodes: []ClassicMindMapNodeDraft{
		{Ref: "root", Label: "Root", Summary: "summary 😀", BodyMarkdown: "body\nwith breaks"},
		{Ref: "child", ParentRef: "root", Label: "Child", Kind: ClassicMindMapNodeQuote, BodyMarkdown: "<literal>"},
	}}, "test", "XML import fixture")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: ClassicMindMapExportOPML, ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest})
	if err != nil {
		t.Fatal(err)
	}
	draft := requireOutlineXML(t, string(artifact.Data), "opml")
	if draft.Title != doc.Map.Title || len(draft.Nodes) != 2 || draft.Nodes[0].Summary != "summary 😀" || draft.Nodes[0].BodyMarkdown != "body\nwith breaks" || draft.Nodes[1].ParentRef != "n1" || draft.Nodes[1].BodyMarkdown != "<literal>" || draft.Nodes[1].Kind != ClassicMindMapNodeQuote {
		t.Fatal(draft)
	}
}

func TestOutlineXMLFreeMindRichTextNotesSourcesAndIgnoredObjects(t *testing.T) {
	raw := `<map version="1.0.1"><node TEXT="Root" ID="external" COLOR="#ff0000" FOLDED="true" LINK="https://example.org/root">
<richcontent TYPE="NODE"><html xmlns="http://www.w3.org/1999/xhtml"><head/><body><p>Rich <b>root 😀</b></p><p>Second<br/>Third</p></body></html></richcontent>
<richcontent TYPE="NOTE"><html><body><p>Note <a href="https://example.org/note">link</a>.</p><pre>x_i = 1&#10;  y = 2</pre><img src="file:///private/image.png"/><script>alert(1)</script><style>x</style><p>After</p></body></html></richcontent>
<icon BUILTIN="idea"/><arrowlink DESTINATION="external"/><node TEXT="Child" LINK="#external"><richcontent TYPE="NOTE"><html><body><p>Child note</p></body></html></richcontent></node></node></map>`
	draft := requireOutlineXML(t, raw, "freemind")
	if draft.Title != "Root" || len(draft.Nodes) != 2 || draft.Nodes[1].ParentRef != "n1" || draft.Nodes[1].BodyMarkdown != "Child note" || len(draft.Nodes[1].Sources) != 0 {
		t.Fatal(draft)
	}
	body := draft.Nodes[0].BodyMarkdown
	if body != "Rich root 😀\nSecond\nThird\n\nNote link.\nx_i = 1\n  y = 2\nAfter" || len(draft.Nodes[0].Sources) != 2 || strings.Contains(body, "alert") || strings.Contains(body, "private") {
		t.Fatal(body, draft.Nodes[0].Sources)
	}
	requireOutlineWarning(t, draft, "scripts/styles")
	requireOutlineWarning(t, draft, "icons, arrows")
	requireOutlineWarning(t, draft, "различающиеся TEXT")
	second := requireOutlineXML(t, raw, "freemind")
	if !reflect.DeepEqual(draft, second) {
		t.Fatal("parser result is not deterministic")
	}
}

func TestOutlineXMLFreeMindRichLabelAndUnknownContentAreNotLost(t *testing.T) {
	long := strings.Repeat("Ж", 520) + " 😀"
	raw := `<map version="1.0.1"><node><richcontent TYPE="NODE"><html><body><p>` + long + `</p></body></html></richcontent><richcontent TYPE="OTHER"><html><body><custom>Other text</custom></body></html></richcontent></node></map>`
	draft := requireOutlineXML(t, raw, "freemind")
	if utf8.RuneCountInString(draft.Nodes[0].Label) != 512 || !strings.Contains(draft.Nodes[0].BodyMarkdown, long) || !strings.Contains(draft.Nodes[0].BodyMarkdown, "Other text") {
		t.Fatal(draft)
	}
	requireOutlineWarning(t, draft, "полный текст сохранён")
	requireOutlineWarning(t, draft, "неизвестный TYPE")
	short := requireOutlineXML(t, `<map><node><richcontent TYPE="NODE"><html><body><p>Первая</p><p>Вторая</p></body></html></richcontent></node></map>`, "freemind")
	if short.Nodes[0].Label != "Первая\nВторая" || short.Title != "Первая\nВторая" {
		t.Fatal(short)
	}
}

func TestOutlineXMLRejectsMalformedDTDNamespacesAndDuplicates(t *testing.T) {
	opml := `<opml version="2.0"><head/><body><outline text="Root"/></body></opml>`
	cases := []struct{ name, raw, format string }{
		{"DTD", `<!DOCTYPE opml [<!ENTITY steal SYSTEM "file:///private/secret">]>` + opml, "opml"},
		{"externalDTD", `<!DOCTYPE map SYSTEM "https://example.org/map.dtd"><map><node TEXT="Root"/></map>`, "freemind"},
		{"entity", `<map><node TEXT="&unknown;"/></map>`, "freemind"},
		{"stylesheet", `<?xml-stylesheet href="https://example.org/x"?><map><node TEXT="Root"/></map>`, "freemind"},
		{"badUTF8", opml + string([]byte{0xff}), "opml"},
		{"UTF16", `<?xml version="1.0" encoding="UTF-16"?>` + opml, "opml"},
		{"duplicate", `<map><node TEXT="A" TEXT="B"/></map>`, "freemind"},
		{"namespaceAttribute", `<map xmlns:x="evil"><node TEXT="A" x:TEXT="B"/></map>`, "freemind"},
		{"namespaceRichURL", `<map xmlns:x="evil"><node TEXT="A"><richcontent TYPE="NOTE"><html><a x:href="https://example.org">link</a></html></richcontent></node></map>`, "freemind"},
		{"namespaceAlias", `<opml xmlns:x="evil"><head/><body><outline x:text="B"/></body></opml>`, "opml"},
		{"namespaceMetadata", `<opml xmlns:mem="evil"><head/><body><outline text="A" mem:body-markdown="eA"/></body></opml>`, "opml"},
		{"namespaceElement", `<map xmlns:x="http://www.w3.org/1999/xhtml"><x:node TEXT="A"/></map>`, "freemind"},
		{"duplicateExpandedAttr", `<opml xmlns:a="urn:mem-tool:classic-mind-map:1" xmlns:b="urn:mem-tool:classic-mind-map:1"><head/><body><outline text="A" a:summary="eA" b:summary="eQ"/></body></opml>`, "opml"},
		{"duplicateHead", `<opml><head/><head/><body><outline text="A"/></body></opml>`, "opml"},
		{"duplicateTitle", `<opml><head><title>A</title><title>B</title></head><body><outline text="A"/></body></opml>`, "opml"},
		{"missingText", `<opml><head/><body><outline title="A"/></body></opml>`, "opml"},
		{"missingBody", `<opml><head/></opml>`, "opml"},
		{"twoFreeMindRoots", `<map><node TEXT="A"/><node TEXT="B"/></map>`, "freemind"},
		{"twoRoots", `<map><node TEXT="A"/></map><map/>`, "freemind"},
		{"mismatched", `<map><node TEXT="A"></map>`, "freemind"},
		{"wrongFormat", opml, "freemind"},
		{"unknownFormat", opml, "xml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := parseOutlineXML([]byte(c.raw), c.format); !errors.Is(err, ErrOutlineImportInvalid) || len(got.Nodes) != 0 {
				t.Fatal(got, err)
			}
		})
	}
	for _, raw := range []string{opml, "\ufeff" + opml, `<?xml version="1.0" encoding="UTF-8"?>` + opml} {
		requireOutlineXML(t, raw, "opml")
	}
}

func TestOutlineXMLLimitsBeforePartialResult(t *testing.T) {
	chain := func(nodes int) string {
		return `<map>` + strings.Repeat(`<node TEXT="N">`, nodes) + strings.Repeat(`</node>`, nodes) + `</map>`
	}
	if got := requireOutlineXML(t, chain(MaxOutlineImportDepth), "freemind"); len(got.Nodes) != MaxOutlineImportDepth {
		t.Fatal(len(got.Nodes))
	}
	for name, raw := range map[string]string{
		"depth":     chain(MaxOutlineImportDepth + 1),
		"nodes":     `<map><node TEXT="Root">` + strings.Repeat(`<node TEXT="N"/>`, MaxOutlineImportNodes) + `</node></map>`,
		"label":     `<map><node TEXT="` + strings.Repeat("Я", 513) + `"/></map>`,
		"body":      `<map><node TEXT="Root"><richcontent TYPE="NOTE"><html><body>` + strings.Repeat("Я", 65537) + `</body></html></richcontent></node></map>`,
		"manyNotes": `<map><node TEXT="Root">` + strings.Repeat(`<richcontent TYPE="NOTE">`+strings.Repeat("x", 1000)+`</richcontent>`, 70) + `</node></map>`,
		"richDepth": `<map><node TEXT="Root"><richcontent TYPE="NOTE">` + strings.Repeat(`<span>`, 65) + `x` + strings.Repeat(`</span>`, 65) + `</richcontent></node></map>`,
		"bytes":     strings.Repeat(" ", MaxOutlineImportBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseOutlineXML([]byte(raw), "freemind"); !errors.Is(err, ErrOutlineImportLimit) || len(got.Nodes) != 0 {
				t.Fatal(len(got.Nodes), err)
			}
		})
	}
	// The synthetic OPML root consumes one of the 1000-node slots.
	raw := `<opml><head/><body>` + strings.Repeat(`<outline text="N"/>`, 999) + `</body></opml>`
	if got := requireOutlineXML(t, raw, "opml"); len(got.Nodes) != 1000 {
		t.Fatal(len(got.Nodes))
	}
	if _, err := parseOutlineXML([]byte(strings.Replace(raw, `</body>`, `<outline text="extra"/></body>`, 1)), "opml"); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatal(err)
	}
}

func TestOutlineXMLUnsafeLinksStayInactiveAndWarningsAreDeduplicated(t *testing.T) {
	for _, link := range []string{"file:///private/data", "../source.pdf", "#node", "javascript:alert(1)", "data:text/plain,x", "https://user:pass@example.org/", "https://example.org/%0a", "https://example.org/%5cpath"} {
		raw := `<map><node TEXT="Root" LINK="` + link + `"><node TEXT="Child" LINK="` + link + `"/></node></map>`
		draft := requireOutlineXML(t, raw, "freemind")
		if len(draft.Nodes[0].Sources) != 0 || len(draft.Nodes[1].Sources) != 0 || len(draft.Warnings) != 1 {
			t.Fatal(link, draft)
		}
	}
	for _, link := range []string{"http://127.0.0.1:1/not-fetched", "https://example.org/Русский?q=a%20b#part"} {
		draft := requireOutlineXML(t, `<map><node TEXT="Root" LINK="`+link+`"/></map>`, "freemind")
		if len(draft.Nodes[0].Sources) != 1 || draft.Nodes[0].Sources[0].URL != link {
			t.Fatal(draft)
		}
	}
}

func TestOutlineXMLRichURLBudgetAndDeduplication(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 2001; i++ {
		fmt.Fprintf(&body, `<a href="https://example.org/%d">x</a>`, i)
	}
	raw := `<map><node TEXT="Root"><richcontent TYPE="NOTE"><html><body>` + body.String() + `</body></html></richcontent></node></map>`
	if got, err := parseOutlineXML([]byte(raw), "freemind"); !errors.Is(err, ErrOutlineImportLimit) || len(got.Nodes) != 0 {
		t.Fatal(got, err)
	}
	raw = `<map><node TEXT="Root" LINK="https://example.org"><richcontent TYPE="NOTE"><html><body>` + strings.Repeat(`<a href="https://example.org">x</a>`, 2001) + `</body></html></richcontent></node></map>`
	draft := requireOutlineXML(t, raw, "freemind")
	if len(draft.Nodes[0].Sources) != 1 || len(draft.Nodes[0].BodyMarkdown) != 2001 {
		t.Fatal(draft)
	}
}
