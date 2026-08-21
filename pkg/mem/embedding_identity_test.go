package mem

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddingIdentityForConfigIsStableAndModelSpecific(t *testing.T) {
	cfg := DefaultLocalConfig()
	first, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ollama.BaseURL = "HTTP://LOCALHOST:11434/"
	second, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent endpoint changed identity: %#v vs %#v", first, second)
	}
	cfg.Ollama.Model = "bge-m3:other"
	other, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if other.SpaceID == first.SpaceID {
		t.Fatal("different embedding models share a vector-space identity")
	}
}

func TestIdentityAwareSearchRejectsSameDimensionDifferentModelsAndLegacyRows(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := EmbeddingIdentity{Backend: "test", Model: "model-a", SpaceID: ChunkContentHash("space-a")}
	second := EmbeddingIdentity{Backend: "test", Model: "model-b", SpaceID: ChunkContentHash("space-b")}
	matching, err := store.AddWithEmbeddingIdentity("matching", "", nil, first, []float32{1, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddWithEmbeddingIdentity("other model", "", nil, second, []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("legacy", "", nil, "test", []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchWithOptions(SearchOptions{
		QueryVector: []float32{1, 0}, Backend: "test", EmbeddingSpace: first.SpaceID, VectorOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != matching.ID {
		t.Fatalf("identity-aware search mixed vector spaces: %#v", results)
	}
	lexical, err := store.SearchWithOptions(SearchOptions{
		Query: "legacy", Backend: "test", EmbeddingSpace: first.SpaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lexical) != 1 || lexical[0].Text != "legacy" || lexical[0].VectorScore != 0 {
		t.Fatalf("legacy row was not available as a lexical-only result: %#v", lexical)
	}
}

func TestEmbeddingIdentityPersistsAndChangesAtomicallyWithReplacementVector(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := EmbeddingIdentity{Backend: "test", Model: "model-a", SpaceID: ChunkContentHash("space-a")}
	second := EmbeddingIdentity{Backend: "test", Model: "model-b", SpaceID: ChunkContentHash("space-b")}
	entry, err := store.AddWithEmbeddingIdentity("before", "", nil, first, []float32{1, 0}, false)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.UpdateByIdWithEmbeddingIdentity(entry.ID, "after", "", nil, []float32{0, 1}, second); err != nil {
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
	got, err := reopened.GetByID(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "after" || got.EmbeddingModel != second.Model || got.EmbeddingSpace != second.SpaceID || got.Embedding[1] != 1 {
		t.Fatalf("replacement embedding provenance was not persisted atomically: %#v", got)
	}
}

func TestEmbeddingIdentityForConfigDoesNotAcceptEndpointSecrets(t *testing.T) {
	cfg := DefaultLocalConfig()
	for _, endpoint := range []string{
		"http://user:secret@localhost:11434",
		"http://localhost:11434?api_key=secret",
	} {
		cfg.Ollama.BaseURL = endpoint
		if _, err := EmbeddingIdentityForConfig(cfg); err == nil || !strings.Contains(err.Error(), "must not contain") {
			t.Fatalf("secret-bearing endpoint %q was accepted: %v", endpoint, err)
		}
	}
}
