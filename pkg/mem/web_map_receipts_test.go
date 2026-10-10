package mem

import (
	"context"
	"errors"
	"testing"
)

func TestWebMapReceiptsBeyondThousandRetainRetryHistory(t *testing.T) {
	dir, s, id, root, input := webEditFixture(t)
	_, err := s.db.Exec(`CREATE TABLE web_map_node_edits(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000) INSERT INTO web_map_node_edits SELECT 'old-'||i,'hash',1,'now' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := EditWebMapNodeIn(context.Background(), dir, "alice", id, root, input)
	if err != nil || first.Revision != 2 {
		t.Fatal(first, err)
	}
	retry, err := EditWebMapNodeIn(context.Background(), dir, "alice", id, root, input)
	if err != nil || !retry.Replayed || retry.Revision != 2 {
		t.Fatal(retry, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM web_map_node_edits`).Scan(&count); err != nil || count != 1001 {
		t.Fatal("receipts lost", count, err)
	}
}

func TestWebMapReceiptByteBudgetKeepsDataAtomic(t *testing.T) {
	dir, s, id, root, input := webEditFixture(t)
	_, err := s.db.Exec(`CREATE TABLE web_map_node_edits(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,created TEXT NOT NULL); INSERT INTO web_map_node_edits VALUES('large',CAST(zeroblob(?) AS TEXT),1,'now')`, maxWebMapMutationReceiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EditWebMapNodeIn(context.Background(), dir, "alice", id, root, input); !errors.Is(err, ErrWebMapEditLimit) {
		t.Fatal(err)
	}
	var revision int64
	if err = s.db.QueryRow(`SELECT revision FROM mind_maps WHERE id=?`, id).Scan(&revision); err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
}
