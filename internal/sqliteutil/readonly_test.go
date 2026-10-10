package sqliteutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyCannotCreateOrWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	if db, err := OpenReadOnly(ctx, path); err == nil {
		db.Close()
		t.Fatal("created missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected file: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE sample(value TEXT); INSERT INTO sample VALUES('preserved')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE sample SET value='changed'`); err == nil {
		t.Fatal("read-only connection accepted write")
	}
	var value string
	if err = db.QueryRow(`SELECT value FROM sample`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}
