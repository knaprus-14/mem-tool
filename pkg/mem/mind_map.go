package mem

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ClassicMindMap is intentionally separate from KnowledgeGraph. The knowledge
// graph is a many-to-many, provenance-first analytical model; a classic mind
// map is an ordered, single-parent tree which is pleasant to read and edit.
const (
	ClassicMindMapFormatVersion = 1
	MaxClassicMindMapTitleRunes = 512
	MaxClassicMindMapTextRunes  = 65536
)

type ClassicMindMapMode string

const (
	ClassicMindMapModeManual    ClassicMindMapMode = "manual"
	ClassicMindMapModeGenerated ClassicMindMapMode = "generated"
	ClassicMindMapModeHybrid    ClassicMindMapMode = "hybrid"
)

type ClassicMindMapStatus string

const (
	ClassicMindMapStatusDraft    ClassicMindMapStatus = "draft"
	ClassicMindMapStatusReady    ClassicMindMapStatus = "ready"
	ClassicMindMapStatusArchived ClassicMindMapStatus = "archived"
)

type ClassicMindMapNodeKind string

const (
	ClassicMindMapNodeTopic    ClassicMindMapNodeKind = "topic"
	ClassicMindMapNodeSubtopic ClassicMindMapNodeKind = "subtopic"
	ClassicMindMapNodeFact     ClassicMindMapNodeKind = "fact"
	ClassicMindMapNodeNote     ClassicMindMapNodeKind = "note"
	ClassicMindMapNodeQuote    ClassicMindMapNodeKind = "quote"
	ClassicMindMapNodeQuestion ClassicMindMapNodeKind = "question"
	ClassicMindMapNodeTask     ClassicMindMapNodeKind = "task"
	ClassicMindMapNodeDecision ClassicMindMapNodeKind = "decision"
	ClassicMindMapNodeLink     ClassicMindMapNodeKind = "link"
)

type ClassicMindMapNodeOrigin string

const (
	ClassicMindMapNodeManual    ClassicMindMapNodeOrigin = "manual"
	ClassicMindMapNodeGenerated ClassicMindMapNodeOrigin = "generated"
	ClassicMindMapNodeImported  ClassicMindMapNodeOrigin = "imported"
)

type ClassicMindMapSourceKind string

const (
	ClassicMindMapSourceEvidence      ClassicMindMapSourceKind = "evidence"
	ClassicMindMapSourceExternalFile  ClassicMindMapSourceKind = "external_file"
	ClassicMindMapSourceURL           ClassicMindMapSourceKind = "url"
	ClassicMindMapSourceKnowledgeNode ClassicMindMapSourceKind = "knowledge_node"
)

type ClassicMindMap struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Description string               `json:"description,omitempty"`
	Mode        ClassicMindMapMode   `json:"mode"`
	Status      ClassicMindMapStatus `json:"status"`
	RootNodeID  string               `json:"root_node_id"`
	Revision    int64                `json:"revision"`
	Created     string               `json:"created"`
	Updated     string               `json:"updated"`
}

type ClassicMindMapNode struct {
	ID           string                   `json:"id"`
	MapID        string                   `json:"map_id"`
	ParentID     string                   `json:"parent_id,omitempty"`
	Position     int                      `json:"position"`
	Label        string                   `json:"label"`
	Summary      string                   `json:"summary,omitempty"`
	BodyMarkdown string                   `json:"body_markdown,omitempty"`
	Kind         ClassicMindMapNodeKind   `json:"kind"`
	Origin       ClassicMindMapNodeOrigin `json:"origin"`
	Locked       bool                     `json:"locked,omitempty"`
	Style        json.RawMessage          `json:"style,omitempty"`
	Created      string                   `json:"created"`
	Updated      string                   `json:"updated"`
	Sources      []ClassicMindMapSource   `json:"sources"`
}

type ClassicMindMapSource struct {
	ID              string                   `json:"id"`
	MapID           string                   `json:"map_id"`
	NodeID          string                   `json:"node_id"`
	Position        int                      `json:"position"`
	Kind            ClassicMindMapSourceKind `json:"kind"`
	Title           string                   `json:"title,omitempty"`
	Locator         string                   `json:"locator,omitempty"`
	URL             string                   `json:"url,omitempty"`
	KnowledgeNodeID string                   `json:"knowledge_node_id,omitempty"`
	Evidence        *EvidenceAnchor          `json:"evidence,omitempty"`
	EvidenceState   EvidenceState            `json:"evidence_state,omitempty"`
	Created         string                   `json:"created"`
}

type ClassicMindMapDocument struct {
	Version     int                  `json:"version"`
	Map         ClassicMindMap       `json:"map"`
	Nodes       []ClassicMindMapNode `json:"nodes"`
	Digest      string               `json:"digest"`
	StateDigest string               `json:"state_digest,omitempty"`
}

type ClassicMindMapSummary struct {
	ClassicMindMap
	NodeCount   int `json:"node_count"`
	SourceCount int `json:"source_count"`
}

type ClassicMindMapChange struct {
	ID              int64  `json:"id"`
	MapID           string `json:"map_id"`
	BaseRevision    int64  `json:"base_revision"`
	NewRevision     int64  `json:"new_revision"`
	Action          string `json:"action"`
	TargetNodeID    string `json:"target_node_id,omitempty"`
	BeforeDigest    string `json:"before_digest"`
	AfterDigest     string `json:"after_digest"`
	RevertsChangeID int64  `json:"reverts_change_id,omitempty"`
	Actor           string `json:"actor"`
	Comment         string `json:"comment,omitempty"`
	Created         string `json:"created"`
	beforeJSON      string
	afterJSON       string
}

type ClassicMindMapSnapshot struct {
	ID             int64  `json:"id"`
	MapID          string `json:"map_id"`
	Revision       int64  `json:"revision"`
	Reason         string `json:"reason"`
	DocumentDigest string `json:"document_digest"`
	Created        string `json:"created"`
}

type ClassicMindMapNodeDraft struct {
	Ref          string                   `json:"ref"`
	ParentRef    string                   `json:"parent_ref,omitempty"`
	Label        string                   `json:"label"`
	Summary      string                   `json:"summary,omitempty"`
	BodyMarkdown string                   `json:"body_markdown,omitempty"`
	Kind         ClassicMindMapNodeKind   `json:"kind,omitempty"`
	Origin       ClassicMindMapNodeOrigin `json:"origin,omitempty"`
	Locked       bool                     `json:"locked,omitempty"`
	Style        json.RawMessage          `json:"style,omitempty"`
	Sources      []ClassicMindMapSource   `json:"sources,omitempty"`
}

type ClassicMindMapGenerationDraft struct {
	Prompt        string          `json:"prompt"`
	Scope         json.RawMessage `json:"scope,omitempty"`
	Model         string          `json:"model,omitempty"`
	RequestDigest string          `json:"request_digest,omitempty"`
	ResultDigest  string          `json:"result_digest,omitempty"`
}

type ClassicMindMapDraft struct {
	Title       string                         `json:"title"`
	Description string                         `json:"description,omitempty"`
	Mode        ClassicMindMapMode             `json:"mode"`
	Status      ClassicMindMapStatus           `json:"status,omitempty"`
	Nodes       []ClassicMindMapNodeDraft      `json:"nodes"`
	Generation  *ClassicMindMapGenerationDraft `json:"generation,omitempty"`
}

type ClassicMindMapNodePatch struct {
	Label        *string
	Summary      *string
	BodyMarkdown *string
	Kind         *ClassicMindMapNodeKind
	Locked       *bool
}

// ClassicMindMapPatch changes library-level metadata without replacing the
// tree. Nil fields are left unchanged so HTTP and CLI clients can use the same
// optimistic-concurrency contract as node edits.
type ClassicMindMapPatch struct {
	Title       *string
	Description *string
	Status      *ClassicMindMapStatus
}

type ClassicMindMapDeleteMode string

const (
	ClassicMindMapDeleteBranch          ClassicMindMapDeleteMode = "branch"
	ClassicMindMapDeletePromoteChildren ClassicMindMapDeleteMode = "promote_children"
)

var (
	ErrClassicMindMapNotFound         = errors.New("classic mind map was not found")
	ErrClassicMindMapNodeNotFound     = errors.New("classic mind map node was not found")
	ErrClassicMindMapAmbiguousRef     = errors.New("classic mind map reference is ambiguous")
	ErrClassicMindMapRevisionConflict = errors.New("classic mind map revision conflict")
	ErrClassicMindMapLocked           = errors.New("classic mind map node is locked")
)

