package mem

import (
	"errors"
	"strings"
	"testing"
)

func TestDocumentRestorePreviewApplyAndRollback(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldChunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(oldChunks[0].Provenance.SourcePath, oldChunks); err != nil {
		t.Fatal(err)
	}
	oldAnchor, err := EvidenceAnchorForEntry(store.GetBySourceFile(oldChunks[0].Provenance.SourcePath)[0], "chunk-0")
	if err != nil {
		t.Fatal(err)
	}
	oldGraph := KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "restore-old", Kind: KnowledgeNodeClaim, Label: "Old graph",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{oldAnchor},
	}}}
	if err := store.UpsertKnowledgeGraph(oldGraph); err != nil {
		t.Fatal(err)
	}

	newChunks := changedRevisionChunks(oldChunks, "restore new revision", "changed chunk-0")
	if err := store.ReplaceDocumentChunks(newChunks[0].Provenance.SourcePath, newChunks); err != nil {
		t.Fatal(err)
	}
	newAnchor, err := EvidenceAnchorForEntry(store.GetBySourceFile(newChunks[0].Provenance.SourcePath)[0], "changed chunk-0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "restore-new", Kind: KnowledgeNodeClaim, Label: "New graph",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{newAnchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.BuildDocumentRestorePlan(newChunks[0].Provenance.SourcePath, oldChunks[0].Provenance.DocumentRevision)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := store.BuildDocumentRestorePlan(newChunks[0].Provenance.SourcePath, oldChunks[0].Provenance.DocumentRevision)
	if err != nil || secondPlan.PlanDigest != plan.PlanDigest {
		t.Fatalf("restore preview is not deterministic: first=%#v second=%#v err=%v", plan, secondPlan, err)
	}
	if !plan.RequiresExplicitConfirmation || !validSHA256Digest(plan.PlanDigest) ||
		plan.CurrentRevision != newChunks[0].Provenance.DocumentRevision ||
		plan.TargetRevision != oldChunks[0].Provenance.DocumentRevision ||
		plan.CurrentGraphNodes != 2 || plan.TargetGraphNodes != 1 || plan.ChunkDiff.ChangedChunks != 1 {
		t.Fatalf("restore preview is incomplete: %#v", plan)
	}
	if _, err := store.db.Exec(`UPDATE entries SET important=1 WHERE source_file=? AND chunk_index=0`, plan.SourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDocumentRestore(plan.SourcePath, plan.TargetRevision, plan.PlanDigest); !errors.Is(err, ErrDocumentRestoreStateChanged) || !strings.Contains(err.Error(), "inside SQLite transaction") {
		t.Fatalf("external database change escaped transactional state pin: %v", err)
	}
	var important int
	if err := store.db.QueryRow(`SELECT important FROM entries WHERE source_file=? AND chunk_index=0`, plan.SourcePath).Scan(&important); err != nil || important != 1 {
		t.Fatalf("rejected transaction changed external update: important=%d err=%v", important, err)
	}
	if _, err := store.db.Exec(`UPDATE entries SET important=0 WHERE source_file=? AND chunk_index=0`, plan.SourcePath); err != nil {
		t.Fatal(err)
	}

	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "restore-concurrent", Kind: KnowledgeNodeNote, Label: "Concurrent change",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{newAnchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDocumentRestore(plan.SourcePath, plan.TargetRevision, plan.PlanDigest); !errors.Is(err, ErrDocumentRestoreStateChanged) {
		t.Fatalf("stale restore plan was accepted: %v", err)
	}
	if current := store.GetBySourceFile(plan.SourcePath); current[0].DocumentRevision != plan.CurrentRevision {
		t.Fatalf("rejected stale plan changed corpus: %#v", current)
	}

	plan, err = store.BuildDocumentRestorePlan(plan.SourcePath, plan.TargetRevision)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.ApplyDocumentRestore(plan.SourcePath, plan.TargetRevision, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if run.FromRevision != newChunks[0].Provenance.DocumentRevision || run.TargetRevision != oldChunks[0].Provenance.DocumentRevision ||
		run.BeforeGraphSnapshotID == "" || run.RestoredChunks != len(oldChunks) {
		t.Fatalf("unexpected restore run: %#v", run)
	}
	current := store.GetBySourceFile(plan.SourcePath)
	if len(current) != len(oldChunks) || current[0].Text != oldChunks[0].Text || current[0].DocumentRevision != oldChunks[0].Provenance.DocumentRevision {
		t.Fatalf("old corpus was not restored: %#v", current)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].ID != "restore-old" {
		t.Fatalf("old graph was not restored exactly: %#v err=%v", graph, err)
	}

	rollbackPlan, err := store.BuildDocumentRestoreRollbackPlan(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rollbackPlan.RollbackOf != run.ID || rollbackPlan.TargetRevision != run.FromRevision ||
		rollbackPlan.TargetGraphSnapshotID != run.BeforeGraphSnapshotID {
		t.Fatalf("rollback preview does not point to exact recovery point: %#v", rollbackPlan)
	}
	rollbackRun, err := store.ApplyDocumentRestoreRollback(run.ID, rollbackPlan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if rollbackRun.RollbackOf != run.ID || rollbackRun.TargetRevision != newChunks[0].Provenance.DocumentRevision {
		t.Fatalf("unexpected rollback run: %#v", rollbackRun)
	}
	current = store.GetBySourceFile(plan.SourcePath)
	if current[0].Text != newChunks[0].Text || current[0].DocumentRevision != newChunks[0].Provenance.DocumentRevision {
		t.Fatalf("rollback did not restore newer corpus: %#v", current)
	}
	graph, err = store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 3 {
		t.Fatalf("rollback did not restore exact newer graph: nodes=%d err=%v", len(graph.Nodes), err)
	}
	runs, err := store.ListDocumentRestoreRuns(10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("restore history is incomplete: %#v err=%v", runs, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_restore_runs SET restored_chunks=0`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("restore history update was not blocked: %v", err)
	}
}

func TestDocumentRestoreFailureIsAtomic(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldChunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(oldChunks[0].Provenance.SourcePath, oldChunks); err != nil {
		t.Fatal(err)
	}
	newChunks := changedRevisionChunks(oldChunks, "atomic restore new revision", "atomic changed chunk-0")
	if err := store.ReplaceDocumentChunks(newChunks[0].Provenance.SourcePath, newChunks); err != nil {
		t.Fatal(err)
	}
	plan, err := store.BuildDocumentRestorePlan(newChunks[0].Provenance.SourcePath, oldChunks[0].Provenance.DocumentRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_restore_run BEFORE INSERT ON knowledge_restore_runs
BEGIN SELECT RAISE(ABORT, 'planned restore failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDocumentRestore(plan.SourcePath, plan.TargetRevision, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "planned restore failure") {
		t.Fatalf("expected planned restore failure, got %v", err)
	}
	current := store.GetBySourceFile(plan.SourcePath)
	if current[0].DocumentRevision != newChunks[0].Provenance.DocumentRevision || current[0].Text != newChunks[0].Text {
		t.Fatalf("failed restore changed current corpus: %#v", current)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 0 {
		t.Fatalf("failed restore changed current graph: %#v err=%v", graph, err)
	}
	runs, err := store.ListDocumentRestoreRuns(10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("failed restore left history: %#v err=%v", runs, err)
	}
}

func changedRevisionChunks(source []DocumentChunk, revisionSeed, firstText string) []DocumentChunk {
	result := make([]DocumentChunk, len(source))
	copy(result, source)
	revision := ChunkContentHash(revisionSeed)
	for i := range result {
		result[i].Embedding = append([]float32(nil), source[i].Embedding...)
		result[i].Tags = append([]string(nil), source[i].Tags...)
		result[i].Provenance.Warnings = append([]string(nil), source[i].Provenance.Warnings...)
		result[i].Provenance.DocumentRevision = revision
	}
	result[0].Text = firstText
	result[0].Provenance.ChunkHash = ChunkContentHash(firstText)
	return result
}
