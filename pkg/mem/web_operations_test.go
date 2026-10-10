package mem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebRechunkUsesOriginalPreservesIdentityAndHistory(t *testing.T) {
	ctx := context.Background()
	dir, s, cfg := queryFixture(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 0, 1}})
	}))
	defer model.Close()
	cfg.Ollama.BaseURL = model.URL
	cfg.Chunking = ChunkConfig{MaxSize: 1000, Overlap: 0, Strategy: "fixed"}
	if e := SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "original.md")
	if e := os.WriteFile(path, []byte(strings.Repeat("A useful original sentence. ", 30)), 0600); e != nil {
		t.Fatal(e)
	}
	first, e := ImportDocument(ctx, cfg, s, path, ImportOptions{Title: "Keep title", Tags: []string{"keep"}, Important: true})
	if e != nil {
		t.Fatal(e)
	}
	cfg.Chunking.MaxSize = 100
	if e = SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	in := WebOperationInput{RequestID: strings.Repeat("a", 32), Kind: "rechunk", Document: first.DocumentID, ConfigRevision: cfg.Revision()}
	op, e := StartWebOperationIn(ctx, dir, "alice", in, true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = StartWebOperationIn(ctx, dir, "alice", in, true); e != nil {
		t.Fatal("retry", e)
	}
	if worked, e := ProcessWebOperationIn(ctx, dir, "alice"); e != nil || !worked {
		t.Fatal(worked, e)
	}
	items, e := ListWebOperationsIn(ctx, dir, "alice")
	if e != nil || len(items) != 1 || items[0].Status != "completed" || items[0].Current != 1 {
		t.Fatal(items, e)
	}
	var chunks int
	var source, id string
	if e = s.db.QueryRow(`SELECT COUNT(*),MIN(source_path),MIN(document_id) FROM entries`).Scan(&chunks, &source, &id); e != nil {
		t.Fatal(e)
	}
	if chunks <= first.Chunks || source != path || id != first.DocumentID {
		t.Fatal("identity or rechunk", chunks, source, id)
	}
	history, e := s.ListDocumentHistorySnapshots(first.DocumentID)
	if e != nil || len(history) < 1 {
		t.Fatal(history, e)
	}
	if _, e = ReadWebOperationResultIn(ctx, dir, "bob", op.ID); e == nil {
		t.Fatal("foreign result")
	}
}

func TestWebOperationsCancelAndRestartRecovery(t *testing.T) {
	ctx := context.Background()
	dir, s, cfg := queryFixture(t)
	cfg.Answer.Model = "test-chat"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	in := WebOperationInput{RequestID: strings.Repeat("b", 32), Kind: "graph-extract", ConfigRevision: cfg.Revision(), Focus: "topics", Batches: 1, ContextChars: 12000}
	op, e := StartWebOperationIn(ctx, dir, "alice", in, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = ActWebOperationIn(ctx, dir, "bob", op.ID, "cancel"); e == nil {
		t.Fatal("foreign cancellation")
	}
	if e = ActWebOperationIn(ctx, dir, "alice", op.ID, "cancel"); e != nil {
		t.Fatal(e)
	}
	if worked, e := ProcessWebOperationIn(ctx, dir, "alice"); worked || e != nil {
		t.Fatal(worked, e)
	}
	if e = ActWebOperationIn(ctx, dir, "alice", op.ID, "retry"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE web_operations SET status='running' WHERE id=?`, op.ID); e != nil {
		t.Fatal(e)
	}
	if worked, e := ProcessWebOperationIn(ctx, dir, "alice"); worked || e != nil {
		t.Fatal(worked, e)
	}
	items, e := ListWebOperationsIn(ctx, dir, "alice")
	if e != nil || items[0].Status != "interrupted" {
		t.Fatal(items, e)
	}
}

func TestWebOperationClaimPreservesCancellation(t *testing.T) {
	ctx := context.Background()
	dir, store, cfg := queryFixture(t)
	cfg.Answer.Model = "test-chat"
	if err := SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"cancel-before-claim", "cancel-after-claim"} {
		t.Run(action, func(t *testing.T) {
			var id string
			// Distinct hexadecimal IDs for each isolated operation.
			switch action {
			case "cancel-before-claim":
				id = strings.Repeat("a", 32)
			default:
				id = strings.Repeat("c", 32)
			}
			input := WebOperationInput{RequestID: id, Kind: "graph-analyze", ConfigRevision: cfg.Revision(), Focus: "topics", Batches: 1, ContextChars: 12000}
			if _, err := StartWebOperationIn(ctx, dir, "owner", input, true); err != nil {
				t.Fatal(err)
			}
			// This is the state selected by ProcessWebOperationIn before its claim.
			selected, err := scanOperation(store.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM web_operations WHERE id=? AND status='queued'`, id))
			if err != nil {
				t.Fatal(err)
			}
			if action == "cancel-before-claim" {
				if err := ActWebOperationIn(ctx, dir, "owner", selected.ID, "cancel"); err != nil {
					t.Fatal(err)
				}
			}
			claimed, err := claimWebOperation(ctx, store.db, selected.ID)
			if err != nil || claimed != (action != "cancel-before-claim") {
				t.Fatalf("claim=%t err=%v", claimed, err)
			}
			if action != "cancel-before-claim" {
				if again, err := claimWebOperation(ctx, store.db, selected.ID); err != nil || again {
					t.Fatalf("claimed twice: %t %v", again, err)
				}
				if err := ActWebOperationIn(ctx, dir, "owner", selected.ID, "cancel"); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			if err := store.db.QueryRowContext(ctx, `SELECT status FROM web_operations WHERE id=?`, id).Scan(&status); err != nil || status != "cancelled" {
				t.Fatalf("cancellation lost: %s %v", status, err)
			}
		})
	}
	if claimed, err := claimWebOperation(ctx, store.db, "missing"); err != nil || claimed {
		t.Fatalf("claimed missing operation: %t %v", claimed, err)
	}
	if worked, err := ProcessWebOperationIn(ctx, dir, "owner"); err != nil || worked {
		t.Fatalf("cancelled work resumed: %t %v", worked, err)
	}
}
