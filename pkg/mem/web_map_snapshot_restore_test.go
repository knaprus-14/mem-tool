package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func snapshotForTest(t *testing.T, dir, id string, revision int64) ClassicMindMapSnapshot {
	t.Helper()
	_, err := CreateWebMapSnapshotIn(context.Background(), dir, "alice", id, WebMapSnapshotInput{Revision: revision, RequestID: strings.Repeat("1", 32), Reason: "Before rewriting"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapSnapshotsIn(context.Background(), dir, id, 0)
	if err != nil || len(page.Items) == 0 {
		t.Fatal(page, err)
	}
	return page.Items[0]
}

func TestWebMapSnapshotPreviewRestoreUndoAndReplay(t *testing.T) {
	dir, s, id, root, _ := webEditFixture(t)
	ctx := context.Background()
	if _, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("2", 32), URL: "https://example.org/source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AttachWebMapFileIn(ctx, dir, "alice", id, root, WebMapDelete{Revision: 2, RequestID: strings.Repeat("3", 32)}, "note.txt", []byte("retained attachment")); err != nil {
		t.Fatal(err)
	}
	locked := true
	if _, _, err := s.EditClassicMindMapNode(id, root, ClassicMindMapNodePatch{Locked: &locked}, 3, "alice", ""); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForTest(t, dir, id, 4)
	locked = false
	label := "Later manual edit"
	if _, _, err := s.EditClassicMindMapNode(id, root, ClassicMindMapNodePatch{Locked: &locked, Label: &label}, 4, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddClassicMindMapNode(id, root, "Later branch", -1, ClassicMindMapNodeNote, "", "manual text", 5, "alice", ""); err != nil {
		t.Fatal(err)
	}
	preview, err := ReadWebMapSnapshotIn(ctx, dir, id, snapshot.ID, 6)
	if err != nil || preview.Revision != 6 || preview.CurrentNodeCount != 2 || len(preview.Nodes) != 1 || !preview.Nodes[0].Locked || len(preview.Nodes[0].Sources) != 2 {
		t.Fatal(preview, err)
	}
	if preview.Nodes[0].Sources[0].State != "url" || preview.Nodes[0].Sources[1].State != "attachment" {
		t.Fatal(preview.Nodes[0].Sources)
	}
	in := WebMapSnapshotRestore{Revision: 6, SnapshotDigest: preview.Snapshot.DocumentDigest, RequestID: strings.Repeat("4", 32)}
	result, err := RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, in)
	if err != nil || result.Revision != 7 || result.Replayed {
		t.Fatal(result, err)
	}
	view, err := s.LoadClassicMindMap(id)
	if err != nil || len(view.Nodes) != 1 || !view.Nodes[0].Locked || view.Nodes[0].Label != "Root" {
		t.Fatal(view, err)
	}
	history, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil || history.Items[0].Action != "restore_snapshot" {
		t.Fatal(history, err)
	}
	undo, err := UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 7, ChangeID: history.UndoID, RequestID: strings.Repeat("5", 32)})
	if err != nil || undo.Revision != 8 {
		t.Fatal(undo, err)
	}
	view, err = s.LoadClassicMindMap(id)
	if err != nil || len(view.Nodes) != 2 {
		t.Fatal(view, err)
	}
	for _, n := range view.Nodes {
		if n.ID == root && (n.Label != label || n.Locked) {
			t.Fatal(n)
		}
	}
	replay, err := RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, in)
	if err != nil || !replay.Replayed || replay.Revision != 7 {
		t.Fatal(replay, err)
	}
	view, _ = s.LoadClassicMindMap(id)
	if view.Map.Revision != 8 {
		t.Fatal("retry changed the map after undo")
	}
	history, _ = ListWebMapHistoryIn(ctx, dir, id, 0)
	redo, err := RedoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 8, ChangeID: history.RedoID, RequestID: strings.Repeat("6", 32)})
	if err != nil || redo.Revision != 9 {
		t.Fatal(redo, err)
	}
	var blob string
	if err = s.db.QueryRow(`SELECT CAST(content AS TEXT) FROM web_map_attachments`).Scan(&blob); err != nil || blob != "retained attachment" {
		t.Fatal(blob, err)
	}
	page, _ := ListWebMapSnapshotsIn(ctx, dir, id, 0)
	if len(page.Items) != 2 || page.Items[0].DocumentDigest != snapshot.DocumentDigest {
		t.Fatal("restore mutated snapshots", page)
	}
}

func TestWebMapSnapshotPreviewRedactionAndCurrentEvidence(t *testing.T) {
	dir, s, id, root, _, path := mapDocumentFixture(t)
	ctx := context.Background()
	snapshot := snapshotForTest(t, dir, id, 2)
	preview, err := ReadWebMapSnapshotIn(ctx, dir, id, snapshot.ID, 2)
	if err != nil || preview.Nodes[0].Sources[0].State != "current" {
		t.Fatal(preview, err)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), "source_path") || strings.Contains(string(encoded), "locator") {
		t.Fatal("private source metadata exposed", string(encoded))
	}
	if _, err = s.db.Exec(`UPDATE entries SET document_revision=?`, strings.Repeat("9", 64)); err != nil {
		t.Fatal(err)
	}
	preview, err = ReadWebMapSnapshotIn(ctx, dir, id, snapshot.ID, 2)
	if err != nil || preview.Nodes[0].Sources[0].State != "stale" {
		t.Fatal(preview, err)
	}
	result, err := RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, WebMapSnapshotRestore{Revision: 2, SnapshotDigest: snapshot.DocumentDigest, RequestID: strings.Repeat("7", 32)})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := ListWebMapSourcesIn(ctx, dir, id, root)
	if err != nil || result.Revision != 3 || sources.Items[0].State != "stale" {
		t.Fatal(sources, err)
	}
	if _, err = s.db.Exec(`DELETE FROM entries`); err != nil {
		t.Fatal(err)
	}
	preview, err = ReadWebMapSnapshotIn(ctx, dir, id, snapshot.ID, 3)
	if err != nil || preview.Nodes[0].Sources[0].State != "missing" {
		t.Fatal(preview, err)
	}
}

