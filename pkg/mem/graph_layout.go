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
	KnowledgeMapLayoutVersion = 1
	DefaultKnowledgeMapView   = "default"
	MaxKnowledgeMapViewNodes  = 10000
	MaxKnowledgeMapLayoutJSON = 1 << 20
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

type KnowledgeMapLayout struct {
	Version  int                                 `json:"version"`
	Nodes    map[string]KnowledgeMapNodePosition `json:"nodes"`
	Viewport KnowledgeMapViewport                `json:"viewport"`
	Updated  string                              `json:"updated,omitempty"`
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
	if layout.Version != KnowledgeMapLayoutVersion {
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
	return nil
}

func finiteMapCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= 1e9
}
