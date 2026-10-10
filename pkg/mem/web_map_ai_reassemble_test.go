package mem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func failedSourceSynthesisFixture(t *testing.T) (string, *Store, WebMapAITask) {
	t.Helper()
	dir, s, in := sourceMapFixture(t)
	var path, id string
	if err := s.db.QueryRow(`SELECT source_path,document_id FROM entries LIMIT 1`).Scan(&path, &id); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Text = strings.Repeat(fmt.Sprintf("Условие %d. ", i), 300)
		chunks[i].Provenance.SourcePath, chunks[i].Provenance.DocumentID = path, id
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("reassembly revision")
	}
	if err := s.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.ContextChars = 6500
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	task, err := CreateWebMapFromSourcesIn(context.Background(), dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	p := &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse, strings.ReplaceAll(sourceMapResponse, `"E1"`, `"E2"`)}}
	if _, err = processWebMapAIIn(context.Background(), dir, "alice", p); err != nil {
		t.Fatal(err)
	}
	tasks, err := ListWebMapAIIn(context.Background(), dir, "alice", task.MapID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "failed" || !tasks[0].CanReassemble || !strings.HasPrefix(tasks[0].Error, "Сборка карты:") {
		t.Fatal(tasks, err)
	}
	return dir, s, tasks[0]
}

func TestWebMapAIReassemblyUsesCheckpointsAndKeepsFailedRun(t *testing.T) {
	dir, s, task := failedSourceSynthesisFixture(t)
	ctx := context.Background()
	if _, err := ReassembleWebMapAIIn(ctx, dir, "bob", task.MapID, task.ID); !errors.Is(err, ErrClassicMindMapAIPreviewNotFound) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		queued, err := ReassembleWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID)
		if err != nil || queued.Status != "queued" {
			t.Fatal(queued, err)
		}
	}
	p := &synthesisTestProvider{t: t, budget: 6500}
	for i := 0; i < 2; i++ {
		if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || !worked {
			t.Fatal(worked, err)
		}
	}
	if len(p.requests) != 1 {
		t.Fatalf("calls=%d want only synthesis", len(p.requests))
	}
	tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || tasks[0].Status != "applied" || tasks[0].RunID == task.RunID || tasks[0].CanReassemble {
		t.Fatal(tasks, err)
	}
	var status string
	if err = s.db.QueryRow(`SELECT status FROM mind_map_generation_runs WHERE id=?`, task.RunID).Scan(&status); err != nil || status != "failed" {
		t.Fatal(status, err)
	}
	var copies int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_batches a JOIN mind_map_generation_batches b ON a.ordinal=b.ordinal WHERE a.run_id=? AND b.run_id=? AND a.result_json=b.result_json AND a.result_digest=b.result_digest AND a.evidence_json=b.evidence_json`, task.RunID, tasks[0].RunID).Scan(&copies); err != nil || copies != 2 {
		t.Fatal(copies, err)
	}
	doc, err := s.LoadClassicMindMap(task.MapID)
	if err != nil || len(doc.Nodes) != 6 || doc.Map.Revision != 2 {
		t.Fatal(len(doc.Nodes), err)
	}
	if _, err = ReassembleWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", p); err != nil || worked || len(p.requests) != 1 {
		t.Fatal("retry duplicated work", err)
	}
}

func TestWebMapAIReassemblyRejectsChangedOrIncompleteInputs(t *testing.T) {
	for _, mode := range []string{"map", "source", "config", "incomplete", "cancelled", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, task := failedSourceSynthesisFixture(t)
			ctx := context.Background()
			var err error
			switch mode {
			case "source":
				_, err = s.db.Exec(`UPDATE entries SET text=text||' changed'`)
			case "map":
				_, err = s.db.Exec(`UPDATE mind_maps SET revision=revision+1 WHERE id=?`, task.MapID)
			case "config":
				cfg, e := LoadConfigIn(dir)
				if e != nil {
					t.Fatal(e)
				}
				cfg.Answer.Model = "changed"
				err = SaveConfigIn(dir, cfg)
			case "incomplete":
				_, err = s.db.Exec(`UPDATE mind_map_generation_batches SET status='failed' WHERE run_id=? AND ordinal=1`, task.RunID)
			case "cancelled":
				_, err = s.db.Exec(`UPDATE web_map_ai_tasks SET status='cancelled' WHERE id=?`, task.ID)
			case "corrupt":
				_, err = s.db.Exec(`UPDATE mind_map_generation_batches SET result_json='[]' WHERE run_id=? AND ordinal=1`, task.RunID)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = ReassembleWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID)
			if mode != "corrupt" {
				if err == nil {
					t.Fatal("accepted changed inputs")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := &synthesisTestProvider{t: t, budget: 6500}
			if _, err = processWebMapAIIn(ctx, dir, "alice", p); err != nil {
				t.Fatal(err)
			}
			if len(p.requests) != 0 {
				t.Fatal("model called before checkpoint validation")
			}
			tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
			if err != nil || tasks[0].Status != "failed" {
				t.Fatal(tasks, err)
			}
			doc, err := s.LoadClassicMindMap(task.MapID)
			if err != nil || len(doc.Nodes) != 1 || doc.Map.Revision != 1 {
				t.Fatal(doc, err)
			}
		})
	}
}
