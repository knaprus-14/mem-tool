package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestWorkspaceOwnershipPersistenceAndNoWorkingDirectorySwitch(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	cwd, _ := os.Getwd()
	m, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	alice := Actor{ID: "alice"}
	bob := Actor{ID: "bob"}
	a, err := m.Create(ctx, alice, "Первая база", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, bob, "Вторая база", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Root == b.Root {
		t.Fatal("workspaces are not isolated")
	}
	if _, err = m.Resolve(ctx, bob, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner resolve: %v", err)
	}
	if _, err = m.Register(ctx, bob, a.Root); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner attach: %v", err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, e := m.Register(ctx, alice, a.Root)
			if e != nil || w.ID != a.ID {
				t.Errorf("concurrent attach: %v %s", e, w.ID)
			}
		}()
	}
	wg.Wait()
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	list, err := m.List(ctx, alice)
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("registry persistence: %+v %v", list, err)
	}
	after, _ := os.Getwd()
	if cwd != after {
		t.Fatalf("cwd changed: %s -> %s", cwd, after)
	}
	if _, err = m.Create(ctx, Actor{ID: "../escape"}, "bad", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid owner: %v", err)
	}
}

func TestLibraryIsReadOnlyPaginatedAndObservesCLIChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, ".mem")
	if err := mem.InitMemIn(dir, "Existing"); err != nil {
		t.Fatal(err)
	}
	s, err := mem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for range 35 {
		if _, err = s.Add("Текст заметки", "Заголовок", []string{"тест"}, "ollama", []float32{1, 0}, false); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if _, err = s.AddChunk("Содержимое файла", "Книга", nil, "ollama", []float32{1, 0}, filepath.Join(root, "книга.txt"), "1", i, 3, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqliteutil.Open(mem.StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO document_import_runs(requested_path,status,started_at) VALUES('inflight.pdf','running','2026-09-09T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(mem.StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	w, err := m.Register(ctx, Actor{ID: "local"}, root)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := ReadLibrary(ctx, w, Query{Kind: "documents", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if docs.EntryCount != 38 || docs.DocumentCount != 1 || docs.NoteCount != 35 || len(docs.Documents) != 1 {
		t.Fatalf("counts: %+v", docs)
	}
	page, err := ReadLibrary(ctx, w, Query{Kind: "notes", Offset: 30, Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 35 || len(page.Entries) != 5 || page.Entries[0].Text != "" {
		t.Fatalf("pagination/body omission: %+v", page)
	}
	chunks, err := ReadLibrary(ctx, w, Query{Kind: "entries", Document: docs.Documents[0].ID, Limit: 30})
	if err != nil || len(chunks.Entries) != 3 {
		t.Fatalf("document selection: %+v %v", chunks, err)
	}
	for i, e := range chunks.Entries {
		if e.ID != int64(36+i) {
			t.Fatalf("source order lost: %+v", chunks.Entries)
		}
	}
	entry, err := ReadEntry(ctx, w, 1)
	if err != nil || entry.Text != "Текст заметки" {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	after, err := os.ReadFile(mem.StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("viewing mutated source database")
	}
	db, err = sqliteutil.Open(mem.StorePathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	if err = db.QueryRow(`SELECT status FROM document_import_runs`).Scan(&status); err != nil || status != "running" {
		t.Fatalf("view recovered another process job: %s %v", status, err)
	}
	if _, err = db.Exec(`UPDATE entries SET text='Обновлено внешним процессом' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	entry, err = ReadEntry(ctx, w, 1)
	if err != nil || entry.Text != "Обновлено внешним процессом" {
		t.Fatalf("stale view: %+v %v", entry, err)
	}
}

func TestCreateNeverOverwritesExistingMemDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".mem")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "store.db")
	want := []byte("preserve existing database")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = m.Create(context.Background(), Actor{ID: "local"}, "Replace", root); err == nil {
		t.Fatal("overwrote .mem")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("source changed: %q %v", got, err)
	}
}
