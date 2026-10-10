package mem

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func webRedoFixture(t *testing.T) (string, *Store, string, string, WebMapUndo) {
	t.Helper()
	dir, s, id, root, input := webEditFixture(t)
	ctx := context.Background()
	if _, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 2, ChangeID: page.UndoID, RequestID: strings.Repeat("c", 32)}); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.RedoID == 0 {
		t.Fatal(page, err)
	}
	return dir, s, id, root, WebMapUndo{Revision: 3, ChangeID: page.RedoID, RequestID: strings.Repeat("d", 32)}
}

func TestWebMapRedoCycleAndReplay(t *testing.T) {
	dir, s, id, _, input := webRedoFixture(t)
	ctx := context.Background()
	result, err := RedoWebMapIn(ctx, dir, "alice", id, input)
	if err != nil || result.Revision != 4 || result.Replayed {
		t.Fatal(result, err)
	}
	doc, err := s.LoadClassicMindMap(id)
	if err != nil || len(doc.Nodes) != 2 {
		t.Fatal(doc, err)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.RedoID != 0 || page.UndoID != page.Items[0].ID || page.Items[0].Action != "redo:add_node" || page.Items[0].Reverts != input.ChangeID || !page.Items[1].Reverted {
		t.Fatal(page, err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 4, ChangeID: page.UndoID, RequestID: strings.Repeat("e", 32)}); err != nil {
		t.Fatal("undo redo", err)
	}
	result, err = RedoWebMapIn(ctx, dir, "alice", id, input)
	if err != nil || !result.Replayed || result.Revision != 4 {
		t.Fatal("old replay", result, err)
	}
	doc, err = s.LoadClassicMindMap(id)
	if err != nil || doc.Map.Revision != 5 || len(doc.Nodes) != 1 {
		t.Fatal("replay restored child", doc, err)
	}
	if _, err = RedoWebMapIn(ctx, dir, "bob", id, input); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("actor", err)
	}
	changed := input
	changed.Revision = 5
	if _, err = RedoWebMapIn(ctx, dir, "alice", id, changed); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("reuse", err)
	}
	doc, _, err = s.RedoClassicMindMapChange(id, 5, "cli", "")
	if err != nil || len(doc.Nodes) != 2 || doc.Map.Revision != 6 {
		t.Fatal("CLI redo", doc, err)
	}
}

func TestWebMapRedoRejectsConflictAndRollsBack(t *testing.T) {
	for _, kind := range []string{"stale", "wrong_record", "new_edit", "receipt", "original_limit", "quota"} {
		t.Run(kind, func(t *testing.T) {
			dir, s, id, root, input := webRedoFixture(t)
			ctx := context.Background()
			wantRevision := 3
			wantChanges := 3
			var setupErr error
			switch kind {
			case "stale":
				input.Revision = 2
			case "wrong_record":
				input.ChangeID = 1
			case "new_edit":
				edit := WebMapNodeEdit{Revision: 3, RequestID: strings.Repeat("f", 32), Label: "New root"}
				_, setupErr = EditWebMapNodeIn(ctx, dir, "alice", id, root, edit)
				input.Revision = 4
				wantRevision = 4
				wantChanges = 4
			case "receipt":
				_, setupErr = s.db.Exec(`CREATE TABLE web_map_redos(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);CREATE TRIGGER fail_redo_receipt BEFORE INSERT ON web_map_redos BEGIN SELECT RAISE(ABORT,'receipt failure');END`)
			case "original_limit":
				// Artificial append-only history points to an oversized original snapshot.
				original, e := s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,created) SELECT map_id,base_revision,new_revision,'add_node',before_json,?,before_digest,after_digest,actor,created FROM mind_map_changes WHERE id=?`, strings.Repeat("x", (8<<20)+1), input.ChangeID)
				if e != nil {
					t.Fatal(e)
				}
				originalID, e := original.LastInsertId()
				if e != nil {
					t.Fatal(e)
				}
				latest, e := s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,created,reverts_change_id) SELECT map_id,base_revision,new_revision,action,before_json,after_json,before_digest,after_digest,actor,created,? FROM mind_map_changes WHERE id=?`, originalID, input.ChangeID)
				if e != nil {
					t.Fatal(e)
				}
				input.ChangeID, setupErr = latest.LastInsertId()
				wantChanges = 5
			case "quota":
				_, setupErr = s.db.Exec(`CREATE TABLE web_map_redos(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);
WITH RECURSIVE numbers(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM numbers WHERE x<?) INSERT INTO web_map_redos SELECT CAST(x AS TEXT),'quota',1,'' FROM numbers`, MaxWebMapMutationReceipts)
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			_, err := RedoWebMapIn(ctx, dir, "alice", id, input)
			if err == nil {
				t.Fatal("unexpected success")
			}
			if (kind == "stale" || kind == "new_edit") && !errors.Is(err, ErrClassicMindMapRevisionConflict) {
				t.Fatal("conflict", err)
			}
			if kind == "new_edit" && !errors.Is(err, ErrWebMapRedoDiverged) {
				t.Fatal("missing actionable conflict", err)
			}
			if (kind == "quota" || kind == "original_limit") && !errors.Is(err, ErrWebMapEditLimit) {
				t.Fatal("limit", err)
			}
			doc, err := s.LoadClassicMindMap(id)
			if err != nil || len(doc.Nodes) != 1 || doc.Map.Revision != int64(wantRevision) {
				t.Fatal("partial restore", doc, err)
			}
			var changes int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_changes WHERE map_id=?`, id).Scan(&changes); err != nil || changes != wantChanges {
				t.Fatal("partial history", changes, err)
			}
			if kind == "quota" {
				_, err = EditWebMapNodeIn(ctx, dir, "alice", id, root, WebMapNodeEdit{Revision: 3, RequestID: strings.Repeat("f", 32), Label: "Root"})
				if !errors.Is(err, ErrWebMapEditLimit) {
					t.Fatal("shared quota", err)
				}
			}
		})
	}
}

func TestWebMapRedoConcurrentRetry(t *testing.T) {
	dir, _, id, _, input := webRedoFixture(t)
	ctx := context.Background()
	var results [2]WebMapNodeEdited
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = RedoWebMapIn(ctx, dir, "alice", id, input) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].Replayed == results[1].Replayed {
		t.Fatal(results, errs)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.Revision != 4 || len(page.Items) != 4 || page.RedoID != 0 {
		t.Fatal(page, err)
	}
}