func TestWebMapSnapshotRestorePinsForeignAndAtomicRollback(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	snapshot := snapshotForTest(t, dir, id, 1)
	in := WebMapSnapshotRestore{Revision: 1, SnapshotDigest: snapshot.DocumentDigest, RequestID: strings.Repeat("8", 32)}
	other, err := s.CreateClassicMindMap("Other", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadWebMapSnapshotIn(ctx, dir, other.Map.ID, snapshot.ID, 1); !errors.Is(err, ErrWebMapSnapshotNotFound) {
		t.Fatal("foreign preview", err)
	}
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", other.Map.ID, snapshot.ID, in); !errors.Is(err, ErrWebMapSnapshotNotFound) {
		t.Fatal("foreign restore", err)
	}
	wrong := in
	wrong.SnapshotDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, wrong); !errors.Is(err, ErrWebMapSnapshotChanged) {
		t.Fatal("digest", err)
	}
	wrong = in
	wrong.Revision = 2
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, wrong); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("revision", err)
	}
	if _, err = ReadWebMapSnapshotIn(ctx, dir, id, snapshot.ID, 2); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("preview revision", err)
	}
	before, _ := s.LoadClassicMindMap(id)
	if _, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS web_map_actions(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,node_id TEXT NOT NULL,created TEXT NOT NULL); CREATE TRIGGER reject_snapshot_restore_receipt BEFORE INSERT ON web_map_actions BEGIN SELECT RAISE(ABORT,'fixture');END`); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, in); err == nil {
		t.Fatal("receipt trigger accepted")
	}
	after, _ := s.LoadClassicMindMap(id)
	history, _ := ListWebMapHistoryIn(ctx, dir, id, 0)
	if after.Digest != before.Digest || len(history.Items) != 1 {
		t.Fatal("partial restore", after, history)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_snapshot_restore_receipt`); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "bob", id, snapshot.ID, in); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("foreign receipt actor", err)
	}
}

func TestWebMapSnapshotRejectsOversizeAndForeignStoredIDs(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	insert := func(raw, digest string) int64 {
		t.Helper()
		r, err := s.db.Exec(`INSERT INTO mind_map_snapshots(map_id,revision,reason,document_json,document_digest,created) VALUES(?,1,?,?,?,'now')`, id, "fixture "+digest, raw, digest)
		if err != nil {
			t.Fatal(err)
		}
		key, _ := r.LastInsertId()
		return key
	}
	huge := insert(strings.Repeat(" ", 8<<20+1), "sha256:"+strings.Repeat("0", 64))
	if _, err := ReadWebMapSnapshotIn(ctx, dir, id, huge, 1); !errors.Is(err, ErrWebMapEditLimit) {
		t.Fatal(err)
	}
	other, err := s.CreateClassicMindMap("Foreign", "")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := s.LoadClassicMindMap(id)
	doc.Nodes[0].ID = other.Map.RootNodeID
	doc.Map.RootNodeID = other.Map.RootNodeID
	doc, raw, err := finalizeClassicMindMapDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	key := insert(string(raw), doc.Digest)
	if _, err = RestoreWebMapSnapshotIn(ctx, dir, "alice", id, key, WebMapSnapshotRestore{Revision: 1, SnapshotDigest: doc.Digest, RequestID: strings.Repeat("9", 32)}); !errors.Is(err, ErrWebMapSnapshotInvalid) {
		t.Fatal(err)
	}
	unchanged, _ := s.LoadClassicMindMap(other.Map.ID)
	if unchanged.Digest != other.Digest {
		t.Fatal("foreign node stolen")
	}
	view, _ := s.LoadClassicMindMap(id)
	if view.Map.Revision != 1 {
		t.Fatal("partial corruption")
	}
}

func TestWebMapSnapshotConcurrentRetryAndCancellation(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	snapshot := snapshotForTest(t, dir, id, 1)
	in := WebMapSnapshotRestore{Revision: 1, SnapshotDigest: snapshot.DocumentDigest, RequestID: strings.Repeat("a", 32)}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadWebMapSnapshotIn(cancelled, dir, id, snapshot.ID, 1); err == nil {
		t.Fatal("cancelled preview")
	}
	if _, err := RestoreWebMapSnapshotIn(cancelled, dir, "alice", id, snapshot.ID, in); err == nil {
		t.Fatal("cancelled restore")
	}
	var wg sync.WaitGroup
	results := make(chan WebMapNodeEdited, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := RestoreWebMapSnapshotIn(ctx, dir, "alice", id, snapshot.ID, in)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for r := range results {
		if r.Revision != 2 {
			t.Fatal(r)
		}
		if !r.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("fresh restores", fresh)
	}
	view, _ := s.LoadClassicMindMap(id)
	history, _ := ListWebMapHistoryIn(ctx, dir, id, 0)
	if view.Map.Revision != 2 || len(history.Items) != 2 {
		t.Fatal(view, history)
	}
}
