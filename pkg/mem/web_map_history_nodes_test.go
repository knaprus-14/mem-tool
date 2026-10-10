package mem

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWebMapHistoryNodeCurrentDeletedAndBounded(t *testing.T) {
	dir, s, id, root, input := webEditFixture(t)
	ctx := context.Background()
	added, err := AddWebMapNodeIn(ctx, dir, "alice", id, root, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Revision = 2
	input.RequestID = strings.Repeat("c", 32)
	input.Label = "Переименовано <script>"
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, added.NodeID, input); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
	for _, change := range page.Items[:2] {
		if change.NodeID != added.NodeID || change.NodeLabel != input.Label || !change.NodePresent {
			t.Fatal("current node", change)
		}
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 3, ChangeID: page.UndoID, RequestID: strings.Repeat("d", 32)}); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 4, ChangeID: page.UndoID, RequestID: strings.Repeat("e", 32)}); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.Revision != 5 || page.Items[0].NodeID != added.NodeID || page.Items[0].NodePresent || page.Items[0].NodeLabel != "Edited" {
		t.Fatal("deleted node", page, err)
	}
	// A legacy database may contain longer labels; history still returns at most 512 runes.
	if _, err = s.db.Exec(`UPDATE mind_map_nodes SET label=? WHERE id=?`, strings.Repeat("Ж", 900), added.NodeID); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || utf8.RuneCountInString(page.Items[0].NodeLabel) != 512 || page.Revision != 5 {
		t.Fatal("bound", page, err)
	}
}

func TestWebMapHistoryNodeDoesNotCrossMaps(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	other, err := CreateWebMapIn(ctx, dir, "alice", WebMapCreate{Title: "Private foreign node", RequestID: strings.Repeat("f", 32)})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.LoadClassicMindMap(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO mind_map_changes(map_id,base_revision,new_revision,action,target_node_id,before_json,after_json,before_digest,after_digest,actor,created) VALUES(?,1,2,'edit_node',?,'','','','','','')`, id, doc.Map.RootNodeID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || page.Items[0].NodeID != "" || page.Items[0].NodeLabel != "" || page.Items[0].NodePresent {
		t.Fatal("foreign node leak", page, err)
	}
}
