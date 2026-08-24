package mem

import "testing"

func TestKnowledgeRevisionDiffTracksCurrentChangedAndMissingEvidence(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "diff-source", Kind: KnowledgeNodeClaim, Label: "Source claim", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "diff-card", Kind: KnowledgeNodeCard, Label: "Study card", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "diff-edge", From: "diff-source", To: "diff-card", Kind: KnowledgeRelationDerivedFrom,
			Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}

	current, err := store.BuildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.Summary.Documents != 1 || current.Summary.CurrentDocuments != 1 || current.Summary.CurrentAnchors != 3 ||
		current.Summary.ChangedAnchors != 0 || current.Summary.AffectedNodes != 0 || len(current.Documents) != 1 {
		t.Fatalf("fresh diff is incorrect: %#v", current)
	}

	chunks := validStructuredChunks()
	newRevision := ChunkContentHash("changed document revision")
	for i := range chunks {
		chunks[i].Provenance.DocumentRevision = newRevision
	}
	chunks[0].Text = "changed chunk-0 text"
	chunks[0].Provenance.ChunkHash = ChunkContentHash(chunks[0].Text)
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	changed, err := store.BuildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{Document: chunks[0].Provenance.SourcePath})
	if err != nil {
		t.Fatal(err)
	}
	document := changed.Documents[0]
	if changed.Summary.ChangedDocuments != 1 || changed.Summary.ChangedAnchors != 3 || changed.Summary.AffectedNodes != 2 ||
		changed.Summary.AffectedEdges != 1 || document.CurrentRevision != newRevision || document.Objects[0].Evidence[0].CurrentText != chunks[0].Text {
		t.Fatalf("changed diff is incorrect: %#v", changed)
	}

	entry := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	missing, err := store.BuildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{Document: anchor.DocumentID})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Summary.MissingDocuments != 1 || missing.Summary.MissingAnchors != 3 || missing.Documents[0].Objects[0].Evidence[0].State != EvidenceMissing {
		t.Fatalf("missing diff is incorrect: %#v", missing)
	}
}

func TestKnowledgeRevisionDiffRejectsUnknownDocumentSelector(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "diff-known", Kind: KnowledgeNodeClaim, Label: "Known", Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{Document: "C:/missing.pdf"}); err == nil {
		t.Fatal("unknown document selector was accepted")
	}
}
