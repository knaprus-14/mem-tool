package webjobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func process(t *testing.T, s *Service, q *Queue, j Job) Job {
	t.Helper()
	if worked, err := s.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("process: %t %v", worked, err)
	}
	got, err := q.Get(context.Background(), workspace.Actor{ID: "alice"}, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRevisionPreservesIdentityHistoryOriginalAndEntryIDs(t *testing.T) {
	_, w, q, s := fixture(t)
	model(t, w)
	ctx := context.Background()
	actor := workspace.Actor{ID: "alice"}
	original := process(t, s, q, enqueue(t, q, "first.md", "# First\n\nOriginal text."))
	if original.Status != "succeeded" {
		t.Fatal(original)
	}
	before, err := q.Revision(ctx, actor, original.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqliteutil.Open(mem.StorePathIn(q.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var firstID int64
	if err = db.QueryRow(`SELECT MIN(id) FROM entries`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	job, dup, err := q.EnqueueRevision(ctx, actor, original.DocumentKey, before.State, "second.md", strings.NewReader("# Second\n\nChanged text."), Metadata{Tags: []string{"revision"}})
	if err != nil || dup {
		t.Fatalf("enqueue revision: %+v %t %v", job, dup, err)
	}
	if job.DocumentKey != original.DocumentKey || job.SourceID == original.SourceID || !job.IsRevision {
		t.Fatal(job)
	}
	job = process(t, s, q, job)
	if job.Status != "succeeded" {
		t.Fatal(job)
	}
	after, err := q.Revision(ctx, actor, original.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	if before.State == after.State || after.Filename != "second.md" {
		t.Fatal(after)
	}
	var id, history int
	var documentID, text string
	if err = db.QueryRow(`SELECT MIN(id),MIN(document_id),MIN(text) FROM entries`).Scan(&id, &documentID, &text); err != nil {
		t.Fatal(err)
	}
	if int64(id) != firstID || documentID != "web:"+original.DocumentKey || !strings.Contains(text, "Changed text") {
		t.Fatalf("identity/content: %d %s %s", id, documentID, text)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM document_history_versions WHERE document_id=? AND source_path=?`, documentID, before.replacement.SourcePath).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history: %d %v", history, err)
	}
	for _, j := range []Job{original, job} {
		path, e := q.sourcePath(ctx, j)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(path); e != nil {
			t.Fatal(e)
		}
	}
	library, err := workspace.ReadLibrary(ctx, w, workspace.Query{Kind: "documents"})
	if err != nil || library.DocumentCount != 1 || library.Documents[0].ID != documentID {
		t.Fatalf("library: %+v %v", library, err)
	}
	// Reimporting a retained, older upload may not duplicate the managed identity.
	_, err = mem.ImportDocumentIn(ctx, q.dir, before.replacement.SourcePath, mem.ImportOptions{})
	if !errors.Is(err, mem.ErrDocumentUpdateStateChanged) {
		t.Fatalf("old CLI original: %v", err)
	}
	// Reuploading the current file is a no-op, even under a different filename.
	existing, dup, err := q.EnqueueRevision(ctx, actor, job.DocumentKey, after.State, "same.md", strings.NewReader("# Second\n\nChanged text."), Metadata{})
	if err != nil || !dup || existing.ID != job.ID {
		t.Fatalf("current duplicate: %t %v %+v", dup, err, existing)
	}
}

func TestRevisionConflictAtCommitAndFailureLeaveCurrentDocument(t *testing.T) {
	_, w, q, s := fixture(t)
	calls := model(t, w)
	ctx := context.Background()
	actor := workspace.Actor{ID: "alice"}
	original := process(t, s, q, enqueue(t, q, "original.md", "Original text."))
	pin, err := q.Revision(ctx, actor, original.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqliteutil.Open(mem.StorePathIn(q.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	job, _, err := q.EnqueueRevision(ctx, actor, original.DocumentKey, pin.State, "new.md", strings.NewReader("New revision."), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	// Change metadata after the worker preflight, immediately before the core
	// write. A revision-only token would miss this concurrent CLI-style edit.
	s.Import = func(ctx context.Context, dir, path string, opts mem.ImportOptions) (mem.ImportResult, error) {
		started := opts.Started
		opts.Started = func(id int64) error {
			if _, e := db.Exec(`UPDATE entries SET tags='["concurrent-edit"]'`); e != nil {
				return e
			}
			return started(id)
		}
		return mem.ImportDocumentIn(ctx, dir, path, opts)
	}
	job = process(t, s, q, job)
	if job.Status != "conflict" {
		t.Fatal(job)
	}
	var text, tags string
	var histories int
	if err = db.QueryRow(`SELECT text,tags FROM entries LIMIT 1`).Scan(&text, &tags); err != nil {
		t.Fatal(err)
	}
	if text != "Original text." || tags != `["concurrent-edit"]` {
		t.Fatalf("lost current content: %s %s", text, tags)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM document_history_versions`).Scan(&histories); err != nil || histories != 0 {
		t.Fatalf("failed update archived history: %d %v", histories, err)
	}
	if _, _, err = q.EnqueueRevision(ctx, actor, original.DocumentKey, pin.State, "stale.md", strings.NewReader("stale"), Metadata{}); !errors.Is(err, mem.ErrDocumentUpdateStateChanged) {
		t.Fatalf("stale preview: %v", err)
	}
	pin, err = q.Revision(ctx, actor, original.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := q.EnqueueRevision(ctx, actor, original.DocumentKey, pin.State, "failure.md", strings.NewReader("Provider failure."), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	s.Import = func(context.Context, string, string, mem.ImportOptions) (mem.ImportResult, error) {
		return mem.ImportResult{}, errors.New("secret-provider-error")
	}
	failed = process(t, s, q, failed)
	if failed.Status != "failed" || strings.Contains(failed.Error, "secret-provider-error") {
		t.Fatal(failed)
	}
	after, err := q.Revision(ctx, actor, original.DocumentKey)
	if err != nil || after.State != pin.State {
		t.Fatalf("failure changed state: %+v %v", after, err)
	}
	// A stale queued revision is rejected before requesting embeddings.
	s.Import = mem.ImportDocumentIn
	stale, _, err := q.EnqueueRevision(ctx, actor, original.DocumentKey, pin.State, "stale-queued.md", strings.NewReader("Obsolete queued content."), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE entries SET important=1`); err != nil {
		t.Fatal(err)
	}
	beforeCalls := calls.Load()
	stale = process(t, s, q, stale)
	if stale.Status != "conflict" || calls.Load() != beforeCalls {
		t.Fatal(stale)
	}
}

func TestFolderLabelsAndRevisionOwnership(t *testing.T) {
	_, w, q, s := fixture(t)
	model(t, w)
	ctx := context.Background()
	actor := workspace.Actor{ID: "alice"}
	for _, path := range []string{"../a.md", "/a.md", "x//a.md", "x/../a.md", `C:\a.md`, "x/b.md", "x/\na.md"} {
		if _, _, err := q.Enqueue(ctx, actor, "a.md", strings.NewReader("text"), Metadata{RelativePath: path}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe label %q: %v", path, err)
		}
	}
	one, _, err := q.Enqueue(ctx, actor, "a.md", strings.NewReader("first"), Metadata{RelativePath: "Курс/Глава 1/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := q.Enqueue(ctx, actor, "a.md", strings.NewReader("second"), Metadata{RelativePath: "Курс/Глава 2/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if one.SourceID == two.SourceID || one.SourceLabel == two.SourceLabel {
		t.Fatal("same-name files conflated")
	}
	one = process(t, s, q, one)
	if _, err := q.Revision(ctx, workspace.Actor{ID: "bob"}, one.DocumentKey); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("cross-owner preview: %v", err)
	}
	if _, _, err := q.EnqueueRevision(ctx, workspace.Actor{ID: "bob"}, one.DocumentKey, "bad", "a.md", strings.NewReader("blocked"), Metadata{}); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("cross-owner revision: %v", err)
	}
}

func TestQueueV1MigrationPreservesJobsAndReadOnlyView(t *testing.T) {
	_, w, q, s := fixture(t)
	model(t, w)
	j := enqueue(t, q, "old.md", "Alpha two upload.")
	// Recreate the exact alpha.2 schema around its existing original/job rows.
	_, err := q.db.Exec(`DROP INDEX sources_fingerprint; DROP INDEX sources_document;
ALTER TABLE sources DROP COLUMN document_key; ALTER TABLE jobs DROP COLUMN replacement;
CREATE UNIQUE INDEX sources_legacy_unique ON sources(owner,sha256,format); PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	q.Close()
	ro, err := Open(context.Background(), w, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ro.Get(context.Background(), workspace.Actor{ID: "alice"}, j.ID)
	if err != nil || got.DocumentKey != j.SourceID || ro.version != 1 {
		t.Fatalf("v1 read: %+v %v", got, err)
	}
	ro.Close()
	rw, err := Open(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	got = process(t, s, rw, got)
	if got.Status != "succeeded" || got.DocumentKey != j.SourceID {
		t.Fatal(got)
	}
	if _, err = rw.sourcePath(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if _, err = rw.db.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if unsupported, err := Open(context.Background(), w, true); err == nil {
		unsupported.Close()
		t.Fatal("future schema accepted")
	}
	if _, err = os.Stat(filepath.Join(w.Root, ".mem", "web-jobs.db")); err != nil {
		t.Fatal(err)
	}
}

func TestRevisionReceiptSurvivesRestartAndHistoricalBytesCanBeReused(t *testing.T) {
	_, w, q, s := fixture(t)
	calls := model(t, w)
	ctx := context.Background()
	actor := workspace.Actor{ID: "alice"}
	original := process(t, s, q, enqueue(t, q, "original.md", "Original content."))
	pin, err := q.Revision(ctx, actor, original.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := q.EnqueueRevision(ctx, actor, original.DocumentKey, pin.State, "new.md", strings.NewReader("New content."), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.claim(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	path, err := q.sourcePath(ctx, claimed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mem.ImportDocumentIn(ctx, q.dir, path, mem.ImportOptions{DocumentID: "web:" + claimed.DocumentKey, Replacement: claimed.Replacement, Started: func(id int64) error { return q.receipt(ctx, claimed, id) }})
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err = s.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := q.Get(ctx, actor, j.ID)
	if err != nil || got.Status != "succeeded" || calls.Load() != before {
		t.Fatalf("revision recovery: %+v %v", got, err)
	}
	current, err := q.Revision(ctx, actor, j.DocumentKey)
	if err != nil {
		t.Fatal(err)
	}
	// Reusing old content after another revision is an explicit new operation,
	// not a deduplication hit that silently leaves the wrong revision current.
	restore, dup, err := q.EnqueueRevision(ctx, actor, j.DocumentKey, current.State, "restored.md", strings.NewReader("Original content."), Metadata{})
	if err != nil || dup {
		t.Fatalf("historical bytes: %t %v", dup, err)
	}
	restore = process(t, s, q, restore)
	if restore.Status != "succeeded" {
		t.Fatal(restore)
	}
}
