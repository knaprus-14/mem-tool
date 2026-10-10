package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func readingFixture(t *testing.T, texts ...string) (string, *Store, []DocumentChunk, []ReadingWorkspaceMutation) {
	t.Helper()
	dir, store, _ := queryFixture(t)
	var chunks []DocumentChunk
	var inputs []ReadingWorkspaceMutation
	for i, text := range texts {
		chunk := validStructuredChunks()[0]
		chunk.Text, chunk.Title, chunk.TotalChunks = text, fmt.Sprint("Документ ", i+1), 1
		p := &chunk.Provenance
		p.DocumentID, p.SourcePath = fmt.Sprint("reading-doc-", i), filepath.Join(dir, "private", fmt.Sprintf("source-%d.pdf", i))
		p.DocumentRevision, p.ChunkHash, p.BlockTotalChunks = ChunkContentHash(text), ChunkContentHash(text), 1
		if err := store.ReplaceDocumentChunks(p.SourcePath, []DocumentChunk{chunk}); err != nil {
			t.Fatal(err)
		}
		entry := store.GetBySourceFile(p.SourcePath)[0]
		pin, err := documentRestoreEntryDigest(entry)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
		inputs = append(inputs, ReadingWorkspaceMutation{Action: "add", EntryID: entry.ID, Pin: pin, Start: 0, End: min(utf8.RuneCountInString(text), MaxReadingWorkspaceTextRunes), RequestID: fmt.Sprintf("%032x", i+1)})
	}
	return dir, store, chunks, inputs
}

func readingMutation(t *testing.T, dir, actor string, in ReadingWorkspaceMutation) ReadingWorkspaceResult {
	t.Helper()
	out, err := MutateReadingWorkspaceIn(context.Background(), dir, actor, in)
	if err != nil {
		t.Fatal(in.Action, err)
	}
	return out
}

func TestReadingWorkspaceThreeDocumentsSurviveReopenAndIsolateActors(t *testing.T) {
	ctx := context.Background()
	dir, store, _, inputs := readingFixture(t, "😀 Первый документ — текст.", "🔧 Второй документ — условие.", "🧭 Третий документ — формула.")
	empty, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || empty.Revision != 0 || empty.ActiveID != "" || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	var tables int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'reading_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("read created schema", tables, err)
	}
	var saved ReadingWorkspace
	for i, in := range inputs {
		in.Revision = int64(i)
		saved = readingMutation(t, dir, "alice", in).Workspace
		if saved.Revision != int64(i+1) || len(saved.Items) != i+1 || saved.ActiveID != saved.Items[i].ID || saved.Items[i].SourceState != EvidenceCurrent || saved.Items[i].Text == "" || saved.Items[i].Scroll != 0 {
			t.Fatal(saved)
		}
	}
	first, third := saved.Items[0].ID, saved.Items[2].ID
	saved = readingMutation(t, dir, "alice", ReadingWorkspaceMutation{Action: "position", Revision: 3, RequestID: fmt.Sprintf("%032x", 4), ID: first, Scroll: .625}).Workspace
	if saved.ActiveID != third || saved.Items[0].Scroll != .625 {
		t.Fatal("position activated another tab", saved)
	}
	saved = readingMutation(t, dir, "alice", ReadingWorkspaceMutation{Action: "select", Revision: 4, RequestID: fmt.Sprintf("%032x", 5), ID: first}).Workspace
	if saved.ActiveID != first || saved.Revision != 5 {
		t.Fatal(saved)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatal("lost saved set", got, err)
	}
	bob, err := ReadReadingWorkspaceIn(ctx, dir, "bob")
	if err != nil || bob.Revision != 0 || len(bob.Items) != 0 {
		t.Fatal("actor leak", bob, err)
	}
	if _, err = MutateReadingWorkspaceIn(ctx, dir, "bob", ReadingWorkspaceMutation{Action: "select", ID: first, RequestID: fmt.Sprintf("%032x", 20)}); !errors.Is(err, ErrReadingWorkspaceNotFound) {
		t.Fatal(err)
	}
	bob = readingMutation(t, dir, "bob", inputs[0]).Workspace
	if len(bob.Items) != 1 || bob.Items[0].ID == first {
		t.Fatal("actor shared item/nonce", bob)
	}
	again, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || !reflect.DeepEqual(again, saved) {
		t.Fatal("other actor changed workspace", again, err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "source_path") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "actor") || strings.Contains(string(raw), dir) {
		t.Fatal("private data leaked", string(raw))
	}
}

