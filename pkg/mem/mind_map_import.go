package mem

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Native JSON import deliberately has the interactive map's limits. It never
// follows URL/file locators and creates an independent map, not a replacement.
const MaxClassicMindMapImportBytes = 8 << 20

var ErrClassicMindMapImportInvalid = errors.New("ожидается корректный JSON экспорта mem-tool: до 8 МиБ, 1000 узлов и 2000 источников")
var ErrClassicMindMapImportPreview = errors.New("содержимое или состояние источников изменилось; повторите предварительный просмотр")
var ErrClassicMindMapImportFile = errors.New("импорт external_file не поддерживается; удалите файловые вложения из копии карты перед экспортом")

type ClassicMindMapImportPreview struct {
	Digest      string   `json:"digest"`
	Title       string   `json:"title"`
	NodeCount   int      `json:"node_count"`
	SourceCount int      `json:"source_count"`
	Current     int      `json:"current_sources"`
	Stale       int      `json:"stale_sources"`
	Missing     int      `json:"missing_sources"`
	Warnings    []string `json:"warnings"`
}

type classicMindMapImportPlan struct {
	Draft   ClassicMindMapDraft
	Preview ClassicMindMapImportPreview
}

// Reject duplicate JSON keys instead of accepting an ambiguous last value.
// The style field remains the export format's extensible JSON object.
func validateClassicMindMapImportJSON(data []byte) error {
	if len(data) == 0 || len(data) > MaxClassicMindMapImportBytes || !utf8.Valid(data) {
		return ErrClassicMindMapImportInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 512 {
			return ErrClassicMindMapImportInvalid
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return ErrClassicMindMapImportInvalid
				}
				seen[key] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return ErrClassicMindMapImportInvalid
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return ErrClassicMindMapImportInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrClassicMindMapImportInvalid
	}
	return nil
}

