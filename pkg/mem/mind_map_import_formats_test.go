package mem

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// Use actual archive bytes, not a parser draft, to exercise the same public
// preview/apply/download path as the web importer. No files are extracted.
func outlineFormatsTestArchive(t *testing.T, member, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range []struct{ name, content string }{
		{member, content},
		{"resources/retained.txt", "Исходное вложение 😀\r\nНе превращать в evidence.\x00"},
	} {
		entry, err := writer.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(item.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestOutlineImportExternalFormatsTransactionalRoundTrip(t *testing.T) {
	const wantRoot = "Корень 😀"
	const wantChild = "Ветка"
	const wantRootBody = "Обзор 😀"
	const wantChildBody = "Давление 5 бар\nстрока 2"
	const wantURL = "https://example.org/manual?q=1#p2"
	opml := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0"><head><title>Книга 😀</title></head><body>
<outline text="Корень 😀" _note="Обзор 😀" type="link" url="https://example.org/manual?q=1#p2">
 <outline text="Ветка" _note="Давление 5 бар&#10;строка 2" type="link" url="file:///private/never-open.pdf"/>
</outline></body></opml>`)
	freemind := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<map version="1.0.1"><node ID="foreign-root" TEXT="Корень 😀" LINK="https://example.org/manual?q=1#p2">
 <richcontent TYPE="NOTE"><html><body><p>Обзор 😀</p></body></html></richcontent>
 <node ID="foreign-child" TEXT="Ветка" LINK="#foreign-root">
  <richcontent TYPE="NOTE"><html><body><pre>Давление 5 бар&#10;строка 2</pre></body></html></richcontent>
 </node></node></map>`)
	modern := outlineFormatsTestArchive(t, "content.json", `[{"id":"foreign-sheet","title":"Книга 😀","rootTopic":{
 "id":"foreign-root","title":"Корень 😀","href":"https://example.org/manual?q=1#p2","notes":{"plain":{"content":"Обзор 😀"}},
 "children":{"attached":[{"id":"foreign-child","title":"Ветка","href":"xmind:#foreign-root","notes":{"plain":{"content":"Давление 5 бар\nстрока 2"}}}]}
}}]`)
	legacy := outlineFormatsTestArchive(t, "content.xml", `<?xml version="1.0" encoding="UTF-8"?>
<xmap-content xmlns="urn:xmind:xmap:xmlns:content:2.0" xmlns:xlink="http://www.w3.org/1999/xlink">
 <sheet id="foreign-sheet"><title>Книга 😀</title><topic id="foreign-root" xlink:href="https://example.org/manual?q=1#p2">
  <title>Корень 😀</title><notes><plain>Обзор 😀</plain></notes>
  <children><topics type="attached"><topic id="foreign-child" xlink:href="file:///private/never-open.pdf">
   <title>Ветка</title><notes><plain>Давление 5 бар&#10;строка 2</plain></notes>
  </topic></topics></children>
 </topic></sheet></xmap-content>`)
	cases := []struct {
		name, format, filename, title string
		raw                           []byte
	}{
		{"opml", "opml", "План.opml", "Книга 😀", opml},
		{"freemind", "freemind", "План.mm", wantRoot, freemind},
		{"xmind_modern", "xmind", "Современная.xmind", "Книга 😀", modern},
		{"xmind_legacy", "xmind", "Старая.xmind", "Книга 😀", legacy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir, store, _ := queryFixture(t)
			var beforeTables int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('web_map_import_sources','mind_maps','web_map_creates')`).Scan(&beforeTables); err != nil {
				t.Fatal(err)
			}
			input := OutlineImportInput{Format: tc.format, Filename: tc.filename, ContentBase64: base64.StdEncoding.EncodeToString(tc.raw)}
			preview, err := PreviewMindMapOutline(input)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Title != tc.title || preview.NodeCount != 2 || preview.SourceCount != 1 || preview.SourceBytes != len(tc.raw) || preview.SourceSHA256 != prefixedSHA256(tc.raw) || len(preview.Nodes) != 2 || len(preview.Warnings) == 0 {
				t.Fatalf("wrong preview: %+v", preview)
			}
			if preview.Nodes[0].Label != wantRoot || preview.Nodes[0].ParentRef != "" || preview.Nodes[0].Depth != 0 || preview.Nodes[1].Label != wantChild || preview.Nodes[1].ParentRef != preview.Nodes[0].Ref || preview.Nodes[1].Depth != 1 {
				t.Fatalf("preview tree changed: %+v", preview.Nodes)
			}
			// Public preview must not migrate a fresh target database.
			var tables int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('web_map_import_sources','mind_maps','web_map_creates')`).Scan(&tables); err != nil || tables != beforeTables {
				t.Fatalf("preview created tables: %d, %v", tables, err)
			}
			apply := OutlineImportApplyInput{OutlineImportInput: input, Digest: preview.Digest, RequestID: strings.Repeat("a", 32)}
			admissions := 0
			created, err := ImportWebMapOutlineIn(ctx, dir, "alice", apply, func() error { admissions++; return nil })
			if err != nil || created.Replayed || created.ID == "" || admissions != 1 {
				t.Fatalf("apply failed: %+v, %v, admissions=%d", created, err, admissions)
			}
			source, err := ReadWebMapImportSourceIn(ctx, dir, created.ID)
			if err != nil || source.Format != tc.format || source.Filename != tc.filename || source.SHA256 != preview.SourceSHA256 || !bytes.Equal(source.Data, tc.raw) {
				t.Fatalf("original bytes/metadata changed: %+v, %v", source, err)
			}
			view, err := ReadWebMapIn(ctx, dir, created.ID)
			if err != nil || !view.HasImportSource || view.Map.Title != tc.title || view.Map.Revision != 1 || view.Map.NodeCount != 2 || view.Map.SourceCount != 1 || len(view.Nodes) != 2 {
				t.Fatalf("web projection: %+v, %v", view, err)
			}
			document, err := store.LoadClassicMindMap(created.ID)
			if err != nil || document.Map.Title != tc.title || document.Map.Revision != 1 || len(document.Nodes) != 2 {
				t.Fatalf("classic document: %+v, %v", document, err)
			}
			byLabel := map[string]ClassicMindMapNode{}
			for _, node := range document.Nodes {
				byLabel[node.Label] = node
				if node.Origin != ClassicMindMapNodeImported || node.ID == "foreign-root" || node.ID == "foreign-child" || !strings.HasPrefix(node.ID, "mmn-") || node.MapID != created.ID {
					t.Fatalf("origin/identity changed: %+v", node)
				}
				for _, item := range node.Sources {
					if item.Kind != ClassicMindMapSourceURL || item.Evidence != nil || item.KnowledgeNodeID != "" || item.Locator != "" || item.URL != wantURL || item.MapID != created.ID || item.NodeID != node.ID {
						t.Fatalf("ordinary link became evidence or unsafe source: %+v", item)
					}
				}
			}
			root, child := byLabel[wantRoot], byLabel[wantChild]
			if root.ID != document.Map.RootNodeID || root.ParentID != "" || child.ParentID != root.ID || root.BodyMarkdown != wantRootBody || child.BodyMarkdown != wantChildBody || len(root.Sources) != 1 || len(child.Sources) != 0 {
				t.Fatalf("tree, notes or sources lost: root=%+v child=%+v", root, child)
			}
			for _, node := range view.Nodes {
				original := byLabel[node.Label]
				if node.ID != original.ID || node.ParentID != original.ParentID || node.Body != original.BodyMarkdown || node.SourceCount != len(original.Sources) {
					t.Fatalf("web/classic projection mismatch: %+v vs %+v", node, original)
				}
			}
			links, err := ListWebMapSourcesIn(ctx, dir, created.ID, root.ID)
			// For URL sources, "current" means syntactically usable; the kind
			// stays URL and no citation, quotation or document anchor is forged.
			if err != nil || links.Revision != 1 || len(links.Items) != 1 || links.Items[0].Kind != ClassicMindMapSourceURL || links.Items[0].URL != wantURL || links.Items[0].CitationID != "" || links.Items[0].Excerpt != "" || links.Items[0].State != string(EvidenceCurrent) {
				t.Fatalf("web source trust changed: %+v, %v", links, err)
			}
			history, err := store.ListClassicMindMapChanges(created.ID, 10)
			if err != nil || len(history) != 1 || history[0].Action != "create_map" || history[0].Actor != "alice" || history[0].NewRevision != 1 {
				t.Fatalf("creation history: %+v, %v", history, err)
			}
			snapshots, err := store.ListClassicMindMapSnapshots(created.ID, 10)
			if err != nil || len(snapshots) != 1 || snapshots[0].Revision != 1 {
				t.Fatalf("creation snapshot: %+v, %v", snapshots, err)
			}
			replay, err := ImportWebMapOutlineIn(ctx, dir, "alice", apply, func() error { t.Fatal("replay calls admission"); return nil })
			if err != nil || !replay.Replayed || replay.ID != created.ID {
				t.Fatalf("retry failed: %+v, %v", replay, err)
			}
			for _, table := range []string{"mind_maps", "mind_map_changes", "mind_map_snapshots", "web_map_import_sources", "web_map_creates"} {
				var count int
				if err := store.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil || count != 1 {
					t.Fatalf("non-atomic or duplicate %s: count=%d error=%v", table, count, err)
				}
			}
		})
	}
}
