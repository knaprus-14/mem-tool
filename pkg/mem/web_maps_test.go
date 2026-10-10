package mem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebMapsCreateReadRetryAndCompatibility(t *testing.T) {
	ctx := context.Background()
	dir, s, _ := queryFixture(t)
	page, err := ListWebMapsIn(ctx, dir, 0, false)
	if err != nil || page.Total != 0 {
		t.Fatalf("empty: %+v %v", page, err)
	}
	body := WebMapCreate{Title: "Project", Description: "description", TemplateID: "project", RequestID: strings.Repeat("a", 32)}
	result, err := CreateWebMapIn(ctx, dir, "alice", body)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := CreateWebMapIn(ctx, dir, "alice", body)
	if err != nil || !replay.Replayed || replay.ID != result.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := body
	changed.Title = "Different"
	if _, err = CreateWebMapIn(ctx, dir, "alice", changed); !errors.Is(err, ErrWebMapRequest) {
		t.Fatalf("reused key: %v", err)
	}
	doc, err := s.LoadClassicMindMap(result.ID)
	if err != nil || len(doc.Nodes) != 6 || doc.Map.Revision != 1 {
		t.Fatalf("CLI compatible: %+v %v", doc, err)
	}
	history, err := s.ListClassicMindMapChanges(result.ID, 10)
	if err != nil || len(history) != 1 || history[0].Actor != "alice" {
		t.Fatalf("history: %+v %v", history, err)
	}
	snapshots, err := s.ListClassicMindMapSnapshots(result.ID, 10)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot: %+v %v", snapshots, err)
	}
	long := strings.Repeat("описание ", 700)
	if _, err = s.db.Exec(`UPDATE mind_maps SET description=? WHERE id=?`, long, result.ID); err != nil {
		t.Fatal(err)
	}
	view, err := ReadWebMapIn(ctx, dir, result.ID)
	if err != nil || len(view.Nodes) != 6 || view.Map.Description != long {
		t.Fatalf("read: %+v %v", view, err)
	}
	// Full source locators must never leave the server through this read-only view.
	if _, err = s.db.Exec(`INSERT INTO mind_map_node_sources(id,map_id,node_id,position,kind,title,locator,url,source_path,created) VALUES('source-test',?,?,0,'external_file','Private file','C:\private\secret.pdf','https://private.invalid','C:\private\secret.pdf','now')`, result.ID, doc.Map.RootNodeID); err != nil {
		t.Fatal(err)
	}
	view, err = ReadWebMapIn(ctx, dir, result.ID)
	if err != nil || view.Map.SourceCount != 1 {
		t.Fatalf("sources: %+v %v", view, err)
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{"secret.pdf", "private.invalid", "locator", "source_path"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("source internals leaked")
		}
	}
	title := "CLI change"
	if _, err = s.EditClassicMindMap(result.ID, ClassicMindMapPatch{Title: &title}, 1, "cli", ""); err != nil {
		t.Fatal(err)
	}
	view, err = ReadWebMapIn(ctx, dir, result.ID)
	if err != nil || view.Map.Title != title || view.Map.Revision != 2 {
		t.Fatal("CLI edit not reflected")
	}
	if _, err = s.db.Exec(`UPDATE mind_maps SET status='archived' WHERE id=?`, result.ID); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapsIn(ctx, dir, 0, false)
	if err != nil || page.Total != 0 {
		t.Fatal("archive filter")
	}
	page, err = ListWebMapsIn(ctx, dir, 0, true)
	if err != nil || page.Total != 1 {
		t.Fatal("include archive")
	}
}
func TestWebMapsRollbackLimitsAndReadonly(t *testing.T) {
	ctx := context.Background()
	t.Run("no schema migration", func(t *testing.T) {
		dir, s, _ := queryFixture(t)
		if _, err := s.db.Exec(`DROP TABLE mind_maps;DROP TABLE mind_map_nodes;DROP TABLE mind_map_node_sources`); err != nil {
			t.Fatal(err)
		}
		page, err := ListWebMapsIn(ctx, dir, 0, false)
		if err != nil || page.Total != 0 {
			t.Fatal(err)
		}
		if _, err = ReadWebMapIn(ctx, dir, "missing"); !errors.Is(err, ErrClassicMindMapNotFound) {
			t.Fatal(err)
		}
		var count int
		s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('mind_maps','mind_map_nodes','mind_map_node_sources','web_map_creates')`).Scan(&count)
		if count != 0 {
			t.Fatal("read migrated schema")
		}
	})
	t.Run("missing database", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		os.Mkdir(dir, 0700)
		_, err := CreateWebMapIn(ctx, dir, "local", WebMapCreate{Title: "Test", RequestID: strings.Repeat("a", 32)})
		if err == nil {
			t.Fatal("missing DB accepted")
		}
		if _, err = os.Stat(StorePathIn(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing DB created")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		dir, s, _ := queryFixture(t)
		_, err := s.db.Exec(`CREATE TABLE web_map_creates(request_id TEXT PRIMARY KEY,request_hash TEXT,map_id TEXT,created TEXT);CREATE TRIGGER reject_web_map BEFORE INSERT ON web_map_creates BEGIN SELECT RAISE(ABORT,'fixture');END`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = CreateWebMapIn(ctx, dir, "local", WebMapCreate{Title: "Test", RequestID: strings.Repeat("a", 32)}); err == nil {
			t.Fatal("failed receipt succeeded")
		}
		for _, table := range []string{"mind_maps", "mind_map_nodes", "mind_map_changes", "mind_map_snapshots"} {
			var count int
			s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count)
			if count != 0 {
				t.Fatal("partial map write", table)
			}
		}
	})
	t.Run("quota and paging", func(t *testing.T) {
		dir, s, _ := queryFixture(t)
		_, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO mind_maps(id,title,mode,status,root_node_id,revision,created,updated) SELECT 'map-'||x,'fixture','manual','draft','root',1,'now','now' FROM n`)
		if err != nil {
			t.Fatal(err)
		}
		page, err := ListWebMapsIn(ctx, dir, 990, false)
		if err != nil || page.Total != 1000 || len(page.Items) != 10 {
			t.Fatalf("paging: %+v %v", page, err)
		}
		if _, err = CreateWebMapIn(ctx, dir, "local", WebMapCreate{Title: "Test", RequestID: strings.Repeat("a", 32)}); !errors.Is(err, ErrWebMapLimit) {
			t.Fatal(err)
		}
	})
	t.Run("oversize and invalid tree", func(t *testing.T) {
		dir, s, _ := queryFixture(t)
		doc, err := s.CreateClassicMindMap("Test", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`UPDATE mind_map_nodes SET body_markdown=? WHERE map_id=?`, strings.Repeat("x", 4<<20), doc.Map.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadWebMapIn(ctx, dir, doc.Map.ID); !errors.Is(err, ErrWebMapLimit) {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`UPDATE mind_map_nodes SET body_markdown='',parent_id=id WHERE map_id=?`, doc.Map.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadWebMapIn(ctx, dir, doc.Map.ID); !errors.Is(err, ErrClassicMindMapRenderLimit) {
			t.Fatal(err)
		}
	})
}
