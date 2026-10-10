package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func webMapViewTestFixture(t *testing.T) (string, *Store, ClassicMindMapDocument, map[string]string) {
	t.Helper()
	dir, store, _ := queryFixture(t)
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{Title: "Представление 😀", Mode: ClassicMindMapModeManual, Status: ClassicMindMapStatusDraft, Nodes: []ClassicMindMapNodeDraft{
		{Ref: "root", Label: "Root", Sources: []ClassicMindMapSource{{Kind: ClassicMindMapSourceURL, URL: "https://example.org/source"}}},
		{Ref: "branch", ParentRef: "root", Label: "Branch", BodyMarkdown: "Исходный текст 😀"},
		{Ref: "leaf", ParentRef: "branch", Label: "Leaf"}, {Ref: "sibling", ParentRef: "root", Label: "Sibling"},
	}}, "alice", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, node := range doc.Nodes {
		ids[node.Label] = node.ID
	}
	return dir, store, doc, ids
}

func webMapViewTestInput(ids map[string]string) WebMapViewStateInput {
	return WebMapViewStateInput{MapRevision: 1, RequestID: strings.Repeat("a", 32), State: WebMapViewState{DisplayMode: "table", Layout: "bilateral", SelectedNodeID: ids["Leaf"], Collapsed: []string{ids["Root"], ids["Branch"]}, Scale: 0.25, ScrollX: 0.4, ScrollY: 0.75, TableSort: "label", TableDescending: true, TableQuery: "давление 😀", TablePage: 2}}
}

func TestWebMapViewStateDefaultsReadOnlyAndActorIsolation(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	read, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Revision != 0 || read.MapRevision != 1 || read.CurrentMapRevision != 1 || read.Changed || read.Replayed || read.State.DisplayMode != "diagram" || read.State.Layout != "right" || read.State.SelectedNodeID != ids["Root"] || read.State.Scale != 1 || read.State.TableSort != "tree" || read.State.TablePage != 0 || read.State.TableQuery != "" || read.State.Collapsed == nil || len(read.State.Collapsed) != 0 {
		t.Fatal(read, err)
	}
	var count int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'web_map_view_%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read migrated schema", count, err)
	}
	in := webMapViewTestInput(ids)
	saved, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil)
	if err != nil || saved.Revision != 1 || !reflect.DeepEqual(saved.State, in.State) {
		t.Fatal(saved, err)
	}
	// Independent calls reopen SQLite, proving persistence rather than cache.
	read, err = ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || !reflect.DeepEqual(read, saved) {
		t.Fatal(read, err)
	}
	other, err := ReadWebMapViewStateIn(ctx, dir, "bob", doc.Map.ID)
	if err != nil || other.Revision != 0 || other.State.TableQuery != "" {
		t.Fatal("actor leak", other, err)
	}
	otherIn := in
	otherIn.State = in.State
	otherIn.State.DisplayMode = "outline"
	otherIn.State.TableQuery = "Личное Bob"
	otherIn.State.Collapsed = []string{}
	if _, err = SaveWebMapViewStateIn(ctx, dir, "bob", doc.Map.ID, otherIn, nil); err != nil {
		t.Fatal(err)
	}
	read, err = ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || !reflect.DeepEqual(read, saved) {
		t.Fatal("other actor overwrote view", read, err)
	}
	rows, err := store.db.Query(`SELECT actor_key FROM web_map_view_states`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil || !validSHA256Digest(key) || strings.Contains(key, "alice") || strings.Contains(key, "bob") {
			t.Fatal(key, err)
		}
	}
	after, err := store.LoadClassicMindMap(doc.Map.ID)
	if err != nil || after.Digest != doc.Digest || after.Map.Revision != doc.Map.Revision || !reflect.DeepEqual(after.Nodes, doc.Nodes) {
		t.Fatal("view changed map/source", after, err)
	}
	history, err := store.ListClassicMindMapChanges(doc.Map.ID, 10)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	snapshots, err := store.ListClassicMindMapSnapshots(doc.Map.ID, 10)
	if err != nil || len(snapshots) != 1 {
		t.Fatal(snapshots, err)
	}
}

