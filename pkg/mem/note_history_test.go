package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func historyNote(t *testing.T) (string, *Store, NoteView) {
	t.Helper()
	dir, s, cfg := queryFixture(t)
	cfg.Ollama.BaseURL = "http://127.0.0.1:1"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	e, err := s.Add("historyneedle body", "Original", []string{"saved"}, "ollama", []float32{1, 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	n, err := ReadNoteIn(context.Background(), dir, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, n
}
func actionKey(n int) string { return fmt.Sprintf("%032x", n) }
func TestNoteHistoryLifecycleAndConflicts(t *testing.T) {
	ctx := context.Background()
	dir, s, n := historyNote(t)
	empty, err := ListNoteHistoryIn(ctx, dir, 0, 0)
	if err != nil || empty.Total != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	var tables int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='web_note_history'`).Scan(&tables)
	if tables != 0 {
		t.Fatal("read migrated database")
	}
	_, err = SaveNoteIn(ctx, dir, NoteInput{ID: n.ID, Pin: n.Pin, RequestID: actionKey(1), Title: "Edited", Tags: []string{"changed"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ListNoteHistoryIn(ctx, dir, n.ID, 0)
	if err != nil || page.Total != 2 || page.Items[1].Action != "before" {
		t.Fatalf("history: %+v %v", page, err)
	}
	original := page.Items[1]
	preview, err := ReadNoteVersionIn(ctx, dir, n.ID, original.VersionID)
	if err != nil || preview.Note.Title != "Original" || preview.Note.Text != n.Text {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	data, _ := json.Marshal(preview)
	for _, secret := range []string{"embedding", "vector", dir} {
		if strings.Contains(string(data), secret) {
			t.Fatal("preview exposed internal data")
		}
	}
	latest, _ := ReadNoteIn(ctx, dir, n.ID)
	restore := NoteAction{Action: "restore-version", ID: n.ID, Pin: n.Pin, VersionID: original.VersionID, VersionPin: original.Pin, RequestID: actionKey(2)}
	if _, err = ApplyNoteActionIn(ctx, dir, restore); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	restore.Pin = latest.Pin
	if _, err = ApplyNoteActionIn(ctx, dir, restore); err != nil {
		t.Fatal(err)
	}
	restored, _ := ReadNoteIn(ctx, dir, n.ID)
	if restored.Title != n.Title || !restored.Important || !reflect.DeepEqual(restored.Tags, n.Tags) || restored.Pin == n.Pin {
		t.Fatalf("restored: %+v", restored)
	}
	entry, err := s.GetByID(n.ID)
	if err != nil || !reflect.DeepEqual(entry.Embedding, []float32{1, 0}) {
		t.Fatalf("vector/cache: %+v %v", entry, err)
	}
	del := NoteAction{Action: "delete", ID: n.ID, Pin: restored.Pin, RequestID: actionKey(3)}
	if _, err = ApplyNoteActionIn(ctx, dir, del); err != nil {
		t.Fatal(err)
	}
	if retry, err := ApplyNoteActionIn(ctx, dir, del); err != nil || !retry.Replayed {
		t.Fatalf("delete replay: %+v %v", retry, err)
	}
	if _, err = ReadNoteIn(ctx, dir, n.ID); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("deleted readable: %v", err)
	}
	if e, _ := s.GetByID(n.ID); e != nil {
		t.Fatal("CLI cache retained deleted entry")
	}
	results, err := QueryIn(ctx, dir, QueryRequest{Query: "historyneedle", Mode: "text"}, false)
	if err != nil || len(results.Hits) != 0 {
		t.Fatalf("deleted searchable: %+v %v", results, err)
	}
	trash, err := ListNoteHistoryIn(ctx, dir, 0, 0)
	if err != nil || trash.Total != 1 {
		t.Fatalf("trash: %+v %v", trash, err)
	}
	item := trash.Items[0]
	back := NoteAction{Action: "restore", ID: n.ID, VersionID: item.VersionID, VersionPin: item.Pin, RequestID: actionKey(4)}
	if _, err = ApplyNoteActionIn(ctx, dir, back); err != nil {
		t.Fatal(err)
	}
	if retry, err := ApplyNoteActionIn(ctx, dir, back); err != nil || !retry.Replayed {
		t.Fatalf("restore replay: %+v %v", retry, err)
	}
	trash, _ = ListNoteHistoryIn(ctx, dir, 0, 0)
	if trash.Total != 0 {
		t.Fatal("restored still in trash")
	}
	again, _ := ReadNoteIn(ctx, dir, n.ID)
	if again.ID != n.ID || again.Text != n.Text || again.Pin == restored.Pin {
		t.Fatal("restore identity/freshness")
	}
	results, err = QueryIn(ctx, dir, QueryRequest{Query: "historyneedle", Mode: "text"}, false)
	if err != nil || len(results.Hits) != 1 {
		t.Fatalf("restored not searchable: %+v %v", results, err)
	}
	del.Pin = again.Pin
	del.RequestID = actionKey(5)
	if _, err = ApplyNoteActionIn(ctx, dir, del); err != nil {
		t.Fatal(err)
	}
	back.RequestID = actionKey(6)
	if _, err = ApplyNoteActionIn(ctx, dir, back); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("old trash snapshot after second delete: %v", err)
	}
	// A replay of a completed earlier restore must not resurrect a later deletion.
	back.RequestID = actionKey(4)
	if retry, err := ApplyNoteActionIn(ctx, dir, back); err != nil || !retry.Replayed {
		t.Fatal("lost receipt")
	}
	if _, err = ReadNoteIn(ctx, dir, n.ID); !errors.Is(err, ErrNoteNotFound) {
		t.Fatal("replay resurrected deleted entry")
	}
}

func TestNoteHistoryRollbackTamperQuotaAndOccupiedID(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"receipt", "quota", "occupied", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, n := historyNote(t)
			if _, err := SaveNoteIn(ctx, dir, NoteInput{ID: n.ID, Pin: n.Pin, RequestID: actionKey(1), Title: "Edited"}); err != nil {
				t.Fatal(err)
			}
			latest, _ := ReadNoteIn(ctx, dir, n.ID)
			if mode == "receipt" || mode == "quota" {
				var err error
				if mode == "receipt" {
					_, err = s.db.Exec(`CREATE TRIGGER fail_note_receipt BEFORE INSERT ON web_note_writes BEGIN SELECT RAISE(ABORT,'test receipt failure'); END`)
				} else {
					_, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9998) INSERT INTO web_note_history(entry_id,pin,entry_pin,title,action,created,payload) SELECT 999,'','','','before','',X'00' FROM n`)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = ApplyNoteActionIn(ctx, dir, NoteAction{Action: "delete", ID: n.ID, Pin: latest.Pin, RequestID: actionKey(2)})
				if err == nil {
					t.Fatal("failed operation succeeded")
				}
				if mode == "quota" && !errors.Is(err, ErrNoteHistoryLimit) {
					t.Fatal(err)
				}
				kept, _ := ReadNoteIn(ctx, dir, n.ID)
				trash, _ := ListNoteHistoryIn(ctx, dir, 0, 0)
				page, _ := ListNoteHistoryIn(ctx, dir, n.ID, 0)
				if kept.Pin != latest.Pin || trash.Total != 0 || page.Total != 2 {
					t.Fatal("failed deletion partially committed")
				}
				return
			}
			if _, err := ApplyNoteActionIn(ctx, dir, NoteAction{Action: "delete", ID: n.ID, Pin: latest.Pin, RequestID: actionKey(2)}); err != nil {
				t.Fatal(err)
			}
			trash, _ := ListNoteHistoryIn(ctx, dir, 0, 0)
			item := trash.Items[0]
			expected := ErrNoteSnapshot
			if mode == "occupied" {
				_, err := s.db.Exec(`INSERT INTO entries(id,text,title,tags,created,backend,dims,embedding) VALUES(?,'new occupant','other','[]','now','ollama',0,X'')`, n.ID)
				if err != nil {
					t.Fatal(err)
				}
				expected = ErrNoteConflict
			} else {
				if _, err := s.db.Exec(`UPDATE web_note_history SET payload=X'7B7D' WHERE version_id=?`, item.VersionID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ApplyNoteActionIn(ctx, dir, NoteAction{Action: "restore", ID: n.ID, VersionID: item.VersionID, VersionPin: item.Pin, RequestID: actionKey(3)})
			if !errors.Is(err, expected) {
				t.Fatalf("%s: %v", mode, err)
			}
			trash, _ = ListNoteHistoryIn(ctx, dir, 0, 0)
			if trash.Total != 1 {
				t.Fatal("failed restore removed tombstone")
			}
			if mode == "occupied" {
				kept, err := ReadNoteIn(ctx, dir, n.ID)
				if err != nil || kept.Text != "new occupant" {
					t.Fatal("occupied ID overwritten")
				}
			}
		})
	}
}
func TestNoteHistoryCapturesExternalChangesAndPaginates(t *testing.T) {
	ctx := context.Background()
	dir, s, n := historyNote(t)
	for i := 1; i <= 32; i++ {
		if i == 2 {
			if _, err := s.ToggleImportant(n.ID); err != nil {
				t.Fatal(err)
			}
		}
		current, err := ReadNoteIn(ctx, dir, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = SaveNoteIn(ctx, dir, NoteInput{ID: n.ID, Pin: current.Pin, RequestID: actionKey(i), Title: fmt.Sprint(i), Important: current.Important}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := ListNoteHistoryIn(ctx, dir, n.ID, 0)
	if err != nil || page.Total != 34 || len(page.Items) != 30 {
		t.Fatalf("page1: %+v %v", page, err)
	}
	page, err = ListNoteHistoryIn(ctx, dir, n.ID, 30)
	if err != nil || len(page.Items) != 4 || page.Items[1].Action != "before" {
		t.Fatalf("page2: %+v %v", page, err)
	}
	external, err := ReadNoteVersionIn(ctx, dir, n.ID, page.Items[1].VersionID)
	if err != nil || external.Note.Important {
		t.Fatalf("external captured: %+v %v", external, err)
	}
}
