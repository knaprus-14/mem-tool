package webjobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func fixture(t *testing.T) (*workspace.Manager, workspace.Workspace, *Queue, *Service) {
	t.Helper()
	m, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	w, err := m.Create(context.Background(), workspace.Actor{ID: "alice"}, "Тест импорта", "")
	if err != nil {
		t.Fatal(err)
	}
	q, err := Open(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return m, w, q, New(m, []workspace.Actor{{ID: "alice"}})
}
func enqueue(t *testing.T, q *Queue, name, text string) Job {
	t.Helper()
	j, _, err := q.Enqueue(context.Background(), workspace.Actor{ID: "alice"}, name, strings.NewReader(text), Metadata{Tags: []string{"проверка"}})
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func model(t *testing.T, w workspace.Workspace) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"embedding":[0.2,0.4,0.8]}`)
	}))
	t.Cleanup(server.Close)
	cfg, err := mem.LoadConfigIn(filepath.Join(w.Root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ollama.BaseURL = server.URL
	cfg.Ollama.Model = "test-embedding"
	cfg.Chunking.MaxSize = 200
	cfg.Chunking.Overlap = 20
	if err = mem.SaveConfigIn(filepath.Join(w.Root, ".mem"), cfg); err != nil {
		t.Fatal(err)
	}
	return calls
}

func TestUploadPersistsDeduplicatesAndIsolates(t *testing.T) {
	_, w, q, _ := fixture(t)
	actor := workspace.Actor{ID: "alice"}
	j := enqueue(t, q, "Документ.md", "# Заголовок\n\nТекст документа.")
	duplicate, exists, err := q.Enqueue(context.Background(), actor, "Другое имя.md", strings.NewReader("# Заголовок\n\nТекст документа."), Metadata{})
	if err != nil || !exists || duplicate.ID != j.ID {
		t.Fatalf("duplicate: %+v %t %v", duplicate, exists, err)
	}
	if _, err = q.Get(context.Background(), workspace.Actor{ID: "bob"}, j.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if _, err = q.Change(context.Background(), workspace.Actor{ID: "bob"}, j.ID, "cancel"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("cross-owner mutation: %v", err)
	}
	q.Close()
	q2, err := Open(context.Background(), w, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	got, err := q2.Get(context.Background(), actor, j.ID)
	if err != nil || got.Status != "queued" || filepath.IsAbs(got.Path) {
		t.Fatalf("persistent job: %+v %v", got, err)
	}
	if _, err = q2.sourcePath(context.Background(), got); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndImportKeepsLegacyDataAndManagedIdentity(t *testing.T) {
	_, w, q, s := fixture(t)
	calls := model(t, w)
	actor := workspace.Actor{ID: "alice"}
	db, err := sqliteutil.Open(mem.StorePathIn(filepath.Join(w.Root, ".mem")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Opening a background writer must not claim another import was interrupted.
	if _, err = db.Exec(`INSERT INTO document_import_runs(requested_path,status,owner_pid,started_at) VALUES('other-import','running',0,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, q, "Источник.md", "<!-- page: 1 -->\n\n# Первая глава\n\nСодержимое первой страницы.\n\n<!-- page: 2 -->\n\nТекст второй страницы.")
	if worked, err := s.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("process: %v %v", worked, err)
	}
	j, err = q.Get(context.Background(), actor, j.ID)
	if err != nil || j.Status != "succeeded" || j.Chunks < 2 || j.RunID == 0 {
		t.Fatalf("result: %+v %v", j, err)
	}
	if calls.Load() < 2 {
		t.Fatalf("embedding not exercised: %d", calls.Load())
	}
	var id string
	var count int
	if err = db.QueryRow(`SELECT MIN(document_id),COUNT(*) FROM entries`).Scan(&id, &count); err != nil {
		t.Fatal(err)
	}
	if id != "web:"+j.SourceID || count != j.Chunks {
		t.Fatalf("document identity/count: %s %d", id, count)
	}
	var otherStatus string
	if err = db.QueryRow(`SELECT status FROM document_import_runs WHERE requested_path='other-import'`).Scan(&otherStatus); err != nil || otherStatus != "running" {
		t.Fatalf("another run changed: %s %v", otherStatus, err)
	}
	path, err := q.sourcePath(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	// The normal importer retains the assigned identity on later CLI reimport.
	result, err := mem.ImportDocumentIn(context.Background(), filepath.Join(w.Root, ".mem"), path, mem.ImportOptions{})
	if err != nil || result.DocumentID != id {
		t.Fatalf("reimport lost identity: %+v %v", result, err)
	}
	var after int
	if err = db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&after); err != nil || after != count {
		t.Fatalf("duplicate entries: %d %v", after, err)
	}
}

