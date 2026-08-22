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
		State: &KnowledgeMapViewState{
			Filters: KnowledgeMapViewFilters{
				Statuses:      []KnowledgeStatus{KnowledgeStatusDraft, KnowledgeStatusActive},
				Evidence:      []EvidenceState{EvidenceCurrent},
				NodeKinds:     []KnowledgeNodeKind{KnowledgeNodeClaim},
				RelationKinds: []KnowledgeRelationKind{},
			},
			Focus:          &KnowledgeMapFocus{NodeID: "layout-node", Depth: 2},
			Collapsed:      []string{"layout-node"},
			ClusterLayout:  true,
			Representation: KnowledgeMapRepresentationCausal,
		},
	}
	saved, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, want)
	if err != nil || saved.Updated == "" {
		t.Fatalf("save layout failed: saved=%#v err=%v", saved, err)
	}
	loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView)
	if err != nil || loaded == nil || loaded.Nodes["layout-node"].X != want.Nodes["layout-node"].X ||
		loaded.Viewport.Scale != want.Viewport.Scale || loaded.Updated != saved.Updated ||
		loaded.State == nil || loaded.State.Focus.NodeID != "layout-node" || !loaded.State.ClusterLayout ||
		loaded.State.Representation != KnowledgeMapRepresentationCausal {
		t.Fatalf("layout round trip failed: loaded=%#v err=%v", loaded, err)
	}
	if legacy, err := decodeKnowledgeMapLayout([]byte(`{"version":1,"nodes":{},"viewport":{"scale":1,"x":0,"y":0}}`)); err != nil || legacy.Version != 1 || legacy.State != nil {
		t.Fatalf("legacy v1 layout is not readable: layout=%#v err=%v", legacy, err)
	}
	if legacy, err := decodeKnowledgeMapLayout([]byte(`{"version":2,"nodes":{},"viewport":{"scale":1,"x":0,"y":0},"state":{"filters":{"statuses":[],"evidence":[],"node_kinds":[],"relation_kinds":[]}}}`)); err != nil || legacy.Version != 2 || legacy.State == nil {
		t.Fatalf("legacy v2 layout is not readable: layout=%#v err=%v", legacy, err)
	}
	if legacy, err := decodeKnowledgeMapLayout([]byte(`{"version":3,"nodes":{},"viewport":{"scale":1,"x":0,"y":0},"state":{"filters":{"statuses":[],"evidence":[],"node_kinds":[],"relation_kinds":[]}}}`)); err != nil || legacy.Version != 3 || legacy.State == nil {
		t.Fatalf("legacy v3 layout is not readable: layout=%#v err=%v", legacy, err)
	}
	if legacy, err := decodeKnowledgeMapLayout([]byte(`{"version":4,"nodes":{},"viewport":{"scale":1,"x":0,"y":0},"state":{"filters":{"statuses":[],"evidence":[],"node_kinds":[],"relation_kinds":[]}}}`)); err != nil || legacy.Version != 4 || legacy.State == nil {
		t.Fatalf("legacy v4 layout is not readable: layout=%#v err=%v", legacy, err)
	}
	if err := store.DeleteKnowledgeMapLayout(DefaultKnowledgeMapView); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.LoadKnowledgeMapLayout(DefaultKnowledgeMapView); err != nil || loaded != nil {
		t.Fatalf("deleted layout still exists: loaded=%#v err=%v", loaded, err)
	}
}

func TestKnowledgeMapNamedViewsAreListedWithPresentationState(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "named-view-node", Kind: KnowledgeNodeTopic, Label: "Композиция",
		Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated,
		Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	layout := KnowledgeMapLayout{
		Version:  KnowledgeMapLayoutVersion,
		Nodes:    map[string]KnowledgeMapNodePosition{"named-view-node": {X: 1, Y: 2}},
		Viewport: KnowledgeMapViewport{Scale: 1},
		State: &KnowledgeMapViewState{
			Filters:        KnowledgeMapViewFilters{},
			Focus:          &KnowledgeMapFocus{ClusterID: "named-view-node"},
			Collapsed:      []string{"named-view-node"},
			ClusterLayout:  true,
			Representation: KnowledgeMapRepresentationCausal,
		},
	}
	if _, err := store.SaveKnowledgeMapLayout("Композиция", layout); err != nil {
		t.Fatal(err)
	}
	views, err := store.ListKnowledgeMapViews()
	if err != nil || len(views) != 2 || views[0].Name != DefaultKnowledgeMapView || views[1].Name != "Композиция" ||
		!views[1].Focused || views[1].Collapsed != 1 || !views[1].ClusterLayout || views[1].NodeCount != 1 ||
		views[1].Representation != KnowledgeMapRepresentationCausal {
		t.Fatalf("named view summaries are incomplete: views=%#v err=%v", views, err)
	}
}

func TestKnowledgeMapLayoutRejectsInvalidOrUnknownNodes(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	base := KnowledgeMapLayout{Version: KnowledgeMapLayoutVersion, Nodes: map[string]KnowledgeMapNodePosition{}, Viewport: KnowledgeMapViewport{Scale: 1}, State: &KnowledgeMapViewState{Representation: KnowledgeMapRepresentationGraph}}
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
	invalidState := base
	invalidState.Version = KnowledgeMapLayoutVersion
	invalidState.State = &KnowledgeMapViewState{
		Filters: KnowledgeMapViewFilters{Statuses: []KnowledgeStatus{KnowledgeStatusDraft, KnowledgeStatusDraft}},
		Focus:   &KnowledgeMapFocus{NodeID: "unknown-node", ClusterID: "unknown-node", Depth: 2}, Representation: KnowledgeMapRepresentationGraph,
	}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, invalidState); err == nil {
		t.Fatal("invalid view filters and ambiguous focus were accepted")
	}
	legacyState := base
	legacyState.Version = knowledgeMapLayoutV1
	legacyState.State = &KnowledgeMapViewState{Filters: KnowledgeMapViewFilters{}}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, legacyState); err == nil || !strings.Contains(err.Error(), "requires version 2") {
		t.Fatalf("legacy layout accepted v2 state: %v", err)
	}
	if _, err := decodeKnowledgeMapLayout([]byte(`{"version":1,"nodes":{},"viewport":{"scale":1,"x":0,"y":0},"extra":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown layout field was accepted: %v", err)
	}
	invalidRepresentation := base
	invalidRepresentation.State = &KnowledgeMapViewState{Representation: "timeline"}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, invalidRepresentation); err == nil || !strings.Contains(err.Error(), "representation") {
		t.Fatalf("invalid representation was accepted: %v", err)
	}
}