const classicMindMapSchema = `
CREATE TABLE IF NOT EXISTS mind_maps (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL,
    status TEXT NOT NULL,
    root_node_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    created TEXT NOT NULL,
    updated TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS mind_map_nodes (
    id TEXT PRIMARY KEY,
    map_id TEXT NOT NULL,
    parent_id TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL,
    label TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    body_markdown TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    origin TEXT NOT NULL,
    locked INTEGER NOT NULL DEFAULT 0,
    style_json TEXT NOT NULL DEFAULT '{}',
    created TEXT NOT NULL,
    updated TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS mind_map_node_sources (
    id TEXT PRIMARY KEY,
    map_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    kind TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    locator TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    knowledge_node_id TEXT NOT NULL DEFAULT '',
    citation_id TEXT NOT NULL DEFAULT '',
    document_id TEXT NOT NULL DEFAULT '',
    document_revision TEXT NOT NULL DEFAULT '',
    chunk_hash TEXT NOT NULL DEFAULT '',
    evidence_hash TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    page INTEGER NOT NULL DEFAULT 0,
    block_index INTEGER NOT NULL DEFAULT 0,
    block_chunk_index INTEGER NOT NULL DEFAULT 0,
    excerpt TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS mind_map_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    map_id TEXT NOT NULL,
    base_revision INTEGER NOT NULL,
    new_revision INTEGER NOT NULL,
    action TEXT NOT NULL,
    target_node_id TEXT NOT NULL DEFAULT '',
    before_json TEXT NOT NULL,
    after_json TEXT NOT NULL,
    before_digest TEXT NOT NULL,
    after_digest TEXT NOT NULL,
    reverts_change_id INTEGER NOT NULL DEFAULT 0,
    actor TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mind_map_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    map_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    reason TEXT NOT NULL,
    document_json TEXT NOT NULL,
    document_digest TEXT NOT NULL,
    created TEXT NOT NULL,
    UNIQUE(map_id, revision, reason)
);

CREATE TABLE IF NOT EXISTS mind_map_generation_runs (
    id TEXT PRIMARY KEY,
    map_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    prompt TEXT NOT NULL,
    scope_json TEXT NOT NULL DEFAULT '{}',
    model TEXT NOT NULL DEFAULT '',
    request_digest TEXT NOT NULL DEFAULT '',
    result_digest TEXT NOT NULL DEFAULT '',
    error_text TEXT NOT NULL DEFAULT '',
    created TEXT NOT NULL,
    completed TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_mind_maps_updated ON mind_maps(deleted_at, updated DESC);
CREATE INDEX IF NOT EXISTS idx_mind_map_nodes_tree ON mind_map_nodes(map_id, deleted_at, parent_id, position);
CREATE INDEX IF NOT EXISTS idx_mind_map_sources_node ON mind_map_node_sources(map_id, node_id, deleted_at, position);
CREATE INDEX IF NOT EXISTS idx_mind_map_changes_map ON mind_map_changes(map_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_mind_map_snapshots_map ON mind_map_snapshots(map_id, revision DESC);
CREATE INDEX IF NOT EXISTS idx_mind_map_generation_map ON mind_map_generation_runs(map_id, created DESC);

CREATE TRIGGER IF NOT EXISTS mind_map_changes_no_update
BEFORE UPDATE ON mind_map_changes BEGIN
    SELECT RAISE(ABORT, 'mind map change history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS mind_map_changes_no_delete
BEFORE DELETE ON mind_map_changes BEGIN
    SELECT RAISE(ABORT, 'mind map change history is append-only');
END;
CREATE TRIGGER IF NOT EXISTS mind_map_snapshots_no_update
BEFORE UPDATE ON mind_map_snapshots BEGIN
    SELECT RAISE(ABORT, 'mind map snapshots are immutable');
END;
CREATE TRIGGER IF NOT EXISTS mind_map_snapshots_no_delete
BEFORE DELETE ON mind_map_snapshots BEGIN
    SELECT RAISE(ABORT, 'mind map snapshots are immutable');
END;
`

type classicMindMapQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func CreateClassicMindMapDraft(title, description string) ClassicMindMapDraft {
	return ClassicMindMapDraft{
		Title: title, Description: description, Mode: ClassicMindMapModeManual,
		Status: ClassicMindMapStatusDraft,
		Nodes:  []ClassicMindMapNodeDraft{{Ref: "root", Label: title, Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual}},
	}
}

func (s *Store) CreateClassicMindMap(title, description string) (ClassicMindMapDocument, error) {
	return s.ImportClassicMindMap(CreateClassicMindMapDraft(title, description), "user", "создана пустая карта")
}

// DuplicateClassicMindMap creates an independent map with new host-assigned
// IDs while preserving the ordered tree and its provenance links.
func (s *Store) DuplicateClassicMindMap(mapRef, title string) (ClassicMindMapDocument, error) {
	source, err := s.LoadClassicMindMap(mapRef)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = source.Map.Title + " — копия"
	}
	refs := make(map[string]string, len(source.Nodes))
	for i, node := range source.Nodes {
		refs[node.ID] = fmt.Sprintf("node-%d", i+1)
	}
	draft := ClassicMindMapDraft{
		Title: title, Description: source.Map.Description, Mode: ClassicMindMapModeManual,
		Status: ClassicMindMapStatusDraft, Nodes: make([]ClassicMindMapNodeDraft, 0, len(source.Nodes)),
	}
	for _, node := range source.Nodes {
		parentRef := ""
		if node.ParentID != "" {
			parentRef = refs[node.ParentID]
		}
		label := node.Label
		if node.ID == source.Map.RootNodeID && node.Label == source.Map.Title {
			label = title
		}
		draft.Nodes = append(draft.Nodes, ClassicMindMapNodeDraft{
			Ref: refs[node.ID], ParentRef: parentRef, Label: label, Summary: node.Summary,
			BodyMarkdown: node.BodyMarkdown, Kind: node.Kind, Origin: ClassicMindMapNodeManual,
			Locked: node.Locked, Style: append(json.RawMessage(nil), node.Style...),
			Sources: append([]ClassicMindMapSource(nil), node.Sources...),
		})
	}
	return s.ImportClassicMindMap(draft, "user", "создана независимая копия карты")
}

