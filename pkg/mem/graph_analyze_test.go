package mem

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCorpusAnalysisPromptUsesOnlyCurrentActiveClaimsAcrossDocuments(t *testing.T) {
	store, anchors := corpusAnalysisStore(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "corpus-draft", Kind: KnowledgeNodeClaim, Label: "Pressure draft",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchors[0]},
	}}}); err != nil {
		t.Fatal(err)
	}
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure requirement", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.EligibleClaims != 2 || len(prompt.Claims) != 2 || prompt.DocumentCount != 2 || prompt.SkippedNonCurrent != 0 {
		t.Fatalf("unexpected corpus selection: %#v", prompt)
	}
	if prompt.Claims[0].Ref != "c1" || prompt.Claims[1].Ref != "c2" {
		t.Fatalf("host claim refs are not deterministic: %#v", prompt.Claims)
	}
	if strings.Contains(prompt.User, "corpus-claim-a") || strings.Contains(prompt.User, "corpus-claim-b") {
		t.Fatal("persistent node IDs leaked into the model prompt")
	}
	if !strings.Contains(prompt.System, "untrusted data") || !strings.Contains(prompt.User, "CLAIMS_JSON_BEGIN") {
		t.Fatal("corpus prompt lost its trust boundary")
	}

	entry := store.GetBySourceFile(anchors[1].SourcePath)[0]
	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	stalePrompt, err := store.BuildCorpusAnalysisPrompt("pressure requirement", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if stalePrompt.SkippedNonCurrent != 1 || len(stalePrompt.Claims) != 1 || stalePrompt.DocumentCount != 1 {
		t.Fatalf("non-current active claim entered corpus prompt: %#v", stalePrompt)
	}
}

func TestDecodeCorpusAnalysisBuildsDraftContradictionAndHostRelations(t *testing.T) {
	store, _ := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	firstCitation := prompt.Claims[0].Evidence[0].CitationID
	secondCitation := prompt.Claims[1].Evidence[0].CitationID
	raw := `{"findings":[{"kind":"contradiction","label":"Incompatible pressure limits",` +
		`"body":"The approved claims specify incompatible limits.","confidence":0.91,` +
		`"claim_refs":["c1","c2"],"citations":["` + firstCitation + `","` + secondCitation + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Graph.Nodes) != 1 || len(decoded.Graph.Edges) != 3 {
		t.Fatalf("unexpected contradiction graph: %#v", decoded.Graph)
	}
	finding := decoded.Graph.Nodes[0]
	if finding.Kind != KnowledgeNodeContradiction || finding.Status != KnowledgeStatusDraft || finding.Origin != KnowledgeOriginGenerated || len(finding.Evidence) != 2 {
		t.Fatalf("finding authority was not host-derived: %#v", finding)
	}
	kinds := make(map[KnowledgeRelationKind]int)
	for _, edge := range decoded.Graph.Edges {
		kinds[edge.Kind]++
		if edge.Status != KnowledgeStatusDraft || edge.Origin != KnowledgeOriginGenerated || len(edge.Evidence) != 2 {
			t.Fatalf("derived corpus edge is invalid: %#v", edge)
		}
	}
	if kinds[KnowledgeRelationContradicts] != 1 || kinds[KnowledgeRelationDerivedFrom] != 2 {
		t.Fatalf("wrong contradiction relations: %#v", kinds)
	}
	if err := store.UpsertCorpusAnalysisGraph(decoded.Graph); err != nil {
		t.Fatal(err)
	}
	second, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil || second.Graph.Nodes[0].ID != finding.ID {
		t.Fatalf("corpus finding ID is unstable: second=%#v err=%v", second, err)
	}
	if err := store.UpsertCorpusAnalysisGraph(second.Graph); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || len(loaded.Nodes) != 3 || len(loaded.Edges) != 3 {
		t.Fatalf("repeated corpus analysis duplicated objects: graph=%#v err=%v", loaded, err)
	}
}

