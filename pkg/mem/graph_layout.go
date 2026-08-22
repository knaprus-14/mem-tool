package mem

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	KnowledgeMapLayoutVersion = 6
	knowledgeMapLayoutV1      = 1
	knowledgeMapLayoutV2      = 2
	knowledgeMapLayoutV3      = 3
	knowledgeMapLayoutV4      = 4
	knowledgeMapLayoutV5      = 5
	DefaultKnowledgeMapView   = "default"
	MaxKnowledgeMapViewNodes  = 10000
	MaxKnowledgeMapLayoutJSON = 1 << 20
	MaxKnowledgeMapViews      = 100
)

type KnowledgeMapRepresentation string

const (
	KnowledgeMapRepresentationGraph        KnowledgeMapRepresentation = "graph"
	KnowledgeMapRepresentationDocumentTree KnowledgeMapRepresentation = "document-tree"
	KnowledgeMapRepresentationCausal       KnowledgeMapRepresentation = "causal"
	KnowledgeMapRepresentationProcedure    KnowledgeMapRepresentation = "procedure-sequence"
	KnowledgeMapRepresentationTimeline     KnowledgeMapRepresentation = "timeline"
)

type KnowledgeMapNodePosition struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Pinned bool    `json:"pinned,omitempty"`
}

type KnowledgeMapViewport struct {
	Scale float64 `json:"scale"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// KnowledgeMapViewFilters stores the user's visual filters. A nil State means
// that every filter is enabled; non-nil empty slices intentionally mean that
// the corresponding group is fully hidden.
type KnowledgeMapViewFilters struct {
	Statuses      []KnowledgeStatus       `json:"statuses"`
	Evidence      []EvidenceState         `json:"evidence"`
	NodeKinds     []KnowledgeNodeKind     `json:"node_kinds"`
	RelationKinds []KnowledgeRelationKind `json:"relation_kinds"`
}

// KnowledgeMapFocus describes one reproducible visual scope. NodeID and
// ClusterID are mutually exclusive. Cluster IDs are deterministic seed-node
// IDs computed by the standalone client from the current graph.
type KnowledgeMapFocus struct {
	NodeID    string `json:"node_id,omitempty"`
	ClusterID string `json:"cluster_id,omitempty"`
	Depth     int    `json:"depth,omitempty"`
}

// KnowledgeMapViewState is presentation state saved together with positions.
// It contains no generated knowledge and never changes graph provenance.
type KnowledgeMapViewState struct {
	Filters        KnowledgeMapViewFilters    `json:"filters"`
	Focus          *KnowledgeMapFocus         `json:"focus,omitempty"`
	Collapsed      []string                   `json:"collapsed,omitempty"`
	ClusterLayout  bool                       `json:"cluster_layout,omitempty"`
	Representation KnowledgeMapRepresentation `json:"representation,omitempty"`
}

type KnowledgeMapLayout struct {
	Version  int                                 `json:"version"`
	Nodes    map[string]KnowledgeMapNodePosition `json:"nodes"`
	Viewport KnowledgeMapViewport                `json:"viewport"`
	State    *KnowledgeMapViewState              `json:"state,omitempty"`
	Updated  string                              `json:"updated,omitempty"`
}

type KnowledgeMapViewSummary struct {
	Name           string                     `json:"name"`
	Updated        string                     `json:"updated,omitempty"`
	NodeCount      int                        `json:"node_count"`
	Focused        bool                       `json:"focused"`
	Collapsed      int                        `json:"collapsed"`
	ClusterLayout  bool                       `json:"cluster_layout"`
	Representation KnowledgeMapRepresentation `json:"representation,omitempty"`
}

func (s *Store) LoadKnowledgeMapLayout(name string) (*KnowledgeMapLayout, error) {
	name, err := validateKnowledgeMapViewName(name)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var raw string
	if err := s.db.QueryRow(`SELECT layout_json FROM knowledge_map_views WHERE name = ?`, name).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load knowledge map view %q: %w", name, err)
	}
	layout, err := decodeKnowledgeMapLayout([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("load knowledge map view %q: %w", name, err)
	}
	return &layout, nil
}

func (s *Store) SaveKnowledgeMapLayout(name string, layout KnowledgeMapLayout) (KnowledgeMapLayout, error) {
	name, err := validateKnowledgeMapViewName(name)
	if err != nil {
		return KnowledgeMapLayout{}, err
	}
	layout.Updated = ""
	if err := validateKnowledgeMapLayout(layout); err != nil {
		return KnowledgeMapLayout{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM knowledge_nodes`)
	if err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("inspect knowledge nodes for map view: %w", err)
	}
	known := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return KnowledgeMapLayout{}, fmt.Errorf("read knowledge node for map view: %w", err)
		}
		known[id] = true
	}
	if err := rows.Close(); err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("close knowledge node map view scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("read knowledge nodes for map view: %w", err)
	}
	for id := range layout.Nodes {
		if !known[id] {
			return KnowledgeMapLayout{}, fmt.Errorf("knowledge map view references unknown node %q", id)
		}
	}
	if layout.State != nil {
		for _, id := range layout.State.Collapsed {
			if !known[id] {
				return KnowledgeMapLayout{}, fmt.Errorf("knowledge map view collapses unknown node %q", id)
			}
		}
		if focus := layout.State.Focus; focus != nil {
			id := focus.NodeID
			if id == "" {
				id = focus.ClusterID
			}
			if !known[id] {
				return KnowledgeMapLayout{}, fmt.Errorf("knowledge map view focuses unknown node %q", id)
			}
		}
	}
	var viewCount, existing int
	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(CASE WHEN name = ? THEN 1 END) FROM knowledge_map_views`, name).Scan(&viewCount, &existing); err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("count knowledge map views: %w", err)
	}
	if existing == 0 && viewCount >= MaxKnowledgeMapViews {
		return KnowledgeMapLayout{}, fmt.Errorf("knowledge map view count exceeds %d", MaxKnowledgeMapViews)
	}
	layout.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(layout)
	if err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("encode knowledge map view: %w", err)
	}
	if len(raw) > MaxKnowledgeMapLayoutJSON {
		return KnowledgeMapLayout{}, fmt.Errorf("knowledge map view exceeds %d bytes", MaxKnowledgeMapLayoutJSON)
	}
	if _, err := s.db.Exec(`
