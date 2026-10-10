package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebMapRestoreCyclesAndReceipts(t *testing.T) {
	ctx := context.Background()
	dir, _, id, _, _ := webAIFixture(t)
	deletion := WebMapDelete{Revision: 1, RequestID: strings.Repeat("a", 32)}
	if _, err := DeleteWebMapIn(ctx, dir, "alice", id, deletion); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapsFilteredIn(ctx, dir, 0, false, true)
	if err != nil || page.Total != 1 || page.Items[0].ID != id {
		t.Fatal(page, err)
	}
	input := WebMapDelete{Revision: 2, RequestID: strings.Repeat("b", 32)}
	wrong := input
	wrong.Revision = 1
	if _, err = RestoreWebMapIn(ctx, dir, "alice", id, wrong); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, e := RestoreWebMapIn(ctx, dir, "alice", id, input)
		if e != nil || r.Revision != 3 || r.Replayed != (i == 1) {
			t.Fatal(r, e)
		}
	}
	if _, err = RestoreWebMapIn(ctx, dir, "bob", id, input); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal(err)
	}
	if _, err = ReadWebMapIn(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	// A delayed retry of the old deletion must not hide the restored map.
	if r, e := DeleteWebMapIn(ctx, dir, "alice", id, deletion); e != nil || !r.Replayed {
		t.Fatal(r, e)
	}
	if _, err = ReadWebMapIn(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	deletion.Revision = 3
	deletion.RequestID = strings.Repeat("c", 32)
	if _, err = DeleteWebMapIn(ctx, dir, "alice", id, deletion); err != nil {
		t.Fatal(err)
	}
	input.Revision = 4
	input.RequestID = strings.Repeat("d", 32)
	if _, err = RestoreWebMapIn(ctx, dir, "alice", id, input); err != nil {
		t.Fatal(err)
	}
	page, err = ListWebMapsFilteredIn(ctx, dir, 0, false, true)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}