// ImportClassicMindMap validates the complete tree before opening a write
// transaction. Generated results therefore cannot publish a partial map.
func (s *Store) ImportClassicMindMap(draft ClassicMindMapDraft, actor, comment string) (ClassicMindMapDocument, error) {
	normalized, rootRef, err := normalizeClassicMindMapDraft(draft)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	actor = normalizeClassicMindMapActor(actor)
	mapID, err := newClassicMindMapID("mm-")
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	ids := make(map[string]string, len(normalized.Nodes))
	for _, node := range normalized.Nodes {
		id, idErr := newClassicMindMapID("mmn-")
		if idErr != nil {
			return ClassicMindMapDocument{}, idErr
		}
		ids[node.Ref] = id
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, node := range normalized.Nodes {
		for _, source := range node.Sources {
			if source.Kind == ClassicMindMapSourceEvidence {
				if source.Evidence == nil || resolveEvidenceAnchorFromEntries(*source.Evidence, s.entries).State != EvidenceCurrent {
					return ClassicMindMapDocument{}, fmt.Errorf("узел %q содержит неактуальный или отсутствующий источник", node.Label)
				}
			}
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	rollback := func(cause error) (ClassicMindMapDocument, error) {
		_ = tx.Rollback()
		return ClassicMindMapDocument{}, cause
	}
	rootID := ids[rootRef]
	if _, err := tx.Exec(`INSERT INTO mind_maps
(id, title, description, mode, status, root_node_id, revision, created, updated)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, mapID, normalized.Title, normalized.Description,
		normalized.Mode, normalized.Status, rootID, now, now); err != nil {
		return rollback(fmt.Errorf("create classic mind map: %w", err))
	}
	positions := make(map[string]int)
	for _, node := range normalized.Nodes {
		parentID := ""
		if node.ParentRef != "" {
			parentID = ids[node.ParentRef]
		}
		position := positions[node.ParentRef]
		positions[node.ParentRef]++
		style := normalizeClassicMindMapStyle(node.Style)
		if _, err := tx.Exec(`INSERT INTO mind_map_nodes
(id, map_id, parent_id, position, label, summary, body_markdown, kind, origin, locked, style_json, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ids[node.Ref], mapID, parentID, position,
			node.Label, node.Summary, node.BodyMarkdown, node.Kind, node.Origin, boolInt(node.Locked), string(style), now, now); err != nil {
			return rollback(fmt.Errorf("create classic mind map node %q: %w", node.Label, err))
		}
		for ordinal, source := range node.Sources {
			if err := insertClassicMindMapSource(tx, mapID, ids[node.Ref], ordinal, source, now); err != nil {
				return rollback(err)
			}
		}
	}
	doc, err := loadClassicMindMapDocument(tx, mapID)
	if err != nil {
		return rollback(err)
	}
	doc, encoded, err := finalizeClassicMindMapDocument(doc)
	if err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO mind_map_changes
(map_id, base_revision, new_revision, action, before_json, after_json, before_digest, after_digest, actor, comment, created)
VALUES (?, 0, 1, 'create_map', '{}', ?, ?, ?, ?, ?, ?)`, mapID, string(encoded), emptyClassicMindMapDigest(), doc.Digest, actor, strings.TrimSpace(comment), now); err != nil {
		return rollback(fmt.Errorf("append classic mind map creation: %w", err))
	}
	if _, err := tx.Exec(`INSERT INTO mind_map_snapshots
(map_id, revision, reason, document_json, document_digest, created) VALUES (?, 1, 'created', ?, ?, ?)`, mapID, string(encoded), doc.Digest, now); err != nil {
		return rollback(fmt.Errorf("snapshot classic mind map creation: %w", err))
	}
	if normalized.Generation != nil {
		runID, idErr := newClassicMindMapID("mmg-")
		if idErr != nil {
			return rollback(idErr)
		}
		scope := normalized.Generation.Scope
		if len(scope) == 0 {
			scope = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(`INSERT INTO mind_map_generation_runs
(id, map_id, status, prompt, scope_json, model, request_digest, result_digest, created, completed)
VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?)`, runID, mapID, normalized.Generation.Prompt,
			string(scope), normalized.Generation.Model, normalized.Generation.RequestDigest,
			normalized.Generation.ResultDigest, now, now); err != nil {
			return rollback(fmt.Errorf("record classic mind map generation: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, fmt.Errorf("commit classic mind map: %w", err)
	}
	return s.resolveClassicMindMapSourceStates(doc)
}

func (s *Store) ListClassicMindMaps(includeArchived bool) ([]ClassicMindMapSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query := `SELECT m.id, m.title, m.description, m.mode, m.status, m.root_node_id, m.revision, m.created, m.updated,
(SELECT COUNT(*) FROM mind_map_nodes n WHERE n.map_id=m.id AND n.deleted_at=''),
(SELECT COUNT(*) FROM mind_map_node_sources src WHERE src.map_id=m.id AND src.deleted_at='')
FROM mind_maps m WHERE m.deleted_at=''`
	args := []any{}
	if !includeArchived {
		query += ` AND m.status != ?`
		args = append(args, ClassicMindMapStatusArchived)
	}
	query += ` ORDER BY m.updated DESC, lower(m.title), m.id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list classic mind maps: %w", err)
	}
	defer rows.Close()
	result := make([]ClassicMindMapSummary, 0)
	for rows.Next() {
		var item ClassicMindMapSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Mode, &item.Status,
			&item.RootNodeID, &item.Revision, &item.Created, &item.Updated, &item.NodeCount, &item.SourceCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) LoadClassicMindMap(ref string) (ClassicMindMapDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, fmt.Errorf("begin classic mind map load: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, err := resolveClassicMindMapRef(tx, ref)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	doc, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	doc, _, err = finalizeClassicMindMapDocument(doc)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	doc, err = resolveClassicMindMapSourceStatesWithQuery(tx, doc)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, fmt.Errorf("finish classic mind map load: %w", err)
	}
	return doc, nil
}

func normalizeClassicMindMapDraft(draft ClassicMindMapDraft) (ClassicMindMapDraft, string, error) {
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Description = strings.TrimSpace(draft.Description)
	if err := validateClassicMindMapText("название карты", draft.Title, MaxClassicMindMapTitleRunes, true); err != nil {
		return draft, "", err
	}
	if err := validateClassicMindMapText("описание карты", draft.Description, MaxClassicMindMapTextRunes, false); err != nil {
		return draft, "", err
	}
	if draft.Mode == "" {
		draft.Mode = ClassicMindMapModeManual
	}
	if !validClassicMindMapMode(draft.Mode) {
		return draft, "", fmt.Errorf("неподдерживаемый режим карты %q", draft.Mode)
	}
	if draft.Status == "" {
		draft.Status = ClassicMindMapStatusDraft
	}
	if !validClassicMindMapStatus(draft.Status) {
		return draft, "", fmt.Errorf("неподдерживаемый статус карты %q", draft.Status)
	}
	if len(draft.Nodes) == 0 || len(draft.Nodes) > 100000 {
		return draft, "", fmt.Errorf("карта должна содержать от 1 до 100000 узлов")
	}
	refs := make(map[string]bool, len(draft.Nodes))
	rootRef := ""
	for i := range draft.Nodes {
		node := &draft.Nodes[i]
		node.Ref = strings.TrimSpace(node.Ref)
		node.ParentRef = strings.TrimSpace(node.ParentRef)
		node.Label = strings.TrimSpace(node.Label)
		node.Summary = strings.TrimSpace(node.Summary)
		if node.Ref == "" || len(node.Ref) > MaxKnowledgeIDBytes || strings.ContainsAny(node.Ref, "\x00\r\n") {
			return draft, "", fmt.Errorf("узел %d имеет недопустимый ref", i+1)
		}
		if refs[node.Ref] {
			return draft, "", fmt.Errorf("повторяющийся ref узла %q", node.Ref)
		}
		refs[node.Ref] = true
		if node.ParentRef == "" {
			if rootRef != "" {
				return draft, "", fmt.Errorf("карта должна иметь ровно один корневой узел")
			}
			rootRef = node.Ref
		}
		if node.Kind == "" {
			if node.ParentRef == "" {
				node.Kind = ClassicMindMapNodeTopic
			} else {
				node.Kind = ClassicMindMapNodeSubtopic
			}
		}
		if node.Origin == "" {
			if draft.Mode == ClassicMindMapModeGenerated {
				node.Origin = ClassicMindMapNodeGenerated
			} else {
				node.Origin = ClassicMindMapNodeManual
			}
		}
		if err := validateClassicMindMapNodeFields(node.Label, node.Summary, node.BodyMarkdown, node.Kind, node.Origin, node.Style); err != nil {
			return draft, "", fmt.Errorf("узел %q: %w", node.Ref, err)
		}
		for j := range node.Sources {
			// IDs and ownership are always host-assigned. A model or imported JSON
			// cannot overwrite another map's source row by choosing an ID.
			node.Sources[j].ID = ""
			node.Sources[j].MapID = ""
			node.Sources[j].NodeID = ""
			node.Sources[j].Position = j
			node.Sources[j].EvidenceState = ""
			if err := validateClassicMindMapSource(node.Sources[j]); err != nil {
				return draft, "", fmt.Errorf("узел %q, источник %d: %w", node.Ref, j+1, err)
			}
		}
	}
	if rootRef == "" {
		return draft, "", fmt.Errorf("карта не содержит корневой узел")
	}
	for _, node := range draft.Nodes {
		if node.ParentRef != "" && !refs[node.ParentRef] {
			return draft, "", fmt.Errorf("узел %q ссылается на неизвестного родителя %q", node.Ref, node.ParentRef)
		}
	}
	parent := make(map[string]string, len(draft.Nodes))
	for _, node := range draft.Nodes {
		parent[node.Ref] = node.ParentRef
	}
	for ref := range refs {
		seen := map[string]bool{}
		cursor := ref
		for cursor != "" {
			if seen[cursor] {
				return draft, "", fmt.Errorf("карта содержит цикл через узел %q", cursor)
			}
			seen[cursor] = true
			cursor = parent[cursor]
		}
	}
	if draft.Generation != nil {
		draft.Generation.Prompt = strings.TrimSpace(draft.Generation.Prompt)
		if draft.Generation.Prompt == "" {
			return draft, "", fmt.Errorf("generation prompt пуст")
		}
		if len(draft.Generation.Scope) > 0 && !json.Valid(draft.Generation.Scope) {
			return draft, "", fmt.Errorf("generation scope не является JSON")
		}
	}
	return draft, rootRef, nil
}

func validateClassicMindMapNodeFields(label, summary, body string, kind ClassicMindMapNodeKind, origin ClassicMindMapNodeOrigin, style json.RawMessage) error {
	if err := validateClassicMindMapText("название узла", strings.TrimSpace(label), MaxClassicMindMapTitleRunes, true); err != nil {
		return err
	}
	if err := validateClassicMindMapText("краткое описание", strings.TrimSpace(summary), MaxClassicMindMapTextRunes, false); err != nil {
		return err
	}
	if err := validateClassicMindMapText("текст узла", body, MaxClassicMindMapTextRunes, false); err != nil {
		return err
	}
	if !validClassicMindMapNodeKind(kind) {
		return fmt.Errorf("неподдерживаемый тип узла %q", kind)
	}
	if origin != ClassicMindMapNodeManual && origin != ClassicMindMapNodeGenerated && origin != ClassicMindMapNodeImported {
		return fmt.Errorf("неподдерживаемое происхождение узла %q", origin)
	}
	if _, err := validateClassicMindMapStyle(style); err != nil {
		return err
	}
	return nil
}

func validateClassicMindMapText(name, value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s не может быть пустым", name)
	}
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s превышает %d символов", name, max)
	}
	return nil
}

func validateClassicMindMapStyle(style json.RawMessage) (json.RawMessage, error) {
	style = normalizeClassicMindMapStyle(style)
	if len(style) > 8192 || !json.Valid(style) {
		return nil, fmt.Errorf("стиль узла должен быть JSON-объектом размером до 8192 байт")
	}
	var object map[string]any
	if err := json.Unmarshal(style, &object); err != nil || object == nil {
		return nil, fmt.Errorf("стиль узла должен быть JSON-объектом")
	}
	return style, nil
}

func normalizeClassicMindMapStyle(style json.RawMessage) json.RawMessage {
	if len(style) == 0 {
		return json.RawMessage(`{}`)
	}
	return style
}

func validClassicMindMapMode(mode ClassicMindMapMode) bool {
	return mode == ClassicMindMapModeManual || mode == ClassicMindMapModeGenerated || mode == ClassicMindMapModeHybrid
}

func validClassicMindMapStatus(status ClassicMindMapStatus) bool {
	return status == ClassicMindMapStatusDraft || status == ClassicMindMapStatusReady || status == ClassicMindMapStatusArchived
}

func validClassicMindMapNodeKind(kind ClassicMindMapNodeKind) bool {
	switch kind {
	case ClassicMindMapNodeTopic, ClassicMindMapNodeSubtopic, ClassicMindMapNodeFact, ClassicMindMapNodeNote,
		ClassicMindMapNodeQuote, ClassicMindMapNodeQuestion, ClassicMindMapNodeTask, ClassicMindMapNodeDecision,
		ClassicMindMapNodeLink:
		return true
	default:
		return false
	}
}

