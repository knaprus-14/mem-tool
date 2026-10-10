package mem

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWebMapAIQueueShowsEarlierMapProgressWithoutPrivatePayload(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	in.RequestID, in.Title = strings.Repeat("a", 32), "Предыдущая <карта>"
	first, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID, in.Title = strings.Repeat("b", 32), "Новая карта"
	second, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID, in.Title = strings.Repeat("c", 32), "Foreign private title"
	if _, err := CreateWebMapFromSourcesIn(ctx, dir, "bob", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET status='running',phase='generate',current=14,total=24 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	items, err := ListWebMapAIIn(ctx, dir, "alice", second.MapID)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	q := items[0].Queue
	if q == nil || q.Position != 2 || q.Ahead != 1 || q.Pending != 2 || q.Head == nil || q.Head.MapID != first.MapID || q.Head.MapTitle != "Предыдущая <карта>" || q.Head.Status != "running" || q.Head.Current != 14 || q.Head.Total != 24 {
		t.Fatalf("queue: %+v", q)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Foreign private title", "private-ai-folder", "input_digest", "run_id", "config_revision", "entries", "source_path"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private field leaked: %s", secret)
		}
	}
	foreign, err := ListWebMapAIIn(ctx, dir, "bob", second.MapID)
	if err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
	for _, id := range []string{first.MapID, second.MapID} {
		view, err := ReadWebMapIn(ctx, dir, id)
		if err != nil || view.Map.Revision != 1 || len(view.Nodes) != 1 {
			t.Fatal("queue read changed map", view, err)
		}
	}
}

func TestWebMapAIQueueMatchesWorkerOrderAndPublicationStates(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	in.RequestID = strings.Repeat("a", 32)
	first, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = strings.Repeat("b", 32)
	second, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET created='2026-10-10T10:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status, failure string
		source, ahead   int
	}{
		{"queued", "", 1, 1}, {"running", "", 1, 1}, {"ready", "", 1, 1},
		{"ready", "publication failed", 1, 0}, {"ready", "", 0, 0},
		{"failed", "", 1, 0}, {"cancelled", "", 1, 0}, {"applied", "", 1, 0},
	} {
		if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET status=?,error=?,payload=json_set(payload,'$.source_overview',json(CASE WHEN ?=1 THEN 'true' ELSE 'false' END)) WHERE id=?`, tc.status, tc.failure, tc.source, first.ID); err != nil {
			t.Fatal(err)
		}
		items, err := ListWebMapAIIn(ctx, dir, "alice", second.MapID)
		if err != nil || len(items) != 1 || items[0].Queue == nil {
			t.Fatal(items, err)
		}
		q := items[0].Queue
		if q.Position != tc.ahead+1 || q.Ahead != tc.ahead || q.Pending != tc.ahead+1 || (q.Head == nil) != (tc.ahead == 0) {
			t.Fatalf("%+v: queue %+v", tc, q)
		}
	}
	if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET status='failed' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	items, err := ListWebMapAIIn(ctx, dir, "alice", second.MapID)
	if err != nil || items[0].Queue != nil {
		t.Fatal(items, err)
	}
}

func TestWebMapAIQueueContinuesAfterInterruptedEarlierMap(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	first, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = strings.Repeat("f", 32)
	second, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE web_map_ai_tasks SET status='running',phase='generate',current=14,total=24 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse, sourceMapResponse}}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if len(provider.requests) != 1 {
		t.Fatal("interrupted task without a saved run did not start once")
	}
	old, err := ListWebMapAIIn(ctx, dir, "alice", first.MapID)
	if err != nil || old[0].Status != "ready" {
		t.Fatal(old, err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
		t.Fatal("resumed map was not published", worked, err)
	}
	queued, err := ListWebMapAIIn(ctx, dir, "alice", second.MapID)
	if err != nil || queued[0].Queue == nil || queued[0].Queue.Position != 1 {
		t.Fatal(queued, err)
	}
	for i := 0; i < 2; i++ {
		if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
			t.Fatal(worked, err)
		}
	}
	view, err := ReadWebMapIn(ctx, dir, second.MapID)
	if err != nil || view.Map.Revision != 2 || len(view.Nodes) != 3 {
		t.Fatal(view, err)
	}
	oldView, err := ReadWebMapIn(ctx, dir, first.MapID)
	if err != nil || oldView.Map.Revision != 2 || len(oldView.Nodes) != 3 {
		t.Fatal(oldView, err)
	}
}