INSERT INTO knowledge_map_views(name, layout_json, updated) VALUES(?, ?, ?)
ON CONFLICT(name) DO UPDATE SET layout_json = excluded.layout_json, updated = excluded.updated`,
		name, string(raw), layout.Updated); err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("save knowledge map view %q: %w", name, err)
	}
	return layout, nil
}

// ListKnowledgeMapViews returns lightweight, user-facing saved-view metadata.
// The default view is always present even before its first layout save.
func (s *Store) ListKnowledgeMapViews() ([]KnowledgeMapViewSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT name, layout_json, updated FROM knowledge_map_views ORDER BY name LIMIT ?`, MaxKnowledgeMapViews+1)
	if err != nil {
		return nil, fmt.Errorf("list knowledge map views: %w", err)
	}
	defer rows.Close()
	views := make([]KnowledgeMapViewSummary, 0)
	hasDefault := false
	for rows.Next() {
		var name, raw, updated string
		if err := rows.Scan(&name, &raw, &updated); err != nil {
			return nil, fmt.Errorf("read knowledge map view: %w", err)
		}
		if len(views) >= MaxKnowledgeMapViews {
			return nil, fmt.Errorf("knowledge map view count exceeds %d", MaxKnowledgeMapViews)
		}
		layout, err := decodeKnowledgeMapLayout([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("read knowledge map view %q: %w", name, err)
		}
		summary := KnowledgeMapViewSummary{Name: name, Updated: updated, NodeCount: len(layout.Nodes)}
		if layout.State != nil {
			summary.Focused = layout.State.Focus != nil
			summary.Collapsed = len(layout.State.Collapsed)
			summary.ClusterLayout = layout.State.ClusterLayout
			summary.Representation = layout.State.Representation
		}
		views = append(views, summary)
		hasDefault = hasDefault || name == DefaultKnowledgeMapView
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list knowledge map views: %w", err)
	}
	if !hasDefault {
		views = append([]KnowledgeMapViewSummary{{Name: DefaultKnowledgeMapView}}, views...)
	}
	return views, nil
}

