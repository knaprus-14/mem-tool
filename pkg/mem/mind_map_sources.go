package mem

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxClassicMindMapSourceResults = 100
	MaxClassicMindMapUploadBytes   = 64 << 20
)

type ClassicMindMapSourceDocument struct {
	DocumentID       string `json:"document_id"`
	DocumentRevision string `json:"document_revision"`
	SourcePath       string `json:"source_path"`
	MediaType        string `json:"media_type,omitempty"`
	ChunkCount       int    `json:"chunk_count"`
	PageCount        int    `json:"page_count"`
}

type ClassicMindMapEvidenceCandidate struct {
	EntryID          int64    `json:"entry_id"`
	Title            string   `json:"title"`
	Excerpt          string   `json:"excerpt"`
	Tags             []string `json:"tags,omitempty"`
	CitationID       string   `json:"citation_id"`
	DocumentID       string   `json:"document_id"`
	DocumentRevision string   `json:"document_revision"`
	SourcePath       string   `json:"source_path"`
	MediaType        string   `json:"media_type,omitempty"`
	Page             int      `json:"page,omitempty"`
	BlockIndex       int      `json:"block_index,omitempty"`
	BlockChunkIndex  int      `json:"block_chunk_index,omitempty"`
	ChunkIndex       int      `json:"chunk_index,omitempty"`
	TotalChunks      int      `json:"total_chunks,omitempty"`
	Score            float64  `json:"score,omitempty"`
}

type ClassicMindMapEvidenceSearchOptions struct {
	Query    string
	Document string
	Page     int
	Limit    int
}

type ClassicMindMapKnowledgeCandidate struct {
	ID            string            `json:"id"`
	Kind          KnowledgeNodeKind `json:"kind"`
	Label         string            `json:"label"`
	Body          string            `json:"body,omitempty"`
	Status        KnowledgeStatus   `json:"status"`
	Origin        KnowledgeOrigin   `json:"origin"`
	EvidenceState EvidenceState     `json:"evidence_state"`
}

type ClassicMindMapResolvedFile struct {
	Path      string
	Title     string
	MediaType string
	Page      int
	State     EvidenceState
}

func (s *Store) resolveClassicMindMapSourceStates(doc ClassicMindMapDocument) (ClassicMindMapDocument, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return ClassicMindMapDocument{}, fmt.Errorf("begin classic mind map source resolution: %w", err)
	}
	resolved, err := resolveClassicMindMapSourceStatesWithQuery(tx, doc)
	if err != nil {
		_ = tx.Rollback()
		return ClassicMindMapDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClassicMindMapDocument{}, fmt.Errorf("finish classic mind map source resolution: %w", err)
	}
	return resolved, nil
}

func resolveClassicMindMapSourceStatesWithQuery(q classicMindMapQuerier, doc ClassicMindMapDocument) (ClassicMindMapDocument, error) {
	for i := range doc.Nodes {
		for j := range doc.Nodes[i].Sources {
			source := &doc.Nodes[i].Sources[j]
			switch source.Kind {
			case ClassicMindMapSourceEvidence:
				if source.Evidence != nil {
					resolution, err := resolveClassicMindMapEvidenceWithQuery(q, *source.Evidence)
					if err != nil {
						return ClassicMindMapDocument{}, fmt.Errorf("resolve classic mind map evidence source %q: %w", source.ID, err)
					}
					source.EvidenceState = resolution.State
				}
			case ClassicMindMapSourceExternalFile:
				source.EvidenceState = EvidenceMissing
				if info, err := os.Stat(source.Locator); err == nil && info.Mode().IsRegular() {
					source.EvidenceState = EvidenceCurrent
				}
			case ClassicMindMapSourceURL:
				source.EvidenceState = EvidenceCurrent
			case ClassicMindMapSourceKnowledgeNode:
				source.EvidenceState = EvidenceMissing
				var count int
				if err := q.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, source.KnowledgeNodeID).Scan(&count); err != nil {
					return ClassicMindMapDocument{}, fmt.Errorf("resolve classic mind map knowledge source %q: %w", source.ID, err)
				} else if count == 1 {
					source.EvidenceState = EvidenceCurrent
				}
			}
		}
	}
	doc.StateDigest = classicMindMapResolvedStateDigest(doc)
	return doc, nil
}

