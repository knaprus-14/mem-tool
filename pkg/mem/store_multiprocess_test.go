package mem

import (
	cryptorand "crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEmptyReindexRefreshesStaleSecondStoreCache(t *testing.T) {
	root := t.TempDir()
	dbDir := filepath.Join(root, "db")
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("shared old text"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeA, err := NewStore(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), storeA, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	source, _ := CanonicalSourcePath(path)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), storeA, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if entries := storeB.GetBySourceFile(source); len(entries) != 0 {
		t.Fatalf("read API exposed stale chunks after external empty replacement: %#v", entries)
	}
	if _, err := indexFileWithEmbedder(testConfig(100, "paragraph"), storeB, path, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if entries := storeB.GetBySourceFile(source); len(entries) != 0 {
		t.Fatalf("idempotent empty reindex kept stale cache: %#v", entries)
	}
}

func TestEntryBackedReadersRefreshAcrossStores(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	external, err := storeA.Add("external searchable text", "external", []string{"shared"}, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := storeB.GetByID(external.ID); err != nil || got.Text != external.Text {
		t.Fatalf("GetByID did not refresh external insert: entry=%#v err=%v", got, err)
	}
	if recent, err := storeB.Recent(5); err != nil || len(recent) != 1 || recent[0].ID != external.ID {
		t.Fatalf("Recent did not refresh external insert: entries=%#v err=%v", recent, err)
	}
	if results, err := storeB.Search([]float32{1, 0}, "test", 5); err != nil || len(results) != 1 || results[0].ID != external.ID {
		t.Fatalf("Search did not refresh external insert: entries=%#v err=%v", results, err)
	}
	if got := storeB.Stats()["total_entries"]; got != 1 {
		t.Fatalf("Stats did not refresh external insert: total=%#v", got)
	}

	if err := storeA.DeleteById(external.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.GetByID(external.ID); err == nil {
		t.Fatal("GetByID exposed externally deleted entry")
	}
	if results, err := storeB.Search([]float32{1, 0}, "test", 5); err != nil || len(results) != 0 {
		t.Fatalf("Search exposed externally deleted entry: entries=%#v err=%v", results, err)
	}
	if got := storeB.Stats()["total_entries"]; got != 0 {
		t.Fatalf("Stats exposed externally deleted entry: total=%#v", got)
	}
}

func TestDocumentReadModelsRefreshAcrossStores(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	revision1 := validStructuredChunks()
	if err := storeA.ReplaceDocumentChunks(revision1[0].Provenance.SourcePath, revision1); err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	revision2 := changedRevisionChunks(revision1, "external read revision", "externally refreshed evidence text")
	if err := storeA.ReplaceDocumentChunks(revision2[0].Provenance.SourcePath, revision2); err != nil {
		t.Fatal(err)
	}
	current := storeB.GetBySourceFile(revision2[0].Provenance.SourcePath)
	if len(current) != len(revision2) || current[0].DocumentRevision != revision2[0].Provenance.DocumentRevision || current[0].Text != revision2[0].Text {
		t.Fatalf("source lookup did not refresh external revision: %#v", current)
	}
	documents := storeB.ListClassicMindMapSourceDocuments()
	if len(documents) != 1 || documents[0].DocumentRevision != revision2[0].Provenance.DocumentRevision {
		t.Fatalf("classic source documents did not refresh external revision: %#v", documents)
	}
	candidates, err := storeB.SearchClassicMindMapEvidence(ClassicMindMapEvidenceSearchOptions{
		Document: revision2[0].Provenance.SourcePath,
		Query:    "externally refreshed evidence text",
		Limit:    10,
	})
	if err != nil || len(candidates) == 0 || candidates[0].DocumentRevision != revision2[0].Provenance.DocumentRevision {
		t.Fatalf("classic evidence search did not refresh external revision: candidates=%#v err=%v", candidates, err)
	}
	report, err := storeB.BuildCorpusRevisionDiff(revision2[0].Provenance.SourcePath, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Available || report.ToRevision != revision2[0].Provenance.DocumentRevision {
		t.Fatalf("corpus diff used stale current revision: %#v", report)
	}
}

func TestDocumentReplacementReadsAndArchivesCurrentDatabaseRevision(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()

	revision1 := validStructuredChunks()
	if err := storeA.ReplaceDocumentChunks(revision1[0].Provenance.SourcePath, revision1); err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	revision2 := changedRevisionChunks(revision1, "external revision 2", "revision-2 text")
	if err := storeB.ReplaceDocumentChunks(revision2[0].Provenance.SourcePath, revision2); err != nil {
		t.Fatal(err)
	}
	external, err := storeB.Add("written by the second store", "external", nil, "test", []float32{0, 1}, false)
	if err != nil {
		t.Fatal(err)
	}

	revision3 := changedRevisionChunks(revision2, "local revision 3", "revision-3 text")
	if err := storeA.ReplaceDocumentChunks(revision3[0].Provenance.SourcePath, revision3); err != nil {
		t.Fatal(err)
	}
	current := storeA.GetBySourceFile(revision3[0].Provenance.SourcePath)
	if len(current) != len(revision3) || current[0].DocumentRevision != revision3[0].Provenance.DocumentRevision ||
		current[0].Text != revision3[0].Text {
		t.Fatalf("store A cache was not refreshed from committed state: %#v", current)
	}
	if cachedExternal, err := storeA.GetByID(external.ID); err != nil || cachedExternal.Text != external.Text {
		t.Fatalf("store A cache lost an external write: entry=%#v err=%v", cachedExternal, err)
	}

	fresh, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	snapshots, err := fresh.ListDocumentHistorySnapshots(revision1[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		found[snapshot.DocumentRevision] = true
	}
	if !found[revision1[0].Provenance.DocumentRevision] || !found[revision2[0].Provenance.DocumentRevision] {
		t.Fatalf("intermediate revisions were not preserved: %#v", snapshots)
	}
	historical, _, err := fresh.loadHistoricalDocumentEntries(
		revision2[0].Provenance.DocumentID, revision2[0].Provenance.DocumentRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(historical) != len(revision2) || historical[0].Text != revision2[0].Text {
		t.Fatalf("revision 2 history contains stale revision 1 data: %#v", historical)
	}
}

func TestKnowledgeMutationsRejectEvidenceChangedByAnotherStore(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	chunks := validStructuredChunks()
	if err := storeA.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	anchor, err := EvidenceAnchorForEntry(storeA.GetBySourceFile(chunks[0].Provenance.SourcePath)[0], "chunk-0")
	if err != nil {
		t.Fatal(err)
	}
	draft := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "external-stale-review", Kind: KnowledgeNodeClaim, Label: "Stale draft",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}
	if err := storeA.UpsertKnowledgeGraph(draft); err != nil {
		t.Fatal(err)
	}

	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	changed := changedRevisionChunks(chunks, "external evidence revision", "externally changed evidence")
	if err := storeB.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatal(err)
	}

	if _, err := storeA.ApproveKnowledgeObject(KnowledgeObjectNode, draft.Nodes[0].ID); !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
		t.Fatalf("approval used stale process cache: %v", err)
	}
	if err := storeA.UpsertCurrentKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "external-stale-upsert", Kind: KnowledgeNodeClaim, Label: "Stale generated node",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
		t.Fatalf("current-evidence upsert used stale process cache: %v", err)
	}
	graph, err := storeA.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].ID != draft.Nodes[0].ID || graph.Nodes[0].Status != KnowledgeStatusDraft {
		t.Fatalf("rejected mutations changed graph: %#v", graph)
	}
}

func TestDuplicateMergeRejectsEvidenceChangedByAnotherStore(t *testing.T) {
	storeA, storeB, chunks, anchor := sharedStoresWithCurrentEvidence(t)
	nodes := []KnowledgeNode{
		{ID: "external-merge-target", Kind: KnowledgeNodeClaim, Label: "Canonical", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		{ID: "external-merge-source", Kind: KnowledgeNodeClaim, Label: "Duplicate", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
	}
	if err := storeA.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	sourceContent, err := KnowledgeNodeContentDigest(nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	targetContent, err := KnowledgeNodeContentDigest(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	sourceEvidence, err := KnowledgeEvidenceDigest(nodes[1].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	targetEvidence, err := KnowledgeEvidenceDigest(nodes[0].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	invalidateEvidenceFromSecondStore(t, storeA, storeB, chunks, anchor)

	_, err = storeA.MergeKnowledgeDuplicate(KnowledgeNodeMergeRequest{
		SourceID: nodes[1].ID, TargetID: nodes[0].ID, Reviewer: "reviewer",
		ExpectedSourceNodeDigest: sourceContent, ExpectedTargetNodeDigest: targetContent,
		ExpectedSourceEvidenceDigest: sourceEvidence, ExpectedTargetEvidenceDigest: targetEvidence,
		Similarity: 0.99, EmbeddingSpace: "sha256:test-space",
	})
	if !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
		t.Fatalf("duplicate merge used stale process cache: %v", err)
	}
	graph, err := storeA.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.ID == nodes[1].ID && node.Status != KnowledgeStatusDraft {
			t.Fatalf("rejected merge changed source status: %#v", node)
		}
	}
	if merges, err := storeA.ListKnowledgeNodeMerges(10); err != nil || len(merges) != 0 {
		t.Fatalf("rejected merge left audit rows: %#v err=%v", merges, err)
	}
}

func TestWorkspaceCreationRejectsEvidenceChangedByAnotherStore(t *testing.T) {
	storeA, storeB, chunks, anchor := sharedStoresWithCurrentEvidence(t)
	const parentID = "external-workspace-parent"
	if err := storeA.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: parentID, Kind: KnowledgeNodeClaim, Label: "Pinned parent", Status: KnowledgeStatusActive,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	parent := knowledgeReviewItemByID(t, storeA, KnowledgeObjectNode, parentID)
	invalidateEvidenceFromSecondStore(t, storeA, storeB, chunks, anchor)

	_, err := storeA.CreateKnowledgeWorkspaceNode(KnowledgeWorkspaceCreateRequest{
		ParentNodeID: parentID, Kind: KnowledgeNodeNote, Label: "Must not be stored", Author: "reviewer",
		ExpectedParentStatus: parent.Status, ExpectedParentContent: parent.ContentDigest,
		ExpectedEvidence: parent.EvidenceDigest,
	})
	if !errors.Is(err, ErrKnowledgeEvidenceNotCurrent) {
		t.Fatalf("workspace creation used stale process cache: %v", err)
	}
	if records, err := storeA.ListKnowledgeWorkspaceCreations(10); err != nil || len(records) != 0 {
		t.Fatalf("rejected workspace creation left audit rows: %#v err=%v", records, err)
	}
}

func TestMindMapAttachRejectsEvidenceChangedByAnotherStore(t *testing.T) {
	storeA, storeB, chunks, anchor := sharedStoresWithCurrentEvidence(t)
	doc, err := storeA.CreateClassicMindMap("External evidence", "")
	if err != nil {
		t.Fatal(err)
	}
	invalidateEvidenceFromSecondStore(t, storeA, storeB, chunks, anchor)

	if _, _, err := storeA.AttachClassicMindMapEvidence(doc.Map.ID, doc.Map.RootNodeID, anchor,
		doc.Map.Revision, "reviewer", "must reject stale"); err == nil || !strings.Contains(err.Error(), "current") {
		t.Fatalf("mind-map attachment used stale process cache: %v", err)
	}
	loaded, err := storeA.LoadClassicMindMap(doc.Map.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Map.Revision != doc.Map.Revision || len(loaded.Nodes) != 1 || len(loaded.Nodes[0].Sources) != 0 {
		t.Fatalf("rejected attachment changed the map: %#v", loaded)
	}
}

func TestSelectionSaveRejectsEvidenceChangedByAnotherStore(t *testing.T) {
	storeA, storeB, chunks, anchor := sharedStoresWithCurrentEvidence(t)
	const nodeID = "external-selection-node"
	if err := storeA.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: nodeID, Kind: KnowledgeNodeClaim, Label: "Selected claim", Status: KnowledgeStatusDraft,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{nodeID}}
	manifest, err := storeA.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := storeA.AnalyzeKnowledgeSelection(KnowledgeSelectionAnalysisRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Pause SaveKnowledgeSelectionAnalysis after both database-aware preflights
	// but before its write transaction. This deterministically exercises the
	// final transaction verifier instead of merely testing the preflight.
	originalEntropy := cryptorand.Reader
	gate := newGatedEntropyReader(originalEntropy)
	cryptorand.Reader = gate
	defer func() {
		gate.unblock()
		cryptorand.Reader = originalEntropy
	}()
	type saveResult struct {
		result KnowledgeSelectionAnalysisSaveResult
		err    error
	}
	saved := make(chan saveResult, 1)
	go func() {
		result, saveErr := storeA.SaveKnowledgeSelectionAnalysis(KnowledgeSelectionAnalysisSaveRequest{
			Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedAnalysisDigest: analysis.Digest,
			Label: "Must not be stored", Author: "reviewer",
		})
		saved <- saveResult{result: result, err: saveErr}
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("selection save did not reach the post-preflight ID boundary")
	}
	invalidateEvidenceFromSecondStore(t, storeA, storeB, chunks, anchor)
	gate.unblock()
	var save saveResult
	select {
	case save = <-saved:
	case <-time.After(5 * time.Second):
		t.Fatal("selection save did not finish after releasing the ID boundary")
	}
	cryptorand.Reader = originalEntropy
	err = save.err
	if !errors.Is(err, ErrKnowledgeSelectionNotCurrent) {
		t.Fatalf("selection save used stale process cache: %v", err)
	}
	if save.result.Node.ID != "" || len(save.result.Edges) != 0 {
		t.Fatalf("rejected selection save returned partial objects: %#v", save.result)
	}
	if reports, err := storeA.ListKnowledgeSelectionReports(10); err != nil || len(reports) != 0 {
		t.Fatalf("rejected selection save left audit rows: %#v err=%v", reports, err)
	}
}

func TestStaleStoreCannotOverwriteNewerFTSGeneration(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	entry, err := storeA.Add("oldftsuniquetoken", "old", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	if err := storeB.UpdateById(entry.ID, "newftsuniquetoken", "new", nil, []float32{1}); err != nil {
		t.Fatal(err)
	}
	if results, err := storeB.SearchWithOptions(SearchOptions{Query: "newftsuniquetoken", Backend: "test"}); err != nil || len(results) != 1 {
		t.Fatalf("newer store did not build current FTS: results=%#v err=%v", results, err)
	}

	// Store A still has the old process-local entry snapshot. A direct rebuild
	// used to overwrite the shared FTS table with that stale text after Store B
	// had already published the newer generation.
	storeA.mu.Lock()
	err = storeA.rebuildFTSLocked()
	storeA.mu.Unlock()
	if err != nil {
		t.Fatalf("stale-store FTS rebuild did not refresh under writer lock: %v", err)
	}
	entryGeneration, err := loadEntryCacheGeneration(storeB.db)
	if err != nil {
		t.Fatal(err)
	}
	ftsGeneration, err := loadEntryFTSGeneration(storeB.db)
	if err != nil {
		t.Fatal(err)
	}
	if ftsGeneration != entryGeneration {
		t.Fatalf("shared FTS generation=%d, entries generation=%d", ftsGeneration, entryGeneration)
	}
	if old, err := storeB.SearchWithOptions(SearchOptions{Query: "oldftsuniquetoken", Backend: "test"}); err != nil || len(old) != 0 {
		t.Fatalf("stale lexical token survived newer generation: results=%#v err=%v", old, err)
	}
	if current, err := storeB.SearchWithOptions(SearchOptions{Query: "newftsuniquetoken", Backend: "test"}); err != nil || len(current) != 1 || current[0].Text != "newftsuniquetoken" {
		t.Fatalf("current lexical token was lost: results=%#v err=%v", current, err)
	}
}

func TestFTSQueryRefreshesWhenExternalRebuildWinsBeforeQuery(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	entry, err := storeA.Add("beforequeryoldtoken", "old", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	storeA.mu.Lock()
	defer storeA.mu.Unlock()
	if err := storeA.refreshEntryCacheIfStaleUnlocked("FTS interleaving test"); err != nil {
		t.Fatal(err)
	}
	if err := storeA.rebuildFTSLocked(); err != nil {
		t.Fatal(err)
	}
	oldGeneration := storeA.entryGeneration

	// Store A has already refreshed and rebuilt generation G. Publish both the
	// entries and FTS for G+1 immediately before A's FTS SELECT. Without the
	// query-side snapshot check, A would combine B's lexical hit with A's old
	// process-local Entry value.
	if err := storeB.UpdateById(entry.ID, "beforequerynewtoken", "new", nil, []float32{1}); err != nil {
		t.Fatal(err)
	}
	if results, err := storeB.SearchWithOptions(SearchOptions{Query: "beforequerynewtoken", Backend: "test"}); err != nil || len(results) != 1 {
		t.Fatalf("external store did not publish generation G+1 FTS: results=%#v err=%v", results, err)
	}

	scores, err := storeA.lexicalScoresLocked("beforequerynewtoken")
	if err != nil {
		t.Fatalf("query after external FTS rebuild: %v", err)
	}
	if _, ok := scores[entry.ID]; !ok {
		t.Fatalf("query did not return the current entry: scores=%#v", scores)
	}
	if storeA.entryGeneration <= oldGeneration {
		t.Fatalf("Store A generation=%d, want newer than %d", storeA.entryGeneration, oldGeneration)
	}
	if len(storeA.entries) != 1 || storeA.entries[0].Text != "beforequerynewtoken" {
		t.Fatalf("FTS result would be paired with stale entry cache: %#v", storeA.entries)
	}
	if storeA.lexicalMode != lexicalFTS5 {
		t.Fatalf("ordinary generation race disabled FTS: mode=%q", storeA.lexicalMode)
	}
}

func TestEntryUpdateWriterPreventsProvenanceInterleave(t *testing.T) {
	storeA, storeB, chunks, _ := sharedStoresWithCurrentEvidence(t)
	entries := storeA.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if len(entries) == 0 {
		t.Fatal("source fixture has no entries")
	}
	current := entries[0]
	changed := changedRevisionChunks(chunks, "update interleave revision", "replacement text from store B")
	var interleaveErr error
	err := storeA.updateByIDWithBeforeWrite(current.ID, current.Text, "metadata from store A", current.Tags, nil, nil, func() {
		interleaveErr = storeB.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed)
	})
	if err != nil {
		t.Fatalf("serialized metadata update failed: %v", err)
	}
	if interleaveErr == nil {
		t.Fatal("second Store committed a document replacement inside the entry update writer transaction")
	}
	afterMetadata, err := storeA.GetByID(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterMetadata.Text != current.Text || afterMetadata.ChunkHash != ChunkContentHash(afterMetadata.Text) ||
		afterMetadata.DocumentRevision != current.DocumentRevision {
		t.Fatalf("metadata update broke source provenance: %#v", afterMetadata)
	}

	if err := storeB.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatalf("replacement did not succeed after the first writer committed: %v", err)
	}
	final, err := storeA.GetByID(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Text != changed[0].Text || final.DocumentRevision != changed[0].Provenance.DocumentRevision ||
		final.ChunkHash != ChunkContentHash(final.Text) {
		t.Fatalf("new document provenance was not preserved: %#v", final)
	}
}

func TestToggleImportantIsAtomicAcrossStores(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	entry, err := storeA.Add("toggle target", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	var second *Entry
	var interleaveErr error
	first, err := storeA.toggleImportantWithBeforeWrite(entry.ID, func() {
		second, interleaveErr = storeB.ToggleImportant(entry.ID)
	})
	if err != nil {
		t.Fatalf("first atomic toggle: %v", err)
	}
	if !first.Important {
		t.Fatalf("first toggle result=%#v, want important", first)
	}
	if interleaveErr != nil {
		second, err = storeB.ToggleImportant(entry.ID)
		if err != nil {
			t.Fatalf("second toggle after writer serialization: %v (interleave error: %v)", err, interleaveErr)
		}
	}
	if second == nil || second.Important {
		t.Fatalf("two successful toggles did not restore false: second=%#v interleaveErr=%v", second, interleaveErr)
	}
	final, err := storeA.GetByID(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Important {
		t.Fatalf("two Store toggles lost an update: %#v", final)
	}
}

type gatedEntropyReader struct {
	reader      io.Reader
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newGatedEntropyReader(reader io.Reader) *gatedEntropyReader {
	return &gatedEntropyReader{reader: reader, entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *gatedEntropyReader) Read(p []byte) (int, error) {
	r.enteredOnce.Do(func() { close(r.entered) })
	<-r.release
	return r.reader.Read(p)
}

func (r *gatedEntropyReader) unblock() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func sharedStoresWithCurrentEvidence(t *testing.T) (*Store, *Store, []DocumentChunk, EvidenceAnchor) {
	t.Helper()
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	chunks := validStructuredChunks()
	if err := storeA.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := storeA.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if len(entries) == 0 {
		t.Fatal("shared evidence fixture did not store document chunks")
	}
	anchor, err := EvidenceAnchorForEntry(entries[0], entries[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	return storeA, storeB, chunks, anchor
}

func invalidateEvidenceFromSecondStore(t *testing.T, storeA, storeB *Store, chunks []DocumentChunk, anchor EvidenceAnchor) {
	t.Helper()
	changed := changedRevisionChunks(chunks, "external mutator evidence revision", "externally changed evidence")
	if err := storeB.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatal(err)
	}
	storeA.mu.RLock()
	resolution := resolveEvidenceAnchorFromEntries(anchor, storeA.entries)
	storeA.mu.RUnlock()
	if resolution.State != EvidenceCurrent {
		t.Fatalf("test setup did not retain store A's stale cache: %#v", resolution)
	}
}
