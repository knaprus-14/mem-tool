package mem

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func outlineInput(format, filename, text string) OutlineImportInput {
	return OutlineImportInput{Format: format, Filename: filename, ContentBase64: base64.StdEncoding.EncodeToString([]byte(text))}
}

func TestOutlineImportMarkdownStructureAndLiteralContent(t *testing.T) {
	raw := "\ufeff# Учебник\r\nВведение\r\n## Закон\r\n- Определение\r\n  - Формула\r\n```txt\r\n# Не узел\r\n- Не список\r\n```\r\n- [ ] Проверить <script>alert(1)</script>\r\n## Пример\r\n[ссылка](file:///private)"
	p, err := parseOutlineImport(outlineInput("markdown", "Учебник.md", raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ label, parent string }{{"Учебник", ""}, {"Закон", "n1"}, {"Определение", "n2"}, {"Формула", "n3"}, {"[ ] Проверить <script>alert(1)</script>", "n2"}, {"Пример", "n1"}}
	if len(p.Draft.Nodes) != len(want) {
		t.Fatal(p.Draft.Nodes)
	}
	for i, w := range want {
		n := p.Draft.Nodes[i]
		if n.Label != w.label || n.ParentRef != w.parent || n.Origin != ClassicMindMapNodeImported || len(n.Sources) != 0 {
			t.Fatal(i, n)
		}
	}
	if !strings.Contains(p.Draft.Nodes[3].BodyMarkdown, "# Не узел\n- Не список") || !strings.Contains(p.Draft.Nodes[5].BodyMarkdown, "file:///private") {
		t.Fatal("lost literal text", p.Draft)
	}
	if !bytes.Equal(p.Source.Data, []byte(raw)) || p.Preview.SourceSHA256 != prefixedSHA256([]byte(raw)) || p.Preview.Nodes[3].Depth != 3 {
		t.Fatal(p.Preview)
	}
	for _, raw := range []string{"- первый\n    - второй\n- третий", "просто текст", "### раздел\nтекст"} {
		if _, err := PreviewMindMapOutline(outlineInput("markdown", "План.md", raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
}

func TestOutlineImportMarkdownNestedFenceStaysWithOwningNode(t *testing.T) {
	for _, indent := range []string{"    ", "\t"} {
		raw := "# Root\n- Example\n" + indent + "```text\n" + indent + "- not a node\n" + indent + "# not a heading\n" + indent + "```\n- Next"
		p, err := parseOutlineImport(outlineInput("markdown", "fences.md", raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Draft.Nodes) != 3 || p.Draft.Nodes[2].Label != "Next" || !strings.Contains(p.Draft.Nodes[1].BodyMarkdown, indent+"- not a node") || !strings.HasSuffix(p.Draft.Nodes[1].BodyMarkdown, indent+"```") {
			t.Fatal(p.Draft)
		}
	}
}

func TestOutlineImportRejectsInvalidAndBoundedInputs(t *testing.T) {
	good := outlineInput("markdown", "map.md", "# Root")
	cases := []OutlineImportInput{outlineInput("markdown", "../map.md", "# Root"), outlineInput("markdown", "map.md", " "), outlineInput("markdown", "map.md", "\x00"), outlineInput("markdown", "map.md", string([]byte{0xff})), outlineInput("json", "map.json", "{}")}
	bad := good
	bad.ContentBase64 += "\n"
	cases = append(cases, bad)
	for _, in := range cases {
		if _, err := PreviewMindMapOutline(in); !errors.Is(err, ErrOutlineImportInvalid) {
			t.Fatal(in.Filename, in.Format, err)
		}
	}
	limits := []string{"# " + strings.Repeat("я", 513), "# Root\n" + strings.Repeat("x", 65537), "# Root\n" + strings.Repeat("- child\n", 1000), "# Root" + strings.Repeat("\n", 65537)}
	for _, raw := range limits {
		if _, err := PreviewMindMapOutline(outlineInput("markdown", "map.md", raw)); !errors.Is(err, ErrOutlineImportLimit) {
			t.Fatal(len(raw), err)
		}
	}
	var chain strings.Builder
	chain.WriteString("# Root\n")
	for i := 0; i < 256; i++ {
		chain.WriteString(strings.Repeat(" ", i) + "- child\n")
	}
	if _, err := PreviewMindMapOutline(outlineInput("markdown", "map.md", chain.String())); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatal("depth", err)
	}
	oversized := good
	oversized.ContentBase64 = strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxOutlineImportBytes)+4)
	if _, err := PreviewMindMapOutline(oversized); !errors.Is(err, ErrOutlineImportLimit) {
		t.Fatal("bytes", err)
	}
}

func TestOutlineImportArchiveTransactionRetryAndPreviewPin(t *testing.T) {
	ctx := context.Background()
	dir, s, _ := queryFixture(t)
	original, err := CreateWebMapIn(ctx, dir, "alice", WebMapCreate{Title: "Существующая", RequestID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.LoadClassicMindMap(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := "\ufeff# Импорт\r\n- дочерний\r\n  текст"
	in := outlineInput("markdown", "Исходник.md", raw)
	preview, err := PreviewMindMapOutline(in)
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='web_map_import_sources'`).Scan(&tables)
	if tables != 0 {
		t.Fatal("preview writes schema")
	}
	apply := OutlineImportApplyInput{OutlineImportInput: in, Digest: preview.Digest, RequestID: strings.Repeat("b", 32)}
	// The receipt pins all bytes and the filename, not only the parsed tree.
	for _, changed := range []OutlineImportInput{outlineInput("markdown", "Other.md", raw), outlineInput("markdown", "Исходник.md", raw+"\r\n")} {
		bad := apply
		bad.OutlineImportInput = changed
		if _, err = ImportWebMapOutlineIn(ctx, dir, "alice", bad, nil); !errors.Is(err, ErrClassicMindMapImportPreview) {
			t.Fatal("preview pin", err)
		}
	}
	denied := errors.New("capacity")
	if _, err = ImportWebMapOutlineIn(ctx, dir, "alice", apply, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	created, err := ImportWebMapOutlineIn(ctx, dir, "alice", apply, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ReadWebMapImportSourceIn(ctx, dir, created.ID)
	if err != nil || source.Filename != in.Filename || !bytes.Equal(source.Data, []byte(raw)) {
		t.Fatal(source, err)
	}
	view, err := ReadWebMapIn(ctx, dir, created.ID)
	if err != nil || !view.HasImportSource || len(view.Nodes) != 2 {
		t.Fatal(view, err)
	}
	for _, n := range view.Nodes {
		if n.ID == "n1" || n.ID == "n2" {
			t.Fatal("foreign ID reused")
		}
	}
	doc, err := s.LoadClassicMindMap(created.ID)
	if err != nil || doc.Map.Revision != 1 {
		t.Fatal(doc, err)
	}
	history, err := s.ListClassicMindMapChanges(created.ID, 10)
	if err != nil || len(history) != 1 || history[0].Actor != "alice" {
		t.Fatal(history, err)
	}
	snapshots, err := s.ListClassicMindMapSnapshots(created.ID, 10)
	if err != nil || len(snapshots) != 1 {
		t.Fatal(snapshots, err)
	}
	replay, err := ImportWebMapOutlineIn(ctx, dir, "alice", apply, func() error { t.Fatal("replay admission called"); return denied })
	if err != nil || !replay.Replayed || replay.ID != created.ID {
		t.Fatal(replay, err)
	}
	if _, err = ImportWebMapOutlineIn(ctx, dir, "bob", apply, nil); !errors.Is(err, ErrWebMapRequest) {
		t.Fatal("actor pin", err)
	}
	changed := apply
	changed.Filename = "changed.md"
	if _, err = ImportWebMapOutlineIn(ctx, dir, "alice", changed, nil); !errors.Is(err, ErrWebMapRequest) {
		t.Fatal("payload pin", err)
	}
	after, err := s.LoadClassicMindMap(original.ID)
	if err != nil || after.Digest != old.Digest {
		t.Fatal("existing map changed", err)
	}
	page, err := ListWebMapsIn(ctx, dir, 0, true)
	if err != nil || page.Total != 2 {
		t.Fatal(page, err)
	}
	if _, err = s.db.Exec(`UPDATE web_map_import_sources SET content=x'00' WHERE map_id=?`, created.ID); err == nil {
		t.Fatal("source not immutable")
	}
	if _, err = s.db.Exec(`UPDATE mind_maps SET deleted_at='now' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadWebMapImportSourceIn(ctx, dir, created.ID); !errors.Is(err, ErrOutlineImportSourceNotFound) {
		t.Fatal("deleted download", err)
	}
}

func TestOutlineImportRollbackOnArchiveFailure(t *testing.T) {
	ctx := context.Background()
	dir, s, _ := queryFixture(t)
	if _, err := s.db.Exec(classicMindMapSchema + outlineImportSourceSchema + `CREATE TRIGGER reject_import BEFORE INSERT ON web_map_import_sources BEGIN SELECT RAISE(ABORT,'failure'); END;`); err != nil {
		t.Fatal(err)
	}
	in := outlineInput("markdown", "plan.md", "# Root")
	p, err := PreviewMindMapOutline(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImportWebMapOutlineIn(ctx, dir, "alice", OutlineImportApplyInput{in, p.Digest, strings.Repeat("c", 32)}, nil); err == nil {
		t.Fatal("expected archive failure")
	}
	for _, table := range []string{"mind_maps", "mind_map_nodes", "web_map_import_sources"} {
		var count int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}