func TestWebMapViewStatePrunesWithoutWritingAndReplaysExactResult(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	in := webMapViewTestInput(ids)
	saved, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.DeleteClassicMindMapNode(doc.Map.ID, ids["Leaf"], ClassicMindMapDeleteBranch, 1, "alice", "delete selected")
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Revision != 1 || read.MapRevision != 1 || read.CurrentMapRevision != updated.Map.Revision || !read.Changed || read.State.SelectedNodeID != ids["Root"] || !reflect.DeepEqual(read.State.Collapsed, []string{ids["Root"]}) || read.State.TableQuery != in.State.TableQuery {
		t.Fatal(read, err)
	}
	var payload string
	if err = store.db.QueryRow(`SELECT payload FROM web_map_view_states`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored WebMapViewStateResult
	if err = json.Unmarshal([]byte(payload), &stored); err != nil || !reflect.DeepEqual(stored, saved) {
		t.Fatal("GET wrote pruned view", stored, err)
	}
	replay, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, func() error { t.Fatal("replay admission"); return nil })
	replay.Replayed = false
	if err != nil || !reflect.DeepEqual(replay, saved) {
		t.Fatal("receipt mutated with map", replay, err)
	}
	stale := in
	stale.RequestID = strings.Repeat("b", 32)
	stale.Revision = 1
	if _, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, stale, nil); !errors.Is(err, ErrWebMapViewStateStale) {
		t.Fatal("stale pin", err)
	}
	stale.MapRevision = updated.Map.Revision
	stale.State = read.State
	current, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, stale, nil)
	if err != nil || current.Revision != 2 || current.Changed {
		t.Fatal(current, err)
	}
	replay, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil)
	replay.Replayed = false
	if err != nil || !reflect.DeepEqual(replay, saved) {
		t.Fatal("late replay not exact", replay, err)
	}
	read, err = ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || !reflect.DeepEqual(read, current) {
		t.Fatal("retry overwrote current view", read, err)
	}
	if _, err = store.db.Exec(`UPDATE mind_maps SET deleted_at='deleted' WHERE id=?`, doc.Map.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID); !errors.Is(err, ErrWebMapViewStateNotFound) {
		t.Fatal("deleted GET", err)
	}
	if retry, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil); err != nil || !retry.Replayed {
		t.Fatal("deleted map receipt recovery", retry, err)
	}
}

