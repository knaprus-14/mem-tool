package mem

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebMapAttachmentAtomicHistoryAndDownload(t *testing.T) {
	ctx := context.Background()
	dir, _, id, root, _ := webAIFixture(t)
	in := WebMapDelete{Revision: 1, RequestID: strings.Repeat("a", 32)}
	data := []byte("private attachment fixture")
	for i := 0; i < 2; i++ {
		out, e := AttachWebMapFileIn(ctx, dir, "alice", id, root, in, "sample.txt", data)
		if e != nil || out.Replayed != (i == 1) {
			t.Fatal(out, e)
		}
	}
	sources, e := ListWebMapSourcesIn(ctx, dir, id, root)
	if e != nil || len(sources.Items) != 1 || !sources.Items[0].Attachment {
		t.Fatal(sources, e)
	}
	name, got, e := ReadWebMapAttachmentIn(ctx, dir, id, root, sources.Items[0].ID, 2)
	if e != nil || name != "sample.txt" || !bytes.Equal(got, data) {
		t.Fatal(name, string(got), e)
	}
	if _, _, e = ReadWebMapAttachmentIn(ctx, dir, id, root, sources.Items[0].ID, 1); !errors.Is(e, ErrClassicMindMapRevisionConflict) {
		t.Fatal(e)
	}
	if _, e = AttachWebMapFileIn(ctx, dir, "alice", id, root, WebMapDelete{Revision: 2, RequestID: strings.Repeat("b", 32)}, "../secret.txt", data); e == nil {
		t.Fatal("path accepted")
	}
}

func TestEntryMetadataPreservesProvenanceAndVectors(t *testing.T) {
	ctx := context.Background()
	dir, s, _, _, _ := webAIFixture(t)
	var id int64
	if e := s.db.QueryRow(`SELECT id FROM entries LIMIT 1`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	before, e := readMetadataEntry(ctx, tx, id)
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadEntryMetadataIn(ctx, dir, id)
	if e != nil {
		t.Fatal(e)
	}
	in := NoteInput{ID: id, Pin: view.Pin, RequestID: strings.Repeat("c", 32), Title: "New metadata", Tags: []string{"new"}, Important: true}
	for i := 0; i < 2; i++ {
		out, e := SaveEntryMetadataIn(ctx, dir, "alice", in)
		if e != nil || out.Replayed != (i == 1) {
			t.Fatal(out, e)
		}
	}
	tx, e = s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	after, e := readMetadataEntry(ctx, tx, id)
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if before.Text != after.Text || before.SourcePath != after.SourcePath || before.ChunkHash != after.ChunkHash || before.DocumentID != after.DocumentID || len(before.Embedding) != len(after.Embedding) || after.Title != in.Title || !after.Important {
		t.Fatal("metadata changed provenance")
	}
	in.RequestID = strings.Repeat("d", 32)
	if _, e = SaveEntryMetadataIn(ctx, dir, "alice", in); !errors.Is(e, ErrNoteConflict) {
		t.Fatal(e)
	}
}
