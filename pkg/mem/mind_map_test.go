package mem

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestClassicMindMapCRUDHistoryAndUndo(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	doc, err := store.CreateClassicMindMap("Пожарная безопасность", "Учебная карта")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Map.Revision != 1 || doc.Map.Mode != ClassicMindMapModeManual || len(doc.Nodes) != 1 || doc.Nodes[0].ID != doc.Map.RootNodeID {
		t.Fatalf("unexpected created map: %#v", doc)
	}
	doc, systems, err := store.AddClassicMindMapNode(doc.Map.Title, doc.Map.Title, "Системы", -1,
		ClassicMindMapNodeSubtopic, "Виды систем", "", doc.Map.Revision, "test", "first branch")
	if err != nil {
		t.Fatal(err)
	}
	doc, water, err := store.AddClassicMindMapNode(doc.Map.Title, systems.Label, "Водяные", -1,
		ClassicMindMapNodeFact, "", "Спринклерные и дренчерные", doc.Map.Revision, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	newLabel := "Водяные АУП"
	doc, water, err = store.EditClassicMindMapNode(doc.Map.Title, water.Label,
		ClassicMindMapNodePatch{Label: &newLabel}, doc.Map.Revision, "test", "rename")
	if err != nil {
		t.Fatal(err)
	}
	if water.Label != newLabel || doc.Map.Revision != 4 {
		t.Fatalf("unexpected edit: node=%#v map=%#v", water, doc.Map)
	}
	if _, _, err := store.MoveClassicMindMapNode(doc.Map.Title, systems.Label, water.Label, -1,
		doc.Map.Revision, "test", "cycle"); err == nil || !strings.Contains(err.Error(), "цикл") {
		t.Fatalf("cycle was not rejected: %v", err)
	}
	if loaded, err := store.LoadClassicMindMap(doc.Map.Title); err != nil || loaded.Map.Revision != doc.Map.Revision || len(loaded.Nodes) != 3 {
		t.Fatalf("failed mutation changed the map: doc=%#v err=%v", loaded, err)
	}

	doc, err = store.DeleteClassicMindMapNode(doc.Map.Title, systems.Label, ClassicMindMapDeletePromoteChildren,
		doc.Map.Revision, "test", "promote")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 2 || findClassicMindMapNode(t, doc, newLabel).ParentID != doc.Map.RootNodeID {
		t.Fatalf("children were not promoted: %#v", doc.Nodes)
	}
	doc, undo, err := store.UndoClassicMindMapChange(doc.Map.Title, 0, doc.Map.Revision, "test", "undo delete")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(undo.Action, "undo:") || len(doc.Nodes) != 3 || doc.Map.Revision != 6 {
		t.Fatalf("unexpected undo: change=%#v doc=%#v", undo, doc)
	}
	if findClassicMindMapNode(t, doc, newLabel).ParentID != findClassicMindMapNode(t, doc, systems.Label).ID {
		t.Fatalf("undo did not restore the tree: %#v", doc.Nodes)
	}
	doc, secondUndo, err := store.UndoClassicMindMapChange(doc.Map.Title, 0, doc.Map.Revision, "test", "undo rename")
	if err != nil {
		t.Fatal(err)
	}
	if secondUndo.Action != "undo:edit_node" || doc.Map.Revision != 7 {
		t.Fatalf("second undo did not advance history: change=%#v doc=%#v", secondUndo, doc.Map)
	}
	findClassicMindMapNode(t, doc, "Водяные")
	changes, err := store.ListClassicMindMapChanges(doc.Map.Title, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 7 || changes[0].RevertsChangeID == 0 || changes[len(changes)-1].Action != "create_map" {
		t.Fatalf("unexpected append-only history: %#v", changes)
	}
	if _, err := store.db.Exec(`UPDATE mind_map_changes SET comment='tampered'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("change history can be modified: %v", err)
	}
	if _, err := store.CreateClassicMindMapSnapshot(doc.Map.Title, "before UI", doc.Map.Revision); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.ListClassicMindMapSnapshots(doc.Map.Title, 10)
	if err != nil || len(snapshots) != 2 || snapshots[0].Reason != "before UI" {
		t.Fatalf("unexpected snapshots: %#v err=%v", snapshots, err)
	}
	if _, err := store.db.Exec(`UPDATE mind_map_snapshots SET reason='tampered'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot can be modified: %v", err)
	}
}

func TestClassicMindMapRevisionConflictAndAtomicGeneratedImport(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("Concurrency", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddClassicMindMapNode(doc.Map.Title, doc.Map.Title, "First", -1,
		ClassicMindMapNodeNote, "", "", doc.Map.Revision, "test", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddClassicMindMapNode(doc.Map.Title, doc.Map.Title, "Stale", -1,
		ClassicMindMapNodeNote, "", "", doc.Map.Revision, "test", ""); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatalf("stale revision was accepted: %v", err)
	}

	before, err := store.ListClassicMindMaps(true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ImportClassicMindMap(ClassicMindMapDraft{
		Title: "Broken generated map", Mode: ClassicMindMapModeGenerated,
		Nodes: []ClassicMindMapNodeDraft{
			{Ref: "root", Label: "Root", Origin: ClassicMindMapNodeGenerated},
			{Ref: "child", ParentRef: "missing", Label: "Child", Origin: ClassicMindMapNodeGenerated},
		},
		Generation: &ClassicMindMapGenerationDraft{Prompt: "Build it"},
	}, "model", "")
	if err == nil || !strings.Contains(err.Error(), "неизвестного родителя") {
		t.Fatalf("invalid generated map was accepted: %v", err)
	}
	after, err := store.ListClassicMindMaps(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed generated import published partial data: before=%d after=%d", len(before), len(after))
	}
	generated, err := store.ImportClassicMindMap(ClassicMindMapDraft{
		Title: "Generated", Mode: ClassicMindMapModeGenerated,
		Nodes: []ClassicMindMapNodeDraft{
			{Ref: "r", Label: "Generated", Origin: ClassicMindMapNodeGenerated},
			{Ref: "c", ParentRef: "r", Label: "Branch", Origin: ClassicMindMapNodeGenerated},
		},
		Generation: &ClassicMindMapGenerationDraft{Prompt: "Build a concise map", Model: "test-model"},
	}, "model", "validated result")
	if err != nil {
		t.Fatal(err)
	}
	if generated.Map.Mode != ClassicMindMapModeGenerated || len(generated.Nodes) != 2 {
		t.Fatalf("valid generated map was not imported: %#v", generated)
	}
	var runs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_runs WHERE map_id=? AND status='completed'`, generated.Map.ID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("generation run was not recorded atomically: count=%d err=%v", runs, err)
	}
}

func TestClassicMindMapMoveOrderingAndLockedBranchProtection(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc, err := store.CreateClassicMindMap("Order", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"A", "B", "C"} {
		doc, _, err = store.AddClassicMindMapNode(doc.Map.Title, doc.Map.Title, label, -1,
			ClassicMindMapNodeSubtopic, "", "", doc.Map.Revision, "test", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	doc, _, err = store.MoveClassicMindMapNode(doc.Map.Title, "C", doc.Map.Title, 0,
		doc.Map.Revision, "test", "first")
	if err != nil {
		t.Fatal(err)
	}
	var children []ClassicMindMapNode
	for _, node := range doc.Nodes {
		if node.ParentID == doc.Map.RootNodeID {
			children = append(children, node)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Position < children[j].Position })
	if len(children) != 3 || children[0].Label != "C" || children[1].Label != "A" || children[2].Label != "B" {
		t.Fatalf("move order is not deterministic: %#v", children)
	}
	doc, lockedChild, err := store.AddClassicMindMapNode(doc.Map.Title, "A", "Locked child", -1,
		ClassicMindMapNodeNote, "", "", doc.Map.Revision, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	doc, _, err = store.EditClassicMindMapNode(doc.Map.Title, lockedChild.Label,
		ClassicMindMapNodePatch{Locked: &locked}, doc.Map.Revision, "test", "lock")
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := doc.Map.Revision
	if _, err := store.DeleteClassicMindMapNode(doc.Map.Title, "A", ClassicMindMapDeleteBranch,
		doc.Map.Revision, "test", "blocked"); !errors.Is(err, ErrClassicMindMapLocked) {
		t.Fatalf("locked descendant was deleted: %v", err)
	}
	loaded, err := store.LoadClassicMindMap(doc.Map.Title)
	if err != nil || loaded.Map.Revision != beforeRevision || len(loaded.Nodes) != 5 {
		t.Fatalf("blocked deletion changed the map: doc=%#v err=%v", loaded, err)
	}
}

func TestClassicMindMapEvidenceTracksCurrentAndStaleRevisions(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.CreateClassicMindMap("Sources", "")
	if err != nil {
		t.Fatal(err)
	}
	doc, source, err := store.AttachClassicMindMapEvidence(doc.Map.Title, doc.Map.Title, anchor,
		doc.Map.Revision, "test", "exact source")
	if err != nil {
		t.Fatal(err)
	}
	if source.EvidenceState != EvidenceCurrent || doc.Nodes[0].Sources[0].EvidenceState != EvidenceCurrent {
		t.Fatalf("current source is not current: source=%#v doc=%#v", source, doc)
	}
	changed := validStructuredChunks()
	newRevision := ChunkContentHash("next source revision")
	for i := range changed {
		changed[i].Provenance.DocumentRevision = newRevision
	}
	changed[0].Text = "changed chunk zero"
	changed[0].Provenance.ChunkHash = ChunkContentHash(changed[0].Text)
	if err := store.ReplaceDocumentChunks(changed[0].Provenance.SourcePath, changed); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadClassicMindMap(doc.Map.Title)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Nodes[0].Sources[0].EvidenceState; got != EvidenceStale {
		t.Fatalf("source state=%q, want stale", got)
	}
	if _, _, err := store.AttachClassicMindMapEvidence(doc.Map.Title, doc.Map.Title, anchor,
		loaded.Map.Revision, "test", "stale"); err == nil || !strings.Contains(err.Error(), "current") {
		t.Fatalf("stale source was attached: %v", err)
	}
}

func TestClassicMindMapSchemaMigratesExistingStore(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := []string{"mind_maps", "mind_map_nodes", "mind_map_node_sources", "mind_map_changes", "mind_map_snapshots", "mind_map_generation_runs"}
	for _, table := range want {
		var found string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); err != nil || found != table {
			t.Errorf("table %s missing: found=%q err=%v", table, found, err)
		}
	}
}

func findClassicMindMapNode(t *testing.T, doc ClassicMindMapDocument, label string) ClassicMindMapNode {
	t.Helper()
	for _, node := range doc.Nodes {
		if node.Label == label {
			return node
		}
	}
	t.Fatalf("node %q not found in %#v", label, doc.Nodes)
	return ClassicMindMapNode{}
}
