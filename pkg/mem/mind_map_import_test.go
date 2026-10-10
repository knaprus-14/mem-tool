package mem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func classicImportFixture(t *testing.T) (*Store, ClassicMindMapDocument, []byte) {
	t.Helper()
	s, entries := newClassicMindMapAITestStore(t)
	a, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.ImportClassicMindMap(ClassicMindMapDraft{Title: "Импорт <script> — пример", Description: "Описание", Mode: ClassicMindMapModeHybrid, Status: ClassicMindMapStatusReady, Nodes: []ClassicMindMapNodeDraft{
		{Ref: "root", Label: "Другой корень", BodyMarkdown: "Ручной текст", Locked: true},
		{Ref: "a", ParentRef: "root", Label: "Одинаково", Summary: "Первый", Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceEvidence, Evidence: &a, Title: "Фрагмент"}}},
		{Ref: "nested", ParentRef: "a", Label: "Вложенный", Style: json.RawMessage(`{"color":"red"}`)},
		{Ref: "b", ParentRef: "root", Label: "Одинаково", Summary: "Второй", Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceURL, URL: "https://example.invalid/no-fetch", Title: "URL"}}},
	}}, "test", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := s.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: ClassicMindMapExportJSON})
	if err != nil {
		t.Fatal(err)
	}
	return s, doc, artifact.Data
}