func TestWebMapViewStateCASAdmissionConflictAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	in := webMapViewTestInput(ids)
	denied := errors.New("capacity")
	if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='web_map_view_states'`).Scan(&count)
	if count != 0 {
		t.Fatal("denial committed schema")
	}
	saved, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, func() error { t.Fatal("quota blocks replay"); return denied }); err != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	changed := in
	changed.State.Scale = 0.5
	if _, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, changed, nil); !errors.Is(err, ErrWebMapViewStateRequest) {
		t.Fatal(err)
	}
	changed = in
	changed.RequestID = strings.Repeat("b", 32)
	if _, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, changed, func() error { t.Fatal("admission before CAS"); return nil }); !errors.Is(err, ErrWebMapViewStateConflict) {
		t.Fatal(err)
	}
	changed.Revision = 1
	if _, err = store.db.Exec(`CREATE TRIGGER reject_view_receipt BEFORE INSERT ON web_map_view_state_receipts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, changed, nil); err == nil {
		t.Fatal("expected receipt failure")
	}
	read, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || !reflect.DeepEqual(read, saved) {
		t.Fatal("failed transaction changed view", read, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER reject_view_receipt`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			attempt := changed
			attempt.RequestID = fmt.Sprintf("%032x", i+99)
			_, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, attempt, nil)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	passed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ErrWebMapViewStateConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflicts != 1 {
		t.Fatal(passed, conflicts)
	}
	read, err = ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Revision != 2 {
		t.Fatal(read, err)
	}
}

func TestWebMapViewStateBoundsInvalidIDsAndCancellation(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	base := webMapViewTestInput(ids)
	changes := []func(*WebMapViewStateInput){
		func(in *WebMapViewStateInput) { in.Revision = -1 }, func(in *WebMapViewStateInput) { in.Revision = math.MaxInt64 }, func(in *WebMapViewStateInput) { in.MapRevision = 0 }, func(in *WebMapViewStateInput) { in.RequestID = "bad" },
		func(in *WebMapViewStateInput) { in.State.Scale = math.NaN() }, func(in *WebMapViewStateInput) { in.State.Scale = math.Inf(1) }, func(in *WebMapViewStateInput) { in.State.Scale = 0.049 }, func(in *WebMapViewStateInput) { in.State.Scale = 2.01 },
		func(in *WebMapViewStateInput) { in.State.ScrollX = -0.01 }, func(in *WebMapViewStateInput) { in.State.ScrollY = 1.01 }, func(in *WebMapViewStateInput) { in.State.DisplayMode = "canvas" }, func(in *WebMapViewStateInput) { in.State.Layout = "left" }, func(in *WebMapViewStateInput) { in.State.TableSort = "invalid" },
		func(in *WebMapViewStateInput) { in.State.TablePage = 1000 }, func(in *WebMapViewStateInput) { in.State.TablePage = -1 }, func(in *WebMapViewStateInput) { in.State.TableQuery = strings.Repeat("😀", 201) }, func(in *WebMapViewStateInput) { in.State.TableQuery = string([]byte{0xff}) }, func(in *WebMapViewStateInput) { in.State.TableQuery = "a\x00b" },
		func(in *WebMapViewStateInput) { in.State.SelectedNodeID = "mmn-" + strings.Repeat("f", 32) }, func(in *WebMapViewStateInput) { in.State.Collapsed = nil }, func(in *WebMapViewStateInput) { in.State.Collapsed = []string{ids["Leaf"]} }, func(in *WebMapViewStateInput) { in.State.Collapsed = []string{ids["Root"], ids["Root"]} },
	}
	for i, change := range changes {
		in := base
		in.State.Collapsed = append([]string{}, base.State.Collapsed...)
		change(&in)
		if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateInvalid) {
			t.Fatalf("case%d: %v", i, err)
		}
	}
	for _, actor := range []string{"", " alice", "alice\n", strings.Repeat("a", 129)} {
		if _, err := ReadWebMapViewStateIn(ctx, dir, actor, doc.Map.ID); !errors.Is(err, ErrWebMapViewStateInvalid) {
			t.Fatal(actor, err)
		}
	}
	if _, err := ReadWebMapViewStateIn(ctx, dir, "local", "mm-"+strings.Repeat("f", 32)); !errors.Is(err, ErrWebMapViewStateNotFound) {
		t.Fatal(err)
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='web_map_view_states'`).Scan(&count)
	if count != 0 {
		t.Fatal("invalid saves wrote schema")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, base, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel", err)
	}
}

