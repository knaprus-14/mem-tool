package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWebMapDeletePreservesDocumentsHistoryAndRetry(t *testing.T) {
	ctx := context.Background()
	dir, s, id, _, _ := webAIFixture(t)
	var entries, history, nodes int
	s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&entries)
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_changes WHERE map_id=?`, id).Scan(&history)
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=?`, id).Scan(&nodes)
	other, e := CreateWebMapIn(ctx, dir, "alice", WebMapCreate{Title: "Keep", RequestID: strings.Repeat("c", 32)})
	if e != nil {
		t.Fatal(e)
	}
	in := WebMapDelete{Revision: 1, RequestID: strings.Repeat("d", 32)}
	wrong := in
	wrong.Revision = 2
	if _, e = DeleteWebMapIn(ctx, dir, "alice", id, wrong); !errors.Is(e, ErrClassicMindMapRevisionConflict) {
		t.Fatal("revision", e)
	}
	result, e := DeleteWebMapIn(ctx, dir, "alice", id, in)
	if e != nil || result.Revision != 2 {
		t.Fatal(result, e)
	}
	if _, e = ReadWebMapIn(ctx, dir, id); !errors.Is(e, ErrClassicMindMapNotFound) {
		t.Fatal("deleted map visible", e)
	}
	if _, e = s.LoadClassicMindMap(id); !errors.Is(e, ErrClassicMindMapNotFound) {
		t.Fatal("CLI still sees deleted map", e)
	}
	for _, archived := range []bool{false, true} {
		page, e := ListWebMapsIn(ctx, dir, 0, archived)
		if e != nil || page.Total != 1 || page.Items[0].ID != other.ID {
			t.Fatal(page, e)
		}
	}
	for _, check := range []struct {
		query string
		args  []any
		want  int
	}{{`SELECT COUNT(*) FROM entries`, nil, entries}, {`SELECT COUNT(*) FROM mind_map_changes WHERE map_id=?`, []any{id}, history}, {`SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=?`, []any{id}, nodes}} {
		var got int
		if e = s.db.QueryRow(check.query, check.args...).Scan(&got); e != nil || got != check.want {
			t.Fatal(check.query, got, e)
		}
	}
	replay, e := DeleteWebMapIn(ctx, dir, "alice", id, in)
	if e != nil || !replay.Replayed || replay.Revision != 2 {
		t.Fatal(replay, e)
	}
	if _, e = DeleteWebMapIn(ctx, dir, "bob", id, in); !errors.Is(e, ErrWebMapEditRequest) {
		t.Fatal("actor-bound receipt", e)
	}
	if _, e = DeleteWebMapIn(ctx, dir, "alice", other.ID, in); !errors.Is(e, ErrWebMapEditRequest) {
		t.Fatal("map-bound receipt", e)
	}
}

func TestWebMapDeleteCancelsGeneration(t *testing.T) {
	for _, mode := range []string{"queued", "running", "ready"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir, s, source := sourceMapFixture(t)
			task, e := CreateWebMapFromSourcesIn(ctx, dir, "alice", source)
			if e != nil {
				t.Fatal(e)
			}
			remove := func() {
				if _, e := DeleteWebMapIn(ctx, dir, "alice", task.MapID, WebMapDelete{Revision: 1, RequestID: strings.Repeat("a", 32)}); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "running" {
				provider := webBranchProviderFunc(func(call context.Context, _ AnswerRequest) (string, error) {
					remove()
					select {
					case <-call.Done():
						return "", call.Err()
					case <-time.After(3 * time.Second):
						t.Fatal("deleted map did not cancel provider")
						return "", nil
					}
				})
				if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
					t.Fatal(worked, e)
				}
			} else {
				if mode == "ready" {
					if _, e := processWebMapAIIn(ctx, dir, "alice", &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse}}); e != nil {
						t.Fatal(e)
					}
				}
				remove()
			}
			var status string
			if e = s.db.QueryRow(`SELECT status FROM web_map_ai_tasks WHERE id=?`, task.ID).Scan(&status); e != nil || status != "cancelled" {
				t.Fatal(status, e)
			}
			provider := &classicMindMapAIFakeProvider{}
			if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); worked || e != nil || len(provider.requests) != 0 {
				t.Fatal("deleted work resumed", worked, e)
			}
			if _, e = ReadWebMapIn(ctx, dir, task.MapID); !errors.Is(e, ErrClassicMindMapNotFound) {
				t.Fatal(e)
			}
		})
	}
}

func TestWebMapDeleteRollback(t *testing.T) {
	ctx := context.Background()
	dir, s, source := sourceMapFixture(t)
	task, e := CreateWebMapFromSourcesIn(ctx, dir, "alice", source)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`CREATE TABLE web_map_deletions(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL UNIQUE,revision INTEGER NOT NULL,created TEXT NOT NULL);
 CREATE TRIGGER reject_deletion BEFORE INSERT ON web_map_deletions BEGIN SELECT RAISE(ABORT,'fixture receipt failure');END;`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = DeleteWebMapIn(ctx, dir, "alice", task.MapID, WebMapDelete{Revision: 1, RequestID: strings.Repeat("a", 32)}); e == nil {
		t.Fatal("missing failure")
	}
	view, e := ReadWebMapIn(ctx, dir, task.MapID)
	if e != nil || view.Map.Revision != 1 {
		t.Fatal("map deletion not rolled back", view, e)
	}
	var status string
	s.db.QueryRow(`SELECT status FROM web_map_ai_tasks WHERE id=?`, task.ID).Scan(&status)
	if status != "queued" {
		t.Fatal("cancellation not rolled back", status)
	}
}

func TestWebMapDeleteWorksWithoutRenderableTree(t *testing.T) {
	ctx := context.Background()
	dir, s, id, root, _ := webAIFixture(t)
	if _, e := s.db.Exec(`UPDATE mind_map_nodes SET parent_id=id WHERE id=?`, root); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadWebMapIn(ctx, dir, id); e == nil {
		t.Fatal("fixture should not render")
	}
	if _, e := DeleteWebMapIn(ctx, dir, "alice", id, WebMapDelete{Revision: 1, RequestID: strings.Repeat("a", 32)}); e != nil {
		t.Fatal(e)
	}
}