func TestReadingWorkspaceReplayCASDuplicateAndFreshStates(t *testing.T) {
	ctx := context.Background()
	dir, store, chunks, inputs := readingFixture(t, "😀 Старая цитата и контекст.")
	add := inputs[0]
	first := readingMutation(t, dir, "alice", add)
	item := first.Workspace.Items[0]
	duplicate := add
	duplicate.Revision = 1
	duplicate.RequestID = fmt.Sprintf("%032x", 2)
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", duplicate); !errors.Is(err, ErrReadingWorkspaceDuplicate) {
		t.Fatal(err)
	}
	wrong := add
	wrong.End--
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", wrong); !errors.Is(err, ErrReadingWorkspaceRequest) {
		t.Fatal(err)
	}
	position := ReadingWorkspaceMutation{Action: "position", ID: item.ID, Scroll: .75, Revision: 1, RequestID: fmt.Sprintf("%032x", 3)}
	second := readingMutation(t, dir, "alice", position)
	if second.Workspace.Revision != 2 {
		t.Fatal(second)
	}
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", duplicate); !errors.Is(err, ErrReadingWorkspaceConflict) {
		t.Fatal(err)
	}
	selectIn := ReadingWorkspaceMutation{Action: "select", ID: item.ID, Revision: 2, RequestID: fmt.Sprintf("%032x", 4)}
	third := readingMutation(t, dir, "alice", selectIn)
	if third.Workspace.Revision != 3 {
		t.Fatal("no-op select did not advance", third)
	}
	chunk := chunks[0]
	chunk.Text = "Новый абзац. " + chunk.Text
	chunk.Provenance.DocumentRevision = ChunkContentHash(chunk.Text)
	chunk.Provenance.ChunkHash = ChunkContentHash(chunk.Text)
	if err := store.ReplaceDocumentChunks(chunk.Provenance.SourcePath, []DocumentChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	retry := readingMutation(t, dir, "alice", add)
	if !retry.Replayed || retry.Workspace.Revision != 1 || retry.Workspace.Items[0].Scroll != 0 || retry.Workspace.Items[0].Text != item.Text || retry.Workspace.Items[0].SourceState != EvidenceStale {
		t.Fatal("late replay replaced confirmed version or stale state", retry)
	}
	retry = readingMutation(t, dir, "alice", position)
	if !retry.Replayed || retry.Workspace.Revision != 2 || retry.Workspace.Items[0].Scroll != .75 {
		t.Fatal(retry)
	}
	current, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || current.Revision != 3 || current.Items[0].Scroll != .75 || current.Items[0].SourceState != EvidenceStale || current.Items[0].DocumentRevision != item.DocumentRevision {
		t.Fatal(current, err)
	}
	removed := readingMutation(t, dir, "alice", ReadingWorkspaceMutation{Action: "remove", ID: item.ID, Revision: 3, RequestID: fmt.Sprintf("%032x", 5)})
	if removed.Workspace.ActiveID != "" || len(removed.Workspace.Items) != 0 {
		t.Fatal(removed)
	}
	retry = readingMutation(t, dir, "alice", add)
	if !retry.Replayed || len(retry.Workspace.Items) != 1 {
		t.Fatal(retry)
	}
	current, err = ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || current.Revision != 4 || len(current.Items) != 0 {
		t.Fatal("replay resurrected removed item", current, err)
	}
}