func TestWebMapViewStateReceiptWindowAndStorageBounds(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	first := webMapViewTestInput(ids)
	if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, first, nil); err != nil {
		t.Fatal(err)
	}
	key, _ := webMapViewActorKey("alice")
	// Seed the retained-window boundary with copies of this valid immutable
	// outcome; the next real save must prune the oldest actual request.
	for i := 1; i < MaxWebMapViewStateReceipts; i++ {
		if _, err := store.db.Exec(`INSERT INTO web_map_view_state_receipts(actor_key,map_id,request_id,request_hash,payload,payload_hash) SELECT actor_key,map_id,?,request_hash,payload,payload_hash FROM web_map_view_state_receipts WHERE actor_key=? AND map_id=? ORDER BY seq LIMIT 1`, fmt.Sprintf("%032x", i), key, doc.Map.ID); err != nil {
			t.Fatal(err)
		}
	}
	next := first
	next.Revision = 1
	next.RequestID = strings.Repeat("b", 32)
	if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM web_map_view_state_receipts`).Scan(&count)
	if count != MaxWebMapViewStateReceipts {
		t.Fatal(count)
	}
	if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, first, nil); !errors.Is(err, ErrWebMapViewStateConflict) {
		t.Fatal("expired retry repeated", err)
	}
	if _, err := store.db.Exec(`UPDATE web_map_view_state_receipts SET request_hash='changed'`); err == nil {
		t.Fatal("receipt mutable")
	}
	// Metadata-first bounds reject a corrupt oversized row before loading it.
	if _, err := store.db.Exec(`UPDATE web_map_view_states SET payload=zeroblob(?) WHERE actor_key=?`, MaxWebMapViewStateBytes+1, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID); !errors.Is(err, ErrWebMapViewStateLimit) {
		t.Fatal(err)
	}
}

func TestWebMapViewStateGlobalQuotasAndReceiptBytePruning(t *testing.T) {
	t.Run("view_count", func(t *testing.T) {
		ctx := context.Background()
		dir, store, doc, ids := webMapViewTestFixture(t)
		in := webMapViewTestInput(ids)
		if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil); err != nil {
			t.Fatal(err)
		}
		// Prefill only quota metadata, avoiding 9999 unrelated API operations.
		// Each seeded payload is a copy of an already validated saved outcome.
		if _, err := store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999)
INSERT INTO web_map_view_states(actor_key,map_id,revision,map_revision,payload,payload_hash)
SELECT 'quota-'||n.x,s.map_id,s.revision,s.map_revision,s.payload,s.payload_hash FROM n CROSS JOIN web_map_view_states s WHERE s.actor_key=?`, mustWebMapViewTestKey(t, "alice")); err != nil {
			t.Fatal(err)
		}
		if _, err := SaveWebMapViewStateIn(ctx, dir, "bob", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateLimit) {
			t.Fatal("10001st view", err)
		}
		// Updating one of the existing 10000 views does not consume another row.
		in.Revision = 1
		in.RequestID = strings.Repeat("b", 32)
		if out, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil); err != nil || out.Revision != 2 {
			t.Fatal("update at count cap", out, err)
		}
		var count int
		store.db.QueryRow(`SELECT COUNT(*) FROM web_map_view_states`).Scan(&count)
		if count != maxWebMapViewStateRows {
			t.Fatal(count)
		}
	})
	t.Run("view_bytes", func(t *testing.T) {
		ctx := context.Background()
		dir, store, doc, ids := webMapViewTestFixture(t)
		in := webMapViewTestInput(ids)
		saved, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		// SQLite creates the quota fixture on disk. The core must account for
		// these bytes without materializing other users' payloads in Go.
		if _, err = store.db.Exec(`INSERT INTO web_map_view_states VALUES('quota-bytes',?,1,1,zeroblob(?),'fixture')`, doc.Map.ID, maxWebMapViewStateAllBytes); err != nil {
			t.Fatal(err)
		}
		in.Revision = 1
		in.RequestID = strings.Repeat("b", 32)
		if _, err = SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateLimit) {
			t.Fatal("byte cap", err)
		}
		current, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
		if err != nil || !reflect.DeepEqual(current, saved) {
			t.Fatal("quota failure committed", current, err)
		}
	})
	t.Run("receipt_bytes", func(t *testing.T) {
		ctx := context.Background()
		dir, store, doc, ids := webMapViewTestFixture(t)
		first := webMapViewTestInput(ids)
		if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, first, nil); err != nil {
			t.Fatal(err)
		}
		// A full global retry budget must evict oldest receipts, not block all
		// future saves. The new exact outcome stays available atomically.
		if _, err := store.db.Exec(`INSERT INTO web_map_view_state_receipts(actor_key,map_id,request_id,request_hash,payload,payload_hash) VALUES('quota-bytes',?,'fixture','fixture',zeroblob(?),'fixture')`, doc.Map.ID, maxWebMapViewStateReceiptBytes); err != nil {
			t.Fatal(err)
		}
		next := first
		next.Revision = 1
		next.RequestID = strings.Repeat("b", 32)
		if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil {
			t.Fatal(err)
		}
		var total, count int64
		if err := store.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))+length(actor_key)+length(map_id)+length(request_id)+length(request_hash)+length(payload_hash)+8),0) FROM web_map_view_state_receipts`).Scan(&count, &total); err != nil || total > maxWebMapViewStateReceiptBytes || count != 1 {
			t.Fatal(count, total, err)
		}
		if _, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, first, nil); !errors.Is(err, ErrWebMapViewStateConflict) {
			t.Fatal("global eviction replay", err)
		}
		if out, err := SaveWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil || !out.Replayed || out.Revision != 2 {
			t.Fatal("new receipt lost", out, err)
		}
	})
}

func mustWebMapViewTestKey(t *testing.T, actor string) string {
	t.Helper()
	key, err := webMapViewActorKey(actor)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
