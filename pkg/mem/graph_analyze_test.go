package mem

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildCorpusAnalysisPlanIsDeterministicAndReportsCoverage(t *testing.T) {
	store := corpusAnalysisStoreWithClaims(t, 5)
	defer store.Close()

	candidates, _, _, err := store.loadCorpusAnalysisCandidates("pressure")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 5 {
		t.Fatalf("got %d candidates, want 5", len(candidates))
	}
	_, twoClaimBudget, err := buildCorpusAnalysisPromptPayload("pressure", []CorpusAnalysisClaim{candidates[0].claim, candidates[1].claim})
	if err != nil {
		t.Fatal(err)
	}
	_, threeClaimSize, err := buildCorpusAnalysisPromptPayload("pressure", []CorpusAnalysisClaim{candidates[0].claim, candidates[1].claim, candidates[2].claim})
	if err != nil {
		t.Fatal(err)
	}
	if threeClaimSize <= twoClaimBudget {
		t.Fatal("test fixture does not distinguish two- and three-claim prompts")
	}

	first, err := store.BuildCorpusAnalysisPlan("pressure", twoClaimBudget, 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BuildCorpusAnalysisPlan("pressure", twoClaimBudget, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.EligibleClaims != 5 || first.CoveredClaims != 5 || first.UncoveredClaims != 0 ||
		first.EligibleDocuments != 5 || first.CoveredDocuments != 5 || len(first.Batches) != 3 {
		t.Fatalf("unexpected plan coverage: %#v", first)
	}
	if len(second.Batches) != len(first.Batches) {
		t.Fatalf("plan batch count changed: first=%d second=%d", len(first.Batches), len(second.Batches))
	}
	covered := make(map[string]bool)
	for i, batch := range first.Batches {
		if batch.BatchID == "" || batch.BatchID != second.Batches[i].BatchID || batch.DocumentCount < 2 || len(batch.Claims) != 2 {
			t.Fatalf("invalid or unstable batch %d: first=%#v second=%#v", i, batch, second.Batches[i])
		}
		for claimIndex, claim := range batch.Claims {
			if claim.Ref != "c"+string(rune('1'+claimIndex)) {
				t.Fatalf("batch-local refs are not deterministic: %#v", batch.Claims)
			}
			covered[claim.nodeID] = true
		}
	}
	if len(covered) != 5 {
		t.Fatalf("planner did not cover every claim: %#v", covered)
	}

	limited, err := store.BuildCorpusAnalysisPlan("pressure", twoClaimBudget, 2)
	if err != nil {
		t.Fatal(err)
	}
	if limited.CoveredClaims != 4 || limited.UncoveredClaims != 1 || len(limited.Batches) != 2 {
		t.Fatalf("batch limit coverage is wrong: %#v", limited)
	}
	if _, err := store.BuildCorpusAnalysisPlan("pressure", twoClaimBudget, MaxCorpusAnalysisBatches+1); err == nil {
		t.Fatal("planner accepted an excessive batch limit")
	}
}

func TestMergeCorpusAnalysisGraphsDeduplicatesOrRejectsConflicts(t *testing.T) {
	store, _ := corpusAnalysisStore(t)
	defer store.Close()
	prompt, err := store.BuildCorpusAnalysisPrompt("pressure", 100000)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"findings":[{"kind":"gap","label":"Shared gap","confidence":0.7,"claim_refs":["c1","c2"],"citations":["` +
		prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`
	decoded, err := DecodeCorpusAnalysis(raw, prompt.Claims)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := decoded.Graph
	duplicate.Nodes = append([]KnowledgeNode(nil), duplicate.Nodes...)
	duplicate.Edges = append([]KnowledgeEdge(nil), duplicate.Edges...)
	duplicate.Nodes[0].Confidence = 0.9
	for i := range duplicate.Edges {
		duplicate.Edges[i].Confidence = 0.9
	}
	merged, err := MergeCorpusAnalysisGraphs(decoded.Graph, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Nodes) != 1 || len(merged.Edges) != 2 || merged.Nodes[0].Confidence != 0.9 {
		t.Fatalf("duplicate corpus graph was not merged deterministically: %#v", merged)
	}

	conflict := decoded.Graph
	conflict.Nodes = append([]KnowledgeNode(nil), conflict.Nodes...)
	conflict.Nodes[0].Body = "conflicting body"
	if _, err := MergeCorpusAnalysisGraphs(decoded.Graph, conflict); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("conflicting stable node was accepted: %v", err)
	}
	if reflect.DeepEqual(KnowledgeGraph{}, merged) {
		t.Fatal("merged graph unexpectedly empty")
	}
}

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

func corpusAnalysisStoreWithClaims(t *testing.T, count int) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]KnowledgeNode, 0, count)
	for i := 0; i < count; i++ {
		text := "Pressure requirement " + string(rune('A'+i)) + "."
		source := filepath.ToSlash(filepath.Join(t.TempDir(), "plan-doc-"+string(rune('a'+i))+".md"))
		entry, err := store.AddDocumentChunk(text, "Plan document", nil, "test", []float32{1, 0}, source, 0, 1, false, Provenance{
			DocumentID: "plan-doc-" + string(rune('a'+i)), DocumentRevision: ChunkContentHash("plan-revision-" + string(rune('a'+i))),
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
		nodes = append(nodes, KnowledgeNode{
			ID: "plan-claim-" + string(rune('a'+i)), Kind: KnowledgeNodeClaim,
			Label: "Pressure claim " + string(rune('A'+i)), Body: text,
			Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		})
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}
