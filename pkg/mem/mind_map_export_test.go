package mem

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExportClassicMindMapPortableFormatsAreDeterministicAndGrounded(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	evidence := groundedEvidenceForEntry(entries[0], entries[0].Text, DefaultAnswerLowConfidence)
	anchor, err := evidenceAnchorFromGrounded(evidence)
	if err != nil {
		t.Fatal(err)
	}
	sentinelHash := anchor.EvidenceHash
	sentinelPath := anchor.SourcePath
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{
		Title:       `Карта "Безопасность" </script><script>alert(1)</script>`,
		Description: "Полная переносимая карта",
		Mode:        ClassicMindMapModeHybrid,
		Status:      ClassicMindMapStatusReady,
		Nodes: []ClassicMindMapNodeDraft{
			{Ref: "root", Label: "Корневая тема", Summary: "Обзор", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual},
			{Ref: "child-a", ParentRef: "root", Label: "Повтор", Summary: "Первый", BodyMarkdown: "Кириллица: АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ", Kind: ClassicMindMapNodeFact, Origin: ClassicMindMapNodeGenerated,
				Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceEvidence, Title: "Проверяемый фрагмент", Evidence: &anchor}}},
			{Ref: "child-b", ParentRef: "root", Label: "Повтор", Summary: "Второй", BodyMarkdown: "XML control: \x01 safe", Kind: ClassicMindMapNodeQuestion, Origin: ClassicMindMapNodeManual,
				Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceURL, Title: "URL как текст", URL: "https://example.org/spec?q=%3Cscript%3E#section"}}},
		},
	}, "test", "portable export fixture")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		format    ClassicMindMapExportFormat
		extension string
		mediaType string
	}{
		{ClassicMindMapExportHTML, ".html", "text/html; charset=utf-8"},
		{ClassicMindMapExportSVG, ".svg", "image/svg+xml; charset=utf-8"},
		{ClassicMindMapExportPNG, ".png", "image/png"},
		{ClassicMindMapExportJSON, ".json", "application/json; charset=utf-8"},
		{ClassicMindMapExportOPML, ".opml", "text/x-opml; charset=utf-8"},
	}
	artifacts := make(map[ClassicMindMapExportFormat]ClassicMindMapExportArtifact)
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			request := ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: test.format, ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest}
			first, err := store.ExportClassicMindMap(request)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.ExportClassicMindMap(request)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Data, second.Data) {
				t.Fatalf("%s export is not byte-deterministic", test.format)
			}
			if first.Format != test.format || !strings.HasSuffix(first.Filename, test.extension) || first.MediaType != test.mediaType || first.MapID != doc.Map.ID || first.Revision != doc.Map.Revision || first.Digest != doc.Digest || first.StateDigest != doc.StateDigest || first.NodeCount != 3 || first.SourceCount != 2 || len(first.Data) == 0 {
				t.Fatalf("unexpected artifact metadata: %#v", first)
			}
			artifacts[test.format] = first
		})
	}

	var jsonExport classicMindMapPortableDocument
	if err := json.Unmarshal(artifacts[ClassicMindMapExportJSON].Data, &jsonExport); err != nil {
		t.Fatal(err)
	}
	assertClassicMindMapPortableProvenance(t, jsonExport, sentinelPath, sentinelHash)

	htmlExport := string(artifacts[ClassicMindMapExportHTML].Data)
	for _, marker := range []string{"Content-Security-Policy", `id="mem-data"`, "nodeElements=new Map", "button.dataset.nodeId=n.id", "button.title=n.label", "const stack=[{node:root,parent:list}]", "while(stack.length)", "a.source_path,s.knowledge_node_id", sentinelPath, sentinelHash} {
		if !strings.Contains(htmlExport, marker) {
			t.Fatalf("HTML export is missing %q", marker)
		}
	}
	if strings.Contains(htmlExport, `</script><script>alert(1)</script>`) || strings.Contains(htmlExport, `<script src=`) || strings.Contains(htmlExport, `<link rel=`) {
		t.Fatal("HTML export permits injected or external executable assets")
	}
	if strings.Contains(htmlExport, "function branch(") {
		t.Fatal("HTML export regressed to recursive DOM construction")
	}

	svgExport := string(artifacts[ClassicMindMapExportSVG].Data)
	if !strings.Contains(svgExport, `<metadata id="mem-provenance">`) || !strings.Contains(svgExport, sentinelPath) || !strings.Contains(svgExport, sentinelHash) || strings.Contains(svgExport, `<script>alert(1)</script>`) {
		t.Fatal("SVG export lost provenance or failed escaping")
	}
	if err := consumeXML([]byte(svgExport)); err != nil {
		t.Fatalf("SVG is not valid XML: %v", err)
	}

	opmlExport := artifacts[ClassicMindMapExportOPML].Data
	opmlSources, err := classicMindMapTestOPMLSources(opmlExport)
	if err != nil {
		t.Fatal(err)
	}
	if len(opmlSources) != 2 || !bytes.Contains(opmlExport, []byte(`xmlns:mem="urn:mem-tool:classic-mind-map:1"`)) || !bytes.Contains(opmlExport, []byte(`mem:id=`)) {
		t.Fatalf("OPML namespace/source extension is incomplete: sources=%d", len(opmlSources))
	}
	foundAnchor := false
	for _, source := range opmlSources {
		if source.Evidence != nil && source.Evidence.SourcePath == sentinelPath && source.Evidence.EvidenceHash == sentinelHash {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatal("OPML source JSON did not round-trip the evidence anchor")
	}
	opmlMetadata, opmlNodes, err := classicMindMapTestOPMLMetadata(opmlExport)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"mapId": doc.Map.ID, "description": doc.Map.Description, "mode": string(doc.Map.Mode),
		"status": string(doc.Map.Status), "rootNodeId": doc.Map.RootNodeID,
		"revision": strconv.FormatInt(doc.Map.Revision, 10), "digest": doc.Digest,
		"stateDigest": doc.StateDigest, "created": doc.Map.Created, "updated": doc.Map.Updated,
	} {
		if got := opmlMetadata[key]; got != want {
			t.Fatalf("OPML map metadata %s=%q, want %q", key, got, want)
		}
	}
	for _, node := range doc.Nodes {
		got := opmlNodes[node.ID]
		style, _ := json.Marshal(node.Style)
		for key, want := range map[string]string{
			"text": node.Label, "mem:parent-id": node.ParentID, "mem:position": strconv.Itoa(node.Position),
			"mem:kind": string(node.Kind), "mem:origin": string(node.Origin), "mem:locked": strconv.FormatBool(node.Locked),
			"mem:summary": node.Summary, "mem:body-markdown": node.BodyMarkdown, "mem:style": string(style),
			"mem:created": node.Created, "mem:updated": node.Updated,
		} {
			if got[key] != want {
				t.Fatalf("OPML node %s field %s=%q, want %q", node.ID, key, got[key], want)
			}
		}
	}

	pngArtifact := artifacts[ClassicMindMapExportPNG]
	image, err := png.Decode(bytes.NewReader(pngArtifact.Data))
	if err != nil || image.Bounds().Dx() < 1 || image.Bounds().Dy() < 1 {
		t.Fatalf("PNG decode failed: bounds=%v err=%v", image.Bounds(), err)
	}
	pngProvenance, err := classicMindMapTestPNGInternationalText(pngArtifact.Data, "mem-provenance")
	if err != nil {
		t.Fatal(err)
	}
	var pngExport classicMindMapPortableDocument
	if err := json.Unmarshal(pngProvenance, &pngExport); err != nil {
		t.Fatal(err)
	}
	assertClassicMindMapPortableProvenance(t, pngExport, sentinelPath, sentinelHash)
}