func resolveClassicMindMapEvidenceWithQuery(q classicMindMapQuerier, anchor EvidenceAnchor) (EvidenceResolution, error) {
	if err := validateEvidenceAnchor(anchor); err != nil {
		return EvidenceResolution{Anchor: anchor, State: EvidenceMissing}, err
	}
	rows, err := q.Query(`SELECT id, text, document_id, document_revision, chunk_hash,
source_file, source_path, page, block_index, block_chunk_index, block_total_chunks, chunk_index
FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=? ORDER BY id`,
		anchor.DocumentID, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex)
	if err != nil {
		return EvidenceResolution{Anchor: anchor, State: EvidenceMissing}, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.ID, &entry.Text, &entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash,
			&entry.SourceFile, &entry.SourcePath, &entry.Page, &entry.BlockIndex, &entry.BlockChunkIndex,
			&entry.BlockTotalChunks, &entry.ChunkIndex); err != nil {
			return EvidenceResolution{Anchor: anchor, State: EvidenceMissing}, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return EvidenceResolution{Anchor: anchor, State: EvidenceMissing}, err
	}
	return resolveEvidenceAnchorFromEntries(anchor, entries), nil
}

const evidenceResolutionCoordinateBatchSize = 200

type evidenceResolutionCoordinate struct {
	documentID      string
	page            int
	blockIndex      int
	blockChunkIndex int
}

// resolveEvidenceAnchorsWithQuery preserves the single-anchor resolution
// semantics while resolving repeated coordinates in bounded batches. It is
// used by whole-graph snapshots to avoid one SQLite round trip per anchor.
func resolveEvidenceAnchorsWithQuery(q knowledgeEvidenceQuerier, anchors []EvidenceAnchor) ([]EvidenceResolution, error) {
	return resolveEvidenceAnchorsWithQueryContext(context.Background(), q, anchors, nil)
}

