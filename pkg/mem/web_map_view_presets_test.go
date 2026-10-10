package mem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func presetInput(ids map[string]string) WebMapViewPresetInput {
	return WebMapViewPresetInput{WebMapViewStateInput: webMapViewTestInput(ids), Operation: "create", ID: "mvp-" + strings.Repeat("b", 32), Name: "Вопросы 😀"}
}

func TestWebMapViewPresetsCRUDIsolationAndUnchangedMap(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	read, err := ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Revision != 0 || read.Items == nil || len(read.Items) != 0 || read.CurrentMapRevision != 1 {
		t.Fatal(read, err)
	}
	var count int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'web_map_view_preset%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("GET migrated", count, err)
	}
	save := func(in WebMapViewPresetInput) WebMapViewPresetResult {
		t.Helper()
		out, e := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	in := presetInput(ids)
	first := save(in)
	if first.Revision != 1 || first.Replayed || first.Operation != "create" {
		t.Fatal(first)
	}
	read, err = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || len(read.Items) != 1 || !reflect.DeepEqual(read.Items[0].State, in.State) {
		t.Fatal(read, err)
	}
	bob, err := ReadWebMapViewPresetsIn(ctx, dir, "bob", doc.Map.ID)
	if err != nil || bob.Revision != 0 || len(bob.Items) != 0 {
		t.Fatal("actor leak", bob, err)
	}
	def, err := ReadWebMapViewStateIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || def.Revision != 0 {
		t.Fatal("default view changed", def, err)
	}
	in.Revision = 1
	in.RequestID = strings.Repeat("c", 32)
	in.Operation = "rename"
	in.Name = "Обзор"
	in.State.Layout = "right"
	save(in)
	read, err = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Items[0].Name != "Обзор" || read.Items[0].State.Layout != "bilateral" {
		t.Fatal("rename rewrote view", read, err)
	}
	in.Revision = 2
	in.RequestID = strings.Repeat("d", 32)
	in.Operation = "update"
	save(in)
	read, err = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Items[0].State.Layout != "right" {
		t.Fatal(read, err)
	}
	in.Revision = 3
	in.RequestID = strings.Repeat("e", 32)
	in.Operation = "delete"
	in.Name = ""
	save(in)
	read, err = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || read.Revision != 4 || read.Items == nil || len(read.Items) != 0 {
		t.Fatal("empty set reset CAS", read, err)
	}
	after, err := store.LoadClassicMindMap(doc.Map.ID)
	if err != nil || after.Digest != doc.Digest || !reflect.DeepEqual(after.Nodes, doc.Nodes) {
		t.Fatal("map changed", err)
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

func TestWebMapViewPresetsAdaptedReadPreservesOriginalAndHistoricalReplay(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	in := presetInput(ids)
	first, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err = store.db.QueryRow(`SELECT payload FROM web_map_view_presets`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DeleteClassicMindMapNode(doc.Map.ID, ids["Leaf"], ClassicMindMapDeleteBranch, 1, "alice", "fixture"); err != nil {
		t.Fatal(err)
	}
	read, err := ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if err != nil || !read.Items[0].Changed || read.CurrentMapRevision != 2 || read.Items[0].State.SelectedNodeID != ids["Root"] || len(read.Items[0].State.Collapsed) != 1 {
		t.Fatal(read, err)
	}
	var after string
	store.db.QueryRow(`SELECT payload FROM web_map_view_presets`).Scan(&after)
	if after != original {
		t.Fatal("GET normalized stored data")
	}
	fail := func() error { return errors.New("quota") }
	replay, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, fail)
	if err != nil || !replay.Replayed || replay.MapRevision != 1 || replay.Revision != first.Revision {
		t.Fatal(replay, err)
	}
	changed := in
	changed.Name = "different"
	if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, changed, nil); !errors.Is(err, ErrWebMapViewStateRequest) {
		t.Fatal(err)
	}
	rename := in
	rename.Operation = "rename"
	rename.Revision = 1
	rename.MapRevision = 2
	rename.RequestID = strings.Repeat("f", 32)
	rename.Name = "Имя"
	if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, rename, nil); err != nil {
		t.Fatal(err)
	}
	key, _ := webMapViewActorKey("alice")
	tx, _ := store.db.Begin()
	raw, err := readWebMapViewPresetsTx(ctx, tx, key, doc.Map.ID)
	tx.Rollback()
	if err != nil || raw.Items[0].State.SelectedNodeID != ids["Leaf"] || raw.Items[0].MapRevision != 1 {
		t.Fatal("rename adapted original", raw, err)
	}
	store.db.Exec(`UPDATE mind_maps SET deleted_at='deleted' WHERE id=?`, doc.Map.ID)
	if _, err = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID); !errors.Is(err, ErrWebMapViewStateNotFound) {
		t.Fatal(err)
	}
	if replay, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, fail); err != nil || !replay.Replayed {
		t.Fatal("lost ACK after map delete", replay, err)
	}
}

