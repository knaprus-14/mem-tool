package mem

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeTopicsLayoutCompatibility(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	layout := KnowledgeMapLayout{Version: KnowledgeMapLayoutVersion, Nodes: map[string]KnowledgeMapNodePosition{}, Viewport: KnowledgeMapViewport{Scale: 1}, State: &KnowledgeMapViewState{Representation: KnowledgeMapRepresentationTopics, RelationLens: KnowledgeMapRelationLensSmart}}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, layout); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView)
	if err != nil || loaded.State.Representation != KnowledgeMapRepresentationTopics {
		t.Fatalf("topics layout not restored: %v, %v", loaded, err)
	}
	layout.Version = knowledgeMapLayoutV9
	if err := validateKnowledgeMapLayout(layout); err == nil || !strings.Contains(err.Error(), "version 10") {
		t.Fatalf("v9 accepted topics: %v", err)
	}
	layout.State.Representation = KnowledgeMapRepresentationGraph
	if err := validateKnowledgeMapLayout(layout); err != nil {
		t.Fatalf("old v9 graph with relation lens must remain valid: %v", err)
	}
}

func TestKnowledgeTopicsOfflineBundle(t *testing.T) {
	var html bytes.Buffer
	if err := WriteKnowledgeMapHTML(&html, "test", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"root.KnowledgeTopics = api", "function updateTopicsView()", `id="topicsView"`, `value="topics"`} {
		if !strings.Contains(html.String(), marker) {
			t.Fatalf("offline bundle missing %s", marker)
		}
	}
	if strings.Contains(html.String(), "/*__KNOWLEDGE_TOPICS") {
		t.Fatal("unresolved script embedding marker")
	}
}

// Explicitly opt in with a disposable corpus copy. Never opens the original DB.
func TestKnowledgeTopicsCopiedCorpusPreview(t *testing.T) {
	dir := os.Getenv("MEM_TOPICS_CORPUS_COPY")
	if dir == "" {
		t.Skip("set MEM_TOPICS_CORPUS_COPY to a disposable copied corpus")
	}
	store, err := NewStore(filepath.Join(dir, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		t.Fatal(err)
	}
	var html bytes.Buffer
	if err = WriteKnowledgeMapHTML(&html, "SP10 — проверка обзора тем", data); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "topics-preview.html"), html.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("copied corpus: %d nodes, %d edges", len(data.Graph.Nodes), len(data.Graph.Edges))
}