func validateClassicMindMapSource(source ClassicMindMapSource) error {
	switch source.Kind {
	case ClassicMindMapSourceEvidence:
		if source.Evidence == nil {
			return fmt.Errorf("evidence source не содержит anchor")
		}
		return validateEvidenceAnchor(*source.Evidence)
	case ClassicMindMapSourceExternalFile:
		if strings.TrimSpace(source.Locator) == "" {
			return fmt.Errorf("external_file source не содержит путь или координаты")
		}
	case ClassicMindMapSourceURL:
		if _, err := validateClassicMindMapHTTPURL(source.URL); err != nil {
			return err
		}
	case ClassicMindMapSourceKnowledgeNode:
		if err := validateKnowledgeID(strings.TrimSpace(source.KnowledgeNodeID)); err != nil {
			return fmt.Errorf("knowledge_node source: %w", err)
		}
	default:
		return fmt.Errorf("неподдерживаемый тип источника %q", source.Kind)
	}
	return nil
}

func newClassicMindMapID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate classic mind map ID: %w", err)
	}
	return prefix + hex.EncodeToString(raw), nil
}

func normalizeClassicMindMapActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "user"
	}
	if utf8.RuneCountInString(actor) > 128 {
		return string([]rune(actor)[:128])
	}
	return actor
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func resolveClassicMindMapRef(q classicMindMapQuerier, ref string) (ClassicMindMap, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ClassicMindMap{}, ErrClassicMindMapNotFound
	}
	if item, found, err := queryOneClassicMindMap(q, `m.deleted_at='' AND m.id=?`, ref); err != nil || found {
		return item, err
	}
	items, err := queryClassicMindMaps(q, `m.deleted_at='' AND lower(m.title)=lower(?)`, ref)
	if err != nil {
		return ClassicMindMap{}, err
	}
	if len(items) == 1 {
		return items[0], nil
	}
	if len(items) > 1 {
		return ClassicMindMap{}, fmt.Errorf("%w: название %q совпадает с несколькими картами", ErrClassicMindMapAmbiguousRef, ref)
	}
	items, err = queryClassicMindMaps(q, `m.deleted_at='' AND m.id LIKE ?`, ref+"%")
	if err != nil {
		return ClassicMindMap{}, err
	}
	if len(items) == 1 {
		return items[0], nil
	}
	if len(items) > 1 {
		return ClassicMindMap{}, fmt.Errorf("%w: префикс %q совпадает с несколькими картами", ErrClassicMindMapAmbiguousRef, ref)
	}
	return ClassicMindMap{}, fmt.Errorf("%w: %s", ErrClassicMindMapNotFound, ref)
}

func queryOneClassicMindMap(q classicMindMapQuerier, where string, args ...any) (ClassicMindMap, bool, error) {
	items, err := queryClassicMindMaps(q, where, args...)
	if err != nil || len(items) == 0 {
		return ClassicMindMap{}, false, err
	}
	return items[0], true, nil
}