func TestClassicMindMapImportJSONRoundTripPreservesTreeAndEvidence(t *testing.T) {
	s, original, data := classicImportFixture(t)
	ctx := context.Background()
	preview, err := s.PreviewClassicMindMapJSON(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != original.Map.Title || preview.NodeCount != 4 || preview.SourceCount != 2 || preview.Current != 2 {
		t.Fatal(preview)
	}
	maps, _ := s.ListClassicMindMaps(true)
	if len(maps) != 1 {
		t.Fatal("preview wrote map")
	}
	doc, err := s.ImportClassicMindMapJSON(ctx, data, preview.Digest, "importer", "JSON")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Map.ID == original.Map.ID || doc.Map.Title != original.Map.Title || len(doc.Nodes) != 4 || doc.Map.Revision != 2 {
		t.Fatal(doc)
	}
	originalIDs := map[string]bool{}
	for _, n := range original.Nodes {
		originalIDs[n.ID] = true
		for _, src := range n.Sources {
			originalIDs[src.ID] = true
		}
	}
	bySummary := map[string]ClassicMindMapNode{}
	var root, nested ClassicMindMapNode
	for _, n := range doc.Nodes {
		if originalIDs[n.ID] || n.MapID != doc.Map.ID {
			t.Fatal("reused node identity")
		}
		bySummary[n.Summary] = n
		if n.ParentID == "" {
			root = n
		}
		if n.Label == "Вложенный" {
			nested = n
		}
		for _, src := range n.Sources {
			if originalIDs[src.ID] || src.NodeID != n.ID || src.MapID != doc.Map.ID || src.EvidenceState != EvidenceCurrent {
				t.Fatal(src)
			}
		}
	}
	if !root.Locked || root.Label != "Другой корень" || root.BodyMarkdown != "Ручной текст" || bySummary["Первый"].ParentID != root.ID || bySummary["Второй"].ParentID != root.ID || nested.ParentID != bySummary["Первый"].ID || bySummary["Первый"].Position >= bySummary["Второй"].Position {
		t.Fatal("tree or manual fields changed", doc)
	}
	history, err := s.ListClassicMindMapChanges(doc.Map.ID, 10)
	if err != nil || len(history) != 2 || history[0].Action != "import_sources" || history[0].Actor != "importer" {
		t.Fatal(history, err)
	}
	snapshots, err := s.ListClassicMindMapSnapshots(doc.Map.ID, 10)
	if err != nil || len(snapshots) != 2 || snapshots[0].Revision != doc.Map.Revision {
		t.Fatal(snapshots, err)
	}
	unchanged, err := s.LoadClassicMindMap(original.Map.ID)
	if err != nil || unchanged.Digest != original.Digest {
		t.Fatal("original was modified", err)
	}
}

func TestClassicMindMapImportJSONRejectsInvalidWithoutWrites(t *testing.T) {
	s, _, data := classicImportFixture(t)
	ctx := context.Background()
	mutate := func(f func(*classicMindMapPortableDocument)) []byte {
		var p classicMindMapPortableDocument
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		f(&p)
		b, _ := json.Marshal(p)
		return b
	}
	redigest := func(p *classicMindMapPortableDocument) {
		doc, _, e := finalizeClassicMindMapDocument(p.Document)
		if e != nil {
			t.Fatal(e)
		}
		p.Document = doc
		p.MapDigest = doc.Digest
		p.StateDigest = ""
	}
	tests := map[string][]byte{
		"unknown":         bytes.Replace(data, []byte(`"export_version":`), []byte(`"unknown":true,"export_version":`), 1),
		"nested unknown":  bytes.Replace(data, []byte(`"map_id":`), []byte(`"arbitrary_path":"C:/secret","map_id":`), 1),
		"duplicate":       bytes.Replace(data, []byte(`"export_version":`), []byte(`"export_version":1,"export_version":`), 1),
		"extra document":  append(append([]byte{}, data...), []byte(`{}`)...),
		"oversized":       bytes.Repeat([]byte(" "), MaxClassicMindMapImportBytes+1),
		"wrong version":   mutate(func(p *classicMindMapPortableDocument) { p.ExportVersion = 2 }),
		"wrong counts":    mutate(func(p *classicMindMapPortableDocument) { p.SourceCount++ }),
		"wrong digest":    mutate(func(p *classicMindMapPortableDocument) { p.Document.Map.Title = "Changed" }),
		"wrong ownership": mutate(func(p *classicMindMapPortableDocument) { p.Document.Nodes[0].MapID = "another-map"; redigest(p) }),
		"duplicate node":  mutate(func(p *classicMindMapPortableDocument) { p.Document.Nodes[1].ID = p.Document.Nodes[0].ID; redigest(p) }),
		"cycle": mutate(func(p *classicMindMapPortableDocument) {
			p.Document.Nodes[0].ParentID = p.Document.Nodes[0].ID
			redigest(p)
		}),
		"external file": mutate(func(p *classicMindMapPortableDocument) {
			for i := range p.Document.Nodes {
				for j := range p.Document.Nodes[i].Sources {
					p.Document.Nodes[i].Sources[j].Kind = ClassicMindMapSourceExternalFile
					p.Document.Nodes[i].Sources[j].Locator = "C:/never-read"
				}
			}
			redigest(p)
		}),
		"invalid URL": mutate(func(p *classicMindMapPortableDocument) {
			for i := range p.Document.Nodes {
				for j := range p.Document.Nodes[i].Sources {
					src := &p.Document.Nodes[i].Sources[j]
					if src.Kind == ClassicMindMapSourceURL {
						src.URL = "file:///C:/secret"
					}
				}
			}
			redigest(p)
		}),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PreviewClassicMindMapJSON(ctx, input); err == nil {
				t.Fatal("preview accepted invalid input")
			}
			if _, err := s.ImportClassicMindMapJSON(ctx, input, "sha256:"+strings.Repeat("0", 64), "x", ""); err == nil {
				t.Fatal("apply accepted invalid input")
			}
		})
	}
	var maps, history, snapshots int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&maps)
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_changes`).Scan(&history)
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_snapshots`).Scan(&snapshots)
	if maps != 1 || history != 1 || snapshots != 1 {
		t.Fatalf("writes: maps %d history %d snapshots %d", maps, history, snapshots)
	}
}

func TestClassicMindMapImportJSONPinsPreviewAndPreservesStaleAnchors(t *testing.T) {
	s, _, data := classicImportFixture(t)
	ctx := context.Background()
	before, err := s.PreviewClassicMindMapJSON(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Text += " Updated"
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("new revision")
	}
	if err = s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportClassicMindMapJSON(ctx, data, before.Digest, "x", ""); !errors.Is(err, ErrClassicMindMapImportPreview) {
		t.Fatal(err)
	}
	after, err := s.PreviewClassicMindMapJSON(ctx, data)
	if err != nil || after.Digest == before.Digest || after.Stale != 1 || after.Current != 1 {
		t.Fatal(after, err)
	}
	doc, err := s.ImportClassicMindMapJSON(ctx, data, after.Digest, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range doc.Nodes {
		for _, src := range n.Sources {
			if src.Kind == ClassicMindMapSourceEvidence && (src.EvidenceState != EvidenceStale || src.Evidence.DocumentRevision == chunks[0].Provenance.DocumentRevision || strings.ContainsAny(src.Evidence.SourcePath, "\\/")) {
				t.Fatal(src)
			}
		}
	}
}
