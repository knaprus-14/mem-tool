package mem

import (
	"fmt"
	"strings"
	"testing"
)

func TestClassicMindMapDraftRejectsUnsafeSizeAndDepth(t *testing.T) {
	t.Run("node count", func(t *testing.T) {
		_, _, err := normalizeClassicMindMapDraft(ClassicMindMapDraft{
			Title: "Oversized",
			Nodes: make([]ClassicMindMapNodeDraft, MaxClassicMindMapNodes+1),
		})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(MaxClassicMindMapNodes)) {
			t.Fatalf("oversized map error=%v", err)
		}
	})

	t.Run("depth", func(t *testing.T) {
		nodes := make([]ClassicMindMapNodeDraft, MaxClassicMindMapDepth+1)
		for i := range nodes {
			nodes[i] = ClassicMindMapNodeDraft{
				Ref:   fmt.Sprintf("node-%d", i),
				Label: fmt.Sprintf("Node %d", i),
			}
			if i > 0 {
				nodes[i].ParentRef = nodes[i-1].Ref
			}
		}
		_, _, err := normalizeClassicMindMapDraft(ClassicMindMapDraft{Title: "Too deep", Nodes: nodes})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(MaxClassicMindMapDepth)) {
			t.Fatalf("deep map error=%v", err)
		}
	})
}

func TestClassicMindMapAddRejectsDepthBeyondBrowserLimit(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodes := make([]ClassicMindMapNodeDraft, MaxClassicMindMapDepth)
	for i := range nodes {
		nodes[i] = ClassicMindMapNodeDraft{Ref: fmt.Sprintf("node-%d", i), Label: fmt.Sprintf("Node %d", i)}
		if i > 0 {
			nodes[i].ParentRef = nodes[i-1].Ref
		}
	}
	doc, err := store.ImportClassicMindMap(ClassicMindMapDraft{Title: "At depth limit", Nodes: nodes}, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	deepest := findClassicMindMapNode(t, doc, fmt.Sprintf("Node %d", MaxClassicMindMapDepth-1))
	_, _, err = store.AddClassicMindMapNode(doc.Map.ID, deepest.ID, "One too deep", -1,
		ClassicMindMapNodeSubtopic, "", "", doc.Map.Revision, "test", "")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(MaxClassicMindMapDepth)) {
		t.Fatalf("node beyond depth limit was accepted: %v", err)
	}
}