func TestExportClassicMindMapPinsStateAndRejectsUnsafeRasterCanvas(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	doc, err := store.CreateClassicMindMap("Проверка", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: ClassicMindMapExportJSON, ExpectedRevision: doc.Map.Revision + 1}); !errors.Is(err, ErrClassicMindMapExportChanged) {
		t.Fatalf("stale revision pin error=%v", err)
	}
	if _, err := store.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: ClassicMindMapExportJSON, ExpectedDigest: "sha256:stale"}); !errors.Is(err, ErrClassicMindMapExportChanged) {
		t.Fatalf("stale digest pin error=%v", err)
	}
	if _, err := store.ExportClassicMindMap(ClassicMindMapExportRequest{MapRef: doc.Map.ID, Format: "pdf"}); err == nil {
		t.Fatal("unsupported export format was accepted")
	}
	model, err := buildClassicMindMapExportModel(doc, string(ClassicMindMapExportPNG))
	if err != nil {
		t.Fatal(err)
	}
	model.Width = MaxClassicMindMapPNGWidth + 1
	if _, err := renderClassicMindMapPNG(model); !errors.Is(err, ErrClassicMindMapPNGTooLarge) || !strings.Contains(err.Error(), "SVG") {
		t.Fatalf("unsafe PNG canvas error=%v", err)
	}
}

