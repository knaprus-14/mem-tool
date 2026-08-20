package mem

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestKnowledgeGraphPersistsTypedNonTreeRelationsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		store.Close()
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	first, err := EvidenceAnchorForEntry(entries[0], "chunk-0")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	second, err := EvidenceAnchorForEntry(entries[1], "chunk-1")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "topic-hydraulics", Kind: KnowledgeNodeTopic, Label: "Hydraulics", Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{first, second}},
			{ID: "claim-a", Kind: KnowledgeNodeClaim, Label: "Claim A", Confidence: 0.9, Evidence: []EvidenceAnchor{first}},
			{ID: "claim-b", Kind: KnowledgeNodeClaim, Label: "Claim B", Confidence: 0.8, Evidence: []EvidenceAnchor{second}},
			{ID: "gap-a", Kind: KnowledgeNodeGap, Label: "Unresolved boundary", Status: KnowledgeStatusDraft, Evidence: []EvidenceAnchor{first, second}},
		},
		Edges: []KnowledgeEdge{
			{ID: "edge-topic-a", From: "topic-hydraulics", To: "claim-a", Kind: KnowledgeRelationContains, Evidence: []EvidenceAnchor{first}},
			{ID: "edge-topic-b", From: "topic-hydraulics", To: "claim-b", Kind: KnowledgeRelationContains, Evidence: []EvidenceAnchor{second}},
			{ID: "edge-conflict", From: "claim-a", To: "claim-b", Kind: KnowledgeRelationContradicts, Evidence: []EvidenceAnchor{first, second}},
			{ID: "edge-gap-a", From: "claim-a", To: "gap-a", Kind: KnowledgeRelationRevealsGap, Evidence: []EvidenceAnchor{first}},
			{ID: "edge-gap-b", From: "claim-b", To: "gap-a", Kind: KnowledgeRelationRevealsGap, Evidence: []EvidenceAnchor{second}},
		},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != 4 || len(loaded.Edges) != 5 {
		t.Fatalf("knowledge graph was not persisted: %#v", loaded)
	}
	if loaded.Edges[0].Kind == "" || len(loaded.Edges[0].Evidence) == 0 || loaded.Nodes[0].Created == "" {
		t.Fatalf("typed graph metadata was lost: %#v", loaded)
	}
}

func TestKnowledgeGraphRejectsUnanchoredOrBrokenFragmentAtomically(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	stable := KnowledgeGraph{Nodes: []KnowledgeNode{
		{ID: "topic-stable", Kind: KnowledgeNodeTopic, Label: "Stable", Evidence: []EvidenceAnchor{anchor}},
	}}
	if err := store.UpsertKnowledgeGraph(stable); err != nil {
		t.Fatal(err)
	}

	invalid := KnowledgeGraph{
		Nodes: []KnowledgeNode{{ID: "claim-invalid", Kind: KnowledgeNodeClaim, Label: "Invalid"}},
		Edges: []KnowledgeEdge{{
			ID: "edge-broken", From: "topic-stable", To: "missing-node",
			Kind: KnowledgeRelationSupports, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(invalid); err == nil || !strings.Contains(err.Error(), "no source evidence") {
		t.Fatalf("unanchored graph fragment was accepted: %v", err)
	}
	loaded, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].ID != "topic-stable" || len(loaded.Edges) != 0 {
		t.Fatalf("rejected graph fragment changed persistent state: %#v", loaded)
	}

	brokenEndpoint := KnowledgeGraph{Edges: []KnowledgeEdge{{
		ID: "edge-broken", From: "topic-stable", To: "missing-node",
		Kind: KnowledgeRelationSupports, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := store.UpsertKnowledgeGraph(brokenEndpoint); err == nil || !strings.Contains(err.Error(), "missing endpoint") {
		t.Fatalf("edge with missing endpoint was accepted: %v", err)
	}
}

func TestKnowledgeEvidenceBecomesStaleWhenDocumentRevisionChanges(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "book.md")
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testConfig(10, "fixed")

	firstDoc, err := ingest.ParseMarkdown(source, "short\n\n<!-- page: 2 -->\n\ntarget text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(cfg, store, firstDoc, ImportOptions{}, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	before := entryForPage(t, store.GetBySourceFile(firstDoc.SourcePath), 2)
	anchor, err := EvidenceAnchorForEntry(before, before.Text)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ResolveEvidenceAnchor(anchor); got.State != EvidenceCurrent {
		t.Fatalf("fresh evidence was not current: %#v", got)
	}

	secondDoc, err := ingest.ParseMarkdown(source, "a much longer earlier block that creates several chunks\n\n<!-- page: 2 -->\n\ntarget text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importExtractedDocumentWithEmbedder(cfg, store, secondDoc, ImportOptions{}, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	after := entryForPage(t, store.GetBySourceFile(secondDoc.SourcePath), 2)
	resolved := store.ResolveEvidenceAnchor(anchor)
	if resolved.State != EvidenceStale || resolved.CurrentDocumentRevision != after.DocumentRevision || resolved.CurrentChunkHash != after.ChunkHash {
		t.Fatalf("old graph evidence did not become traceably stale: %#v", resolved)
	}
	if anchor.CitationID != mustCitationID(after) || anchor.ChunkHash != after.ChunkHash {
		t.Fatalf("test did not preserve source location/chunk while changing document revision: anchor=%#v after=%#v", anchor, after)
	}
	if err := store.DeleteById(after.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.ResolveEvidenceAnchor(anchor); got.State != EvidenceMissing {
		t.Fatalf("deleted source evidence was not marked missing: %#v", got)
	}
}

func graphStoreAndAnchor(t *testing.T) (*Store, EvidenceAnchor) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		store.Close()
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, anchor
}
