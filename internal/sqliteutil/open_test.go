package sqliteutil

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBusyTimeoutAppliesToEachConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "corpus # 100%.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep both connections checked out, so the pool must open a second one.
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var timeout int
		if err := conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != BusyTimeoutMS {
			t.Fatalf("connection %d timeout=%d", i, timeout)
		}
	}
	read, err := BeginRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	// A read-only BEGIN must not reserve the writer slot.
	write, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	write.Rollback()
}
