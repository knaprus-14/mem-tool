package mem

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noteText(s string) *string { return &s }

func TestNoteServiceAtomicEditsRetriesAndCache(t *testing.T) {
	dir, s, cfg := queryFixture(t)
	var calls atomic.Int32
	var currentID atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct{ Prompt string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Prompt == "fail" {
			http.Error(w, "PRIVATE PROVIDER DETAIL", 500)
			return
		}
		if body.Prompt == "race" {
			if _, err := s.ToggleImportant(currentID.Load()); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embedding":[1,0]}`))
	}))
	defer provider.Close()
	cfg.Ollama.BaseURL = provider.URL
	cfg.Ollama.Model = "fixture-embed"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	input := NoteInput{RequestID: strings.Repeat("a", 32), Text: noteText("needle note"), Title: "Initial", Tags: []string{" first ", "first"}}
	created, err := SaveNoteIn(context.Background(), dir, input)
	if err != nil {
		t.Fatal(err)
	}
	currentID.Store(created.ID)
	retry, err := SaveNoteIn(context.Background(), dir, input)
	if err != nil || retry.ID != created.ID || !retry.Replayed || calls.Load() != 1 {
		t.Fatalf("retry: %+v %v calls=%d", retry, err, calls.Load())
	}
	different := input
	different.Title = "changed body"
	if _, err = SaveNoteIn(context.Background(), dir, different); !errors.Is(err, ErrNoteRequest) {
		t.Fatalf("reused request: %v", err)
	}
	note, err := ReadNoteIn(context.Background(), dir, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.GetByID(note.ID)
	if err != nil || entry.EmbeddingModel != "fixture-embed" || len(entry.Tags) != 1 {
		t.Fatalf("CLI cache: %+v %v", entry, err)
	}
	edit := NoteInput{ID: note.ID, Pin: note.Pin, RequestID: strings.Repeat("b", 32), Title: "Changed metadata", Tags: []string{"keep"}, Important: true}
	if _, err = SaveNoteIn(context.Background(), dir, edit); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("metadata invoked model")
	}
	updated, err := ReadNoteIn(context.Background(), dir, note.ID)
	if err != nil || updated.Pin == note.Pin || updated.Text != note.Text || !updated.Important {
		t.Fatalf("metadata: %+v %v", updated, err)
	}
	entry, err = s.GetByID(note.ID)
	if err != nil || entry.Title != "Changed metadata" || entry.EmbeddingSpace == "" || !entry.Important {
		t.Fatalf("cached metadata: %+v %v", entry, err)
	}
	stale := edit
	stale.RequestID = strings.Repeat("c", 32)
	if _, err = SaveNoteIn(context.Background(), dir, stale); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("stale: %v", err)
	}
	edit.Pin = updated.Pin
	edit.RequestID = strings.Repeat("d", 32)
	edit.Text = noteText("fail")
	if _, err = SaveNoteIn(context.Background(), dir, edit); !errors.Is(err, ErrQueryEmbedding) {
		t.Fatalf("model failure: %v", err)
	}
	still, err := ReadNoteIn(context.Background(), dir, note.ID)
	if err != nil || still.Pin != updated.Pin {
		t.Fatal("model failure changed note")
	}
	edit.RequestID = strings.Repeat("e", 32)
	edit.Text = noteText("race")
	if _, err = SaveNoteIn(context.Background(), dir, edit); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("concurrent metadata: %v", err)
	}
	latest, err := ReadNoteIn(context.Background(), dir, note.ID)
	if err != nil || latest.Text != note.Text || latest.Important {
		t.Fatalf("race overwrote note: %+v %v", latest, err)
	}
	edit.Pin = latest.Pin
	edit.RequestID = strings.Repeat("f", 32)
	edit.Text = noteText("new searchable wording")
	if _, err = SaveNoteIn(context.Background(), dir, edit); err != nil {
		t.Fatal(err)
	}
	result, err := QueryIn(context.Background(), dir, QueryRequest{Query: "searchable", Mode: "text", Tags: []string{"keep"}}, false)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].ID != note.ID {
		t.Fatalf("search after edit: %+v %v", result, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate note: %d %v", count, err)
	}
	// A receipt failure must roll back the entry itself.
	if _, err = s.db.Exec(`CREATE TRIGGER fail_web_note_receipt BEFORE INSERT ON web_note_writes BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	input.RequestID = strings.Repeat("1", 32)
	if _, err = SaveNoteIn(context.Background(), dir, input); err == nil {
		t.Fatal("receipt failure accepted")
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil || count != 1 {
		t.Fatal("receipt failure left entry")
	}
}

func TestNoteServiceCancellationBoundsAndAnchoredProtection(t *testing.T) {
	dir, s, cfg := queryFixture(t)
	entry, err := s.Add(strings.Repeat("long", 600), "Long", nil, "legacy", []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	note, err := ReadNoteIn(context.Background(), dir, entry.ID)
	if err != nil || note.TextEditable || len(note.Text) != 2400 {
		t.Fatalf("long note: %+v %v", note, err)
	}
	input := NoteInput{ID: note.ID, Pin: note.Pin, RequestID: strings.Repeat("a", 32), Title: "Metadata only", Tags: []string{"long"}}
	if _, err = SaveNoteIn(context.Background(), dir, input); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetByID(entry.ID)
	if err != nil || after.Text != entry.Text || after.EmbeddingModel != entry.EmbeddingModel {
		t.Fatal("long note truncated or vector changed")
	}
	if _, err = s.db.Exec(`UPDATE entries SET document_id='source-anchored' WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadNoteIn(context.Background(), dir, entry.ID); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("anchored read: %v", err)
	}
	input.RequestID = strings.Repeat("b", 32)
	if _, err = SaveNoteIn(context.Background(), dir, input); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("anchored write: %v", err)
	}
	started := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		close(started)
		<-r.Context().Done()
	}))
	defer provider.Close()
	cfg.Ollama.BaseURL = provider.URL
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	input = NoteInput{RequestID: strings.Repeat("c", 32), Text: noteText("cancelled note")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := SaveNoteIn(ctx, dir, input); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model not called")
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel hung")
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count)
	if count != 1 {
		t.Fatal("cancel saved note")
	}
	input.Text = noteText(strings.Repeat("x", 2001))
	if _, err = SaveNoteIn(context.Background(), dir, input); !errors.Is(err, ErrNoteInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err = SaveNoteIn(context.Background(), missing, NoteInput{RequestID: strings.Repeat("d", 32), Text: noteText("note")}); err == nil {
		t.Fatal("missing base accepted")
	}
	if _, err = os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing base created")
	}
}
