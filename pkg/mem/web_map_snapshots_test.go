package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebMapSnapshotsRetryReadOnlyAndRollback(t *testing.T) {
	dir, s, id, _, _ := webEditFixture(t)
	ctx := context.Background()
	in := WebMapSnapshotInput{Revision: 1, RequestID: strings.Repeat("b", 32), Reason: "Before edit"}
	result, err := CreateWebMapSnapshotIn(ctx, dir, "alice", id, in)
	if err != nil || result.Digest == "" || result.Revision != 1 {
		t.Fatal(result, err)
	}
	replay, err := CreateWebMapSnapshotIn(ctx, dir, "alice", id, in)
	if err != nil || !replay.Replayed || replay.Digest != result.Digest {
		t.Fatal(replay, err)
	}
	page, err := ListWebMapSnapshotsIn(ctx, dir, id, 0)
	if err != nil || len(page.Items) != 2 || page.Items[0].Reason != in.Reason || page.Revision != 1 {
		t.Fatal(page, err)
	}
	h, _ := ListWebMapHistoryIn(ctx, dir, id, 0)
	if len(h.Items) != 1 {
		t.Fatal("snapshot changed map history")
	}
	in.Reason = "Other"
	if _, err = CreateWebMapSnapshotIn(ctx, dir, "alice", id, in); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal(err)
	}
	in.RequestID = strings.Repeat("c", 32)
	in.Revision = 2
	if _, err = CreateWebMapSnapshotIn(ctx, dir, "alice", id, in); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal(err)
	}
	in.Revision = 1
	if _, err = s.db.Exec(`CREATE TRIGGER reject_snapshot_receipt BEFORE INSERT ON web_map_snapshots BEGIN SELECT RAISE(ABORT,'fixture');END`); err != nil {
		t.Fatal(err)
	}
	if _, err = CreateWebMapSnapshotIn(ctx, dir, "alice", id, in); err == nil {
		t.Fatal("receipt accepted")
	}
	page, err = ListWebMapSnapshotsIn(ctx, dir, id, 0)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("partial snapshot", err)
	}
}