func resolveEvidenceAnchorsWithQueryContext(ctx context.Context, q knowledgeEvidenceQuerier, anchors []EvidenceAnchor, progress func(completed, total int)) ([]EvidenceResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	coordinates := make([]evidenceResolutionCoordinate, 0, len(anchors))
	seen := make(map[evidenceResolutionCoordinate]struct{}, len(anchors))
	for i, anchor := range anchors {
		if err := validateEvidenceAnchor(anchor); err != nil {
			return nil, fmt.Errorf("validate evidence anchor %d: %w", i, err)
		}
		coordinate := evidenceResolutionCoordinate{
			documentID: anchor.DocumentID, page: anchor.Page, blockIndex: anchor.BlockIndex, blockChunkIndex: anchor.BlockChunkIndex,
		}
		if _, exists := seen[coordinate]; !exists {
			seen[coordinate] = struct{}{}
			coordinates = append(coordinates, coordinate)
		}
	}
	entriesByCoordinate := make(map[evidenceResolutionCoordinate][]Entry, len(coordinates))
	if progress != nil {
		progress(0, len(coordinates))
	}
	for offset := 0; offset < len(coordinates); offset += evidenceResolutionCoordinateBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := offset + evidenceResolutionCoordinateBatchSize
		if end > len(coordinates) {
			end = len(coordinates)
		}
		var where strings.Builder
		args := make([]any, 0, (end-offset)*4)
		for i, coordinate := range coordinates[offset:end] {
			if i > 0 {
				where.WriteString(" OR ")
			}
			where.WriteString("(document_id=? AND page=? AND block_index=? AND block_chunk_index=?)")
			args = append(args, coordinate.documentID, coordinate.page, coordinate.blockIndex, coordinate.blockChunkIndex)
		}
		rows, err := q.Query(`SELECT id, text, document_id, document_revision, chunk_hash,
source_file, source_path, page, block_index, block_chunk_index, block_total_chunks, chunk_index
FROM entries WHERE `+where.String()+` ORDER BY id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var entry Entry
			if err := rows.Scan(&entry.ID, &entry.Text, &entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash,
				&entry.SourceFile, &entry.SourcePath, &entry.Page, &entry.BlockIndex, &entry.BlockChunkIndex,
				&entry.BlockTotalChunks, &entry.ChunkIndex); err != nil {
				rows.Close()
				return nil, err
			}
			coordinate := evidenceResolutionCoordinate{
				documentID: entry.DocumentID, page: entry.Page, blockIndex: entry.BlockIndex, blockChunkIndex: entry.BlockChunkIndex,
			}
			entriesByCoordinate[coordinate] = append(entriesByCoordinate[coordinate], entry)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(end, len(coordinates))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]EvidenceResolution, 0, len(anchors))
	for _, anchor := range anchors {
		coordinate := evidenceResolutionCoordinate{
			documentID: anchor.DocumentID, page: anchor.Page, blockIndex: anchor.BlockIndex, blockChunkIndex: anchor.BlockChunkIndex,
		}
		result = append(result, resolveEvidenceAnchorFromEntries(anchor, entriesByCoordinate[coordinate]))
	}
	return result, nil
}

// classicMindMapResolvedStateDigest pins only the dynamic resolution state of
// sources. It deliberately excludes content and timestamps: content is pinned
// by Digest/revision, while repeated loads against unchanged backing state must
// remain byte-deterministic.
func classicMindMapResolvedStateDigest(doc ClassicMindMapDocument) string {
	type sourceState struct {
		ID     string                   `json:"id"`
		NodeID string                   `json:"node_id"`
		Kind   ClassicMindMapSourceKind `json:"kind"`
		State  EvidenceState            `json:"state"`
	}
	states := make([]sourceState, 0)
	for _, node := range doc.Nodes {
		for _, source := range node.Sources {
			states = append(states, sourceState{ID: source.ID, NodeID: node.ID, Kind: source.Kind, State: source.EvidenceState})
		}
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].NodeID != states[j].NodeID {
			return states[i].NodeID < states[j].NodeID
		}
		if states[i].ID != states[j].ID {
			return states[i].ID < states[j].ID
		}
		if states[i].Kind != states[j].Kind {
			return states[i].Kind < states[j].Kind
		}
		return states[i].State < states[j].State
	})
	payload := struct {
		Version int           `json:"version"`
		MapID   string        `json:"map_id"`
		Sources []sourceState `json:"sources"`
	}{Version: 1, MapID: doc.Map.ID, Sources: states}
	encoded, _ := json.Marshal(payload)
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// ListClassicMindMapSourceDocuments lists only versioned documents that are
// physically present in the active store. Old revisions are represented by
// stale anchors on maps, never as attachable candidates.
func (s *Store) ListClassicMindMapSourceDocuments() []ClassicMindMapSourceDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type aggregate struct {
		item  ClassicMindMapSourceDocument
		pages map[int]struct{}
	}
	groups := make(map[string]*aggregate)
	for _, entry := range s.entries {
		if entry.DocumentID == "" || entry.DocumentRevision == "" || entry.ChunkHash == "" || entry.SourcePath == "" {
			continue
		}
		key := entry.DocumentID + "\x00" + entry.DocumentRevision + "\x00" + entry.SourcePath
		group := groups[key]
		if group == nil {
			group = &aggregate{item: ClassicMindMapSourceDocument{
				DocumentID: entry.DocumentID, DocumentRevision: entry.DocumentRevision,
				SourcePath: entry.SourcePath, MediaType: entry.MediaType,
			}, pages: make(map[int]struct{})}
			groups[key] = group
		}
		group.item.ChunkCount++
		if entry.Page > 0 {
			group.pages[entry.Page] = struct{}{}
		}
	}
	result := make([]ClassicMindMapSourceDocument, 0, len(groups))
	for _, group := range groups {
		group.item.PageCount = len(group.pages)
		result = append(result, group.item)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].SourcePath) < strings.ToLower(result[j].SourcePath)
	})
	return result
}

// SearchClassicMindMapEvidence performs a local lexical search and document/
// page browsing without contacting an embedding or answer model.
func (s *Store) SearchClassicMindMapEvidence(options ClassicMindMapEvidenceSearchOptions) ([]ClassicMindMapEvidenceCandidate, error) {
	query := strings.TrimSpace(options.Query)
	document := strings.TrimSpace(options.Document)
	limit := options.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > MaxClassicMindMapSourceResults {
		return nil, fmt.Errorf("limit источников не может превышать %d", MaxClassicMindMapSourceResults)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lexical := map[int64]float64{}
	if query != "" {
		var err error
		lexical, err = s.lexicalScoresLocked(query)
		if err != nil {
			return nil, err
		}
	}
	result := make([]ClassicMindMapEvidenceCandidate, 0, limit)
	for i := range s.entries {
		entry := cloneEntry(s.entries[i])
		if entry.DocumentID == "" || entry.DocumentRevision == "" || entry.ChunkHash == "" || entry.SourcePath == "" {
			continue
		}
		if document != "" && entry.DocumentID != document && !strings.EqualFold(entry.SourcePath, document) {
			continue
		}
		if options.Page > 0 && entry.Page != options.Page {
			continue
		}
		score, hit := lexical[entry.ID]
		if query != "" && !hit && !classicMindMapTextMatches(entry, query) {
			continue
		}
		annotateCitation(&entry)
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			title = filepath.Base(entry.SourcePath)
		}
		result = append(result, ClassicMindMapEvidenceCandidate{
			EntryID: entry.ID, Title: title, Excerpt: classicMindMapPreview(entry.Text, 900),
			Tags: append([]string(nil), entry.Tags...), CitationID: entry.CitationID,
			DocumentID: entry.DocumentID, DocumentRevision: entry.DocumentRevision,
			SourcePath: entry.SourcePath, MediaType: entry.MediaType, Page: entry.Page,
			BlockIndex: entry.BlockIndex, BlockChunkIndex: entry.BlockChunkIndex,
			ChunkIndex: entry.ChunkIndex, TotalChunks: entry.TotalChunks, Score: score,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if !strings.EqualFold(result[i].SourcePath, result[j].SourcePath) {
			return strings.ToLower(result[i].SourcePath) < strings.ToLower(result[j].SourcePath)
		}
		if result[i].Page != result[j].Page {
			return result[i].Page < result[j].Page
		}
		if result[i].BlockIndex != result[j].BlockIndex {
			return result[i].BlockIndex < result[j].BlockIndex
		}
		if result[i].BlockChunkIndex != result[j].BlockChunkIndex {
			return result[i].BlockChunkIndex < result[j].BlockChunkIndex
		}
		return result[i].EntryID < result[j].EntryID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func classicMindMapTextMatches(entry Entry, query string) bool {
	haystack := strings.ToLower(entry.Title + "\n" + entry.Text + "\n" + strings.Join(entry.Tags, " "))
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

func classicMindMapPreview(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

func (s *Store) SearchClassicMindMapKnowledgeNodes(query string, limit int) ([]ClassicMindMapKnowledgeCandidate, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 30
	}
	if limit > MaxClassicMindMapSourceResults {
		return nil, fmt.Errorf("limit knowledge nodes не может превышать %d", MaxClassicMindMapSourceResults)
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		return nil, err
	}
	review, err := s.ReviewKnowledgeGraph()
	if err != nil {
		return nil, err
	}
	states := make(map[string]EvidenceState, len(review.Items))
	for _, item := range review.Items {
		if item.ObjectType == KnowledgeObjectNode {
			states[item.ID] = item.EvidenceState
		}
	}
	result := make([]ClassicMindMapKnowledgeCandidate, 0, limit)
	for _, node := range graph.Nodes {
		if query != "" && !strings.Contains(strings.ToLower(node.Label+"\n"+node.Body), strings.ToLower(query)) {
			continue
		}
		result = append(result, ClassicMindMapKnowledgeCandidate{
			ID: node.ID, Kind: node.Kind, Label: node.Label,
			Body: classicMindMapPreview(node.Body, 700), Status: node.Status,
			Origin: node.Origin, EvidenceState: states[node.ID],
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Label != result[j].Label {
			return strings.ToLower(result[i].Label) < strings.ToLower(result[j].Label)
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) AttachClassicMindMapSource(mapRef, nodeRef string, source ClassicMindMapSource, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, ClassicMindMapSource, error) {
	if source.Kind == ClassicMindMapSourceEvidence {
		return ClassicMindMapDocument{}, ClassicMindMapSource{}, fmt.Errorf("evidence source должен добавляться через AttachClassicMindMapEvidence")
	}
	normalized, err := s.normalizeClassicMindMapSource(source)
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapSource{}, err
	}
	var attached ClassicMindMapSource
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "attach_source:"+string(normalized.Kind), func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		if normalized.Kind == ClassicMindMapSourceKnowledgeNode {
			var label string
			var kind KnowledgeNodeKind
			var status KnowledgeStatus
			if err := tx.QueryRow(`SELECT label, kind, status FROM knowledge_nodes WHERE id=?`, normalized.KnowledgeNodeID).Scan(&label, &kind, &status); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return "", fmt.Errorf("knowledge node %q не найден", normalized.KnowledgeNodeID)
				}
				return "", err
			}
			normalized.Title = label
			normalized.Locator = fmt.Sprintf("%s · %s", kind, status)
		}
		column, value := "locator", normalized.Locator
		switch normalized.Kind {
		case ClassicMindMapSourceURL:
			column, value = "url", normalized.URL
		case ClassicMindMapSourceKnowledgeNode:
			column, value = "knowledge_node_id", normalized.KnowledgeNodeID
		}
		var duplicate int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' AND kind=? AND %s=?`, column)
		if err := tx.QueryRow(query, item.ID, node.ID, normalized.Kind, value).Scan(&duplicate); err != nil {
			return "", err
		}
		if duplicate != 0 {
			return "", fmt.Errorf("этот источник уже привязан к узлу %q", node.Label)
		}
		var position int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at=''`, item.ID, node.ID).Scan(&position); err != nil {
			return "", err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		attached = normalized
		attached.ID, err = newClassicMindMapID("mms-")
		if err != nil {
			return "", err
		}
		attached.MapID, attached.NodeID, attached.Position, attached.Created = item.ID, node.ID, position, now
		if err := insertClassicMindMapSource(tx, item.ID, node.ID, position, attached, now); err != nil {
			return "", err
		}
		return node.ID, nil
	})
	if err == nil {
		for _, node := range doc.Nodes {
			for _, source := range node.Sources {
				if source.ID == attached.ID {
					attached = source
				}
			}
		}
	}
	return doc, attached, err
}

func (s *Store) normalizeClassicMindMapSource(source ClassicMindMapSource) (ClassicMindMapSource, error) {
	source.ID, source.MapID, source.NodeID, source.Created = "", "", "", ""
	source.Position = 0
	source.Title = strings.TrimSpace(source.Title)
	source.Locator = strings.TrimSpace(source.Locator)
	source.URL = strings.TrimSpace(source.URL)
	source.KnowledgeNodeID = strings.TrimSpace(source.KnowledgeNodeID)
	source.Evidence, source.EvidenceState = nil, ""
	switch source.Kind {
	case ClassicMindMapSourceExternalFile:
		path, err := filepath.Abs(source.Locator)
		if err != nil {
			return source, fmt.Errorf("абсолютный путь файла: %w", err)
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return source, fmt.Errorf("открыть файл источника: %w", err)
		}
		if !info.Mode().IsRegular() {
			return source, fmt.Errorf("источник не является обычным файлом")
		}
		source.Locator = path
		if source.Title == "" {
			source.Title = filepath.Base(path)
		}
	case ClassicMindMapSourceURL:
		parsed, err := validateClassicMindMapHTTPURL(source.URL)
		if err != nil {
			return source, err
		}
		if source.Title == "" {
			source.Title = parsed.Host
		}
	case ClassicMindMapSourceKnowledgeNode:
		if err := validateKnowledgeID(source.KnowledgeNodeID); err != nil {
			return source, fmt.Errorf("knowledge node: %w", err)
		}
	default:
		return source, fmt.Errorf("неподдерживаемый тип источника %q", source.Kind)
	}
	if err := validateClassicMindMapSource(source); err != nil {
		return source, err
	}
	return source, nil
}

// validateClassicMindMapHTTPURL is the single trust boundary for web sources.
// Query strings and fragments are legitimate document locators, but relative,
// opaque, credential-bearing and non-HTTP URLs must never become clickable.
func validateClassicMindMapHTTPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if err := validateClassicMindMapText("URL source", raw, MaxClassicMindMapTextRunes, true); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return nil, fmt.Errorf("URL должен быть абсолютной ссылкой http или https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("URL источника не должен содержать имя пользователя или пароль")
	}
	return parsed, nil
}

func (s *Store) DetachClassicMindMapSource(mapRef, nodeRef, sourceID string, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return ClassicMindMapDocument{}, fmt.Errorf("source ID пуст")
	}
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "detach_source", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := tx.Exec(`UPDATE mind_map_node_sources SET deleted_at=? WHERE id=? AND map_id=? AND node_id=? AND deleted_at=''`, now, sourceID, item.ID, node.ID)
		if err != nil {
			return "", err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return "", fmt.Errorf("источник узла не найден")
		}
		if err := normalizeClassicMindMapSourcePositions(tx, item.ID, node.ID); err != nil {
			return "", err
		}
		return node.ID, nil
	})
	return doc, err
}

func (s *Store) MoveClassicMindMapSource(mapRef, nodeRef, sourceID string, position int, expectedRevision int64, actor, comment string) (ClassicMindMapDocument, error) {
	doc, _, err := s.mutateClassicMindMap(mapRef, expectedRevision, actor, comment, "move_source", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		node, err := resolveClassicMindMapNodeRef(tx, item.ID, nodeRef)
		if err != nil {
			return "", err
		}
		if node.Locked {
			return "", fmt.Errorf("%w: %q", ErrClassicMindMapLocked, node.Label)
		}
		rows, err := tx.Query(`SELECT id FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' ORDER BY position, id`, item.ID, node.ID)
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
		old := -1
		for i, id := range ids {
			if id == sourceID {
				old = i
				break
			}
		}
		if old < 0 {
			return "", fmt.Errorf("источник узла не найден")
		}
		ids = append(ids[:old], ids[old+1:]...)
		position = clampClassicMindMapPosition(position, len(ids))
		ids = append(ids, "")
		copy(ids[position+1:], ids[position:])
		ids[position] = sourceID
		for i, id := range ids {
			if _, err := tx.Exec(`UPDATE mind_map_node_sources SET position=? WHERE id=?`, i, id); err != nil {
				return "", err
			}
		}
		return node.ID, nil
	})
	return doc, err
}

