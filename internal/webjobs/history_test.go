package webjobs

import (
	"context"
	"errors"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
)

func TestJobHistoryOwnedAttemptsAndReceipt(t *testing.T) {
	_, w, q, s := fixture(t)
	model(t, w)
	ctx := context.Background()
	alice := workspace.Actor{ID: "alice"}
	bob := workspace.Actor{ID: "bob"}
	j := enqueue(t, q, "history.md", "<!-- page: 1 -->\n\nFirst import text.")
	if rows, err := q.History(ctx, alice, j.ID); err != nil || len(rows) != 0 {
		t.Fatalf("queued=%+v %v", rows, err)
	}
	if _, err := q.History(ctx, bob, j.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := q.AttemptPages(ctx, alice, j.ID, 1, 0); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatal(err)
	}
	if worked, err := s.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("process %t %v", worked, err)
	}
	rows, err := q.History(ctx, alice, j.ID)
	if err != nil || len(rows) != 1 || rows[0].Number != 1 || !rows[0].HasReport || rows[0].Finished == "" {
		t.Fatalf("history=%+v %v", rows, err)
	}
	pages, err := q.AttemptPages(ctx, alice, j.ID, 1, 0)
	if err != nil || len(pages.Pages) != 0 || pages.Chunks != 1 || !pages.Updated {
		t.Fatalf("pages=%+v %v", pages, err)
	}
	if _, err = q.AttemptPages(ctx, bob, j.ID, 1, 0); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = q.AttemptPages(ctx, alice, j.ID, 2, 0); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatal(err)
	}
}
