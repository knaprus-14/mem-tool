package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebMapDuplicateIsIndependentAndIdempotent(t *testing.T) {
	dir, s, id, root, _ := webEditFixture(t)
	ctx := context.Background()
	if _, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("b", 32), URL: "https://example.org/"}); err != nil {
		t.Fatal(err)
	}
	in := WebMapDuplicate{Revision: 2, RequestID: strings.Repeat("c", 32), Title: "Copy"}
	copy, err := DuplicateWebMapIn(ctx, dir, "alice", id, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DuplicateWebMapIn(ctx, dir, "alice", id, in)
	if err != nil || !again.Replayed || again.ID != copy.ID {
		t.Fatal(again, err)
	}
	before, err := s.LoadClassicMindMap(id)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.LoadClassicMindMap(copy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Map.Revision != 1 || after.Map.Title != "Copy" || after.Map.RootNodeID == before.Map.RootNodeID || len(after.Nodes[0].Sources) != 1 || after.Nodes[0].Sources[0].ID == before.Nodes[0].Sources[0].ID {
		t.Fatal(after)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", copy.ID, after.Map.RootNodeID, WebMapNodeEdit{Revision: 1, RequestID: strings.Repeat("d", 32), Label: "Changed copy"}); err != nil {
		t.Fatal(err)
	}
	original, err := s.LoadClassicMindMap(id)
	if err != nil || original.Digest != before.Digest {
		t.Fatal("copy changed source", err)
	}
	in.RequestID = strings.Repeat("e", 32)
	in.Revision = 1
	if _, err = DuplicateWebMapIn(ctx, dir, "alice", id, in); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal(err)
	}
}
