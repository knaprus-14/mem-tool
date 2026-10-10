package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sourceMapFixture(t *testing.T) (string, *Store, WebMapFromSources) {
	dir, s, _, _, _ := webAIFixture(t)
	var path, docID string
	if err := s.db.QueryRow(`SELECT source_path,document_id FROM entries LIMIT 1`).Scan(&path, &docID); err != nil {
		t.Fatal(err)
	}
	return dir, s, WebMapFromSources{RequestID: strings.Repeat("e", 32), Title: "Обзор документа", Detail: "standard", Documents: []string{LibraryDocumentID(path, docID)}}
}

const sourceMapResponse = `{"nodes":[{"ref":"p","parent_ref":"","label":"Главная тема","summary":"Обзор","body_markdown":"","kind":"subtopic","citations":["E1"]},{"ref":"c","parent_ref":"p","label":"Подробность","summary":"Факт из документа","body_markdown":"","kind":"fact","citations":["E1"]}]}`

func TestWebSourceMapFullSelectionAutoPublishAndUndo(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count)
	if !task.SourceOverview || task.SourceChunks != count || task.SourceDocuments != 1 {
		t.Fatalf("incomplete selection: %+v", task)
	}
	if _, err := time.Parse(time.RFC3339Nano, task.CreatedAt); err != nil {
		t.Fatalf("missing creation time for progress display: %q: %v", task.CreatedAt, err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse}}
	if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
		t.Fatal(worked, e)
	}
	doc, e := s.LoadClassicMindMap(task.MapID)
	if e != nil || len(doc.Nodes) != 1 {
		t.Fatal(doc, e)
	}
	if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
		t.Fatal(worked, e)
	}
	doc, e = s.LoadClassicMindMap(task.MapID)
	if e != nil || len(doc.Nodes) != 3 || doc.Map.Revision != 2 {
		t.Fatal(doc, e)
	}
	for _, r := range provider.requests {
		if strings.Contains(r.Prompt, "private-ai-folder") {
			t.Fatal("private source path sent")
		}
		if !strings.Contains(r.Prompt, "главным") && !strings.Contains(r.Prompt, "главны") {
			t.Fatal("missing outline instruction")
		}
	}
	replayed, e := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if e != nil || replayed.MapID != task.MapID || replayed.Status != "applied" {
		t.Fatal(replayed, e)
	}
	if replayed.CreatedAt != task.CreatedAt {
		t.Fatal("creation time changed after processing or replay")
	}
	if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || worked {
		t.Fatal("repeated model work", worked, e)
	}
	if _, e = CreateWebMapFromSourcesIn(ctx, dir, "bob", in); !errors.Is(e, ErrWebMapAIChanged) {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(task)
	if strings.Contains(string(encoded), "private-ai-folder") || strings.Contains(string(encoded), "entries") {
		t.Fatal("private selection payload")
	}
	h, err := ListWebMapHistoryIn(ctx, dir, task.MapID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", task.MapID, WebMapUndo{Revision: h.Revision, ChangeID: h.UndoID, RequestID: strings.Repeat("f", 32)}); err != nil {
		t.Fatal(err)
	}
	doc, err = s.LoadClassicMindMap(task.MapID)
	if err != nil || len(doc.Nodes) != 1 {
		t.Fatal("undo", doc, err)
	}
}

func TestWebSourceMapCreationAtomicAndExistingMapCannotAutoPublish(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(classicMindMapAISchema + webMapAISchema); err != nil {
		t.Fatal(err)
	}
	var before int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&before)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_source_task BEFORE INSERT ON web_map_ai_tasks BEGIN SELECT RAISE(ABORT,'fixture queue failure');END`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in); err == nil {
		t.Fatal("missing injected failure")
	}
	var after int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&after)
	if before != after {
		t.Fatal("orphan map after queue failure")
	}
	s.db.Exec(`DROP TRIGGER reject_source_task`)
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = StartWebMapAIIn(ctx, dir, "alice", task.MapID, task.NodeID, WebMapAIInput{SourceOverview: true}); !errors.Is(err, ErrWebMapAIInvalid) {
		t.Fatal("existing map auto mode accepted", err)
	}
	if err = CancelWebMapAIIn(ctx, dir, "alice", task.MapID, task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", &classicMindMapAIFakeProvider{}); err != nil || worked {
		t.Fatal(worked, err)
	}
}

func TestWebSourceMapChangedSourceKeepsPreviewUnpublished(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse}}
	if _, err = processWebMapAIIn(ctx, dir, "alice", provider); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE entries SET text=text||' changed'`); err != nil {
		t.Fatal(err)
	}
	if _, err = processWebMapAIIn(ctx, dir, "alice", provider); err != nil {
		t.Fatal(err)
	}
	doc, err := s.LoadClassicMindMap(task.MapID)
	if err != nil || doc.Map.Revision != 1 {
		t.Fatal("stale source published", err)
	}
	list, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || list[0].Status != "ready" || list[0].Error == "" {
		t.Fatal(list, err)
	}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || worked {
		t.Fatal("failed auto apply loops", worked, err)
	}
}

func TestWebSourceMapComplete512ChunksAndLimit(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	var source string
	if err := s.db.QueryRow(`SELECT source_path FROM entries LIMIT 1`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	makeChunks := func(n int) []DocumentChunk {
		chunks := make([]DocumentChunk, n)
		for i := range chunks {
			c := validStructuredChunks()[0]
			c.Text = fmt.Sprintf("Фрагмент %d", i)
			c.ChunkIndex = i
			c.TotalChunks = n
			c.Provenance.SourcePath = source
			c.Provenance.BlockChunkIndex = i
			c.Provenance.BlockTotalChunks = n
			c.Provenance.ChunkHash = ChunkContentHash(c.Text)
			chunks[i] = c
		}
		return chunks
	}
	if err := s.ReplaceDocumentChunks(source, makeChunks(512)); err != nil {
		t.Fatal(err)
	}
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil || task.SourceChunks != 512 || len(task.Input.Entries) != 512 {
		t.Fatal("truncated corpus", task.SourceChunks, err)
	}
	if err = s.ReplaceDocumentChunks(source, makeChunks(513)); err != nil {
		t.Fatal(err)
	}
	in.RequestID = strings.Repeat("d", 32)
	var before, after int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&before)
	if _, err = CreateWebMapFromSourcesIn(ctx, dir, "alice", in); !errors.Is(err, ErrWebMapSourceOverviewLimit) {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&after)
	if after != before {
		t.Fatal("oversized input left a map")
	}
}

func TestWebSourceMapRestartAfterPreviewResumesPublication(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	ctx := context.Background()
	task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{sourceMapResponse}}
	if _, err = processWebMapAIIn(ctx, dir, "alice", provider); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE web_map_ai_tasks SET status='running' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); e != nil || !worked {
			t.Fatal(i, worked, e)
		}
	}
	list, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
	if err != nil || list[0].Status != "applied" || len(provider.requests) != 1 {
		t.Fatal("restart repeated generation", list, err)
	}
}

func TestWebSourceMapLegacyDocumentNeedsProvenance(t *testing.T) {
	dir, s, in := sourceMapFixture(t)
	if _, err := s.db.Exec(`UPDATE entries SET document_revision=''`); err != nil {
		t.Fatal(err)
	}
	var before, after int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&before)
	if _, err := CreateWebMapFromSourcesIn(context.Background(), dir, "alice", in); !errors.Is(err, ErrWebMapSourceOverviewProvenance) {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&after)
	if before != after {
		t.Fatal("legacy input left a map")
	}
}