func queryClassicMindMaps(q classicMindMapQuerier, where string, args ...any) ([]ClassicMindMap, error) {
	rows, err := q.Query(`SELECT m.id, m.title, m.description, m.mode, m.status, m.root_node_id,
m.revision, m.created, m.updated FROM mind_maps m WHERE `+where+` ORDER BY m.updated DESC, m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClassicMindMap
	for rows.Next() {
		var item ClassicMindMap
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Mode, &item.Status,
			&item.RootNodeID, &item.Revision, &item.Created, &item.Updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func resolveClassicMindMapNodeRef(q classicMindMapQuerier, mapID, ref string) (ClassicMindMapNode, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ClassicMindMapNode{}, ErrClassicMindMapNodeNotFound
	}
	items, err := queryClassicMindMapNodes(q, mapID, `n.deleted_at='' AND n.id=?`, ref)
	if err != nil {
		return ClassicMindMapNode{}, err
	}
	if len(items) == 1 {
		return items[0], nil
	}
	items, err = queryClassicMindMapNodes(q, mapID, `n.deleted_at='' AND lower(n.label)=lower(?)`, ref)
	if err != nil {
		return ClassicMindMapNode{}, err
	}
	if len(items) == 1 {
		return items[0], nil
	}
	if len(items) > 1 {
		return ClassicMindMapNode{}, fmt.Errorf("%w: название узла %q встречается несколько раз", ErrClassicMindMapAmbiguousRef, ref)
	}
	items, err = queryClassicMindMapNodes(q, mapID, `n.deleted_at='' AND n.id LIKE ?`, ref+"%")
	if err != nil {
		return ClassicMindMapNode{}, err
	}
	if len(items) == 1 {
		return items[0], nil
	}
	if len(items) > 1 {
		return ClassicMindMapNode{}, fmt.Errorf("%w: префикс узла %q неоднозначен", ErrClassicMindMapAmbiguousRef, ref)
	}
	return ClassicMindMapNode{}, fmt.Errorf("%w: %s", ErrClassicMindMapNodeNotFound, ref)
}

func queryClassicMindMapNodes(q classicMindMapQuerier, mapID, where string, args ...any) ([]ClassicMindMapNode, error) {
	params := []any{mapID}
	params = append(params, args...)
	rows, err := q.Query(`SELECT n.id, n.map_id, n.parent_id, n.position, n.label, n.summary,
n.body_markdown, n.kind, n.origin, n.locked, n.style_json, n.created, n.updated
FROM mind_map_nodes n WHERE n.map_id=? AND `+where+` ORDER BY n.parent_id, n.position, n.id`, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClassicMindMapNode
	for rows.Next() {
		var node ClassicMindMapNode
		var locked int
		var style string
		if err := rows.Scan(&node.ID, &node.MapID, &node.ParentID, &node.Position, &node.Label,
			&node.Summary, &node.BodyMarkdown, &node.Kind, &node.Origin, &locked, &style,
			&node.Created, &node.Updated); err != nil {
			return nil, err
		}
		node.Locked = locked != 0
		node.Style = json.RawMessage(style)
		result = append(result, node)
	}
	return result, rows.Err()
}

func loadClassicMindMapDocument(q classicMindMapQuerier, mapID string) (ClassicMindMapDocument, error) {
	item, found, err := queryOneClassicMindMap(q, `m.deleted_at='' AND m.id=?`, mapID)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	if !found {
		return ClassicMindMapDocument{}, ErrClassicMindMapNotFound
	}
	nodes, err := queryClassicMindMapNodes(q, mapID, `n.deleted_at=''`)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	byID := make(map[string]*ClassicMindMapNode, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	rows, err := q.Query(`SELECT id, map_id, node_id, position, kind, title, locator, url,
knowledge_node_id, citation_id, document_id, document_revision, chunk_hash, evidence_hash,
source_path, page, block_index, block_chunk_index, excerpt, created
FROM mind_map_node_sources WHERE map_id=? AND deleted_at='' ORDER BY node_id, position, id`, mapID)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var source ClassicMindMapSource
		var anchor EvidenceAnchor
		if err := rows.Scan(&source.ID, &source.MapID, &source.NodeID, &source.Position, &source.Kind,
			&source.Title, &source.Locator, &source.URL, &source.KnowledgeNodeID, &anchor.CitationID,
			&anchor.DocumentID, &anchor.DocumentRevision, &anchor.ChunkHash, &anchor.EvidenceHash,
			&anchor.SourcePath, &anchor.Page, &anchor.BlockIndex, &anchor.BlockChunkIndex,
			&anchor.Excerpt, &source.Created); err != nil {
			return ClassicMindMapDocument{}, err
		}
		if source.Kind == ClassicMindMapSourceEvidence {
			source.Evidence = &anchor
		}
		if node := byID[source.NodeID]; node != nil {
			node.Sources = append(node.Sources, source)
		}
	}
	if err := rows.Err(); err != nil {
		return ClassicMindMapDocument{}, err
	}
	for i := range nodes {
		if nodes[i].Sources == nil {
			nodes[i].Sources = []ClassicMindMapSource{}
		}
	}
	return ClassicMindMapDocument{Version: ClassicMindMapFormatVersion, Map: item, Nodes: nodes}, nil
}

func finalizeClassicMindMapDocument(doc ClassicMindMapDocument) (ClassicMindMapDocument, []byte, error) {
	doc.Version = ClassicMindMapFormatVersion
	doc.Digest = ""
	doc.StateDigest = ""
	sort.Slice(doc.Nodes, func(i, j int) bool {
		if doc.Nodes[i].ParentID != doc.Nodes[j].ParentID {
			return doc.Nodes[i].ParentID < doc.Nodes[j].ParentID
		}
		if doc.Nodes[i].Position != doc.Nodes[j].Position {
			return doc.Nodes[i].Position < doc.Nodes[j].Position
		}
		return doc.Nodes[i].ID < doc.Nodes[j].ID
	})
	for i := range doc.Nodes {
		sort.Slice(doc.Nodes[i].Sources, func(a, b int) bool {
			if doc.Nodes[i].Sources[a].Position != doc.Nodes[i].Sources[b].Position {
				return doc.Nodes[i].Sources[a].Position < doc.Nodes[i].Sources[b].Position
			}
			return doc.Nodes[i].Sources[a].ID < doc.Nodes[i].Sources[b].ID
		})
		for j := range doc.Nodes[i].Sources {
			doc.Nodes[i].Sources[j].EvidenceState = ""
		}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return doc, nil, fmt.Errorf("encode classic mind map: %w", err)
	}
	hash := sha256.Sum256(encoded)
	doc.Digest = "sha256:" + hex.EncodeToString(hash[:])
	encoded, err = json.Marshal(doc)
	if err != nil {
		return doc, nil, err
	}
	return doc, encoded, nil
}

func emptyClassicMindMapDigest() string {
	hash := sha256.Sum256([]byte(`{}`))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func insertClassicMindMapSource(tx *sql.Tx, mapID, nodeID string, position int, source ClassicMindMapSource, now string) error {
	if err := validateClassicMindMapSource(source); err != nil {
		return err
	}
	id := strings.TrimSpace(source.ID)
	if id == "" {
		var err error
		id, err = newClassicMindMapID("mms-")
		if err != nil {
			return err
		}
	}
	anchor := EvidenceAnchor{}
	if source.Evidence != nil {
		anchor = *source.Evidence
	}
	if _, err := tx.Exec(`INSERT INTO mind_map_node_sources
(id, map_id, node_id, position, kind, title, locator, url, knowledge_node_id,
citation_id, document_id, document_revision, chunk_hash, evidence_hash, source_path,
page, block_index, block_chunk_index, excerpt, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, mapID, nodeID,
		position, source.Kind, strings.TrimSpace(source.Title), strings.TrimSpace(source.Locator),
		strings.TrimSpace(source.URL), strings.TrimSpace(source.KnowledgeNodeID), anchor.CitationID,
		anchor.DocumentID, anchor.DocumentRevision, anchor.ChunkHash, anchor.EvidenceHash,
		anchor.SourcePath, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex, anchor.Excerpt, now); err != nil {
		return fmt.Errorf("attach classic mind map source: %w", err)
	}
	return nil
}

type classicMindMapMutation func(tx *sql.Tx, item ClassicMindMap) (targetNodeID string, err error)

func (s *Store) mutateClassicMindMap(mapRef string, expectedRevision int64, actor, comment, action string, mutate classicMindMapMutation) (ClassicMindMapDocument, ClassicMindMapChange, error) {
	actor = normalizeClassicMindMapActor(actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, err
	}
	rollback := func(cause error) (ClassicMindMapDocument, ClassicMindMapChange, error) {
		_ = tx.Rollback()
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, cause
	}
	item, err := resolveClassicMindMapRef(tx, mapRef)
	if err != nil {
		return rollback(err)
	}
	if expectedRevision > 0 && item.Revision != expectedRevision {
		return rollback(fmt.Errorf("%w: ожидалась %d, текущая %d", ErrClassicMindMapRevisionConflict, expectedRevision, item.Revision))
	}
	before, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	before, beforeJSON, err := finalizeClassicMindMapDocument(before)
	if err != nil {
		return rollback(err)
	}
	targetNodeID, err := mutate(tx, item)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	newRevision := item.Revision + 1
	result, err := tx.Exec(`UPDATE mind_maps SET revision=?, updated=?,
mode=CASE WHEN mode=? THEN ? ELSE mode END WHERE id=? AND revision=? AND deleted_at=''`,
		newRevision, now, ClassicMindMapModeGenerated, ClassicMindMapModeHybrid, item.ID, item.Revision)
	if err != nil {
		return rollback(fmt.Errorf("advance classic mind map revision: %w", err))
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return rollback(fmt.Errorf("%w: карта изменилась во время операции", ErrClassicMindMapRevisionConflict))
	}
	after, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	after, afterJSON, err := finalizeClassicMindMapDocument(after)
	if err != nil {
		return rollback(err)
	}
	change := ClassicMindMapChange{
		MapID: item.ID, BaseRevision: item.Revision, NewRevision: newRevision, Action: action,
		TargetNodeID: targetNodeID, BeforeDigest: before.Digest, AfterDigest: after.Digest,
		Actor: actor, Comment: strings.TrimSpace(comment), Created: now,
	}
	insert, err := tx.Exec(`INSERT INTO mind_map_changes
(map_id, base_revision, new_revision, action, target_node_id, before_json, after_json,
before_digest, after_digest, actor, comment, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Revision, newRevision, action,
		targetNodeID, string(beforeJSON), string(afterJSON), before.Digest, after.Digest, actor, change.Comment, now)
	if err != nil {
		return rollback(fmt.Errorf("append classic mind map change: %w", err))
	}
	change.ID, err = insert.LastInsertId()
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, fmt.Errorf("commit classic mind map change: %w", err)
	}
	resolved, err := s.resolveClassicMindMapSourceStates(after)
	return resolved, change, err
}

// EditClassicMindMap updates the card shown in the map library. When a newly
// created map still has a root label equal to its old title, renaming the map
// also renames that root; a deliberately customized root label is preserved.
func (s *Store) EditClassicMindMap(mapRef string, patch ClassicMindMapPatch, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, error) {
	if patch.Title == nil && patch.Description == nil && patch.Status == nil {
		return ClassicMindMapDocument{}, fmt.Errorf("не указано ни одного изменения карты")
	}
	if patch.Title != nil {
		value := strings.TrimSpace(*patch.Title)
		if err := validateClassicMindMapText("название карты", value, MaxClassicMindMapTitleRunes, true); err != nil {
			return ClassicMindMapDocument{}, err
		}
		patch.Title = &value
	}
	if patch.Description != nil {
		value := strings.TrimSpace(*patch.Description)
		if err := validateClassicMindMapText("описание карты", value, MaxClassicMindMapTextRunes, false); err != nil {
			return ClassicMindMapDocument{}, err
		}
		patch.Description = &value
	}
	if patch.Status != nil && !validClassicMindMapStatus(*patch.Status) {
		return ClassicMindMapDocument{}, fmt.Errorf("неподдерживаемый статус карты %q", *patch.Status)
	}
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "edit_map", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		title, description, status := item.Title, item.Description, item.Status
		if patch.Title != nil {
			title = *patch.Title
		}
		if patch.Description != nil {
			description = *patch.Description
		}
		if patch.Status != nil {
			status = *patch.Status
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if patch.Title != nil {
			if _, err := tx.Exec(`UPDATE mind_map_nodes SET label=?, updated=?
WHERE id=? AND map_id=? AND deleted_at='' AND label=?`, title, now, item.RootNodeID, item.ID, item.Title); err != nil {
				return "", fmt.Errorf("rename classic mind map root: %w", err)
			}
		}
		if _, err := tx.Exec(`UPDATE mind_maps SET title=?, description=?, status=? WHERE id=? AND deleted_at=''`,
			title, description, status, item.ID); err != nil {
			return "", fmt.Errorf("edit classic mind map: %w", err)
		}
		return item.RootNodeID, nil
	})
	return doc, err
}

func (s *Store) AddClassicMindMapNode(mapRef, parentRef, label string, position int, kind ClassicMindMapNodeKind, summary, body string, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapNode, error) {
	label, summary = strings.TrimSpace(label), strings.TrimSpace(summary)
	if kind == "" {
		kind = ClassicMindMapNodeSubtopic
	}
	if err := validateClassicMindMapNodeFields(label, summary, body, kind, ClassicMindMapNodeManual, nil); err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, err
	}
	nodeID, err := newClassicMindMapID("mmn-")
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, err
	}
	var created ClassicMindMapNode
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "add_node", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		parent, err := resolveClassicMindMapNodeRef(tx, item.ID, parentRef)
		if err != nil {
			return "", err
		}
		if parent.Locked {
			return "", fmt.Errorf("%w: родитель %q", ErrClassicMindMapLocked, parent.Label)
		}
		count, err := classicMindMapSiblingCount(tx, item.ID, parent.ID)
		if err != nil {
			return "", err
		}
		position = clampClassicMindMapPosition(position, count)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE mind_map_nodes SET position=position+1, updated=?
WHERE map_id=? AND parent_id=? AND deleted_at='' AND position>=?`, now, item.ID, parent.ID, position); err != nil {
			return "", err
		}
		if _, err := tx.Exec(`INSERT INTO mind_map_nodes
(id, map_id, parent_id, position, label, summary, body_markdown, kind, origin, style_json, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`, nodeID, item.ID, parent.ID, position,
			label, summary, body, kind, ClassicMindMapNodeManual, now, now); err != nil {
			return "", fmt.Errorf("add classic mind map node: %w", err)
		}
		created = ClassicMindMapNode{ID: nodeID, MapID: item.ID, ParentID: parent.ID, Position: position,
			Label: label, Summary: summary, BodyMarkdown: body, Kind: kind, Origin: ClassicMindMapNodeManual,
			Style: json.RawMessage(`{}`), Created: now, Updated: now, Sources: []ClassicMindMapSource{}}
		return nodeID, nil
	})
	return doc, created, err
}

