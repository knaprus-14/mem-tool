package mem

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestWebMapNodeAddHistoryRetryAndUndo(t *testing.T) {
	dir, s, id, parent, input := webEditFixture(t)
	ctx := context.Background()
	first, err := AddWebMapNodeIn(ctx, dir, "alice", id, parent, input)
	if err != nil || first.NodeID == "" || first.Revision != 2 {
		t.Fatal("add", first, err)
	}
	doc, err := s.LoadClassicMindMap(id)
	if err != nil || len(doc.Nodes) != 2 {
		t.Fatal(err)
	}
	var child ClassicMindMapNode
	for _, n := range doc.Nodes {
		if n.ID == first.NodeID {
			child = n
		}
	}
	if child.ParentID != parent || child.Position != 0 || child.Kind != ClassicMindMapNodeSubtopic || child.Origin != ClassicMindMapNodeManual || child.BodyMarkdown != input.Body {
		t.Fatal("child metadata", child)
	}
	replay, err := AddWebMapNodeIn(ctx, dir, "alice", id, parent, input)
	if err != nil || !replay.Replayed || replay.NodeID != first.NodeID {
		t.Fatal("replay", replay, err)
	}
	changed := input
	changed.Label = "other"
	if _, err = AddWebMapNodeIn(ctx, dir, "alice", id, parent, changed); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("key reuse", err)
	}
	if _, err = AddWebMapNodeIn(ctx, dir, "bob", id, parent, input); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("actor", err)
	}
	next := input
	next.RequestID = strings.Repeat("c", 32)
	next.Revision = 2
	next.Label = "Second"
	second, err := AddWebMapNodeIn(ctx, dir, "alice", id, parent, next)
	if err != nil {
		t.Fatal(err)
	}
	var position int
	s.db.QueryRow(`SELECT position FROM mind_map_nodes WHERE id=?`, second.NodeID).Scan(&position)
	if position != 1 {
		t.Fatal("not appended")
	}
	history, err := s.ListClassicMindMapChanges(id, 10)
	if err != nil || len(history) != 3 || history[0].Action != "add_node" || history[0].TargetNodeID != second.NodeID {
		t.Fatal("history", err)
	}
	undone, _, err := s.UndoClassicMindMapChange(id, history[0].ID, 3, "cli", "")
	if err != nil || len(undone.Nodes) != 2 {
		t.Fatal("undo", err)
	}
	replay, err = AddWebMapNodeIn(ctx, dir, "alice", id, parent, next)
	if err != nil || !replay.Replayed || replay.NodeID != second.NodeID {
		t.Fatal("replay undone", err)
	}
	doc, _ = s.LoadClassicMindMap(id)
	if len(doc.Nodes) != 2 || doc.Map.Revision != 4 {
		t.Fatal("replay recreated removed child")
	}
	input.RequestID = strings.Repeat("d", 32)
	if _, err = AddWebMapNodeIn(ctx, dir, "alice", id, parent, input); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("stale", err)
	}
}
func TestWebMapNodeAddLockedForeignAndRollback(t *testing.T) {
	dir, s, id, parent, input := webEditFixture(t)
	ctx := context.Background()
	other, err := s.CreateClassicMindMap("Other", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{other.Map.RootNodeID, "Root", parent[:8]} {
		if _, err = AddWebMapNodeIn(ctx, dir, "alice", id, ref, input); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
			t.Fatal("foreign/alias", err)
		}
	}
	if _, err = s.db.Exec(`UPDATE mind_map_nodes SET locked=1 WHERE id=?`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err = AddWebMapNodeIn(ctx, dir, "alice", id, parent, input); !errors.Is(err, ErrClassicMindMapLocked) {
		t.Fatal("locked", err)
	}
	if _, err = s.db.Exec(`UPDATE mind_map_nodes SET locked=0 WHERE id=?`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TABLE web_map_node_adds(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL,node_id TEXT NOT NULL);CREATE TRIGGER reject_add BEFORE INSERT ON web_map_node_adds BEGIN SELECT RAISE(ABORT,'fixture');END`); err != nil {
		t.Fatal(err)
	}
	if _, err = AddWebMapNodeIn(ctx, dir, "alice", id, parent, input); err == nil {
		t.Fatal("receipt should fail")
	}
	doc, _ := s.LoadClassicMindMap(id)
	history, _ := s.ListClassicMindMapChanges(id, 10)
	if len(doc.Nodes) != 1 || doc.Map.Revision != 1 || len(history) != 1 {
		t.Fatal("partial add committed")
	}
}
func TestWebMapNodeAddBoundsAndSharedQuota(t *testing.T) {
	for _, limit := range []string{"nodes", "depth", "receipts"} {
		t.Run(limit, func(t *testing.T) {
			dir, s, id, parent, input := webEditFixture(t)
			target := parent
			switch limit {
			case "nodes":
				_, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<999) INSERT INTO mind_map_nodes(id,map_id,parent_id,position,label,kind,origin,created,updated) SELECT 'child-'||i,?,?,i-1,'Child','subtopic','manual','now','now' FROM n`, id, parent)
				if err != nil {
					t.Fatal(err)
				}
			case "depth":
				_, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<255) INSERT INTO mind_map_nodes(id,map_id,parent_id,position,label,kind,origin,created,updated) SELECT 'deep-'||i,?,CASE WHEN i=1 THEN ? ELSE 'deep-'||(i-1) END,0,'Deep','subtopic','manual','now','now' FROM n`, id, parent)
				if err != nil {
					t.Fatal(err)
				}
				target = "deep-255"
			case "receipts":
				_, err := s.db.Exec(`CREATE TABLE web_map_node_edits(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO web_map_node_edits SELECT 'receipt-'||i,'hash',1,'now' FROM n`, MaxWebMapMutationReceipts)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AddWebMapNodeIn(context.Background(), dir, "alice", id, target, input); !errors.Is(err, ErrWebMapEditLimit) {
				t.Fatal("limit", err)
			}
			var revision int
			s.db.QueryRow(`SELECT revision FROM mind_maps WHERE id=?`, id).Scan(&revision)
			if revision != 1 {
				t.Fatal("revision changed")
			}
		})
	}
}
func TestWebMapNodeAddConcurrentRetry(t *testing.T) {
	dir, s, id, parent, input := webEditFixture(t)
	start := make(chan struct{})
	out := make(chan WebMapNodeEdited, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := AddWebMapNodeIn(context.Background(), dir, "alice", id, parent, input)
			out <- r
			errs <- e
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	replays := 0
	for r := range out {
		ids[r.NodeID] = true
		if r.Replayed {
			replays++
		}
	}
	doc, _ := s.LoadClassicMindMap(id)
	if len(ids) != 1 || replays != 1 || len(doc.Nodes) != 2 || doc.Map.Revision != 2 {
		t.Fatal("duplicate child", ids, replays, len(doc.Nodes))
	}
}
