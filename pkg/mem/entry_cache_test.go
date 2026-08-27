package mem

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEntryCacheGenerationTracksEveryRowMutation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial, err := loadEntryCacheGeneration(store.db)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Add("baseline", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE entries SET important = 1 WHERE id = ?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	final, err := loadEntryCacheGeneration(store.db)
	if err != nil {
		t.Fatal(err)
	}
	if final != initial+3 {
		t.Fatalf("generation=%d, want %d after insert/update/delete", final, initial+3)
	}
}

func TestEntryMutationTransactionRefreshesExternalCacheBeforeWriter(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	external, err := storeB.Add("external", "", nil, "test", []float32{0, 1}, false)
	if err != nil {
		t.Fatal(err)
	}

	storeA.mu.Lock()
	tx, cached, err := storeA.beginEntryMutationTx("test mutation")
	if err != nil {
		storeA.mu.Unlock()
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
		storeA.mu.Unlock()
	}()
	if len(cached) != 1 || cached[0].ID != external.ID || cached[0].Text != external.Text {
		t.Fatalf("external entry was not loaded before writer transaction: %#v", cached)
	}
	databaseGeneration, err := loadEntryCacheGeneration(tx)
	if err != nil {
		t.Fatal(err)
	}
	if databaseGeneration != storeA.entryGeneration {
		t.Fatalf("writer snapshot generation=%d, cache generation=%d", databaseGeneration, storeA.entryGeneration)
	}
}

func TestEntryMutationTransactionRefreshesLocalCRUDWatermark(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry, err := store.Add("before", "", nil, "test", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateById(entry.ID, "after", "", nil, []float32{0, 1}); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	tx, cached, err := store.beginEntryMutationTx("test local CRUD")
	if err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].ID != entry.ID || cached[0].Text != "after" {
		_ = tx.Rollback()
		store.mu.Unlock()
		t.Fatalf("local CRUD cache refresh=%#v", cached)
	}
	if err := tx.Rollback(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	if err := store.DeleteById(entry.ID); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	tx, cached, err = store.beginEntryMutationTx("test local delete")
	if err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	if len(cached) != 0 {
		_ = tx.Rollback()
		store.mu.Unlock()
		t.Fatalf("deleted entry survived generation refresh: %#v", cached)
	}
	if err := tx.Rollback(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
}

func TestReplaceSourceInEntryCachePreservesUnrelatedEntries(t *testing.T) {
	cached := []Entry{
		{ID: 1, SourceFile: "manual", Embedding: []float32{1}},
		{ID: 2, SourceFile: "document", Embedding: []float32{2}},
		{ID: 5, SourceFile: "other", Embedding: []float32{5}},
	}
	replacement := []Entry{
		{ID: 4, SourceFile: "document", Embedding: []float32{4}},
		{ID: 3, SourceFile: "document", Embedding: []float32{3}},
	}
	entries, vectors := replaceSourceInEntryCache(cached, "document", replacement)
	if len(entries) != 4 || len(vectors) != 4 {
		t.Fatalf("cache sizes entries=%d vectors=%d", len(entries), len(vectors))
	}
	wantIDs := []int64{1, 3, 4, 5}
	for i, want := range wantIDs {
		if entries[i].ID != want || len(vectors[i]) != 1 || vectors[i][0] != float32(want) {
			t.Fatalf("cache[%d]=%#v vector=%v, want id %d", i, entries[i], vectors[i], want)
		}
	}
}

func TestReplaceSourceInEntryCacheUsesHostPathIdentity(t *testing.T) {
	source := filepath.Join(t.TempDir(), "Document.md")
	caseVariant := strings.ToUpper(source)
	cached := []Entry{
		{ID: 1, SourceFile: caseVariant, Embedding: []float32{1}},
		{ID: 2, SourceFile: filepath.Join(filepath.Dir(source), "other.md"), Embedding: []float32{2}},
	}
	replacement := []Entry{{ID: 3, SourceFile: source, Embedding: []float32{3}}}
	entries, _ := replaceSourceInEntryCache(cached, source, replacement)
	if runtime.GOOS == "windows" {
		if len(entries) != 2 || entries[0].ID != 2 || entries[1].ID != 3 {
			t.Fatalf("Windows case-variant source survived replacement: %#v", entries)
		}
		return
	}
	if len(entries) != 3 || entries[0].ID != 1 || entries[1].ID != 2 || entries[2].ID != 3 {
		t.Fatalf("case-sensitive host conflated distinct source paths: %#v", entries)
	}
}

func TestDocumentReplacementNormalizesWindowsSourcePathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path identity is case-insensitive")
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(t.TempDir(), "Document.pdf")
	upperSource := strings.ToUpper(source)
	initial := validStructuredChunks()
	for i := range initial {
		initial[i].Provenance.SourcePath = upperSource
		initial[i].Provenance.DocumentID = documentIDForSourcePath(upperSource)
	}
	if err := store.ReplaceDocumentChunks(upperSource, initial); err != nil {
		t.Fatal(err)
	}
	replacement := changedRevisionChunks(initial, "case-normalized revision", "case-normalized text")
	for i := range replacement {
		replacement[i].Provenance.SourcePath = source
		replacement[i].Provenance.DocumentID = documentIDForSourcePath(source)
	}
	if err := store.ReplaceDocumentChunks(source, replacement); err != nil {
		t.Fatal(err)
	}

	var rows, spellings int
	if err := store.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT source_file) FROM entries WHERE LOWER(source_file) = LOWER(?)`, source).
		Scan(&rows, &spellings); err != nil {
		t.Fatal(err)
	}
	if rows != len(replacement) || spellings != 1 {
		t.Fatalf("database retained path-case duplicates: rows=%d spellings=%d", rows, spellings)
	}
	entries := store.GetBySourceFile(source)
	if len(entries) != len(replacement) {
		t.Fatalf("cache contains %d chunks, want %d: %#v", len(entries), len(replacement), entries)
	}
	for _, entry := range entries {
		if entry.SourceFile != source || entry.SourcePath != source {
			t.Fatalf("source path was not normalized to the replacement spelling: %#v", entry)
		}
	}
}