func (s *Store) EditClassicMindMapNode(mapRef, nodeRef string, patch ClassicMindMapNodePatch, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapNode, error) {
	if patch.Label == nil && patch.Summary == nil && patch.BodyMarkdown == nil && patch.Kind == nil && patch.Locked == nil {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, fmt.Errorf("не указано ни одного изменения узла")
	}
	var updated ClassicMindMapNode
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "edit_node", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.Locked && (patch.Locked == nil || *patch.Locked) {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		if patch.Label != nil {
			node.Label = strings.TrimSpace(*patch.Label)
		}
		if patch.Summary != nil {
			node.Summary = strings.TrimSpace(*patch.Summary)
		}
		if patch.BodyMarkdown != nil {
			node.BodyMarkdown = *patch.BodyMarkdown
		}
		if patch.Kind != nil {
			node.Kind = *patch.Kind
		}
		if patch.Locked != nil {
			node.Locked = *patch.Locked
		}
		if err := validateClassicMindMapNodeFields(node.Label, node.Summary, node.BodyMarkdown, node.Kind, node.Origin, node.Style); err != nil {
			return "", err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE mind_map_nodes SET label=?, summary=?, body_markdown=?, kind=?, locked=?, updated=?
WHERE id=? AND map_id=? AND deleted_at=''`, node.Label, node.Summary, node.BodyMarkdown, node.Kind,
			boolInt(node.Locked), now, node.ID, item.ID); err != nil {
			return "", fmt.Errorf("edit classic mind map node: %w", err)
		}
		node.Updated = now
		updated = node
		return node.ID, nil
	})
	return doc, updated, err
}

func (s *Store) MoveClassicMindMapNode(mapRef, nodeRef, parentRef string, position int, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapNode, error) {
	var moved ClassicMindMapNode
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "move_node", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.ID == item.RootNodeID {
			return "", fmt.Errorf("корневой узел нельзя перемещать")
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		parent, err := resolveClassicMindMapNodeRef(tx, item.ID, parentRef)
		if err != nil {
			return "", err
		}
		if parent.Locked {
			return "", fmt.Errorf("%w: родитель %q", ErrClassicMindMapLocked, parent.Label)
		}
		if node.ID == parent.ID {
			return "", fmt.Errorf("узел нельзя сделать собственным родителем")
		}
		doc, err := loadClassicMindMapDocument(tx, item.ID)
		if err != nil {
			return "", err
		}
		if classicMindMapDescendant(doc.Nodes, node.ID, parent.ID) {
			return "", fmt.Errorf("перемещение создаст цикл: %q находится внутри ветки %q", parent.Label, node.Label)
		}
		targetOrder, err := classicMindMapSiblingIDs(tx, item.ID, parent.ID, node.ID)
		if err != nil {
			return "", err
		}
		position = clampClassicMindMapPosition(position, len(targetOrder))
		targetOrder = insertClassicMindMapNodeID(targetOrder, node.ID, position)
		oldParentID := node.ParentID
		oldOrder, err := classicMindMapSiblingIDs(tx, item.ID, oldParentID, node.ID)
		if err != nil {
			return "", err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE mind_map_nodes SET parent_id=?, updated=? WHERE id=? AND map_id=?`,
			parent.ID, now, node.ID, item.ID); err != nil {
			return "", err
		}
		if oldParentID != parent.ID {
			if err := setClassicMindMapSiblingOrderTx(tx, oldOrder, now); err != nil {
				return "", err
			}
		}
		if err := setClassicMindMapSiblingOrderTx(tx, targetOrder, now); err != nil {
			return "", err
		}
		node.ParentID, node.Position, node.Updated = parent.ID, position, now
		moved = node
		return node.ID, nil
	})
	return doc, moved, err
}

func (s *Store) DeleteClassicMindMapNode(mapRef, nodeRef string, mode ClassicMindMapDeleteMode, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, error) {
	if mode == "" {
		mode = ClassicMindMapDeleteBranch
	}
	if mode != ClassicMindMapDeleteBranch && mode != ClassicMindMapDeletePromoteChildren {
		return ClassicMindMapDocument{}, fmt.Errorf("неподдерживаемый режим удаления %q", mode)
	}
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "delete_node:"+string(mode), func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.ID == item.RootNodeID {
			return "", fmt.Errorf("корневой узел нельзя удалить")
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if mode == ClassicMindMapDeleteBranch {
			rows, err := tx.Query(`WITH RECURSIVE branch(id) AS (
SELECT id FROM mind_map_nodes WHERE id=? AND map_id=? AND deleted_at=''
UNION ALL SELECT n.id FROM mind_map_nodes n JOIN branch b ON n.parent_id=b.id
WHERE n.map_id=? AND n.deleted_at='') SELECT id FROM branch`, node.ID, item.ID, item.ID)
			if err != nil {
				return "", err
			}
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return "", err
				}
				ids = append(ids, id)
			}
			if err := rows.Close(); err != nil {
				return "", err
			}
			var locked int
			for _, id := range ids {
				if err := tx.QueryRow(`SELECT locked FROM mind_map_nodes WHERE id=?`, id).Scan(&locked); err != nil {
					return "", err
				}
				if locked != 0 {
					return "", fmt.Errorf("%w: ветка содержит закреплённый узел", ErrClassicMindMapLocked)
				}
			}
			for _, id := range ids {
				if _, err := tx.Exec(`UPDATE mind_map_node_sources SET deleted_at=? WHERE map_id=? AND node_id=? AND deleted_at=''`, now, item.ID, id); err != nil {
					return "", err
				}
				if _, err := tx.Exec(`UPDATE mind_map_nodes SET deleted_at=?, updated=? WHERE map_id=? AND id=? AND deleted_at=''`, now, now, item.ID, id); err != nil {
					return "", err
				}
			}
		} else {
			children, err := classicMindMapSiblingIDs(tx, item.ID, node.ID, "")
			if err != nil {
				return "", err
			}
			for _, childID := range children {
				var locked int
				if err := tx.QueryRow(`SELECT locked FROM mind_map_nodes WHERE id=?`, childID).Scan(&locked); err != nil {
					return "", err
				}
				if locked != 0 {
					return "", fmt.Errorf("%w: дочерний узел нельзя автоматически перенести", ErrClassicMindMapLocked)
				}
			}
			parentOrder, err := classicMindMapSiblingIDs(tx, item.ID, node.ParentID, node.ID)
			if err != nil {
				return "", err
			}
			insertAt := clampClassicMindMapPosition(node.Position, len(parentOrder))
			promotedOrder := make([]string, 0, len(parentOrder)+len(children))
			promotedOrder = append(promotedOrder, parentOrder[:insertAt]...)
			promotedOrder = append(promotedOrder, children...)
			promotedOrder = append(promotedOrder, parentOrder[insertAt:]...)
			for _, childID := range children {
				if _, err := tx.Exec(`UPDATE mind_map_nodes SET parent_id=?, updated=? WHERE id=?`, node.ParentID, now, childID); err != nil {
					return "", err
				}
			}
			if _, err := tx.Exec(`UPDATE mind_map_node_sources SET deleted_at=? WHERE map_id=? AND node_id=? AND deleted_at=''`, now, item.ID, node.ID); err != nil {
				return "", err
			}
			if _, err := tx.Exec(`UPDATE mind_map_nodes SET deleted_at=?, updated=? WHERE map_id=? AND id=? AND deleted_at=''`, now, now, item.ID, node.ID); err != nil {
				return "", err
			}
			if err := setClassicMindMapSiblingOrderTx(tx, promotedOrder, now); err != nil {
				return "", err
			}
		}
		if mode == ClassicMindMapDeleteBranch {
			if err := normalizeClassicMindMapSiblingPositions(tx, item.ID, node.ParentID, now); err != nil {
				return "", err
			}
		}
		return node.ID, nil
	})
	return doc, err
}

func (s *Store) AttachClassicMindMapEvidence(mapRef, nodeRef string, anchor EvidenceAnchor, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapSource, error) {
	if err := validateEvidenceAnchor(anchor); err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapSource{}, err
	}
	var attached ClassicMindMapSource
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "attach_evidence", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		if resolveEvidenceAnchorFromEntries(anchor, s.entries).State != EvidenceCurrent {
			return "", fmt.Errorf("источник %s не является current в активной базе", anchor.CitationID)
		}
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		var duplicate int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources
WHERE map_id=? AND node_id=? AND deleted_at='' AND kind=? AND citation_id=? AND evidence_hash=?`,
			item.ID, node.ID, ClassicMindMapSourceEvidence, anchor.CitationID, anchor.EvidenceHash).Scan(&duplicate); err != nil {
			return "", err
		}
		if duplicate != 0 {
			return "", fmt.Errorf("этот фрагмент уже привязан к узлу %q", node.Label)
		}
		var position int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at=''`, item.ID, node.ID).Scan(&position); err != nil {
			return "", err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		attached = ClassicMindMapSource{MapID: item.ID, NodeID: node.ID, Position: position,
			Kind: ClassicMindMapSourceEvidence, Title: classicMindMapEvidenceTitle(anchor),
			Locator: classicMindMapEvidenceLocator(anchor), Evidence: &anchor, EvidenceState: EvidenceCurrent, Created: now}
		attached.ID, err = newClassicMindMapID("mms-")
		if err != nil {
			return "", err
		}
		if err := insertClassicMindMapSource(tx, item.ID, node.ID, position, attached, now); err != nil {
			return "", err
		}
		return node.ID, nil
	})
	return doc, attached, err
}

