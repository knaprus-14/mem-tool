package mem

import (
	"strings"
	"testing"
)

func TestKnowledgeMapLayoutRoundTripAndDelete(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "layout-node", Kind: KnowledgeNodeClaim, Label: "Layout claim",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	want := KnowledgeMapLayout{
		Version: KnowledgeMapLayoutVersion,
		Nodes: map[string]KnowledgeMapNodePosition{
			"layout-node": {X: 123.5, Y: -44.25, Pinned: true},
		},
		Viewport: KnowledgeMapViewport{Scale: 1.25, X: 10, Y: -20},
	}
	saved, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, want)
	if err != nil || saved.Updated == "" {
		t.Fatalf("save layout failed: saved=%#v err=%v", saved, err)
	}
	loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView)
	if err != nil || loaded == nil || loaded.Nodes["layout-node"].X != want.Nodes["layout-node"].X ||
		loaded.Viewport.Scale != want.Viewport.Scale || loaded.Updated != saved.Updated {
		t.Fatalf("layout round trip failed: loaded=%#v err=%v", loaded, err)
	}
	if err := store.DeleteKnowledgeMapLayout(DefaultKnowledgeMapView); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView); err != nil || loaded != nil {
		t.Fatalf("deleted layout still exists: loaded=%#v err=%v", loaded, err)
	}
}

func TestKnowledgeMapLayoutRejectsInvalidOrUnknownNodes(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	base := KnowledgeMapLayout{Version: KnowledgeMapLayoutVersion, Nodes: map[string]KnowledgeMapNodePosition{}, Viewport: KnowledgeMapViewport{Scale: 1}}
	for name, mutate := range map[string]func(*KnowledgeMapLayout){
		"version": func(layout *KnowledgeMapLayout) { layout.Version++ },
		"scale":   func(layout *KnowledgeMapLayout) { layout.Viewport.Scale = 0 },
		"unknown": func(layout *KnowledgeMapLayout) { layout.Nodes["unknown-node"] = KnowledgeMapNodePosition{} },
	} {
		t.Run(name, func(t *testing.T) {
			layout := base
			layout.Nodes = map[string]KnowledgeMapNodePosition{}
			mutate(&layout)
			if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, layout); err == nil {
				t.Fatalf("invalid layout %s was accepted", name)
			}
		})
	}
	if _, err := decodeKnowledgeMapLayout([]byte(`{"version":1,"nodes":{},"viewport":{"scale":1,"x":0,"y":0},"extra":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown layout field was accepted: %v", err)
	}
}
