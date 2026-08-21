package mem

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeMapHTMLContainsOfflineInteractiveProvenancePayload(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	attack := `</script><script>alert("x")</script>`
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "view-claim", Kind: KnowledgeNodeClaim, Label: attack, Body: "Pinned claim", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "view-gap", Kind: KnowledgeNodeGap, Label: "Evidence gap", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "view-edge", From: "view-claim", To: "view-gap", Kind: KnowledgeRelationRevealsGap,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, KnowledgeMapLayout{
		Version: KnowledgeMapLayoutVersion,
		Nodes: map[string]KnowledgeMapNodePosition{
			"view-claim": {X: 120, Y: 80, Pinned: true},
		},
		Viewport: KnowledgeMapViewport{Scale: 1.2, X: 4, Y: -3},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		t.Fatal(err)
	}
	if data.Version != KnowledgeMapViewVersion || len(data.Graph.Nodes) != 2 || len(data.Review.Items) != 3 || data.Merges == nil ||
		data.Layout == nil || data.Layout.Nodes["view-claim"].X != 120 || data.Workspace != nil {
		t.Fatalf("view payload is incomplete: %#v", data)
	}
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, `Map </title><script>alert(1)</script>`, data); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, marker := range []string{
		`data-mem-map="v1"`, `Content-Security-Policy`, `function tick`, `pointerdown`,
		`statusFilters`, `evidenceFilters`, `relationFilters`, `showEvidence`,
		`contradiction`, `reveals_gap`, `document_revision`,
		`n._g.setPointerCapture(ev.pointerId)`, `if(ev.target!==svg)return`,
		`Технические данные источника`, `Внутренний ID`, `источник актуален`,
		`страница `, `фрагмент `, `refreshBtn`, `liveMode`,
		`resetLayoutBtn`, `scheduleLayoutSave`, `X-Mem-Session`, `savedLayout`,
		`двойной щелчок — освободить`, `connect-src 'self'`,
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("HTML is missing %q", marker)
		}
	}
	if strings.Contains(html, `details.append(title,badges,field('ID',item.id))`) ||
		strings.Contains(html, `details.append(field('Evidence digest'`) {
		t.Fatal("technical identifiers are still rendered in the primary details view")
	}
	if strings.Contains(html, attack) || strings.Contains(html, `</title><script>alert(1)</script>`) {
		t.Fatal("untrusted title or graph text escaped its data/title context")
	}
	if !strings.Contains(html, `\u003c/script\u003e`) || !strings.Contains(html, `&lt;/title&gt;`) {
		t.Fatal("HTML does not contain safely escaped untrusted text")
	}
	if strings.Contains(html, `src="http`) || strings.Contains(html, `href="http`) {
		t.Fatal("offline map contains a remote dependency")
	}
	const payloadStart = `<script id="mem-map-data" type="application/json">`
	start := strings.Index(html, payloadStart)
	if start < 0 {
		t.Fatal("map payload start not found")
	}
	start += len(payloadStart)
	end := strings.Index(html[start:], `</script>`)
	if end < 0 {
		t.Fatal("map payload end not found")
	}
	var decoded KnowledgeMapViewData
	if err := json.Unmarshal([]byte(html[start:start+end]), &decoded); err != nil {
		t.Fatalf("embedded payload is not valid JSON: %v", err)
	}
	if len(decoded.Graph.Nodes) != 2 || decoded.Graph.Nodes[0].Label != attack || decoded.Review.Items[0].Evidence[0].Anchor.SourcePath == "" ||
		decoded.Layout == nil || decoded.Layout.Nodes["view-claim"].Pinned != true || decoded.Workspace != nil {
		t.Fatalf("embedded payload lost graph provenance: %#v", decoded)
	}
}

func TestKnowledgeMapHTMLRejectsInvalidArguments(t *testing.T) {
	if err := WriteKnowledgeMapHTML(nil, "", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err == nil {
		t.Fatal("nil writer was accepted")
	}
	if err := WriteKnowledgeMapHTML(&bytes.Buffer{}, "", KnowledgeMapViewData{Version: KnowledgeMapViewVersion + 1}); err == nil {
		t.Fatal("unknown view version was accepted")
	}
}
