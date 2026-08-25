package mem

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type countingKnowledgeGraphQuerier struct {
	knowledgeGraphQuerier
	queries int
}

func (q *countingKnowledgeGraphQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	q.queries++
	return q.knowledgeGraphQuerier.Query(query, args...)
}

func TestLoadKnowledgeGraphUsesBoundedQueries(t *testing.T) {
	store := knowledgeGraphPerformanceStore(t, 200)
	defer store.Close()
	querier := &countingKnowledgeGraphQuerier{knowledgeGraphQuerier: store.db}
	graph, err := loadKnowledgeGraphFromQuerier(querier)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 200 || len(graph.Edges) != 199 {
		t.Fatalf("unexpected graph size: %d nodes, %d edges", len(graph.Nodes), len(graph.Edges))
	}
	if querier.queries != 4 {
		t.Fatalf("graph load used %d queries, want 4 independent of graph size", querier.queries)
	}
}

func TestResolveEvidenceAnchorsUsesBoundedBatchesAndPreservesStates(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	anchors := []EvidenceAnchor{anchor, anchor}
	anchors[1].DocumentRevision = "sha256:" + strings.Repeat("0", 64)
	for i := 1; i <= 400; i++ {
		missing := anchor
		missing.BlockIndex = anchor.BlockIndex + i
		missing.CitationID, _ = CitationForEntry(Entry{
			DocumentID: missing.DocumentID, SourcePath: missing.SourcePath, Page: missing.Page,
			BlockIndex: missing.BlockIndex, BlockChunkIndex: missing.BlockChunkIndex, BlockTotalChunks: missing.BlockChunkIndex + 1,
		})
		anchors = append(anchors, missing)
	}
	querier := &countingKnowledgeGraphQuerier{knowledgeGraphQuerier: store.db}
	resolved, err := resolveEvidenceAnchorsWithQuery(querier, anchors)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != len(anchors) || resolved[0].State != EvidenceCurrent || resolved[1].State != EvidenceStale {
		t.Fatalf("current/stale resolution changed: %#v", resolved[:2])
	}
	for i := 2; i < len(resolved); i++ {
		if resolved[i].State != EvidenceMissing {
			t.Fatalf("anchor %d: state=%q, want missing", i, resolved[i].State)
		}
	}
	if querier.queries != 3 {
		t.Fatalf("401 unique coordinates used %d queries, want 3 bounded batches", querier.queries)
	}
}

func BenchmarkLoadKnowledgeGraph1000(b *testing.B) {
	store := knowledgeGraphPerformanceStore(b, 1000)
	defer store.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.LoadKnowledgeGraph(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProfileKnowledgeMap1000(b *testing.B) {
	store := knowledgeGraphPerformanceStore(b, 1000)
	defer store.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ProfileKnowledgeMap(KnowledgeMapProfileOptions{Iterations: 1}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReviewKnowledgeGraphSeesExternalDocumentRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial := coverageTestChunks("external revision one", []string{"Первоначальный фрагмент"})
	if err := store.ReplaceDocumentChunks(initial[0].Provenance.SourcePath, initial); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(initial[0].Provenance.SourcePath)
	anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "external-review", Kind: KnowledgeNodeClaim, Label: "Проверяемое утверждение",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	external, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := coverageTestChunks("external revision two", []string{"Изменённый фрагмент"})
	if err := external.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		external.Close()
		t.Fatal(err)
	}
	if err := external.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := store.ReviewKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 1 || report.Items[0].EvidenceState != EvidenceStale || report.Summary.StaleEvidence != 1 {
		t.Fatalf("external document revision was not observed: %#v", report)
	}
	diff, err := store.BuildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Documents) != 1 || diff.Documents[0].CurrentRevision != changed[0].Provenance.DocumentRevision ||
		len(diff.Documents[0].Objects) != 1 || len(diff.Documents[0].Objects[0].Evidence) != 1 ||
		diff.Documents[0].Objects[0].Evidence[0].CurrentText != changed[0].Text {
		t.Fatalf("revision diff used stale in-memory entries: %#v", diff)
	}
}

type testingTB interface {
	Helper()
	TempDir() string
	Fatal(...any)
}

func knowledgeGraphPerformanceStore(tb testingTB, nodeCount int) *Store {
	tb.Helper()
	store, err := NewStore(filepath.Join(tb.TempDir(), "db"))
	if err != nil {
		tb.Fatal(err)
	}
	chunks := coverageTestChunks("performance revision", []string{"Общий проверяемый фрагмент"})
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		store.Close()
		tb.Fatal(err)
	}
	entries := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		store.Close()
		tb.Fatal(err)
	}
	graph := KnowledgeGraph{Nodes: make([]KnowledgeNode, 0, nodeCount), Edges: make([]KnowledgeEdge, 0, nodeCount-1)}
	for i := 0; i < nodeCount; i++ {
		graph.Nodes = append(graph.Nodes, KnowledgeNode{
			ID: fmt.Sprintf("perf-node-%04d", i), Kind: KnowledgeNodeClaim, Label: fmt.Sprintf("Утверждение %d", i),
			Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
		})
		if i > 0 {
			graph.Edges = append(graph.Edges, KnowledgeEdge{
				ID: fmt.Sprintf("perf-edge-%04d", i), From: fmt.Sprintf("perf-node-%04d", i-1), To: fmt.Sprintf("perf-node-%04d", i),
				Kind: KnowledgeRelationRelated, Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
			})
		}
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		store.Close()
		tb.Fatal(err)
	}
	return store
}