func parseClassicMindMapImport(data []byte) (classicMindMapImportPlan, error) {
	var plan classicMindMapImportPlan
	if err := validateClassicMindMapImportJSON(data); err != nil {
		return plan, err
	}
	var portable classicMindMapPortableDocument
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&portable); err != nil {
		return plan, ErrClassicMindMapImportInvalid
	}
	doc := portable.Document
	if portable.ExportVersion != ClassicMindMapExportVersion || portable.Format != "json" || doc.Version != ClassicMindMapFormatVersion || doc.Map.ID == "" || doc.Map.Revision < 1 || len(doc.Nodes) < 1 || len(doc.Nodes) > 1000 || portable.NodeCount != len(doc.Nodes) || portable.SourceCount < 0 || portable.SourceCount > 2000 || portable.MapDigest != doc.Digest || portable.StateDigest != doc.StateDigest {
		return plan, ErrClassicMindMapImportInvalid
	}
	// Check the exported integrity digest before normalizing any field. State is
	// advisory at export time and is resolved anew in the destination snapshot.
	if doc.StateDigest != "" && classicMindMapResolvedStateDigest(doc) != doc.StateDigest {
		return plan, ErrClassicMindMapImportInvalid
	}
	canonical, _, err := finalizeClassicMindMapDocument(doc)
	if err != nil || canonical.Digest != portable.MapDigest {
		return plan, ErrClassicMindMapImportInvalid
	}
	model, err := buildClassicMindMapExportModel(canonical, "json")
	if err != nil || model.Portable.SourceCount != portable.SourceCount {
		return plan, ErrClassicMindMapImportInvalid
	}
	plan.Draft = ClassicMindMapDraft{Title: doc.Map.Title, Description: doc.Map.Description, Mode: doc.Map.Mode, Status: doc.Map.Status}
	seenSources := map[string]bool{}
	positions := map[string]map[int]bool{}
	for _, item := range model.Ordered {
		n := item.Node
		if n.MapID != doc.Map.ID || n.Position < 0 {
			return plan, ErrClassicMindMapImportInvalid
		}
		if positions[n.ParentID] == nil {
			positions[n.ParentID] = map[int]bool{}
		}
		if positions[n.ParentID][n.Position] {
			return plan, ErrClassicMindMapImportInvalid
		}
		positions[n.ParentID][n.Position] = true
		nd := ClassicMindMapNodeDraft{Ref: n.ID, ParentRef: n.ParentID, Label: n.Label, Summary: n.Summary, BodyMarkdown: n.BodyMarkdown, Kind: n.Kind, Origin: n.Origin, Locked: n.Locked, Style: n.Style}
		sort.Slice(n.Sources, func(i, j int) bool { return n.Sources[i].Position < n.Sources[j].Position })
		sourcePositions := map[int]bool{}
		for _, src := range n.Sources {
			if src.ID == "" || seenSources[src.ID] || src.MapID != doc.Map.ID || src.NodeID != n.ID || src.Position < 0 || sourcePositions[src.Position] {
				return plan, ErrClassicMindMapImportInvalid
			}
			seenSources[src.ID] = true
			sourcePositions[src.Position] = true
			if src.Kind == ClassicMindMapSourceExternalFile {
				return plan, ErrClassicMindMapImportFile
			}
			// Evidence paths are descriptive only. Do not import executable local
			// locators, including unrelated path fields on otherwise valid sources.
			if src.Kind == ClassicMindMapSourceEvidence {
				if src.Evidence == nil {
					return plan, ErrClassicMindMapImportInvalid
				}
				a := *src.Evidence
				if err := validateEvidenceAnchor(a); err != nil {
					return plan, ErrClassicMindMapImportInvalid
				}
				a.SourcePath = sourceName(Entry{SourcePath: strings.ReplaceAll(a.SourcePath, "\\", "/")})
				src.Evidence = &a
				src.Locator = classicMindMapEvidenceLocator(a)
				src.URL = ""
				src.KnowledgeNodeID = ""
			} else {
				src.Evidence = nil
				src.Locator = ""
				if src.Kind == ClassicMindMapSourceURL {
					src.KnowledgeNodeID = ""
				} else {
					src.URL = ""
				}
			}
			src.ID = ""
			src.MapID = ""
			src.NodeID = ""
			src.Created = ""
			src.EvidenceState = ""
			nd.Sources = append(nd.Sources, src)
		}
		plan.Draft.Nodes = append(plan.Draft.Nodes, nd)
	}
	plan.Draft, _, err = normalizeClassicMindMapDraft(plan.Draft)
	if err != nil {
		return plan, ErrClassicMindMapImportInvalid
	}
	// Reserve generated identities/timestamps as well as strings against the
	// same 4 MiB interactive data budget checked again inside the transaction.
	size := len(plan.Draft.Title) + len(plan.Draft.Description) + 512
	for _, n := range plan.Draft.Nodes {
		size += len(n.Label) + len(n.Summary) + len(n.BodyMarkdown) + len(n.Style) + 512
		for _, s := range n.Sources {
			size += len(s.Title) + len(s.Locator) + len(s.URL) + len(s.KnowledgeNodeID) + 512
			if s.Evidence != nil {
				size += len(s.Evidence.DocumentID) + len(s.Evidence.SourcePath) + len(s.Evidence.Excerpt) + 512
			}
		}
	}
	if size > 4<<20 {
		return plan, ErrWebMapEditLimit
	}
	plan.Preview = ClassicMindMapImportPreview{Title: plan.Draft.Title, NodeCount: len(plan.Draft.Nodes), SourceCount: portable.SourceCount, Warnings: []string{"Будет создана новая карта с новыми идентификаторами. История исходной карты не переносится.", "Пути документов сокращаются до имён файлов; URL не загружаются. Статусы источников проверяются в выбранной базе."}}
	return plan, nil
}

func previewClassicMindMapImportTx(ctx context.Context, tx *sql.Tx, data []byte) (classicMindMapImportPlan, error) {
	plan, err := parseClassicMindMapImport(data)
	if err != nil {
		return plan, err
	}
	r := webMapEvidenceResolver{ctx: ctx, tx: tx, entries: map[evidenceResolutionCoordinate][]Entry{}, anchors: map[string]webMapResolvedAnchor{}, graph: map[string]webMapGraphEvidence{}}
	states := []EvidenceState{}
	for i := range plan.Draft.Nodes {
		for j := range plan.Draft.Nodes[i].Sources {
			s := &plan.Draft.Nodes[i].Sources[j]
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			state := EvidenceMissing
			switch s.Kind {
			case ClassicMindMapSourceEvidence:
				resolved, e := r.resolve(*s.Evidence)
				if e != nil {
					return plan, e
				}
				state = resolved.Resolution.State
				if state == EvidenceCurrent {
					// Only a unique trusted entry may restore a physical locator. A
					// browser-supplied path never becomes authority to open a file.
					a := s.Evidence
					paths := map[string]bool{}
					for _, entry := range r.entries[evidenceResolutionCoordinate{a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex}] {
						if resolveEvidenceAnchorFromEntries(*a, []Entry{entry}).State == EvidenceCurrent && sourceName(entry) == a.SourcePath {
							paths[firstNonEmpty(entry.SourcePath, entry.SourceFile)] = true
						}
					}
					if len(paths) != 1 {
						return plan, ErrClassicMindMapImportInvalid
					}
					for path := range paths {
						a.SourcePath = path
					}
					s.Locator = classicMindMapEvidenceLocator(*a)
				}
			case ClassicMindMapSourceURL:
				state = EvidenceCurrent
			case ClassicMindMapSourceKnowledgeNode:
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, s.KnowledgeNodeID).Scan(&count); err != nil {
					return plan, err
				}
				if count == 1 {
					state = EvidenceCurrent
				}
			}
			states = append(states, state)
			switch state {
			case EvidenceCurrent:
				plan.Preview.Current++
			case EvidenceStale:
				plan.Preview.Stale++
			default:
				plan.Preview.Missing++
			}
		}
	}
	pins, _ := json.Marshal(struct {
		States []EvidenceState
		Draft  ClassicMindMapDraft
	}{states, plan.Draft})
	plan.Preview.Digest = prefixedSHA256(append(append(append([]byte("classic-import-v1\n"), data...), '\n'), pins...))
	return plan, nil
}