func TestCommitReceiptRecoversWithoutReembedding(t *testing.T) {
	_, w, q, s := fixture(t)
	calls := model(t, w)
	actor := workspace.Actor{ID: "alice"}
	j := enqueue(t, q, "receipt.md", "# Receipt\n\nA committed document.")
	claimed, err := q.claim(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	path, err := q.sourcePath(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mem.ImportDocumentIn(context.Background(), filepath.Join(w.Root, ".mem"), path, mem.ImportOptions{DocumentID: "web:" + j.SourceID, Started: func(id int64) error { return q.receipt(context.Background(), claimed, id) }})
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	// Simulate process exit after the atomic document commit but before finish.
	s.Import = func(context.Context, string, string, mem.ImportOptions) (mem.ImportResult, error) {
		t.Error("committed import executed again")
		return mem.ImportResult{}, errors.New("unexpected")
	}
	if _, err = s.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := q.Get(context.Background(), actor, j.ID)
	if err != nil || got.Status != "succeeded" || got.Chunks == 0 || calls.Load() != before {
		t.Fatalf("receipt recovery: %+v %v", got, err)
	}
}

func TestInterruptedJobRequiresExplicitRetry(t *testing.T) {
	_, _, q, s := fixture(t)
	actor := workspace.Actor{ID: "alice"}
	j := enqueue(t, q, "interrupted.md", "Interrupted test")
	if _, err := q.claim(context.Background(), actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := q.Get(context.Background(), actor, j.ID)
	if err != nil || got.Status != "interrupted" {
		t.Fatalf("interrupted: %+v %v", got, err)
	}
	got, err = q.Change(context.Background(), actor, j.ID, "retry")
	if err != nil || got.Status != "queued" || got.Attempt != 2 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	if _, err = q.Change(context.Background(), actor, j.ID, "retry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate retry: %v", err)
	}
}

func TestWorkerLockAndCancellation(t *testing.T) {
	m, _, q, s := fixture(t)
	actor := workspace.Actor{ID: "alice"}
	j := enqueue(t, q, "cancel.md", "Cancel test")
	started := make(chan struct{})
	done := make(chan error, 1)
	s.Import = func(ctx context.Context, _ string, _ string, _ mem.ImportOptions) (mem.ImportResult, error) {
		close(started)
		<-ctx.Done()
		return mem.ImportResult{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _, e := s.ProcessNext(ctx); done <- e }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	s2 := New(m, []workspace.Actor{actor})
	if worked, e := s2.ProcessNext(ctx); e != nil || worked {
		t.Fatalf("second worker entered: %t %v", worked, e)
	}
	if _, e := q.Change(ctx, actor, j.ID, "cancel"); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("cancellation did not stop worker")
	}
	got, e := q.Get(context.Background(), actor, j.ID)
	if e != nil || got.Status != "cancelled" {
		t.Fatalf("cancelled: %+v %v", got, e)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestUploadValidationSizeAndQueueLimits(t *testing.T) {
	_, _, q, _ := fixture(t)
	actor := workspace.Actor{ID: "alice"}
	for _, name := range []string{"../escape.md", `a\escape.md`, "bad.exe", "CON.md", "bad:.md", "file.md."} {
		if _, _, err := q.Enqueue(context.Background(), actor, name, strings.NewReader("text"), Metadata{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
	if _, _, err := q.Enqueue(context.Background(), actor, "large.md", io.LimitReader(zeroReader{}, MaxFileBytes+1), Metadata{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("size limit: %v", err)
	}
	for i := 0; i < MaxPending; i++ {
		enqueue(t, q, fmt.Sprintf("doc%d.md", i), fmt.Sprintf("text %d", i))
	}
	if _, _, err := q.Enqueue(context.Background(), actor, "full.md", strings.NewReader("over capacity"), Metadata{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("queue limit: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(q.dir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != MaxPending {
		t.Fatalf("rejected files left temporary data: %d", len(entries))
	}
}

func TestChangedOriginalFailsWithoutModelCall(t *testing.T) {
	_, _, q, s := fixture(t)
	j := enqueue(t, q, "changed.md", "original")
	if err := os.WriteFile(filepath.Join(q.dir, "uploads", filepath.FromSlash(j.Path)), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Import = func(context.Context, string, string, mem.ImportOptions) (mem.ImportResult, error) {
		t.Error("changed original reached importer")
		return mem.ImportResult{}, nil
	}
	if _, err := s.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := q.Get(context.Background(), workspace.Actor{ID: "alice"}, j.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("tampering: %+v %v", got, err)
	}
}
