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
	if _, err := store.db.Exec(`UPDATE document_history_snapshots SET reason='tampered'`); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot update was not blocked: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM document_history_chunks`); err == nil ||
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
	if _, err := store.db.Exec(`CREATE TRIGGER fail_history_insert BEFORE INSERT ON document_history_chunks
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

func TestDocumentReplacementRejectsRevisionReuseWithDifferentContent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	changed := validStructuredChunks()
	changed[0].Text = "different text under reused revision"
	changed[0].Provenance.ChunkHash = ChunkContentHash(changed[0].Text)
	if err := store.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err == nil ||
		!strings.Contains(err.Error(), "reused for different chunk content") {
		t.Fatalf("expected reused revision rejection, got %v", err)
	}
	current := store.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if current[0].Text != chunks[0].Text {
		t.Fatalf("rejected revision reuse changed current text: %q", current[0].Text)
	}
}
