package mem

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeGraphPortableExportProducesEveryFormatAndCanonicalProvenance(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "source-node", Kind: KnowledgeNodeClaim, Label: "Требование «А»", Body: "Полный текст & детали", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Confidence: .9, Evidence: []EvidenceAnchor{anchor}},
			{ID: "work-node", Kind: KnowledgeNodeTask, Label: "Проверить результат", Body: "Ручная задача", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{ID: "edge-1", From: "work-node", To: "source-node", Kind: KnowledgeRelationBasedOn, Label: "основано на", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	if pin.NodeCount != 2 || pin.EdgeCount != 1 || pin.Evidence != 3 || pin.Current != 3 || pin.Stale != 0 || pin.Missing != 0 {
		t.Fatalf("unexpected export pin: %#v", pin)
	}
	tests := []struct {
		format      KnowledgeGraphExportFormat
		filename    string
		contentType string
		markers     []string
		xml         bool
	}{
		{KnowledgeGraphExportMarkdown, "mem-knowledge-map.md", "text/markdown", []string{"# Проверяемая карта", "Требование", "Типизированные связи", "book.pdf", "mem-provenance"}, false},
		{KnowledgeGraphExportOutline, "mem-knowledge-map.txt", "text/plain", []string{"Проверяемая карта", "ИСХОДНЫЕ ЗНАНИЯ", "СВЯЗИ", "MEM-PROVENANCE"}, false},
		{KnowledgeGraphExportOPML, "mem-knowledge-map.opml", "text/x-opml", []string{"<opml", "mem:evidence", "mem-provenance"}, true},
		{KnowledgeGraphExportGraphML, "mem-knowledge-map.graphml", "application/graphml+xml", []string{"<graphml", "<node", "<edge", "mem-provenance"}, true},
		{KnowledgeGraphExportGEXF, "mem-knowledge-map.gexf", "application/gexf+xml", []string{"<gexf", "<nodes>", "<edges>", "mem:provenance"}, true},
		{KnowledgeGraphExportMermaid, "mem-knowledge-map.mmd", "text/plain", []string{"flowchart LR", "основано на", "mem-provenance"}, false},
		{KnowledgeGraphExportObsidian, "mem-knowledge-map.obsidian.md", "text/markdown", []string{"mem_tool: knowledge-graph", "```mermaid", "## Узлы", "^mem-source-node"}, false},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			artifact, exportErr := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{
				Format: test.format, Title: "Проверяемая карта", ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest,
			})
			if exportErr != nil {
				t.Fatal(exportErr)
			}
			if artifact.Filename != test.filename || !strings.HasPrefix(artifact.ContentType, test.contentType) || len(artifact.Data) == 0 {
				t.Fatalf("unexpected artifact metadata: %#v", artifact)
			}
			if artifact.Digest != pin.Digest || artifact.StateDigest != pin.StateDigest || artifact.NodeCount != 2 || artifact.EdgeCount != 1 {
				t.Fatalf("artifact lost pins/counts: %#v", artifact)
			}
			for _, marker := range test.markers {
				if !bytes.Contains(artifact.Data, []byte(marker)) {
					t.Fatalf("%s export is missing %q:\n%s", test.format, marker, artifact.Data)
				}
			}
			if test.xml {
				decoder := xml.NewDecoder(bytes.NewReader(artifact.Data))
				for {
					if _, decodeErr := decoder.Token(); decodeErr != nil {
						if decodeErr.Error() == "EOF" {
							break
						}
						t.Fatalf("%s is not well-formed XML: %v\n%s", test.format, decodeErr, artifact.Data)
					}
				}
			}
		})
	}

	markdown, err := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{Format: KnowledgeGraphExportMarkdown, ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest})
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.SplitN(string(markdown.Data), "mem-provenance-base64-raw-std-v1:\n", 2)
	if len(encoded) != 2 {
		t.Fatal("markdown export has no canonical provenance envelope")
	}
	payload := strings.TrimSpace(strings.SplitN(encoded[1], "\n-->", 2)[0])
	decoded, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope knowledgeGraphExportEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil || envelope.Digest != pin.Digest || len(envelope.Graph.Nodes) != 2 || len(envelope.Evidence) != 3 {
		t.Fatalf("canonical envelope is incomplete: envelope=%#v err=%v", envelope, err)
	}
}

