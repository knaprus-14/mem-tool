package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWebMapImportPreviewApplyHostedEvidenceAndRetry(t *testing.T) {
	ctx := context.Background()
	dir, s, mapID, _, _, _ := mapDocumentFixture(t)
	original, err := s.LoadClassicMindMap(mapID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := ExportWebMapIn(ctx, dir, mapID, ClassicMindMapExportJSON, original.Map.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewWebMapImportIn(ctx, dir, WebMapImportPreviewInput{Content: string(artifact.Data)})
	if err != nil || preview.Current != 1 {
		t.Fatal(preview, err)
	}
	body, _ := json.Marshal(preview)
	if strings.Contains(string(body), "source_path") || strings.Contains(string(body), dir) {
		t.Fatal("preview leaked path")
	}
	in := WebMapImportInput{Content: string(artifact.Data), Digest: preview.Digest, RequestID: strings.Repeat("8", 32)}
	created, err := ImportWebMapIn(ctx, dir, "alice", in)
	if err != nil || created.ID == mapID {
		t.Fatal(created, err)
	}
	doc, err := s.LoadClassicMindMap(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var source ClassicMindMapSource
	for _, n := range doc.Nodes {
		for _, src := range n.Sources {
			if src.Kind == ClassicMindMapSourceEvidence {
				source = src
			}
		}
	}
	if source.ID == "" || source.EvidenceState != EvidenceCurrent {
		t.Fatal(source)
	}
	opened, err := ReadWebMapDocumentIn(ctx, dir, doc.Map.ID, source.NodeID, source.ID, doc.Map.Revision)
	if err != nil || opened.State != string(EvidenceCurrent) {
		t.Fatal(opened, err)
	}
	// Retry is the confirmed receipt, not a fresh import or current-source test.
	if _, err = s.db.Exec(`UPDATE entries SET document_revision=?`, ChunkContentHash("changed after import")); err != nil {
		t.Fatal(err)
	}
	retry, err := ImportWebMapIn(ctx, dir, "alice", in)
	if err != nil || !retry.Replayed || retry.ID != created.ID {
		t.Fatal(retry, err)
	}
	changed := in
	changed.Content = in.Content + "\n"
	if _, err = ImportWebMapIn(ctx, dir, "alice", changed); !errors.Is(err, ErrWebMapRequest) {
		t.Fatal("receipt allowed changed payload", err)
	}
	changed = in
	changed.RequestID = strings.Repeat("9", 32)
	if _, err = ImportWebMapIn(ctx, dir, "alice", changed); !errors.Is(err, ErrClassicMindMapImportPreview) {
		t.Fatal("stale preview accepted", err)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM mind_maps`).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM web_map_creates WHERE request_id IN (?,?)`, in.RequestID, changed.RequestID).Scan(&count)
	if count != 1 {
		t.Fatal("uncommitted receipt", count)
	}
	unchanged, err := s.LoadClassicMindMap(mapID)
	if err != nil || unchanged.Digest != original.Digest {
		t.Fatal("original changed", err)
	}
}