func TestDecodeCorpusAnalysisBuildsGapRelations(t *testing.T) {
	store, _ := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"findings":[{"kind":"gap","label":"Undefined comparison condition",` +
		`"body":"The claims use different limits without a shared operating condition.","confidence":0.82,` +
		`"claim_refs":["c1","c2"],"citations":["` + prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Graph.Nodes) != 1 || decoded.Graph.Nodes[0].Kind != KnowledgeNodeGap || len(decoded.Graph.Edges) != 2 {
		t.Fatalf("unexpected gap graph: %#v", decoded.Graph)
	}
	for _, edge := range decoded.Graph.Edges {
		if edge.Kind != KnowledgeRelationRevealsGap || edge.To != decoded.Graph.Nodes[0].ID {
			t.Fatalf("gap relation was not host-derived: %#v", edge)
		}
	}
}

func TestDecodeCorpusAnalysisRejectsInventedOrSingleDocumentFindings(t *testing.T) {
	store, anchors := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	firstCitation := prompt.Claims[0].Evidence[0].CitationID
	secondCitation := prompt.Claims[1].Evidence[0].CitationID
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown ref", `{"findings":[{"kind":"contradiction","label":"x","confidence":0.5,"claim_refs":["c1","attacker"],"citations":["` + firstCitation + `","` + secondCitation + `"]}]}`, "unknown claim ref"},
		{"missing claim citation", `{"findings":[{"kind":"contradiction","label":"x","confidence":0.5,"claim_refs":["c1","c2"],"citations":["` + firstCitation + `"]}]}`, "no cited evidence"},
		{"invented citation", `{"findings":[{"kind":"contradiction","label":"x","confidence":0.5,"claim_refs":["c1","c2"],"citations":["` + firstCitation + `","cite-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-p1-b0-c0"]}]}`, "does not belong"},
		{"invented id", `{"findings":[{"id":"attacker","kind":"gap","label":"x","confidence":0.5,"claim_refs":["c1","c2"],"citations":["` + firstCitation + `","` + secondCitation + `"]}]}`, "unknown field"},
		{"wrong contradiction arity", `{"findings":[{"kind":"contradiction","label":"x","confidence":0.5,"claim_refs":["c1"],"citations":["` + firstCitation + `"]}]}`, "claim_refs count"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeCorpusAnalysis(tt.raw, prompt.Claims); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want substring %q", err, tt.want)
			}
		})
	}

	sameDocumentClaims := append([]CorpusAnalysisClaim(nil), prompt.Claims...)
	sameDocumentClaims[1].anchors = []EvidenceAnchor{anchors[0]}
	sameDocumentClaims[1].Evidence = []GroundedEvidence{groundedEvidenceForAnchor(anchors[0])}
	sameDocRaw := `{"findings":[{"kind":"contradiction","label":"x","confidence":0.5,"claim_refs":["c1","c2"],"citations":["` + firstCitation + `"]}]}`
	if _, err := DecodeCorpusAnalysis(sameDocRaw, sameDocumentClaims); err == nil || !strings.Contains(err.Error(), "two documents") {
		t.Fatalf("single-document finding was accepted: %v", err)
	}
	insufficient, err := DecodeCorpusAnalysis(`{"insufficient_evidence":"no conflict"}`, prompt.Claims)
	if err != nil || !insufficient.Insufficient || insufficient.Reason != "no conflict" {
		t.Fatalf("honest insufficient response failed: %#v err=%v", insufficient, err)
	}
}

func TestUpsertCurrentKnowledgeGraphRejectsEvidenceChangedDuringAnalysis(t *testing.T) {
	store, _ := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"findings":[{"kind":"gap","label":"Changed evidence gap","confidence":0.8,"claim_refs":["c1","c2"],"citations":["` + prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(prompt.Claims[0].Evidence[0].SourcePath)[0]
	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCorpusAnalysisGraph(decoded.Graph); !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
		t.Fatalf("changed evidence did not block corpus persistence: %v", err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || len(loaded.Nodes) != 2 || len(loaded.Edges) != 0 {
		t.Fatalf("rejected corpus analysis partially changed graph: graph=%#v err=%v", loaded, err)
	}
}

func TestUpsertCorpusAnalysisGraphRejectsClaimDemotedDuringAnalysis(t *testing.T) {
	store, anchors := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"findings":[{"kind":"gap","label":"Demoted endpoint gap","confidence":0.8,"claim_refs":["c1","c2"],"citations":["` + prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "corpus-claim-a", Kind: KnowledgeNodeClaim, Label: "Maximum pressure", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchors[0]},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCorpusAnalysisGraph(decoded.Graph); !errors.Is(err, ErrKnowledgeEndpointsNotActive) {
		t.Fatalf("demoted corpus endpoint did not block persistence: %v", err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil || len(loaded.Nodes) != 2 || len(loaded.Edges) != 0 {
		t.Fatalf("blocked endpoint persistence changed graph: graph=%#v err=%v", loaded, err)
	}
}

func corpusAnalysisStore(t *testing.T) (*Store, []EvidenceAnchor) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"Pressure must not exceed 1.0 MPa.", "Pressure must be at least 1.2 MPa."}
	anchors := make([]EvidenceAnchor, 0, len(texts))
	for i, text := range texts {
		source := filepath.ToSlash(filepath.Join(t.TempDir(), "doc-"+string(rune('a'+i))+".md"))
		entry, err := store.AddDocumentChunk(text, "Document", nil, "test", []float32{1, 0}, source, 0, 1, false, Provenance{
			DocumentID: "corpus-doc-" + string(rune('a'+i)), DocumentRevision: ChunkContentHash("revision-" + string(rune('a'+i))),
			ChunkHash: ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown",
			Page: 1, BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		anchor, err := EvidenceAnchorForEntry(*entry, entry.Text)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		anchors = append(anchors, anchor)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "corpus-claim-a", Kind: KnowledgeNodeClaim, Label: "Maximum pressure", Body: texts[0], Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchors[0]}},
		{ID: "corpus-claim-b", Kind: KnowledgeNodeClaim, Label: "Minimum pressure", Body: texts[1], Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchors[1]}},
	}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, anchors
}