func TestKnowledgeGraphPortableExportFailsClosedWhenGraphOrEvidenceStateChanges(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "pinned", Kind: KnowledgeNodeClaim, Label: "Закреплено", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{Format: KnowledgeGraphExportMarkdown, ExpectedDigest: "sha256:wrong", ExpectedStateDigest: pin.StateDigest}); !errors.Is(err, ErrKnowledgeGraphExportChanged) {
		t.Fatalf("wrong content pin was accepted: %v", err)
	}

	second, err := NewStore(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.db.Exec(`UPDATE entries SET text=text || ' изменено', document_revision=?, chunk_hash=? WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=?`,
		"sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), anchor.DocumentID, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex); err != nil {
		second.Close()
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{Format: KnowledgeGraphExportMarkdown, ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest}); !errors.Is(err, ErrKnowledgeGraphExportChanged) {
		t.Fatalf("changed source state was accepted through a stale Store cache: %v", err)
	}
	current, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	if current.Digest != pin.Digest || current.StateDigest == pin.StateDigest || current.Stale+current.Missing == 0 {
		t.Fatalf("state-only change was not represented: before=%#v after=%#v", pin, current)
	}

	if _, err := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{Format: "unknown"}); err == nil {
		t.Fatal("unsupported format was accepted")
	}
	if _, err := store.ExportKnowledgeGraph(KnowledgeGraphExportRequest{Format: KnowledgeGraphExportMarkdown, Title: "line one\nline two"}); err == nil {
		t.Fatal("multiline title was accepted")
	}
}

func TestKnowledgeGraphPortableExportWorkspaceHTTPFlow(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "http-export", Kind: KnowledgeNodeClaim, Label: "HTTP экспорт", Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	const token = "portable-export-session-capability"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "HTTP карта", token, DefaultKnowledgeMapView)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"portable_export"`) || !strings.Contains(page.Body.String(), `"state_digest":"sha256:`) {
		t.Fatalf("workspace page has no export pins: status=%d body=%q", page.Code, page.Body.String())
	}
	for _, marker := range []string{"portableExportBtn", "portableExportBackdrop", "submitPortableExport", "'/api/export'", `value="graphml"`, `value="gexf"`, `value="obsidian"`, "Экспортируется весь граф"} {
		if !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("workspace export UI is missing %q", marker)
		}
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeGraphExportRequest{Format: KnowledgeGraphExportGraphML, Title: "HTTP карта", ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest}
	raw, _ := json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/export", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized export returned %d", got.Code)
	}
	exported := requestKnowledgeMapMutation(t, handler, "/api/export", host, "http://"+host, token, "same-origin", raw)
	if exported.Code != http.StatusOK || exported.Header().Get("Content-Type") != "application/graphml+xml; charset=utf-8" ||
		!strings.Contains(exported.Header().Get("Content-Disposition"), "mem-knowledge-map.graphml") || !strings.Contains(exported.Body.String(), "<graphml") {
		t.Fatalf("workspace export failed: status=%d type=%q disposition=%q body=%q", exported.Code, exported.Header().Get("Content-Type"), exported.Header().Get("Content-Disposition"), exported.Body.String())
	}
	request.ExpectedStateDigest = "sha256:wrong"
	raw, _ = json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/export", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusConflict {
		t.Fatalf("stale export pin returned %d instead of conflict", got.Code)
	}
	request.ExpectedDigest, request.ExpectedStateDigest = "", ""
	raw, _ = json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/export", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusBadRequest {
		t.Fatalf("unpinned browser export returned %d", got.Code)
	}
}