func (s *Store) DeleteKnowledgeMapLayout(name string) error {
	name, err := validateKnowledgeMapViewName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM knowledge_map_views WHERE name = ?`, name); err != nil {
		return fmt.Errorf("delete knowledge map view %q: %w", name, err)
	}
	return nil
}

func validateKnowledgeMapViewName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", errors.New("knowledge map view name must contain 1..128 bytes")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("knowledge map view name contains control characters")
		}
	}
	return name, nil
}

func decodeKnowledgeMapLayout(raw []byte) (KnowledgeMapLayout, error) {
	if len(raw) == 0 || len(raw) > MaxKnowledgeMapLayoutJSON {
		return KnowledgeMapLayout{}, fmt.Errorf("knowledge map layout must contain 1..%d bytes", MaxKnowledgeMapLayoutJSON)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var layout KnowledgeMapLayout
	if err := decoder.Decode(&layout); err != nil {
		return KnowledgeMapLayout{}, fmt.Errorf("decode knowledge map layout: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return KnowledgeMapLayout{}, errors.New("knowledge map layout contains data after the JSON object")
		}
		return KnowledgeMapLayout{}, fmt.Errorf("decode knowledge map layout: %w", err)
	}
	if err := validateKnowledgeMapLayout(layout); err != nil {
		return KnowledgeMapLayout{}, err
	}
	return layout, nil
}

func validateKnowledgeMapLayout(layout KnowledgeMapLayout) error {
	if layout.Version != knowledgeMapLayoutV1 && layout.Version != knowledgeMapLayoutV2 &&
		layout.Version != knowledgeMapLayoutV3 && layout.Version != knowledgeMapLayoutV4 &&
		layout.Version != knowledgeMapLayoutV5 &&
		layout.Version != KnowledgeMapLayoutVersion {
		return fmt.Errorf("unsupported knowledge map layout version %d", layout.Version)
	}
	if len(layout.Nodes) > MaxKnowledgeMapViewNodes {
		return fmt.Errorf("knowledge map layout exceeds %d nodes", MaxKnowledgeMapViewNodes)
	}
	for id, position := range layout.Nodes {
		if strings.TrimSpace(id) != id || id == "" || len(id) > MaxKnowledgeIDBytes {
			return fmt.Errorf("knowledge map layout contains invalid node ID %q", id)
		}
		if !finiteMapCoordinate(position.X) || !finiteMapCoordinate(position.Y) {
			return fmt.Errorf("knowledge map layout node %q has invalid coordinates", id)
		}
	}
	if !finiteMapCoordinate(layout.Viewport.X) || !finiteMapCoordinate(layout.Viewport.Y) ||
		math.IsNaN(layout.Viewport.Scale) || math.IsInf(layout.Viewport.Scale, 0) ||
		layout.Viewport.Scale < 0.05 || layout.Viewport.Scale > 10 {
		return errors.New("knowledge map layout has invalid viewport")
	}
	if layout.State != nil {
		if layout.Version < knowledgeMapLayoutV2 {
			return errors.New("knowledge map layout state requires version 2")
		}
		if err := validateKnowledgeMapViewState(layout.Version, *layout.State); err != nil {
			return err
		}
	}
	return nil
}

func validateKnowledgeMapViewState(version int, state KnowledgeMapViewState) error {
	if version < knowledgeMapLayoutV5 && state.Representation != "" {
		return errors.New("knowledge map representation requires version 5")
	}
	if version >= knowledgeMapLayoutV5 && state.Representation != KnowledgeMapRepresentationGraph &&
		state.Representation != KnowledgeMapRepresentationDocumentTree &&
		state.Representation != KnowledgeMapRepresentationCausal &&
		state.Representation != KnowledgeMapRepresentationProcedure &&
		state.Representation != KnowledgeMapRepresentationTimeline {
		return errors.New("knowledge map representation must be graph, document-tree, causal, procedure-sequence, or timeline")
	}
	if version < KnowledgeMapLayoutVersion && state.Representation == KnowledgeMapRepresentationTimeline {
		return errors.New("knowledge map timeline representation requires version 6")
	}
	if err := validateUniqueMapValues("status", len(state.Filters.Statuses), func(index int) string {
		value := state.Filters.Statuses[index]
		if !validKnowledgeStatus(value) {
			return ""
		}
		return string(value)
	}); err != nil {
		return err
	}
	if err := validateUniqueMapValues("evidence", len(state.Filters.Evidence), func(index int) string {
		value := state.Filters.Evidence[index]
		if value != EvidenceCurrent && value != EvidenceStale && value != EvidenceMissing {
			return ""
		}
		return string(value)
	}); err != nil {
		return err
	}
	if err := validateUniqueMapValues("node kind", len(state.Filters.NodeKinds), func(index int) string {
		value := state.Filters.NodeKinds[index]
		if !validKnowledgeNodeKind(value) {
			return ""
		}
		return string(value)
	}); err != nil {
		return err
	}
	if err := validateUniqueMapValues("relation kind", len(state.Filters.RelationKinds), func(index int) string {
		value := state.Filters.RelationKinds[index]
		if !validKnowledgeRelationKind(value) {
			return ""
		}
		return string(value)
	}); err != nil {
		return err
	}
	if len(state.Collapsed) > MaxKnowledgeMapViewNodes {
		return fmt.Errorf("knowledge map view collapses more than %d nodes", MaxKnowledgeMapViewNodes)
	}
	seenCollapsed := make(map[string]bool, len(state.Collapsed))
	for _, id := range state.Collapsed {
		if !validKnowledgeMapStateID(id) || seenCollapsed[id] {
			return fmt.Errorf("knowledge map view contains invalid or duplicate collapsed node %q", id)
		}
		seenCollapsed[id] = true
	}
	if focus := state.Focus; focus != nil {
		if (focus.NodeID == "") == (focus.ClusterID == "") {
			return errors.New("knowledge map focus must contain exactly one node_id or cluster_id")
		}
		if focus.NodeID != "" {
			if !validKnowledgeMapStateID(focus.NodeID) || focus.Depth < 1 || focus.Depth > 3 {
				return errors.New("knowledge map node focus must use a known ID and depth 1..3")
			}
		} else if !validKnowledgeMapStateID(focus.ClusterID) || focus.Depth != 0 {
			return errors.New("knowledge map cluster focus must use a known seed ID without depth")
		}
	}
	return nil
}

func validateUniqueMapValues(kind string, count int, value func(int) string) error {
	if count > 64 {
		return fmt.Errorf("knowledge map view contains too many %s filters", kind)
	}
	seen := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		item := value(i)
		if item == "" || seen[item] {
			return fmt.Errorf("knowledge map view contains invalid or duplicate %s filter", kind)
		}
		seen[item] = true
	}
	return nil
}

func validKnowledgeMapStateID(id string) bool {
	return strings.TrimSpace(id) == id && id != "" && len(id) <= MaxKnowledgeIDBytes
}

func finiteMapCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= 1e9
}