func TestReadingWorkspaceMapChangeDeletionAndMissingSourceFallback(t *testing.T) {
	ctx := context.Background()
	dir, store, chunks, inputs := readingFixture(t, "😀 Цитата для узла карты.")
	classic, err := store.CreateClassicMindMap("Чтение", "")
	if err != nil {
		t.Fatal(err)
	}
	classic, node, err := store.AddClassicMindMapNode(classic.Map.ID, classic.Map.RootNodeID, "Связанный узел", -1, ClassicMindMapNodeNote, "", "", classic.Map.Revision, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	in := inputs[0]
	in.MapRef = &ReadingWorkspaceMapRef{MapID: classic.Map.ID, NodeID: node.ID, Revision: classic.Map.Revision}
	out := readingMutation(t, dir, "alice", in).Workspace
	if out.Items[0].MapState != "current" {
		t.Fatal(out)
	}
	oldRef := *out.Items[0].MapRef
	label := "Новое название"
	classic, _, err = store.EditClassicMindMapNode(classic.Map.ID, node.ID, ClassicMindMapNodePatch{Label: &label}, classic.Map.Revision, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || out.Items[0].MapState != "changed" || *out.Items[0].MapRef != oldRef || out.Revision != 1 {
		t.Fatal(out, err)
	}
	bad := in
	bad.RequestID = fmt.Sprintf("%032x", 2)
	bad.Revision = 1
	if _, err = MutateReadingWorkspaceIn(ctx, dir, "alice", bad); !errors.Is(err, ErrReadingWorkspaceStale) {
		t.Fatal("old map revision accepted", err)
	}
	if _, err = store.DeleteClassicMindMapNode(classic.Map.ID, node.ID, ClassicMindMapDeleteBranch, classic.Map.Revision, "alice", ""); err != nil {
		t.Fatal(err)
	}
	out, err = ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || out.Items[0].MapState != "missing" || out.ActiveID != out.Items[0].ID || out.Items[0].SourceState != EvidenceCurrent {
		t.Fatal("deleted node lost source fallback", out, err)
	}
	if _, err = store.db.Exec(`DELETE FROM entries WHERE source_path=?`, chunks[0].Provenance.SourcePath); err != nil {
		t.Fatal(err)
	}
	out, err = ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || out.Items[0].SourceState != EvidenceMissing || out.Items[0].Text != chunks[0].Text || *out.Items[0].MapRef != oldRef {
		t.Fatal(out, err)
	}
	replay := readingMutation(t, dir, "alice", in)
	if !replay.Replayed || replay.Workspace.Items[0].MapState != "missing" || replay.Workspace.Items[0].SourceState != EvidenceMissing {
		t.Fatal(replay)
	}
}

func TestReadingWorkspaceBoundsUnicodeAndRollback(t *testing.T) {
	ctx := context.Background()
	dir, store, _, inputs := readingFixture(t, "😀 Цитата, повтор Цитата. "+strings.Repeat("а", 100))
	base := inputs[0]
	for _, actor := range []string{"", " alice", "alice\n", strings.Repeat("a", 129), "\xff"} {
		if _, err := ReadReadingWorkspaceIn(ctx, dir, actor); !errors.Is(err, ErrReadingWorkspaceInvalid) {
			t.Fatal(actor, err)
		}
		if _, err := MutateReadingWorkspaceIn(ctx, dir, actor, base); !errors.Is(err, ErrReadingWorkspaceInvalid) {
			t.Fatal(actor, err)
		}
	}
	for _, change := range []func(*ReadingWorkspaceMutation){
		func(i *ReadingWorkspaceMutation) { i.Start = -1 }, func(i *ReadingWorkspaceMutation) { i.End = i.Start }, func(i *ReadingWorkspaceMutation) { i.End = 8001 }, func(i *ReadingWorkspaceMutation) { i.End = 500 }, func(i *ReadingWorkspaceMutation) { i.Revision = -1 }, func(i *ReadingWorkspaceMutation) { i.Revision = math.MaxInt64 }, func(i *ReadingWorkspaceMutation) { i.Scroll = math.NaN() }, func(i *ReadingWorkspaceMutation) { i.Scroll = math.Inf(1) }, func(i *ReadingWorkspaceMutation) { i.Scroll = 1.1 }, func(i *ReadingWorkspaceMutation) { i.ID = "read-" + strings.Repeat("a", 32) }, func(i *ReadingWorkspaceMutation) { i.Action = "overwrite" }, func(i *ReadingWorkspaceMutation) {
			i.MapRef = &ReadingWorkspaceMapRef{MapID: "other", NodeID: "other", Revision: 1}
		},
	} {
		in := base
		change(&in)
		if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", in); !errors.Is(err, ErrReadingWorkspaceInvalid) {
			t.Fatal(in, err)
		}
	}
	wrong := base
	wrong.Pin = "sha256:" + strings.Repeat("0", 64)
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", wrong); !errors.Is(err, ErrReadingWorkspaceStale) {
		t.Fatal(err)
	}
	for _, bad := range []string{"\xffbad", "\x00bad"} {
		if _, err := store.db.Exec(`UPDATE entries SET text=? WHERE id=?`, bad, base.EntryID); err != nil {
			t.Fatal(err)
		}
		if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", base); !errors.Is(err, ErrReadingWorkspaceInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE entries SET text=zeroblob(?) WHERE id=?`, maxDocumentAnnotationChunkBytes+1, base.EntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", base); !errors.Is(err, ErrReadingWorkspaceLimit) {
		t.Fatal(err)
	}
	current, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || current.Revision != 0 || len(current.Items) != 0 {
		t.Fatal("failed action left writes", current, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = ReadReadingWorkspaceIn(canceled, dir, "alice"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = MutateReadingWorkspaceIn(canceled, dir, "alice", base); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReadingWorkspaceItemLimitAndConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	dir, _, _, inputs := readingFixture(t, strings.Repeat("😀", 30))
	var workspace ReadingWorkspace
	for i := 0; i < 24; i++ {
		in := inputs[0]
		in.Revision = int64(i)
		in.Start = i
		in.End = i + 1
		in.RequestID = fmt.Sprintf("%032x", i+1)
		workspace = readingMutation(t, dir, "alice", in).Workspace
	}
	extra := inputs[0]
	extra.Revision = 24
	extra.Start = 24
	extra.End = 25
	extra.RequestID = fmt.Sprintf("%032x", 25)
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", extra); !errors.Is(err, ErrReadingWorkspaceLimit) {
		t.Fatal(err)
	}
	removed := readingMutation(t, dir, "alice", ReadingWorkspaceMutation{Action: "remove", Revision: 24, ID: workspace.Items[0].ID, RequestID: fmt.Sprintf("%032x", 26)})
	if len(removed.Workspace.Items) != 23 || removed.Workspace.ActiveID != workspace.ActiveID {
		t.Fatal(removed)
	}
	extra.Revision = 25
	if got := readingMutation(t, dir, "alice", extra); len(got.Workspace.Items) != 24 {
		t.Fatal(got)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := inputs[0]
			in.Start = i
			in.End = i + 1
			in.RequestID = fmt.Sprintf("%032x", 100+i)
			_, err := MutateReadingWorkspaceIn(ctx, dir, "concurrent", in)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrReadingWorkspaceConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
}

func TestReadingWorkspaceFiniteReceiptWindowPreservesLateRetrySafety(t *testing.T) {
	ctx := context.Background()
	dir, store, _, inputs := readingFixture(t, strings.Repeat("😀", 8003))
	var current ReadingWorkspace
	first := inputs[0]
	for i := 0; i < 3; i++ {
		in := first
		in.Revision = int64(i)
		in.Start = i
		in.End = i + 8000
		in.RequestID = fmt.Sprintf("%032x", i+1)
		current = readingMutation(t, dir, "alice", in).Workspace
	}
	var latest ReadingWorkspaceMutation
	for i := 0; i < 135; i++ {
		latest = ReadingWorkspaceMutation{Action: "position", Revision: current.Revision, ID: current.Items[0].ID, Scroll: float64(i%100) / 100, RequestID: fmt.Sprintf("%032x", i+10)}
		current = readingMutation(t, dir, "alice", latest).Workspace
	}
	key, _ := readingWorkspaceActorKey("alice")
	var count, size int64
	if err := store.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))+length(actor_key)+length(request_id)+length(request_hash)+length(payload_hash)+8),0) FROM reading_workspace_receipts WHERE actor_key=?`, key).Scan(&count, &size); err != nil || count > MaxReadingWorkspaceReceipts || count >= 128 || count < 1 || size > MaxReadingWorkspaceReceiptBytes {
		t.Fatal(count, size, err)
	}
	if _, err := MutateReadingWorkspaceIn(ctx, dir, "alice", first); !errors.Is(err, ErrReadingWorkspaceConflict) {
		t.Fatal("evicted retry repeated action", err)
	}
	retry := readingMutation(t, dir, "alice", latest)
	if !retry.Replayed || !reflect.DeepEqual(retry.Workspace, current) {
		t.Fatal(retry)
	}
	if _, err := store.db.Exec(`UPDATE reading_workspace_receipts SET request_hash='changed' WHERE actor_key=?`, key); err == nil {
		t.Fatal("receipt was mutable")
	}
	now, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || !reflect.DeepEqual(now, current) {
		t.Fatal(now, err)
	}
}

func TestReadingWorkspaceStoredPayloadBoundsAndHash(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"hash", "oversized", "global-limit"} {
		t.Run(kind, func(t *testing.T) {
			dir, store, _, inputs := readingFixture(t, "Exact quote")
			made := readingMutation(t, dir, "alice", inputs[0])
			key, _ := readingWorkspaceActorKey("alice")
			expected := ErrReadingWorkspaceInvalid
			switch kind {
			case "hash":
				if _, err := store.db.Exec(`UPDATE reading_workspaces SET payload_hash='wrong' WHERE actor_key=?`, key); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				expected = ErrReadingWorkspaceLimit
				if _, err := store.db.Exec(`UPDATE reading_workspaces SET payload=zeroblob(?) WHERE actor_key=?`, MaxReadingWorkspaceJSONBytes+1, key); err != nil {
					t.Fatal(err)
				}
			case "global-limit":
				if _, err := store.db.Exec(`INSERT INTO reading_workspaces(actor_key,revision,payload,payload_hash) VALUES('quota-fixture',1,zeroblob(?),'fixture')`, maxReadingWorkspaceBytes); err != nil {
					t.Fatal(err)
				}
				_, err := MutateReadingWorkspaceIn(ctx, dir, "alice", ReadingWorkspaceMutation{Action: "position", Revision: 1, RequestID: fmt.Sprintf("%032x", 2), ID: made.Workspace.Items[0].ID, Scroll: .5})
				if !errors.Is(err, ErrReadingWorkspaceLimit) {
					t.Fatal(err)
				}
				got, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
				if err != nil || got.Revision != 1 || got.Items[0].Scroll != 0 {
					t.Fatal("quota left partial update", got, err)
				}
				return
			}
			if _, err := ReadReadingWorkspaceIn(ctx, dir, "alice"); !errors.Is(err, expected) {
				t.Fatal(err)
			}
		})
	}
}

func TestReadingWorkspaceAdmissionRunsAfterReplayAndCASAndOnlyForNewAdd(t *testing.T) {
	ctx := context.Background()
	dir, store, _, inputs := readingFixture(t, "😀 Первый документ", "🔧 Второй документ")
	calls := 0
	denied := errors.New("external storage admission denied")
	deny := func() error { calls++; return denied }
	if _, err := MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", inputs[0], deny); !errors.Is(err, denied) || calls != 1 {
		t.Fatal(calls, err)
	}
	initial, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || initial.Revision != 0 || len(initial.Items) != 0 {
		t.Fatal("denied admission changed workspace", initial, err)
	}
	var schemas int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'reading_%'`).Scan(&schemas); err != nil || schemas != 0 {
		t.Fatal("denied admission committed schema", schemas, err)
	}
	first, err := MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", inputs[0], func() error { calls++; return nil })
	if err != nil || first.Replayed || calls != 2 {
		t.Fatal(first, calls, err)
	}
	retry, err := MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", inputs[0], deny)
	if err != nil || !retry.Replayed || calls != 2 || !reflect.DeepEqual(retry.Workspace, first.Workspace) {
		t.Fatal("quota blocked confirmed receipt", retry, calls, err)
	}
	wrong := inputs[0]
	wrong.End--
	if _, err = MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", wrong, deny); !errors.Is(err, ErrReadingWorkspaceRequest) || calls != 2 {
		t.Fatal("admission before request collision", calls, err)
	}
	if _, err = MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", inputs[1], deny); !errors.Is(err, ErrReadingWorkspaceConflict) || calls != 2 {
		t.Fatal("admission before CAS", calls, err)
	}
	second := inputs[1]
	second.Revision = 1
	if _, err = MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", second, deny); !errors.Is(err, denied) || calls != 3 {
		t.Fatal(calls, err)
	}
	current, err := ReadReadingWorkspaceIn(ctx, dir, "alice")
	if err != nil || !reflect.DeepEqual(current, first.Workspace) {
		t.Fatal("denial left partial writes", current, err)
	}
	for i, action := range []string{"position", "select", "remove"} {
		in := ReadingWorkspaceMutation{Action: action, Revision: current.Revision, ID: first.Workspace.ActiveID, RequestID: fmt.Sprintf("%032x", 20+i)}
		if action == "position" {
			in.Scroll = .5
		}
		out, e := MutateReadingWorkspaceWithAdmissionIn(ctx, dir, "alice", in, deny)
		if e != nil || calls != 3 {
			t.Fatal(action, calls, e)
		}
		current = out.Workspace
	}
}