func (s *Store) ListClassicMindMapChanges(mapRef string, limit int) ([]ClassicMindMapChange, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		return nil, fmt.Errorf("limit не может превышать 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, err := resolveClassicMindMapRef(s.db, mapRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, map_id, base_revision, new_revision, action, target_node_id,
before_json, after_json, before_digest, after_digest, reverts_change_id, actor, comment, created
FROM mind_map_changes WHERE map_id=? ORDER BY id DESC LIMIT ?`, item.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClassicMindMapChange
	for rows.Next() {
		var change ClassicMindMapChange
		if err := rows.Scan(&change.ID, &change.MapID, &change.BaseRevision, &change.NewRevision,
			&change.Action, &change.TargetNodeID, &change.beforeJSON, &change.afterJSON,
			&change.BeforeDigest, &change.AfterDigest, &change.RevertsChangeID, &change.Actor,
			&change.Comment, &change.Created); err != nil {
			return nil, err
		}
		result = append(result, change)
	}
	return result, rows.Err()
}

func (s *Store) UndoClassicMindMapChange(mapRef string, changeID, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapChange, error) {
	actor = normalizeClassicMindMapActor(actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, err
	}
	rollback := func(cause error) (ClassicMindMapDocument, ClassicMindMapChange, error) {
		_ = tx.Rollback()
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, cause
	}
	item, err := resolveClassicMindMapRef(tx, mapRef)
	if err != nil {
		return rollback(err)
	}
	if expectedRevision > 0 && item.Revision != expectedRevision {
		return rollback(fmt.Errorf("%w: ожидалась %d, текущая %d", ErrClassicMindMapRevisionConflict, expectedRevision, item.Revision))
	}
	var target ClassicMindMapChange
	query := `SELECT c.id, c.map_id, c.base_revision, c.new_revision, c.action, c.target_node_id,
c.before_json, c.after_json, c.before_digest, c.after_digest, c.reverts_change_id, c.actor, c.comment, c.created
FROM mind_map_changes c WHERE c.map_id=? AND c.action NOT LIKE 'undo:%'
AND c.action!='create_map' AND NOT EXISTS (SELECT 1 FROM mind_map_changes u WHERE u.reverts_change_id=c.id)`
	args := []any{item.ID}
	if changeID > 0 {
		query += ` AND c.id=?`
		args = append(args, changeID)
	}
	query += ` ORDER BY c.id DESC LIMIT 1`
	if err := tx.QueryRow(query, args...).Scan(&target.ID, &target.MapID, &target.BaseRevision,
		&target.NewRevision, &target.Action, &target.TargetNodeID, &target.beforeJSON, &target.afterJSON,
		&target.BeforeDigest, &target.AfterDigest, &target.RevertsChangeID, &target.Actor,
		&target.Comment, &target.Created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollback(fmt.Errorf("нет доступного изменения для undo"))
		}
		return rollback(err)
	}
	current, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	current, currentJSON, err := finalizeClassicMindMapDocument(current)
	if err != nil {
		return rollback(err)
	}
	var targetAfter ClassicMindMapDocument
	if err := json.Unmarshal([]byte(target.afterJSON), &targetAfter); err != nil {
		return rollback(fmt.Errorf("decode change result for undo: %w", err))
	}
	currentContentDigest, err := classicMindMapContentDigest(current)
	if err != nil {
		return rollback(err)
	}
	targetContentDigest, err := classicMindMapContentDigest(targetAfter)
	if err != nil {
		return rollback(err)
	}
	if currentContentDigest != targetContentDigest {
		return rollback(fmt.Errorf("%w: текущее содержимое не совпадает с результатом изменения %d", ErrClassicMindMapRevisionConflict, target.ID))
	}
	var previous ClassicMindMapDocument
	if err := json.Unmarshal([]byte(target.beforeJSON), &previous); err != nil {
		return rollback(fmt.Errorf("decode undo snapshot: %w", err))
	}
	newRevision := item.Revision + 1
	if err := restoreClassicMindMapDocumentTx(tx, item.ID, previous, newRevision); err != nil {
		return rollback(err)
	}
	after, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	after, afterJSON, err := finalizeClassicMindMapDocument(after)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	undo := ClassicMindMapChange{MapID: item.ID, BaseRevision: item.Revision, NewRevision: newRevision,
		Action: "undo:" + target.Action, TargetNodeID: target.TargetNodeID,
		BeforeDigest: current.Digest, AfterDigest: after.Digest, RevertsChangeID: target.ID,
		Actor: actor, Comment: strings.TrimSpace(comment), Created: now}
	insert, err := tx.Exec(`INSERT INTO mind_map_changes
(map_id, base_revision, new_revision, action, target_node_id, before_json, after_json,
before_digest, after_digest, reverts_change_id, actor, comment, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Revision, newRevision,
		undo.Action, undo.TargetNodeID, string(currentJSON), string(afterJSON), current.Digest, after.Digest,
		target.ID, actor, undo.Comment, now)
	if err != nil {
		return rollback(err)
	}
	undo.ID, err = insert.LastInsertId()
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, err
	}
	resolved, err := s.resolveClassicMindMapSourceStates(after)
	return resolved, undo, err
}

