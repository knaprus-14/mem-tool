package mem

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func webEditFixture(t *testing.T) (string, *Store, string, string, WebMapNodeEdit) {
	t.Helper()
	dir, s, _ := queryFixture(t)
	created, err := CreateWebMapIn(context.Background(), dir, "alice", WebMapCreate{Title: "Root", RequestID: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.LoadClassicMindMap(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, created.ID, doc.Map.RootNodeID, WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("b", 32), Label: "Edited", Summary: "Description", Body: "Текст <script>alert(1)</script>"}
}
func TestWebMapNodeEditHistoryRetryAndUndo(t *testing.T) {
	dir, s, id, node, input := webEditFixture(t)
	ctx := context.Background()
	result, err := EditWebMapNodeIn(ctx, dir, "alice", id, node, input)
	if err != nil || result.Revision != 2 || result.Replayed {
		t.Fatalf("edit: %+v %v", result, err)
	}
	doc, err := s.LoadClassicMindMap(id)
	if err != nil || doc.Nodes[0].Label != input.Label || doc.Nodes[0].BodyMarkdown != input.Body || doc.Map.Title != "Root" {
		t.Fatal("shared edit mismatch", err)
	}
	history, err := s.ListClassicMindMapChanges(id, 10)
	if err != nil || len(history) != 2 || history[0].Action != "edit_node" || history[0].Actor != "alice" {
		t.Fatal("history mismatch", err)
	}
	again, err := EditWebMapNodeIn(ctx, dir, "alice", id, node, input)
	if err != nil || !again.Replayed || again.Revision != 2 {
		t.Fatal("retry", err)
	}
	changed := input
	changed.Label = "different"
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, node, changed); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("key reuse", err)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "bob", id, node, input); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("actor reuse", err)
	}
	undone, _, err := s.UndoClassicMindMapChange(id, history[0].ID, 2, "cli", "")
	if err != nil || undone.Nodes[0].Label != "Root" || undone.Map.Revision != 3 {
		t.Fatal("CLI undo", err)
	}
	again, err = EditWebMapNodeIn(ctx, dir, "alice", id, node, input)
	if err != nil || !again.Replayed {
		t.Fatal("replay after undo", err)
	}
	fresh, _ := s.LoadClassicMindMap(id)
	if fresh.Map.Revision != 3 || fresh.Nodes[0].Label != "Root" {
		t.Fatal("replay rewrote later change")
	}
	changed.RequestID = strings.Repeat("c", 32)
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, node, changed); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("stale revision", err)
	}
}
func TestWebMapNodeEditRejectsLockedForeignAndInvalid(t *testing.T) {
	dir, s, id, node, input := webEditFixture(t)
	ctx := context.Background()
	for _, bad := range []string{"Root", id[:8], "missing"} {
		if _, err := EditWebMapNodeIn(ctx, dir, "alice", bad, node, input); !errors.Is(err, ErrClassicMindMapNotFound) {
			t.Fatal("map alias", err)
		}
	}
	for _, bad := range []string{"Root", node[:8], "missing"} {
		if _, err := EditWebMapNodeIn(ctx, dir, "alice", id, bad, input); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
			t.Fatal("node alias", err)
		}
	}
	other, err := s.CreateClassicMindMap("Other", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, other.Map.RootNodeID, input); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
		t.Fatal("foreign node", err)
	}
	for _, change := range []func(*WebMapNodeEdit){func(i *WebMapNodeEdit) { i.Revision = 0 }, func(i *WebMapNodeEdit) { i.Label = " " }, func(i *WebMapNodeEdit) { i.Body = strings.Repeat("x", 65537) }, func(i *WebMapNodeEdit) { i.RequestID = "bad" }} {
		bad := input
		change(&bad)
		if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, node, bad); !errors.Is(err, ErrWebMapNodeInvalid) {
			t.Fatal("invalid", err)
		}
	}
	if _, err = s.db.Exec(`UPDATE mind_map_nodes SET locked=1 WHERE id=?`, node); err != nil {
		t.Fatal(err)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, node, input); !errors.Is(err, ErrClassicMindMapLocked) {
		t.Fatal("locked", err)
	}
	view, err := ReadWebMapIn(ctx, dir, id)
	if err != nil || !view.Nodes[0].Locked || view.Map.Revision != 1 {
		t.Fatal("lock read", err)
	}
}
func TestWebMapNodeEditRollsBackReceiptFailureAndGrowthLimit(t *testing.T) {
	for _, reason := range []string{"receipt", "growth"} {
		t.Run(reason, func(t *testing.T) {
			dir, s, id, node, input := webEditFixture(t)
			if reason == "receipt" {
				_, err := s.db.Exec(`CREATE TABLE web_map_node_edits(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL); CREATE TRIGGER reject_edit BEFORE INSERT ON web_map_node_edits BEGIN SELECT RAISE(ABORT,'fixture');END`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec(`UPDATE mind_maps SET description=? WHERE id=?`, strings.Repeat("x", (4<<20)-8192), id); err != nil {
					t.Fatal(err)
				}
				input.Body = strings.Repeat("y", 16384)
			}
			_, err := EditWebMapNodeIn(context.Background(), dir, "alice", id, node, input)
			if err == nil {
				t.Fatal("expected rollback")
			}
			if reason == "growth" && !errors.Is(err, ErrWebMapEditLimit) {
				t.Fatal(err)
			}
			var label string
			var revision, history int
			s.db.QueryRow(`SELECT label FROM mind_map_nodes WHERE id=?`, node).Scan(&label)
			s.db.QueryRow(`SELECT revision FROM mind_maps WHERE id=?`, id).Scan(&revision)
			s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_changes WHERE map_id=?`, id).Scan(&history)
			if label != "Root" || revision != 1 || history != 1 {
				t.Fatal("partial commit", label, revision, history)
			}
		})
	}
}
func TestWebMapNodeEditConcurrentWriters(t *testing.T) {
	dir, s, id, node, input := webEditFixture(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"b", "c"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			attempt := input
			attempt.RequestID = strings.Repeat(key, 32)
			_, err := EditWebMapNodeIn(context.Background(), dir, "alice", id, node, attempt)
			errs <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrClassicMindMapRevisionConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	doc, _ := s.LoadClassicMindMap(id)
	if doc.Map.Revision != 2 {
		t.Fatal("double edit")
	}
}
func TestWebMapNodeEditPreservesSourcesAndGeneratedMetadata(t *testing.T) {
	dir, s, id, node, input := webEditFixture(t)
	if _, err := s.db.Exec(`UPDATE mind_maps SET mode='generated' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE mind_map_nodes SET origin='generated',kind='fact',style_json='{"color":"green"}' WHERE id=?`, node); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO mind_map_node_sources(id,map_id,node_id,position,kind,title,locator,url,source_path,created) VALUES('private-source',?,?,0,'external_file','Original','private-file.pdf','https://private.invalid','private-file.pdf','now')`, id, node); err != nil {
		t.Fatal(err)
	}
	if _, err := EditWebMapNodeIn(context.Background(), dir, "alice", id, node, input); err != nil {
		t.Fatal(err)
	}
	var mode, origin, kind, style, locator string
	s.db.QueryRow(`SELECT mode FROM mind_maps WHERE id=?`, id).Scan(&mode)
	s.db.QueryRow(`SELECT origin,kind,style_json FROM mind_map_nodes WHERE id=?`, node).Scan(&origin, &kind, &style)
	s.db.QueryRow(`SELECT locator FROM mind_map_node_sources WHERE id='private-source'`).Scan(&locator)
	if mode != "hybrid" || origin != "generated" || kind != "fact" || style != `{"color":"green"}` || locator != "private-file.pdf" {
		t.Fatal("metadata changed", mode, origin, kind, style, locator)
	}
	view, err := ReadWebMapIn(context.Background(), dir, id)
	if err != nil || view.Nodes[0].SourceCount != 1 {
		t.Fatal("source count", err)
	}
}