// Import through the shared tree/history machinery, then attach validated native
// anchors in its own audited revision. Stale anchors are kept as stale evidence;
// the regular AI/draft importer still requires current evidence.
func importClassicMindMapPlanTx(tx *sql.Tx, plan classicMindMapImportPlan, actor, comment string) (ClassicMindMapDocument, error) {
	draft := plan.Draft
	draft.Nodes = append([]ClassicMindMapNodeDraft(nil), draft.Nodes...)
	for i := range draft.Nodes {
		draft.Nodes[i].Sources = nil
	}
	doc, err := importClassicMindMapTx(tx, draft, actor, comment)
	if err != nil || plan.Preview.SourceCount == 0 {
		return doc, err
	}
	children := map[string][]ClassicMindMapNode{}
	for _, n := range doc.Nodes {
		children[n.ParentID] = append(children[n.ParentID], n)
	}
	for key := range children {
		sort.Slice(children[key], func(i, j int) bool { return children[key][i].Position < children[key][j].Position })
	}
	ids := map[string]string{}
	positions := map[string]int{}
	for _, n := range plan.Draft.Nodes {
		parentID := ids[n.ParentRef]
		pos := positions[n.ParentRef]
		positions[n.ParentRef]++
		if pos >= len(children[parentID]) {
			return doc, fmt.Errorf("import tree identity mapping failed")
		}
		ids[n.Ref] = children[parentID][pos].ID
	}
	doc, _, err = mutateClassicMindMapTx(tx, doc.Map.ID, doc.Map.Revision, actor, comment, "import_sources", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for _, n := range plan.Draft.Nodes {
			for j, s := range n.Sources {
				if e := insertClassicMindMapSource(tx, item.ID, ids[n.Ref], j, s, now); e != nil {
					return "", e
				}
			}
		}
		return "", nil
	})
	if err != nil {
		return doc, err
	}
	_, encoded, err := finalizeClassicMindMapDocument(doc)
	if err != nil {
		return doc, err
	}
	_, err = tx.Exec(`INSERT INTO mind_map_snapshots(map_id,revision,reason,document_json,document_digest,created) VALUES(?,?,'imported',?,?,?)`, doc.Map.ID, doc.Map.Revision, string(encoded), doc.Digest, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return doc, err
	}
	return doc, nil
}

func (s *Store) PreviewClassicMindMapJSON(ctx context.Context, data []byte) (ClassicMindMapImportPreview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClassicMindMapImportPreview{}, err
	}
	defer tx.Rollback()
	plan, err := previewClassicMindMapImportTx(ctx, tx, data)
	return plan.Preview, err
}

func (s *Store) ImportClassicMindMapJSON(ctx context.Context, data []byte, digest, actor, comment string) (ClassicMindMapDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	defer tx.Rollback()
	plan, err := previewClassicMindMapImportTx(ctx, tx, data)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	if digest == "" || digest != plan.Preview.Digest {
		return ClassicMindMapDocument{}, ErrClassicMindMapImportPreview
	}
	doc, err := importClassicMindMapPlanTx(tx, plan, actor, comment)
	if err != nil {
		return doc, err
	}
	if err = checkWebMapEditBounds(ctx, tx, doc.Map.ID); err != nil {
		return ClassicMindMapDocument{}, err
	}
	if err = tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, err
	}
	return s.resolveClassicMindMapSourceStates(doc)
}