func TestWebMapViewPresetsCASQuotaRollbackAndLimits(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	in := presetInput(ids)
	a, b := in, in
	b.ID = "mvp-" + strings.Repeat("c", 32)
	b.Name = "Other"
	b.RequestID = strings.Repeat("d", 32)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, input := range []WebMapViewPresetInput{a, b} {
		wg.Add(1)
		go func(x WebMapViewPresetInput) {
			defer wg.Done()
			_, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, nil)
			errs <- err
		}(input)
	}
	wg.Wait()
	close(errs)
	good, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			good++
		} else if errors.Is(err, ErrWebMapViewStateConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if good != 1 || conflicts != 1 {
		t.Fatal(good, conflicts)
	}
	read, _ := ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	in.ID = read.Items[0].ID
	in.Revision = 1
	in.RequestID = strings.Repeat("e", 32)
	in.Operation = "rename"
	in.Name = "Renamed"
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, func() error { return errors.New("quota") }); err == nil {
		t.Fatal("quota ignored")
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_preset_receipt BEFORE INSERT ON web_map_view_preset_receipts BEGIN SELECT RAISE(ABORT,'test rollback'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); err == nil {
		t.Fatal("receipt failure ignored")
	}
	store.db.Exec(`DROP TRIGGER fail_preset_receipt`)
	read, _ = ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID)
	if read.Revision != 1 || read.Items[0].Name == in.Name {
		t.Fatal("failed transaction changed view", read)
	}
	in.MapRevision = 2
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateStale) {
		t.Fatal(err)
	}
	in.MapRevision = 1
	for _, name := range []string{"", " leading", "trailing ", "bad\nname", strings.Repeat("😀", 81)} {
		x := in
		x.Name = name
		if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, nil); !errors.Is(err, ErrWebMapViewStateInvalid) {
			t.Fatal(name, err)
		}
	}
	for i := 1; i < 20; i++ {
		x := presetInput(ids)
		x.Revision = int64(i)
		x.ID = fmt.Sprintf("mvp-%032x", i)
		x.RequestID = fmt.Sprintf("%032x", i)
		x.Name = fmt.Sprintf("View%d", i)
		if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	x := presetInput(ids)
	x.Revision = 20
	x.ID = fmt.Sprintf("mvp-%032x", 21)
	x.RequestID = fmt.Sprintf("%032x", 21)
	x.Name = "VIEW1"
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, nil); !errors.Is(err, ErrWebMapViewStateConflict) {
		t.Fatal("duplicate name", err)
	}
	x.Name = "21"
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, nil); !errors.Is(err, ErrWebMapViewStateLimit) {
		t.Fatal("limit", err)
	}
	x.Operation = "delete"
	x.Name = ""
	x.ID = in.ID
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, x, func() error { t.Fatal("deletion at quota denied"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWebMapViewPresetsReceiptWindowAndBoundedRead(t *testing.T) {
	ctx := context.Background()
	dir, store, doc, ids := webMapViewTestFixture(t)
	in := presetInput(ids)
	original := in
	for i := 0; i < 130; i++ {
		in.Revision = int64(i)
		in.RequestID = fmt.Sprintf("%032x", i+1)
		if i > 0 {
			in.Operation = "rename"
			in.Name = fmt.Sprintf("View%d", i)
		} else {
			original = in
		}
		if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); err != nil {
			t.Fatal(i, err)
		}
	}
	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM web_map_view_preset_receipts`).Scan(&count)
	if count != 128 {
		t.Fatal(count)
	}
	if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, original, nil); !errors.Is(err, ErrWebMapViewStateConflict) {
		t.Fatal("expired retry changed data", err)
	}
	store.db.Exec(`UPDATE web_map_view_presets SET payload=CAST(zeroblob(?) AS TEXT)`, maxWebMapViewPresetsBytes+1)
	if _, err := ReadWebMapViewPresetsIn(ctx, dir, "alice", doc.Map.ID); !errors.Is(err, ErrWebMapViewStateLimit) {
		t.Fatal("unbounded read", err)
	}
}

func TestWebMapViewPresetsGlobalBudgets(t *testing.T) {
	for _, kind := range []string{"rows", "bytes", "receipts"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			dir, store, doc, ids := webMapViewTestFixture(t)
			in := presetInput(ids)
			if _, err := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); err != nil {
				t.Fatal(err)
			}
			next := in
			next.Revision = 1
			next.Operation = "rename"
			next.Name = "New"
			next.RequestID = strings.Repeat("c", 32)
			var err error
			switch kind {
			case "rows":
				_, err = store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO web_map_view_presets SELECT 'quota-'||n.x,s.map_id,s.revision,s.payload,s.payload_hash FROM n CROSS JOIN web_map_view_presets s WHERE s.actor_key=?`, mustWebMapViewTestKey(t, "alice"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "bob", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateLimit) {
					t.Fatal("row cap", err)
				}
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil {
					t.Fatal("existing row", err)
				}
			case "bytes":
				_, err = store.db.Exec(`INSERT INTO web_map_view_presets VALUES('quota',?,1,zeroblob(?),'fixture')`, doc.Map.ID, 64<<20)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, next, nil); !errors.Is(err, ErrWebMapViewStateLimit) {
					t.Fatal("bytes", err)
				}
				if r, e := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); e != nil || !r.Replayed {
					t.Fatal("replay at bytes cap", r, e)
				}
				next.Operation = "delete"
				next.Name = ""
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil {
					t.Fatal("delete at bytes cap", err)
				}
			case "receipts":
				_, err = store.db.Exec(`INSERT INTO web_map_view_preset_receipts(actor_key,map_id,request_id,request_hash,payload,payload_hash) VALUES('quota',?,'fixture','fixture',zeroblob(?),'fixture')`, doc.Map.ID, 16<<20)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, next, nil); err != nil {
					t.Fatal(err)
				}
				var count int
				store.db.QueryRow(`SELECT COUNT(*) FROM web_map_view_preset_receipts`).Scan(&count)
				if count != 1 {
					t.Fatal("global receipt pruning", count)
				}
				if _, err = SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, in, nil); !errors.Is(err, ErrWebMapViewStateConflict) {
					t.Fatal("evicted retry", err)
				}
				if r, e := SaveWebMapViewPresetIn(ctx, dir, "alice", doc.Map.ID, next, nil); e != nil || !r.Replayed {
					t.Fatal(r, e)
				}
				if _, err = store.db.Exec(`UPDATE web_map_view_preset_receipts SET payload='tampered'`); err == nil {
					t.Fatal("mutable receipt")
				}
			}
		})
	}
}
