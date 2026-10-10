package mem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func indexFixture(t *testing.T) (string, *Store, *Config, WebIndexState) {
	t.Helper()
	dir, s, cfg := queryFixture(t)
	for _, text := range []string{"first fragment", "second fragment"} {
		if _, err := s.Add(text, "title", []string{"keep"}, "old", []float32{1, 0}, true); err != nil {
			t.Fatal(err)
		}
	}
	state, err := StartWebIndexIn(context.Background(), dir, "alice", WebIndexInput{RequestID: strings.Repeat("a", 32), ConfigRevision: cfg.Revision()})
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, cfg, state
}
func TestWebIndexPrepareResumeAtomicApplyRollback(t *testing.T) {
	dir, s, cfg, state := indexFixture(t)
	ctx := context.Background()
	initial, err := loadEntryCacheGeneration(s.db)
	if err != nil {
		t.Fatal(err)
	}
	model := func(context.Context, *Config, string) ([]float32, error) { return []float32{0, 1, 0}, nil }
	if worked, err := processWebIndexIn(ctx, dir, "alice", model); !worked || err != nil {
		t.Fatal(worked, err)
	}
	// Every call opens a new connection, just as after a server restart.
	partial, err := ReadWebIndexIn(ctx, dir, "alice")
	if err != nil || partial.Completed != 1 || partial.Status != "preparing" {
		t.Fatal(partial, err)
	}
	if _, err = ChangeWebIndexIn(ctx, dir, "alice", "apply", WebIndexInput{ID: state.ID}); !errors.Is(err, ErrWebIndexConflict) {
		t.Fatal("partial published", err)
	}
	if _, err = processWebIndexIn(ctx, dir, "alice", model); err != nil {
		t.Fatal(err)
	}
	ready, err := ReadWebIndexIn(ctx, dir, "alice")
	if err != nil || ready.Status != "ready" || ready.Completed != 2 {
		t.Fatal(ready, err)
	}
	if g, _ := loadEntryCacheGeneration(s.db); g != initial {
		t.Fatal("preparation changed entries")
	}
	// Failing the final state write rolls back all entry changes as well.
	if _, err = s.db.Exec(`CREATE TRIGGER fail_index_apply BEFORE UPDATE ON web_index_generation WHEN NEW.status='applied' BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = ChangeWebIndexIn(ctx, dir, "alice", "apply", WebIndexInput{ID: state.ID}); err == nil {
		t.Fatal("injected failure ignored")
	}
	if g, _ := loadEntryCacheGeneration(s.db); g != initial {
		t.Fatal("partial generation after failure")
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_index_apply`); err != nil {
		t.Fatal(err)
	}
	applied, err := ChangeWebIndexIn(ctx, dir, "alice", "apply", WebIndexInput{ID: state.ID})
	if err != nil || !applied.CanRollback {
		t.Fatal(applied, err)
	}
	identity, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchInEmbeddingSpace([]float32{0, 1, 0}, identity.Backend, identity.SpaceID, 10)
	if err != nil || len(hits) != 2 {
		t.Fatal("long-lived CLI cache did not see the new generation", len(hits), err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE dims=3 AND backend=? AND text IN ('first fragment','second fragment') AND title='title' AND important=1`, cfg.Backend).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	replay, err := ChangeWebIndexIn(ctx, dir, "alice", "apply", WebIndexInput{ID: state.ID})
	if err != nil || replay.AppliedGeneration != applied.AppliedGeneration {
		t.Fatal("apply replay", replay, err)
	}
	rolled, err := ChangeWebIndexIn(ctx, dir, "alice", "rollback", WebIndexInput{ID: state.ID})
	if err != nil || rolled.Status != "rolled_back" {
		t.Fatal(rolled, err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE dims=2 AND backend='old'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("rollback data", count, err)
	}
	if _, err = StartWebIndexIn(ctx, dir, "alice", WebIndexInput{RequestID: state.ID, ConfigRevision: cfg.Revision()}); err != nil {
		t.Fatal("start replay", err)
	}
}

func TestWebIndexInterruptedCallRemainsResumable(t *testing.T) {
	dir, _, _, _ := indexFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := processWebIndexIn(ctx, dir, "alice", func(context.Context, *Config, string) ([]float32, error) {
		cancel()
		return nil, context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := ReadWebIndexIn(context.Background(), dir, "alice")
	if err != nil || state.Status != "preparing" || state.Completed != 0 {
		t.Fatal(state, err)
	}
	for i := 0; i < 2; i++ {
		if _, err = processWebIndexIn(context.Background(), dir, "alice", func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	state, err = ReadWebIndexIn(context.Background(), dir, "alice")
	if err != nil || state.Status != "ready" || state.Completed != 2 {
		t.Fatal(state, err)
	}
}

func TestWebIndexChangesCancellationAndOwner(t *testing.T) {
	for _, scenario := range []string{"entry", "config", "cancel", "dimensions", "model", "zero"} {
		t.Run(scenario, func(t *testing.T) {
			dir, s, cfg, state := indexFixture(t)
			ctx := context.Background()
			if foreign, err := ReadWebIndexIn(ctx, dir, "bob"); err != nil || foreign.Status != "none" {
				t.Fatal("owner read", foreign, err)
			}
			if _, err := ChangeWebIndexIn(ctx, dir, "bob", "cancel", WebIndexInput{ID: state.ID}); !errors.Is(err, ErrWebIndexConflict) {
				t.Fatal("owner mutation", err)
			}
			calls := 0
			model := func(context.Context, *Config, string) ([]float32, error) {
				calls++
				if scenario == "cancel" {
					if _, e := ChangeWebIndexIn(ctx, dir, "alice", "cancel", WebIndexInput{ID: state.ID}); e != nil {
						t.Fatal(e)
					}
				}
				if scenario == "zero" {
					return []float32{0, 0}, nil
				}
				if scenario == "model" {
					return nil, errors.New("SECRET endpoint failure")
				}
				if scenario == "dimensions" && calls == 2 {
					return []float32{1, 0, 1}, nil
				}
				return []float32{0, 1}, nil
			}
			if scenario == "entry" {
				if _, err := s.db.Exec(`UPDATE entries SET title='changed'`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "config" {
				cfg.Ollama.Model = "another-model"
				if err := SaveConfigIn(dir, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := processWebIndexIn(ctx, dir, "alice", model); err != nil {
				t.Fatal(err)
			}
			if scenario == "dimensions" {
				if _, err := processWebIndexIn(ctx, dir, "alice", model); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadWebIndexIn(ctx, dir, "alice")
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if scenario == "cancel" {
				want = "cancelled"
			}
			if got.Status != want || strings.Contains(got.Error, "SECRET") {
				t.Fatal(got)
			}
			if (scenario == "entry" || scenario == "config") && calls != 0 {
				t.Fatal("stale input sent to model")
			}
			var count int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE backend='old'`).Scan(&count); err != nil || count != 2 {
				t.Fatal("active vectors changed", count, err)
			}
		})
	}
}

func TestWebIndexRejectsRollbackOverNewEdits(t *testing.T) {
	dir, s, _, state := indexFixture(t)
	ctx := context.Background()
	model := func(context.Context, *Config, string) ([]float32, error) { return []float32{0, 1}, nil }
	for i := 0; i < 2; i++ {
		if _, err := processWebIndexIn(ctx, dir, "alice", model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ChangeWebIndexIn(ctx, dir, "alice", "apply", WebIndexInput{ID: state.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE entries SET text='new note' WHERE id=(SELECT MIN(id) FROM entries)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ChangeWebIndexIn(ctx, dir, "alice", "rollback", WebIndexInput{ID: state.ID}); !errors.Is(err, ErrWebIndexConflict) {
		t.Fatal(err)
	}
}