func normalizeClassicMindMapSourcePositions(tx *sql.Tx, mapID, nodeID string) error {
	rows, err := tx.Query(`SELECT id FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' ORDER BY position, id`, mapID, nodeID)
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
		if _, err := tx.Exec(`UPDATE mind_map_node_sources SET position=? WHERE id=?`, position, id); err != nil {
			return err
		}
	}
	return nil
}

// ImportClassicMindMapAttachment copies a browser-selected file beside the
// active store. It never overwrites an existing attachment.
func (s *Store) ImportClassicMindMapAttachment(filename string, reader io.Reader) (string, string, error) {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return "", "", fmt.Errorf("имя файла пусто")
	}
	dir := filepath.Join(filepath.Dir(s.path), "mindmap-files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	id, err := newClassicMindMapID("mmf-")
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, id+"-"+filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, MaxClassicMindMapUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > MaxClassicMindMapUploadBytes {
		_ = os.Remove(path)
		if written > MaxClassicMindMapUploadBytes {
			return "", "", fmt.Errorf("файл превышает лимит %d MiB", MaxClassicMindMapUploadBytes>>20)
		}
		if copyErr != nil {
			return "", "", copyErr
		}
		return "", "", closeErr
	}
	return path, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) ResolveClassicMindMapFileSource(mapRef, sourceID string) (ClassicMindMapResolvedFile, error) {
	doc, err := s.LoadClassicMindMap(mapRef)
	if err != nil {
		return ClassicMindMapResolvedFile{}, err
	}
	for _, node := range doc.Nodes {
		for _, source := range node.Sources {
			if source.ID != sourceID {
				continue
			}
			resolved := ClassicMindMapResolvedFile{Title: source.Title, State: source.EvidenceState}
			switch source.Kind {
			case ClassicMindMapSourceEvidence:
				if source.Evidence == nil || source.EvidenceState != EvidenceCurrent {
					return resolved, fmt.Errorf("источник не является current")
				}
				resolved.Path, resolved.Page = source.Evidence.SourcePath, source.Evidence.Page
			case ClassicMindMapSourceExternalFile:
				resolved.Path = source.Locator
			default:
				return resolved, fmt.Errorf("источник не является локальным файлом")
			}
			if !filepath.IsAbs(resolved.Path) || filepath.Clean(resolved.Path) != resolved.Path {
				return resolved, fmt.Errorf("путь источника некорректен")
			}
			info, err := os.Stat(resolved.Path)
			if err != nil || !info.Mode().IsRegular() {
				return resolved, fmt.Errorf("файл источника недоступен")
			}
			resolved.MediaType = mime.TypeByExtension(filepath.Ext(resolved.Path))
			if resolved.MediaType == "" {
				resolved.MediaType = "application/octet-stream"
			}
			if resolved.Title == "" {
				resolved.Title = filepath.Base(resolved.Path)
			}
			return resolved, nil
		}
	}
	return ClassicMindMapResolvedFile{}, fmt.Errorf("источник карты не найден")
}
