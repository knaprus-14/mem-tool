package mem

import "testing"

func TestSingleEntryMutationsKeepCacheGenerationCurrent(t *testing.T) {
	dir := t.TempDir()
	a, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	foreign, err := b.Add("foreign", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	local, err := a.Add("local", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := loadEntryCacheGeneration(a.db)
	if err != nil || a.entryGeneration != gen || len(a.entries) != 2 {
		t.Fatalf("add cache not pinned: generation=%d want=%d entries=%d err=%v", a.entryGeneration, gen, len(a.entries), err)
	}
	if err := a.DeleteById(foreign.ID); err != nil {
		t.Fatal(err)
	}
	gen, err = loadEntryCacheGeneration(a.db)
	if err != nil || a.entryGeneration != gen || len(a.entries) != 1 || a.entries[0].ID != local.ID {
		t.Fatalf("delete cache not pinned: %+v %v", a.entries, err)
	}
}

func TestFTSSyncTouchesOnlyChangedRows(t *testing.T) {
	store := newSearchTestStore(t)
	defer store.Close()
	a, err := store.Add("untouched", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Add("before", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	// A regular table gives us an audit trigger without coupling the test to
	// private FTS5 shadow-table internals. The production synchronization SQL is unchanged.
	if _, err := store.db.Exec(`DROP TABLE entries_fts;
CREATE TABLE entries_fts(entry_id INTEGER, title TEXT, text TEXT, tags TEXT);`); err != nil {
		t.Fatal(err)
	}
	if err := store.rebuildFTSLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE fts_audit(entry_id INTEGER);
CREATE TRIGGER fts_audit_insert AFTER INSERT ON entries_fts BEGIN INSERT INTO fts_audit VALUES(NEW.entry_id); END;
CREATE TRIGGER fts_audit_delete AFTER DELETE ON entries_fts BEGIN INSERT INTO fts_audit VALUES(OLD.entry_id); END;`); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateById(b.ID, "after", "", nil, []float32{1}); err != nil {
		t.Fatal(err)
	}
	if err := store.rebuildFTSLocked(); err != nil {
		t.Fatal(err)
	}
	var touched, other int
	if err := store.db.QueryRow(`SELECT count(*), count(CASE WHEN entry_id != ? THEN 1 END) FROM fts_audit`, b.ID).Scan(&touched, &other); err != nil {
		t.Fatal(err)
	}
	if touched != 2 || other != 0 {
		t.Fatalf("delta touched %d rows, including %d unrelated changes (untouched ID %d)", touched, other, a.ID)
	}
	if err := store.DeleteById(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.rebuildFTSLocked(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM entries_fts WHERE entry_id=?`, b.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted FTS row survived: %d %v", count, err)
	}
}
