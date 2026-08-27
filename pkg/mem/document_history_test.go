package mem

import (
	"strings"
	"testing"
)

func TestDocumentReplacementArchivesImmutableRevisionAndBuildsFullDiff(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldChunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(oldChunks[0].Provenance.SourcePath, oldChunks); err != nil {
		t.Fatal(err)
	}
	anchor, err := EvidenceAnchorForEntry(store.GetBySourceFile(oldChunks[0].Provenance.SourcePath)[0], "chunk-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "history-node", Kind: KnowledgeNodeClaim, Label: "Historical claim",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	newChunks := validStructuredChunks()
	newRevision := ChunkContentHash("new document revision")
	for i := range newChunks {
		newChunks[i].Provenance.DocumentRevision = newRevision
	}
	newChunks[0].Text = "changed first chunk"
	newChunks[0].Provenance.ChunkHash = ChunkContentHash(newChunks[0].Text)
	newChunks = append(newChunks, newChunks[len(newChunks)-1])
	last := len(newChunks) - 1
	newChunks[last].Text = "new final chunk"
	newChunks[last].ChunkIndex = last
	newChunks[last].TotalChunks = len(newChunks)
	newChunks[last].Provenance.BlockIndex++
	newChunks[last].Provenance.BlockChunkIndex = 0
	newChunks[last].Provenance.BlockTotalChunks = 1
	newChunks[last].Provenance.ChunkHash = ChunkContentHash(newChunks[last].Text)
	for i := range newChunks[:last] {
		newChunks[i].TotalChunks = len(newChunks)
	}
	if err := store.ReplaceDocumentChunks(newChunks[0].Provenance.SourcePath, newChunks); err != nil {
		t.Fatal(err)
	}

	snapshots, err := store.ListDocumentHistorySnapshots(oldChunks[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].DocumentRevision != oldChunks[0].Provenance.DocumentRevision ||
		snapshots[0].ChunkCount != len(oldChunks) || snapshots[0].GraphSnapshotID == "" {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}
	report, err := store.BuildCorpusRevisionDiff(oldChunks[0].Provenance.SourcePath, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Available || report.ChangedChunks != 1 || report.AddedChunks != 1 ||
		report.RemovedChunks != 0 || report.FromRevision != oldChunks[0].Provenance.DocumentRevision ||
		report.ToRevision != newRevision || len(report.Changes) != 2 {
		t.Fatalf("unexpected corpus diff: %#v", report)
	}
	if _, err := store.db.Exec(`UPDATE document_history_versions SET reason='tampered'`); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot update was not blocked: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM document_history_version_chunks`); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot chunk delete was not blocked: %v", err)
	}
}

func TestDocumentSnapshotFailureRollsBackReplacement(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldChunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(oldChunks[0].Provenance.SourcePath, oldChunks); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_history_insert BEFORE INSERT ON document_history_version_chunks
BEGIN SELECT RAISE(ABORT, 'planned snapshot failure'); END`); err != nil {
		t.Fatal(err)
	}
	newChunks := validStructuredChunks()
	newRevision := ChunkContentHash("replacement must roll back")
	for i := range newChunks {
		newChunks[i].Provenance.DocumentRevision = newRevision
	}
	if err := store.ReplaceDocumentChunks(newChunks[0].Provenance.SourcePath, newChunks); err == nil ||
		!strings.Contains(err.Error(), "planned snapshot failure") {
		t.Fatalf("expected planned snapshot failure, got %v", err)
	}
	current := store.GetBySourceFile(oldChunks[0].Provenance.SourcePath)
	if len(current) != len(oldChunks) || current[0].DocumentRevision != oldChunks[0].Provenance.DocumentRevision {
		t.Fatalf("failed replacement changed current document: %#v", current)
	}
	snapshots, err := store.ListDocumentHistorySnapshots(oldChunks[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("failed replacement left a partial snapshot: %#v", snapshots)
	}
}

func TestCorpusRevisionDiffRequiresHistory(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BuildCorpusRevisionDiff(chunks[0].Provenance.SourcePath, "", "current"); err == nil || !strings.Contains(err.Error(), ErrDocumentHistoryUnavailable.Error()) {
		t.Fatalf("expected missing history error, got %v", err)
	}
}

func TestDocumentReplacementAllowsRechunkForSameContentRevisionAndArchivesOldLayout(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	rechunked := []DocumentChunk{chunks[0]}
	rechunked[0].Text = "chunk-0 chunk-1"
	rechunked[0].Embedding = []float32{0, 1}
	rechunked[0].ChunkIndex = 0
	rechunked[0].TotalChunks = 1
	rechunked[0].Provenance.ChunkHash = ChunkContentHash(rechunked[0].Text)
	rechunked[0].Provenance.BlockChunkIndex = 0
	rechunked[0].Provenance.BlockTotalChunks = 1
	if err := store.ReplaceDocumentChunks(rechunked[0].Provenance.SourcePath, rechunked); err != nil {
		t.Fatalf("same-revision rechunk failed: %v", err)
	}
	current := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if len(current) != 1 || current[0].Text != rechunked[0].Text ||
		current[0].DocumentRevision != chunks[0].Provenance.DocumentRevision {
		t.Fatalf("rechunk did not become current: %#v", current)
	}
	snapshots, err := store.ListDocumentHistorySnapshots(chunks[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].DocumentRevision != chunks[0].Provenance.DocumentRevision ||
		snapshots[0].ChunkCount != len(chunks) {
		t.Fatalf("old chunk layout was not archived: %#v", snapshots)
	}
	report, err := store.BuildCorpusRevisionDiff(chunks[0].Provenance.SourcePath, "", "current")
	if err != nil {
		t.Fatal(err)
	}
	if report.FromRevision != report.ToRevision || report.ChangedChunks != 1 || report.RemovedChunks != 1 {
		t.Fatalf("unexpected same-revision rechunk diff: %#v", report)
	}
	if err := store.ReplaceDocumentChunks(rechunked[0].Provenance.SourcePath, rechunked); err != nil {
		t.Fatalf("idempotent rechunk import failed: %v", err)
	}
	snapshots, err = store.ListDocumentHistorySnapshots(chunks[0].Provenance.SourcePath)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("idempotent import created extra history: snapshots=%#v err=%v", snapshots, err)
	}
}

func TestSameRevisionHistoryKeepsEveryDistinctChunkLayoutBySnapshotID(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(original[0].Provenance.SourcePath, original); err != nil {
		t.Fatal(err)
	}
	combined := []DocumentChunk{original[0]}
	combined[0].Text = "chunk-0 chunk-1"
	combined[0].Embedding = []float32{0, 1}
	combined[0].ChunkIndex, combined[0].TotalChunks = 0, 1
	combined[0].Provenance.ChunkHash = ChunkContentHash(combined[0].Text)
	combined[0].Provenance.BlockChunkIndex, combined[0].Provenance.BlockTotalChunks = 0, 1
	if err := store.ReplaceDocumentChunks(combined[0].Provenance.SourcePath, combined); err != nil {
		t.Fatal(err)
	}
	three := make([]DocumentChunk, 3)
	for i, text := range []string{"chunk", "-0 ", "chunk-1"} {
		three[i] = original[0]
		three[i].Text, three[i].Embedding = text, []float32{float32(i + 1), 1}
		three[i].ChunkIndex, three[i].TotalChunks = i, len(three)
		three[i].Provenance.ChunkHash = ChunkContentHash(text)
		three[i].Provenance.BlockChunkIndex, three[i].Provenance.BlockTotalChunks = i, len(three)
	}
	if err := store.ReplaceDocumentChunks(three[0].Provenance.SourcePath, three); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.ListDocumentHistorySnapshots(original[0].Provenance.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].SnapshotID == snapshots[1].SnapshotID ||
		snapshots[0].DocumentRevision != snapshots[1].DocumentRevision {
		t.Fatalf("same-revision layouts collided: %#v", snapshots)
	}
	latest, _, err := store.loadHistoricalDocumentEntries(original[0].Provenance.DocumentID, snapshots[0].SnapshotID)
	if err != nil || len(latest) != 1 || latest[0].Text != combined[0].Text {
		t.Fatalf("latest snapshot selector returned wrong layout: entries=%#v err=%v", latest, err)
	}
	oldest, _, err := store.loadHistoricalDocumentEntries(original[0].Provenance.DocumentID, snapshots[1].SnapshotID)
	if err != nil || len(oldest) != len(original) || oldest[0].Text != original[0].Text {
		t.Fatalf("oldest snapshot selector returned wrong layout: entries=%#v err=%v", oldest, err)
	}
	byRevision, selected, err := store.loadHistoricalDocumentEntries(original[0].Provenance.DocumentID, original[0].Provenance.DocumentRevision)
	if err != nil || selected.SnapshotID != snapshots[0].SnapshotID || len(byRevision) != 1 {
		t.Fatalf("legacy revision selector did not choose newest snapshot: selected=%#v entries=%#v err=%v", selected, byRevision, err)
	}
}
