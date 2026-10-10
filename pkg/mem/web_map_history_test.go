package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestWebMapHistoryUndoStackAndReplay(t *testing.T) {
	dir, s, id, root, input := webEditFixture(t)
	ctx := context.Background()
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || len(page.Items) != 1 || page.UndoID != 0 {
		t.Fatal(page, err)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, root, input); err != nil {
		t.Fatal(err)
	}
	input.Revision = 2
	input.RequestID = strings.Repeat("c", 32)
	child, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input)
	if err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.Revision != 3 || len(page.Items) != 3 || page.UndoID != page.Items[0].ID {
		t.Fatal(page, err)
	}
	undo := WebMapUndo{Revision: 3, ChangeID: page.UndoID, RequestID: strings.Repeat("d", 32)}
	older := undo
	older.ChangeID = page.Items[1].ID
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, older); !errors.Is(err, ErrWebMapUndoUnavailable) {
		t.Fatal("older", err)
	}
	result, err := UndoWebMapIn(ctx, dir, "alice", id, undo)
	if err != nil || result.Revision != 4 {
		t.Fatal(result, err)
	}
	doc, err := s.LoadClassicMindMap(id)
	if err != nil || len(doc.Nodes) != 1 || doc.Nodes[0].Label != "Edited" {
		t.Fatal(doc, err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || !page.Items[1].Reverted || page.Items[0].Reverts != undo.ChangeID {
		t.Fatal(page, err)
	}
	next := WebMapUndo{Revision: 4, ChangeID: page.UndoID, RequestID: strings.Repeat("e", 32)}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, next); err != nil {
		t.Fatal("second undo", err)
	}
	doc, err = s.LoadClassicMindMap(id)
	if err != nil || doc.Nodes[0].Label != "Root" || doc.Map.Revision != 5 {
		t.Fatal(doc, err)
	}
	result, err = UndoWebMapIn(ctx, dir, "alice", id, undo)
	if err != nil || !result.Replayed || result.Revision != 4 {
		t.Fatal("retry", result, err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "bob", id, undo); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("actor", err)
	}
	changed := undo
	changed.Revision = 5
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, changed); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("reuse", err)
	}
	addReplay, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input)
	if err != nil || !addReplay.Replayed || addReplay.NodeID != child.NodeID {
		t.Fatal("add replay", err)
	}
	doc, _, err = s.RedoClassicMindMapChange(id, 5, "cli", "")
	if err != nil || doc.Nodes[0].Label != "Edited" {
		t.Fatal("CLI redo", err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.UndoID != page.Items[0].ID {
		t.Fatal("redo stack", page, err)
	}
	if _, err = ListWebMapHistoryIn(ctx, dir, "Root", 0); !errors.Is(err, ErrClassicMindMapNotFound) {
		t.Fatal("alias", err)
	}
	if _, err = ListWebMapHistoryIn(ctx, dir, id, -1); !errors.Is(err, ErrWebMapHistoryInvalid) {
		t.Fatal("cursor", err)
	}
}

func TestWebMapHistoryPaginationNoSnapshots(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	for i := 0; i < 35; i++ {
		_, err := s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,comment,created) VALUES(?,?,?,'edit_node','private snapshot','private snapshot','','','private actor','C:/private/path','2026-09-09')`, id, i+1, i+2)
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || len(first.Items) != 30 || first.NextBefore == 0 {
		t.Fatal(first, err)
	}
	second, err := ListWebMapHistoryIn(ctx, dir, id, first.NextBefore)
	if err != nil || len(second.Items) != 6 || second.NextBefore != 0 || second.Items[0].ID >= first.NextBefore {
		t.Fatal(second, err)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "private") {
		t.Fatal("snapshot leak")
	}
}

func TestWebMapUndoRollbackBoundsAndConflict(t *testing.T) {
	for _, kind := range []string{"receipt", "snapshot", "history", "stale", "foreign", "quota"} {
		t.Run(kind, func(t *testing.T) {
			dir, s, id, root, input := webEditFixture(t)
			ctx := context.Background()
			if _, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input); err != nil {
				t.Fatal(err)
			}
			page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
			if err != nil {
				t.Fatal(err)
			}
			undo := WebMapUndo{Revision: 2, ChangeID: page.UndoID, RequestID: strings.Repeat("e", 32)}
			switch kind {
			case "receipt":
				_, err = s.db.Exec(`CREATE TABLE web_map_undos(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);CREATE TRIGGER fail_undo_receipt BEFORE INSERT ON web_map_undos BEGIN SELECT RAISE(ABORT,'receipt failure'); END`)
			case "snapshot":
				var inserted interface{ LastInsertId() (int64, error) }
				inserted, err = s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,created) SELECT map_id,base_revision,new_revision,action,?,after_json,before_digest,after_digest,actor,created FROM mind_map_changes WHERE id=?`, strings.Repeat("x", (8<<20)+1), undo.ChangeID)
				if err == nil {
					undo.ChangeID, err = inserted.LastInsertId()
				}
			case "history":
				_, err = s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,created) SELECT map_id,base_revision,new_revision,'create_map',?,after_json,before_digest,after_digest,actor,created FROM mind_map_changes WHERE id=?`, strings.Repeat("x", 64<<20), undo.ChangeID)
			case "stale":
				undo.Revision = 1
			case "foreign":
				undo.ChangeID = 999999
			case "quota":
				_, err = s.db.Exec(`CREATE TABLE web_map_undos(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL)`)
				if err == nil {
					_, err = s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO web_map_undos SELECT CAST(i AS TEXT),'quota',1,'' FROM n`, MaxWebMapMutationReceipts)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = UndoWebMapIn(ctx, dir, "alice", id, undo); err == nil {
				t.Fatal("unexpected success")
			}
			var revision, nodes, changes int
			s.db.QueryRow(`SELECT revision FROM mind_maps WHERE id=?`, id).Scan(&revision)
			s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=? AND deleted_at=''`, id).Scan(&nodes)
			s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_changes WHERE map_id=?`, id).Scan(&changes)
			expectedChanges := 2
			if kind == "snapshot" || kind == "history" {
				expectedChanges = 3
			}
			if revision != 2 || nodes != 2 || changes != expectedChanges {
				t.Fatal("partial undo", revision, nodes, changes)
			}
			if kind == "quota" {
				input.Revision = 2
				input.RequestID = strings.Repeat("f", 32)
				if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, root, input); !errors.Is(err, ErrWebMapEditLimit) {
					t.Fatal("shared quota", err)
				}
			}
		})
	}
}

func TestWebMapUndoConcurrentRetry(t *testing.T) {
	dir, _, id, root, input := webEditFixture(t)
	ctx := context.Background()
	if _, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	undo := WebMapUndo{Revision: 2, ChangeID: page.UndoID, RequestID: strings.Repeat("e", 32)}
	var results [2]WebMapNodeEdited
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = UndoWebMapIn(ctx, dir, "alice", id, undo) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].Replayed == results[1].Replayed {
		t.Fatal(results, errs)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.Revision != 3 || len(page.Items) != 3 || page.UndoID != 0 {
		t.Fatal(page, err)
	}
}
