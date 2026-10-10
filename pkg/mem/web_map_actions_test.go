package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebMapStructureActionsAndHistory(t *testing.T) {
	dir, s, id, root, add := webEditFixture(t)
	ctx := context.Background()
	a, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, add)
	if err != nil {
		t.Fatal(err)
	}
	add.Revision = 2
	add.RequestID = strings.Repeat("c", 32)
	add.Label = "Child"
	b, err := AddWebMapNodeIn(ctx, dir, "alice", id, a.NodeID, add)
	if err != nil {
		t.Fatal(err)
	}
	move := WebMapAction{Revision: 3, RequestID: strings.Repeat("d", 32), ParentID: root, Position: 0}
	moved, err := ActWebMapNodeIn(ctx, dir, "alice", id, b.NodeID, "move", move)
	if err != nil || moved.Revision != 4 {
		t.Fatal(moved, err)
	}
	doc, _ := s.LoadClassicMindMap(id)
	for _, n := range doc.Nodes {
		if n.ID == b.NodeID && (n.ParentID != root || n.Position != 0) {
			t.Fatal("move/order", n)
		}
	}
	replay, err := ActWebMapNodeIn(ctx, dir, "alice", id, b.NodeID, "move", move)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	if _, err = ActWebMapNodeIn(ctx, dir, "bob", id, b.NodeID, "move", move); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("actor", err)
	}
	h, _ := ListWebMapHistoryIn(ctx, dir, id, 0)
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 4, RequestID: strings.Repeat("e", 32), ChangeID: h.UndoID}); err != nil {
		t.Fatal("undo move", err)
	}
	remove := WebMapAction{Revision: 5, RequestID: strings.Repeat("f", 32), Mode: ClassicMindMapDeletePromoteChildren}
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, a.NodeID, "delete", remove); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.LoadClassicMindMap(id)
	if len(doc.Nodes) != 2 {
		t.Fatal("promote count", len(doc.Nodes))
	}
	for _, n := range doc.Nodes {
		if n.ID == b.NodeID && n.ParentID != root {
			t.Fatal("promote parent")
		}
	}
	h, _ = ListWebMapHistoryIn(ctx, dir, id, 0)
	if h.Items[0].NodeID != a.NodeID {
		t.Fatal("history target", h.Items[0])
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 6, RequestID: strings.Repeat("1", 32), ChangeID: h.UndoID}); err != nil {
		t.Fatal(err)
	}
	remove.Revision = 7
	remove.RequestID = strings.Repeat("2", 32)
	remove.Mode = ClassicMindMapDeleteBranch
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, a.NodeID, "delete", remove); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.LoadClassicMindMap(id)
	if len(doc.Nodes) != 1 {
		t.Fatal("branch not deleted")
	}
	h, _ = ListWebMapHistoryIn(ctx, dir, id, 0)
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 8, RequestID: strings.Repeat("3", 32), ChangeID: h.UndoID}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.LoadClassicMindMap(id)
	if len(doc.Nodes) != 3 {
		t.Fatal("branch not restored")
	}
}

func TestWebMapActionGuardsAndRollback(t *testing.T) {
	dir, s, id, root, add := webEditFixture(t)
	ctx := context.Background()
	child, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, add)
	if err != nil {
		t.Fatal(err)
	}
	base := WebMapAction{Revision: 2, RequestID: strings.Repeat("c", 32), ParentID: child.NodeID, Position: -1}
	for _, target := range []string{root, child.NodeID} {
		if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, target, "move", base); !errors.Is(err, ErrWebMapStructure) {
			t.Fatal("root/cycle", err)
		}
	}
	other, err := s.CreateClassicMindMap("Other", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{other.Map.RootNodeID, "Root", root[:8]} {
		in := base
		in.ParentID = parent
		if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, child.NodeID, "move", in); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
			t.Fatal("foreign/alias", err)
		}
	}
	lock := true
	in := WebMapAction{Revision: 2, RequestID: strings.Repeat("d", 32), Locked: &lock}
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, child.NodeID, "properties", in); err != nil {
		t.Fatal(err)
	}
	remove := WebMapAction{Revision: 3, RequestID: strings.Repeat("e", 32), Mode: ClassicMindMapDeleteBranch}
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, child.NodeID, "delete", remove); !errors.Is(err, ErrClassicMindMapLocked) {
		t.Fatal("locked", err)
	}
	lock = false
	kind := ClassicMindMapNodeTask
	in.Locked = &lock
	in.Kind = &kind
	in.RequestID = strings.Repeat("f", 32)
	in.Revision = 3
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, child.NodeID, "properties", in); err != nil {
		t.Fatal("unlock/type", err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_action BEFORE INSERT ON web_map_actions BEGIN SELECT RAISE(ABORT,'fixture');END`); err != nil {
		t.Fatal(err)
	}
	remove.Revision = 4
	if _, err = ActWebMapNodeIn(ctx, dir, "alice", id, child.NodeID, "delete", remove); err == nil {
		t.Fatal("receipt failure expected")
	}
	doc, _ := s.LoadClassicMindMap(id)
	if doc.Map.Revision != 4 || len(doc.Nodes) != 2 {
		t.Fatal("partial commit")
	}
	for _, n := range doc.Nodes {
		if n.ID == child.NodeID && (n.Locked || n.Kind != kind) {
			t.Fatal("properties mismatch")
		}
	}
}