// RedoClassicMindMapChange reapplies the newest undo which has not already
// been redone. Redo is itself append-only and can subsequently be undone.
func (s *Store) RedoClassicMindMapChange(mapRef string, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapChange, error) {
	actor = normalizeClassicMindMapActor(actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, err
	}
	rollback := func(cause error) (ClassicMindMapDocument, ClassicMindMapChange, error) {
		_ = tx.Rollback()
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, cause
	}
	item, err := resolveClassicMindMapRef(tx, mapRef)
	if err != nil {
		return rollback(err)
	}
	if expectedRevision > 0 && item.Revision != expectedRevision {
		return rollback(fmt.Errorf("%w: ожидалась %d, текущая %d", ErrClassicMindMapRevisionConflict, expectedRevision, item.Revision))
	}
	var undo ClassicMindMapChange
	if err := tx.QueryRow(`SELECT u.id, u.map_id, u.base_revision, u.new_revision, u.action, u.target_node_id,
u.before_json, u.after_json, u.before_digest, u.after_digest, u.reverts_change_id, u.actor, u.comment, u.created
FROM mind_map_changes u WHERE u.map_id=? AND u.action LIKE 'undo:%' AND u.reverts_change_id>0
AND NOT EXISTS (SELECT 1 FROM mind_map_changes r WHERE r.map_id=u.map_id AND r.reverts_change_id=u.id)
ORDER BY u.id DESC LIMIT 1`, item.ID).Scan(&undo.ID, &undo.MapID, &undo.BaseRevision,
		&undo.NewRevision, &undo.Action, &undo.TargetNodeID, &undo.beforeJSON, &undo.afterJSON,
		&undo.BeforeDigest, &undo.AfterDigest, &undo.RevertsChangeID, &undo.Actor,
		&undo.Comment, &undo.Created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollback(fmt.Errorf("нет доступного изменения для redo"))
		}
		return rollback(err)
	}
	var original ClassicMindMapChange
	if err := tx.QueryRow(`SELECT id, map_id, base_revision, new_revision, action, target_node_id,
before_json, after_json, before_digest, after_digest, reverts_change_id, actor, comment, created
FROM mind_map_changes WHERE map_id=? AND id=?`, item.ID, undo.RevertsChangeID).Scan(
		&original.ID, &original.MapID, &original.BaseRevision, &original.NewRevision, &original.Action,
		&original.TargetNodeID, &original.beforeJSON, &original.afterJSON, &original.BeforeDigest,
		&original.AfterDigest, &original.RevertsChangeID, &original.Actor, &original.Comment,
		&original.Created); err != nil {
		return rollback(err)
	}
	current, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	current, currentJSON, err := finalizeClassicMindMapDocument(current)
	if err != nil {
		return rollback(err)
	}
	var undoAfter ClassicMindMapDocument
	if err := json.Unmarshal([]byte(undo.afterJSON), &undoAfter); err != nil {
		return rollback(fmt.Errorf("decode undo result for redo: %w", err))
	}
	currentContentDigest, err := classicMindMapContentDigest(current)
	if err != nil {
		return rollback(err)
	}
	undoContentDigest, err := classicMindMapContentDigest(undoAfter)
	if err != nil {
		return rollback(err)
	}
	if currentContentDigest != undoContentDigest {
		return rollback(fmt.Errorf("%w: текущее содержимое изменилось после undo %d", ErrClassicMindMapRevisionConflict, undo.ID))
	}
	var desired ClassicMindMapDocument
	if err := json.Unmarshal([]byte(original.afterJSON), &desired); err != nil {
		return rollback(fmt.Errorf("decode original result for redo: %w", err))
	}
	newRevision := item.Revision + 1
	if err := restoreClassicMindMapDocumentTx(tx, item.ID, desired, newRevision); err != nil {
		return rollback(err)
	}
	after, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return rollback(err)
	}
	after, afterJSON, err := finalizeClassicMindMapDocument(after)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	redo := ClassicMindMapChange{MapID: item.ID, BaseRevision: item.Revision, NewRevision: newRevision,
		Action: "redo:" + original.Action, TargetNodeID: original.TargetNodeID,
		BeforeDigest: current.Digest, AfterDigest: after.Digest, RevertsChangeID: undo.ID,
		Actor: actor, Comment: strings.TrimSpace(comment), Created: now}
	insert, err := tx.Exec(`INSERT INTO mind_map_changes
(map_id, base_revision, new_revision, action, target_node_id, before_json, after_json,
before_digest, after_digest, reverts_change_id, actor, comment, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Revision, newRevision,
		redo.Action, redo.TargetNodeID, string(currentJSON), string(afterJSON), current.Digest,
		after.Digest, undo.ID, actor, redo.Comment, now)
	if err != nil {
		return rollback(err)
	}
	redo.ID, err = insert.LastInsertId()
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapChange{}, err
	}
	resolved, err := s.resolveClassicMindMapSourceStates(after)
	return resolved, redo, err
}

func (s *Store) CreateClassicMindMapSnapshot(mapRef, reason string, expectedRevision int64) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("причина snapshot пуста")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	item, err := resolveClassicMindMapRef(tx, mapRef)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if expectedRevision > 0 && item.Revision != expectedRevision {
		_ = tx.Rollback()
		return "", fmt.Errorf("%w: ожидалась %d, текущая %d", ErrClassicMindMapRevisionConflict, expectedRevision, item.Revision)
	}
	doc, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	doc, encoded, err := finalizeClassicMindMapDocument(doc)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO mind_map_snapshots
(map_id, revision, reason, document_json, document_digest, created) VALUES (?, ?, ?, ?, ?, ?)`,
		item.ID, item.Revision, reason, string(encoded), doc.Digest, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("save classic mind map snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return doc.Digest, nil
}

func (s *Store) ListClassicMindMapSnapshots(mapRef string, limit int) ([]ClassicMindMapSnapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		return nil, fmt.Errorf("limit не может превышать 1000")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, err := resolveClassicMindMapRef(s.db, mapRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, map_id, revision, reason, document_digest, created
FROM mind_map_snapshots WHERE map_id=? ORDER BY revision DESC, id DESC LIMIT ?`, item.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClassicMindMapSnapshot
	for rows.Next() {
		var snapshot ClassicMindMapSnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.MapID, &snapshot.Revision, &snapshot.Reason,
			&snapshot.DocumentDigest, &snapshot.Created); err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

func restoreClassicMindMapDocumentTx(tx *sql.Tx, mapID string, previous ClassicMindMapDocument, newRevision int64) error {
	if previous.Version != ClassicMindMapFormatVersion || previous.Map.ID != mapID {
		return fmt.Errorf("undo snapshot belongs to another map or format")
	}
	if len(previous.Nodes) == 0 {
		return fmt.Errorf("undo snapshot has no nodes")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE mind_map_node_sources SET deleted_at=? WHERE map_id=? AND deleted_at=''`, now, mapID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE mind_map_nodes SET deleted_at=?, updated=? WHERE map_id=? AND deleted_at=''`, now, now, mapID); err != nil {
		return err
	}
	for _, node := range previous.Nodes {
		style := normalizeClassicMindMapStyle(node.Style)
		if _, err := tx.Exec(`INSERT INTO mind_map_nodes
(id, map_id, parent_id, position, label, summary, body_markdown, kind, origin, locked, style_json, created, updated, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(id) DO UPDATE SET map_id=excluded.map_id, parent_id=excluded.parent_id,
position=excluded.position, label=excluded.label, summary=excluded.summary,
body_markdown=excluded.body_markdown, kind=excluded.kind, origin=excluded.origin,
locked=excluded.locked, style_json=excluded.style_json, updated=excluded.updated, deleted_at=''`,
			node.ID, mapID, node.ParentID, node.Position, node.Label, node.Summary, node.BodyMarkdown,
			node.Kind, node.Origin, boolInt(node.Locked), string(style), node.Created, now); err != nil {
			return err
		}
		for position, source := range node.Sources {
			if err := insertOrRestoreClassicMindMapSource(tx, mapID, node.ID, position, source, now); err != nil {
				return err
			}
		}
	}
	result, err := tx.Exec(`UPDATE mind_maps SET title=?, description=?, mode=?, status=?, root_node_id=?,
revision=?, updated=? WHERE id=? AND revision=? AND deleted_at=''`, previous.Map.Title,
		previous.Map.Description, previous.Map.Mode, previous.Map.Status, previous.Map.RootNodeID,
		newRevision, now, mapID, newRevision-1)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("%w: карта изменилась во время undo", ErrClassicMindMapRevisionConflict)
	}
	return nil
}

func insertOrRestoreClassicMindMapSource(tx *sql.Tx, mapID, nodeID string, position int, source ClassicMindMapSource, now string) error {
	anchor := EvidenceAnchor{}
	if source.Evidence != nil {
		anchor = *source.Evidence
	}
	if err := validateClassicMindMapSource(source); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO mind_map_node_sources
(id, map_id, node_id, position, kind, title, locator, url, knowledge_node_id,
citation_id, document_id, document_revision, chunk_hash, evidence_hash, source_path,
page, block_index, block_chunk_index, excerpt, created, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(id) DO UPDATE SET map_id=excluded.map_id, node_id=excluded.node_id,
position=excluded.position, kind=excluded.kind, title=excluded.title, locator=excluded.locator,
url=excluded.url, knowledge_node_id=excluded.knowledge_node_id, citation_id=excluded.citation_id,
document_id=excluded.document_id, document_revision=excluded.document_revision,
chunk_hash=excluded.chunk_hash, evidence_hash=excluded.evidence_hash, source_path=excluded.source_path,
page=excluded.page, block_index=excluded.block_index, block_chunk_index=excluded.block_chunk_index,
excerpt=excluded.excerpt, deleted_at=''`, source.ID, mapID, nodeID, position, source.Kind,
		source.Title, source.Locator, source.URL, source.KnowledgeNodeID, anchor.CitationID, anchor.DocumentID,
		anchor.DocumentRevision, anchor.ChunkHash, anchor.EvidenceHash, anchor.SourcePath, anchor.Page,
		anchor.BlockIndex, anchor.BlockChunkIndex, anchor.Excerpt, source.Created)
	return err
}

func classicMindMapSiblingCount(tx *sql.Tx, mapID, parentID string) (int, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM mind_map_nodes WHERE map_id=? AND parent_id=? AND deleted_at=''`, mapID, parentID).Scan(&count)
	return count, err
}

func clampClassicMindMapPosition(position, count int) int {
	if position < 0 || position > count {
		return count
	}
	return position
}

func normalizeClassicMindMapSiblingPositions(tx *sql.Tx, mapID, parentID, now string) error {
	rows, err := tx.Query(`SELECT id FROM mind_map_nodes WHERE map_id=? AND parent_id=? AND deleted_at='' ORDER BY position, id`, mapID, parentID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for position, id := range ids {
		if _, err := tx.Exec(`UPDATE mind_map_nodes SET position=?, updated=? WHERE id=?`, position, now, id); err != nil {
			return err
		}
	}
	return nil
}

func classicMindMapSiblingIDs(tx *sql.Tx, mapID, parentID, excludeID string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM mind_map_nodes
WHERE map_id=? AND parent_id=? AND deleted_at='' AND id!=? ORDER BY position, id`, mapID, parentID, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func insertClassicMindMapNodeID(ids []string, id string, position int) []string {
	position = clampClassicMindMapPosition(position, len(ids))
	result := make([]string, 0, len(ids)+1)
	result = append(result, ids[:position]...)
	result = append(result, id)
	result = append(result, ids[position:]...)
	return result
}

func setClassicMindMapSiblingOrderTx(tx *sql.Tx, ids []string, now string) error {
	for position, id := range ids {
		if _, err := tx.Exec(`UPDATE mind_map_nodes SET position=?, updated=? WHERE id=? AND deleted_at=''`, position, now, id); err != nil {
			return err
		}
	}
	return nil
}

func classicMindMapContentDigest(doc ClassicMindMapDocument) (string, error) {
	doc.Digest = ""
	doc.Map.Revision = 0
	doc.Map.Updated = ""
	for i := range doc.Nodes {
		doc.Nodes[i].Updated = ""
		for j := range doc.Nodes[i].Sources {
			doc.Nodes[i].Sources[j].EvidenceState = ""
		}
	}
	doc, _, err := finalizeClassicMindMapDocument(doc)
	if err != nil {
		return "", err
	}
	return doc.Digest, nil
}

func classicMindMapDescendant(nodes []ClassicMindMapNode, ancestorID, candidateID string) bool {
	parent := make(map[string]string, len(nodes))
	for _, node := range nodes {
		parent[node.ID] = node.ParentID
	}
	for cursor := candidateID; cursor != ""; cursor = parent[cursor] {
		if cursor == ancestorID {
			return true
		}
	}
	return false
}

func classicMindMapEvidenceTitle(anchor EvidenceAnchor) string {
	title := anchor.SourcePath
	if cut := strings.LastIndexAny(title, `\/`); cut >= 0 {
		title = title[cut+1:]
	}
	return title
}

func classicMindMapEvidenceLocator(anchor EvidenceAnchor) string {
	parts := []string{}
	if anchor.Page > 0 {
		parts = append(parts, fmt.Sprintf("стр. %d", anchor.Page))
	}
	if anchor.BlockIndex > 0 {
		parts = append(parts, fmt.Sprintf("блок %d", anchor.BlockIndex))
	}
	if anchor.BlockChunkIndex > 0 {
		parts = append(parts, fmt.Sprintf("фрагмент %d", anchor.BlockChunkIndex))
	}
	return strings.Join(parts, ", ")
}