func TestExportClassicMindMapStateDigestDetectsExternalStoreEvidenceChange(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	evidence := groundedEvidenceForEntry(entries[0], entries[0].Text, DefaultAnswerLowConfidence)
	anchor, err := evidenceAnchorFromGrounded(evidence)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{
		Title: "State pin", Mode: ClassicMindMapModeGenerated, Status: ClassicMindMapStatusReady,
		Nodes: []ClassicMindMapNodeDraft{{Ref: "root", Label: "Root", Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeGenerated,
			Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceEvidence, Title: "Pinned evidence", Evidence: &anchor}}}},
	}, "test", "state digest fixture")
	if err != nil {
		t.Fatal(err)
	}
	if doc.StateDigest == "" || doc.Nodes[0].Sources[0].EvidenceState != EvidenceCurrent {
		t.Fatalf("initial resolved state is incomplete: %#v", doc)
	}
	other, err := NewStore(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.db.Exec(`UPDATE entries SET document_revision=? WHERE id=?`, "sha256:external-process-revision", entries[0].ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.LoadClassicMindMap(doc.Map.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Map.Revision != doc.Map.Revision || fresh.Digest != doc.Digest {
		t.Fatalf("dynamic source change modified content pin: before=%d/%s after=%d/%s", doc.Map.Revision, doc.Digest, fresh.Map.Revision, fresh.Digest)
	}
	if fresh.StateDigest == doc.StateDigest || fresh.Nodes[0].Sources[0].EvidenceState != EvidenceStale {
		t.Fatalf("fresh DB resolution did not detect external evidence change: before=%s after=%s state=%s", doc.StateDigest, fresh.StateDigest, fresh.Nodes[0].Sources[0].EvidenceState)
	}
	_, err = store.ExportClassicMindMap(ClassicMindMapExportRequest{
		MapRef: doc.Map.ID, Format: ClassicMindMapExportJSON, ExpectedRevision: doc.Map.Revision,
		ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest,
	})
	if !errors.Is(err, ErrClassicMindMapExportChanged) || !strings.Contains(err.Error(), "state digest") {
		t.Fatalf("stale dynamic state pin error=%v", err)
	}
}

func TestClassicMindMapPNGRasterFontCoversRussianAlphabetAndPunctuation(t *testing.T) {
	missing := classicMindMapRasterGlyph('?')
	for _, r := range "АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдеёжзийклмнопрстуфхцчшщъыьэюя«»—–…№" {
		if classicMindMapRasterGlyph(r) == missing {
			t.Fatalf("portable PNG raster font is missing Cyrillic glyph %q", r)
		}
	}
}

func TestClassicMindMapOfflineHTMLBuildsMaximumDepthIteratively(t *testing.T) {
	nodes := make([]ClassicMindMapNode, MaxClassicMindMapExportNodes)
	for i := range nodes {
		id := "deep-" + strconv.Itoa(i)
		parent := ""
		if i > 0 {
			parent = "deep-" + strconv.Itoa(i-1)
		}
		nodes[i] = ClassicMindMapNode{ID: id, MapID: "map-deep", ParentID: parent, Label: "Узел " + strconv.Itoa(i), Kind: ClassicMindMapNodeSubtopic, Origin: ClassicMindMapNodeManual, Sources: []ClassicMindMapSource{}}
	}
	doc := ClassicMindMapDocument{Version: ClassicMindMapFormatVersion, Map: ClassicMindMap{ID: "map-deep", Title: "Глубокая карта", RootNodeID: nodes[0].ID, Revision: 1}, Nodes: nodes, Digest: "sha256:content", StateDigest: "sha256:state"}
	model, err := buildClassicMindMapExportModel(doc, string(ClassicMindMapExportHTML))
	if err != nil {
		t.Fatal(err)
	}
	page, err := renderClassicMindMapHTML(model)
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	if !strings.Contains(text, "const stack=[{node:root,parent:list}]") || !strings.Contains(text, "while(stack.length)") || strings.Contains(text, "function branch(") || !strings.Contains(text, `"id":"deep-9999"`) {
		t.Fatal("maximum-depth offline HTML does not use the iterative complete-tree renderer")
	}
}

func TestClassicMindMapLongLabelsKeepFullTextInTooltipsAndPNGProvenance(t *testing.T) {
	title := strings.Repeat("Длинный заголовок ", 20)
	label := strings.Repeat("Очень длинное название узла ", 40)
	doc := ClassicMindMapDocument{Version: ClassicMindMapFormatVersion,
		Map:    ClassicMindMap{ID: "map-long", Title: title, RootNodeID: "root", Revision: 1},
		Nodes:  []ClassicMindMapNode{{ID: "root", MapID: "map-long", Label: label, Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual, Sources: []ClassicMindMapSource{}}},
		Digest: "sha256:content", StateDigest: "sha256:state"}
	model, err := buildClassicMindMapExportModel(doc, string(ClassicMindMapExportSVG))
	if err != nil {
		t.Fatal(err)
	}
	svg, err := renderClassicMindMapSVG(model)
	if err != nil || !strings.Contains(string(svg), "<title>"+classicMindMapExportXMLEscape(label)+"</title>") {
		t.Fatalf("SVG full-label tooltip is missing: err=%v", err)
	}
	model.Portable.Format = string(ClassicMindMapExportPNG)
	pngData, err := renderClassicMindMapPNG(model)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(pngData))
	if err != nil || config.Width != model.Width || config.Height != model.Height || config.Width > MaxClassicMindMapPNGWidth || config.Height > MaxClassicMindMapPNGHeight {
		t.Fatalf("PNG canvas escaped bounds: config=%#v model=%dx%d err=%v", config, model.Width, model.Height, err)
	}
	metadata, err := classicMindMapTestPNGInternationalText(pngData, "mem-provenance")
	if err != nil || !bytes.Contains(metadata, []byte(label)) || !bytes.Contains(metadata, []byte(title)) {
		t.Fatalf("PNG abbreviated visual lost full text provenance: err=%v", err)
	}
}

func assertClassicMindMapPortableProvenance(t *testing.T, exported classicMindMapPortableDocument, path, hash string) {
	t.Helper()
	if exported.ExportVersion != ClassicMindMapExportVersion || exported.Document.Map.ID == "" || exported.MapDigest != exported.Document.Digest || exported.StateDigest == "" || exported.StateDigest != exported.Document.StateDigest || exported.NodeCount != len(exported.Document.Nodes) {
		t.Fatalf("portable envelope is incomplete: %#v", exported)
	}
	for _, node := range exported.Document.Nodes {
		for _, source := range node.Sources {
			if source.Evidence != nil && source.Evidence.SourcePath == path && source.Evidence.EvidenceHash == hash {
				return
			}
		}
	}
	t.Fatalf("portable envelope lost source path/hash %q/%q", path, hash)
}

func consumeXML(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		if _, err := decoder.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func classicMindMapTestOPMLSources(data []byte) ([]ClassicMindMapSource, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var result []ClassicMindMapSource
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "outline" {
			continue
		}
		for _, attribute := range start.Attr {
			if attribute.Name.Space != "urn:mem-tool:classic-mind-map:1" || attribute.Name.Local != "sources" {
				continue
			}
			encoded, err := base64.RawStdEncoding.DecodeString(attribute.Value)
			if err != nil {
				return nil, fmt.Errorf("decode OPML source base64: %w", err)
			}
			var sources []ClassicMindMapSource
			if err := json.Unmarshal(encoded, &sources); err != nil {
				return nil, fmt.Errorf("decode OPML source JSON: %w", err)
			}
			result = append(result, sources...)
		}
	}
}

func classicMindMapTestOPMLMetadata(data []byte) (map[string]string, map[string]map[string]string, error) {
	const namespace = "urn:mem-tool:classic-mind-map:1"
	decoder := xml.NewDecoder(bytes.NewReader(data))
	metadata := make(map[string]string)
	nodes := make(map[string]map[string]string)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return metadata, nodes, nil
		}
		if err != nil {
			return nil, nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Space == namespace {
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return nil, nil, err
			}
			metadata[start.Name.Local] = value
			continue
		}
		if start.Name.Local != "outline" {
			continue
		}
		attributes := make(map[string]string)
		id := ""
		for _, attribute := range start.Attr {
			key := attribute.Name.Local
			if attribute.Name.Space == namespace {
				key = "mem:" + key
			}
			attributes[key] = attribute.Value
			if key == "mem:id" {
				id = attribute.Value
			}
		}
		if attributes["mem:encoding"] == "base64-raw-std-v1" {
			for _, key := range []string{"mem:summary", "mem:body-markdown", "mem:style", "mem:sources"} {
				decoded, err := base64.RawStdEncoding.DecodeString(attributes[key])
				if err != nil {
					return nil, nil, fmt.Errorf("decode OPML %s: %w", key, err)
				}
				attributes[key] = string(decoded)
			}
		}
		if id != "" {
			nodes[id] = attributes
		}
	}
}

func classicMindMapTestPNGInternationalText(data []byte, keyword string) ([]byte, error) {
	if len(data) < 8 {
		return nil, errors.New("short PNG")
	}
	for offset := 8; offset+12 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if length < 0 || offset+12+length > len(data) {
			return nil, errors.New("invalid PNG chunk length")
		}
		kind := string(data[offset+4 : offset+8])
		payload := data[offset+8 : offset+8+length]
		if kind == "iTXt" {
			prefix := append([]byte(keyword), 0, 0, 0, 0, 0)
			if bytes.HasPrefix(payload, prefix) {
				return append([]byte(nil), payload[len(prefix):]...), nil
			}
		}
		offset += length + 12
	}
	return nil, fmt.Errorf("PNG iTXt %q was not found", keyword)
}
