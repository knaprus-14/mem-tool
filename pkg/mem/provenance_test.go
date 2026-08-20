package mem

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceDocumentChunksRejectsContradictoryProvenance(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tests := []struct {
		name   string
		mutate func([]DocumentChunk)
		want   string
	}{
		{
			name: "document identity",
			mutate: func(chunks []DocumentChunk) {
				chunks[1].Provenance.DocumentID = "doc-other"
			},
			want: "document identity",
		},
		{
			name: "document revision",
			mutate: func(chunks []DocumentChunk) {
				chunks[1].Provenance.DocumentRevision = ChunkContentHash("different revision")
			},
			want: "content revision does not match earlier chunks",
		},
		{
			name: "chunk hash",
			mutate: func(chunks []DocumentChunk) {
				chunks[1].Text = "tampered after hashing"
			},
			want: "content hash does not match its text",
		},
		{
			name: "embedding dimensions",
			mutate: func(chunks []DocumentChunk) {
				chunks[1].Embedding = []float32{1}
			},
			want: "embedding dimensions",
		},
		{
			name: "non-finite embedding",
			mutate: func(chunks []DocumentChunk) {
				chunks[0].Embedding[0] = float32(math.NaN())
			},
			want: "not finite",
		},
		{
			name: "block local gap",
			mutate: func(chunks []DocumentChunk) {
				chunks[1].Provenance.BlockChunkIndex = 2
			},
			want: "inconsistent block-local index/total",
		},
		{
			name: "partial block",
			mutate: func(chunks []DocumentChunk) {
				chunks[0].Provenance.BlockTotalChunks = 3
				chunks[1].Provenance.BlockTotalChunks = 3
			},
			want: "ended after 2/3 chunks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := validStructuredChunks()
			tt.mutate(chunks)
			err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if got := store.GetBySourceFile(chunks[0].Provenance.SourcePath); len(got) != 0 {
				t.Fatalf("rejected replacement changed store: %#v", got)
			}
		})
	}
}

func TestReplaceDocumentChunksPersistsBlockLocalCoordinatesAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries := reopened.GetBySourceFile(chunks[0].Provenance.SourcePath)
	if len(entries) != 2 || entries[0].BlockChunkIndex != 0 || entries[1].BlockChunkIndex != 1 || entries[1].BlockTotalChunks != 2 {
		t.Fatalf("block-local coordinates did not survive reopen: %#v", entries)
	}
	for _, entry := range entries {
		if entry.DocumentRevision != chunks[0].Provenance.DocumentRevision || entry.ChunkHash != ChunkContentHash(entry.Text) {
			t.Fatalf("versioned content provenance did not survive reopen: %#v", entry)
		}
	}
}

func TestUpdateByIDCannotInvalidateImportedChunkHash(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunks := validStructuredChunks()
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	if err := store.UpdateById(entry.ID, "tampered text", entry.Title, entry.Tags, []float32{1, 0}); err == nil || !strings.Contains(err.Error(), "source-anchored") {
		t.Fatalf("source-anchored text edit was accepted: %v", err)
	}
	after := store.GetBySourceFile(chunks[0].Provenance.SourcePath)[0]
	if after.Text != entry.Text || after.ChunkHash != entry.ChunkHash {
		t.Fatalf("rejected edit changed versioned evidence: before=%#v after=%#v", entry, after)
	}
}

func validStructuredChunks() []DocumentChunk {
	const source = "C:/docs/book.pdf"
	result := make([]DocumentChunk, 2)
	for i := range result {
		text := fmt.Sprintf("chunk-%d", i)
		result[i] = DocumentChunk{
			Text: text, Backend: "test", Embedding: []float32{1, 0},
			ChunkIndex: i, TotalChunks: 2,
			Provenance: Provenance{
				DocumentID: "doc-book", DocumentRevision: ChunkContentHash("document revision fixture"),
				ChunkHash: ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
				Page: 4, BlockIndex: 0, BlockChunkIndex: i, BlockTotalChunks: 2,
				BlockMarker: "<!-- page: 4 -->", ExtractionMethod: "text", OCRConfidence: -1,
			},
		}
	}
	return result
}
