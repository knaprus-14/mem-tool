package mem

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebMapSourcesWorkflowAndExport(t *testing.T) {
	dir, s, id, root, _ := webEditFixture(t)
	ctx := context.Background()
	path := filepath.Join(dir, "private-folder", "manual.pdf")
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
	}
	if err := s.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListWebMapCandidatesIn(ctx, dir, 0)
	if err != nil || len(candidates.Items) != 2 {
		t.Fatal(candidates, err)
	}
	encoded, _ := json.Marshal(candidates)
	if strings.Contains(string(encoded), "private-folder") {
		t.Fatal("candidate path leak")
	}
	hit := candidates.Items[0]
	in := WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("b", 32), EntryID: hit.ID, Pin: hit.Pin}
	added, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", in)
	if err != nil || added.Revision != 2 {
		t.Fatal(added, err)
	}
	replay, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", in)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	url := WebMapSourceInput{Revision: 2, RequestID: strings.Repeat("c", 32), Title: "Example", URL: "https://example.org/path?q=1"}
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", url); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapSourcesIn(ctx, dir, id, root)
	if err != nil || len(page.Items) != 2 || page.Items[0].State != "current" || page.Items[1].URL != url.URL {
		t.Fatal(page, err)
	}
	encoded, _ = json.Marshal(page)
	if strings.Contains(string(encoded), "private-folder") {
		t.Fatal("source path leak")
	}
	move := WebMapSourceInput{Revision: 3, RequestID: strings.Repeat("d", 32), SourceID: page.Items[1].ID, Position: 0}
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "move", move); err != nil {
		t.Fatal(err)
	}
	page, _ = ListWebMapSourcesIn(ctx, dir, id, root)
	if page.Items[0].URL != url.URL {
		t.Fatal("source order")
	}
	remove := WebMapSourceInput{Revision: 4, RequestID: strings.Repeat("e", 32), SourceID: page.Items[0].ID}
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "remove", remove); err != nil {
		t.Fatal(err)
	}
	history, _ := ListWebMapHistoryIn(ctx, dir, id, 0)
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 5, ChangeID: history.UndoID, RequestID: strings.Repeat("f", 32)}); err != nil {
		t.Fatal(err)
	}
	page, _ = ListWebMapSourcesIn(ctx, dir, id, root)
	if len(page.Items) != 2 {
		t.Fatal("source undo")
	}
	for _, format := range []ClassicMindMapExportFormat{ClassicMindMapExportHTML, ClassicMindMapExportSVG, ClassicMindMapExportPNG, ClassicMindMapExportJSON, ClassicMindMapExportOPML, ClassicMindMapExportMarkdown, ClassicMindMapExportMermaid, ClassicMindMapExportObsidian} {
		artifact, e := ExportWebMapIn(ctx, dir, id, format, 6, false)
		if e != nil || len(artifact.Data) == 0 || artifact.Digest == "" || artifact.SourceCount != 2 {
			t.Fatal(format, artifact.Digest, e)
		}
		if strings.Contains(string(artifact.Data), "private-folder") {
			t.Fatal("export path leak", format)
		}
	}
	if _, err = ExportWebMapIn(ctx, dir, id, ClassicMindMapExportJSON, 5, false); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("export revision", err)
	}
	if _, err = s.db.Exec(`UPDATE entries SET text='changed' WHERE id=?`, hit.ID); err != nil {
		t.Fatal(err)
	}
	in.Revision = 6
	in.RequestID = strings.Repeat("1", 32)
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", in); !errors.Is(err, ErrQueryStale) {
		t.Fatal("stale pin", err)
	}
}

func TestWebMapSourceRejectionsAndMetadata(t *testing.T) {
	dir, s, id, root, _ := webEditFixture(t)
	ctx := context.Background()
	for _, url := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://user:password@example.org/", "/relative"} {
		_, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("b", 32), URL: url})
		if !errors.Is(err, ErrWebMapSourceInvalid) {
			t.Fatal(url, err)
		}
	}
	other, _ := s.CreateClassicMindMap("Other", "")
	if _, err := ActWebMapSourceIn(ctx, dir, "alice", id, other.Map.RootNodeID, "add", WebMapSourceInput{Revision: 1, RequestID: strings.Repeat("b", 32), URL: "https://example.org"}); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
		t.Fatal(err)
	}
	in := WebMapMetadata{Revision: 1, RequestID: strings.Repeat("c", 32), Title: "Renamed", Description: "Description", Status: ClassicMindMapStatusArchived}
	result, err := EditWebMapMetadataIn(ctx, dir, "alice", id, in)
	if err != nil || result.Revision != 2 {
		t.Fatal(err)
	}
	doc, _ := s.LoadClassicMindMap(id)
	if doc.Map.Title != "Renamed" || doc.Nodes[0].Label != "Renamed" || doc.Map.Status != ClassicMindMapStatusArchived {
		t.Fatal("metadata", doc.Map)
	}
	maps, _ := ListWebMapsIn(ctx, dir, 0, false)
	if maps.Total != 1 {
		t.Fatal("archived not hidden", maps.Total)
	}
	in.Title = "Other"
	if _, err = EditWebMapMetadataIn(ctx, dir, "alice", id, in); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal("metadata receipt", err)
	}
}
