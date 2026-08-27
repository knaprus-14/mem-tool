package mem

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	KnowledgeGraphExportVersion       = 1
	MaxKnowledgeGraphExportNodes      = 10000
	MaxKnowledgeGraphExportEdges      = 100000
	MaxKnowledgeGraphExportBytes      = 128 << 20
	MaxKnowledgeGraphExportTitleRunes = 256
)

type KnowledgeGraphExportFormat string

const (
	KnowledgeGraphExportMarkdown KnowledgeGraphExportFormat = "markdown"
	KnowledgeGraphExportOutline  KnowledgeGraphExportFormat = "outline"
	KnowledgeGraphExportOPML     KnowledgeGraphExportFormat = "opml"
	KnowledgeGraphExportGraphML  KnowledgeGraphExportFormat = "graphml"
	KnowledgeGraphExportGEXF     KnowledgeGraphExportFormat = "gexf"
	KnowledgeGraphExportMermaid  KnowledgeGraphExportFormat = "mermaid"
	KnowledgeGraphExportObsidian KnowledgeGraphExportFormat = "obsidian"
)

var ErrKnowledgeGraphExportChanged = errors.New("knowledge graph or source state changed")

type KnowledgeGraphExportRequest struct {
	Format              KnowledgeGraphExportFormat `json:"format"`
	Title               string                     `json:"title,omitempty"`
	ExpectedDigest      string                     `json:"expected_digest,omitempty"`
	ExpectedStateDigest string                     `json:"expected_state_digest,omitempty"`
}

type KnowledgeGraphExportPin struct {
	Digest      string `json:"digest"`
	StateDigest string `json:"state_digest"`
	NodeCount   int    `json:"node_count"`
	EdgeCount   int    `json:"edge_count"`
	Evidence    int    `json:"evidence_count"`
	Current     int    `json:"current_evidence"`
	Stale       int    `json:"stale_evidence"`
	Missing     int    `json:"missing_evidence"`
}

type KnowledgeGraphExportArtifact struct {
	Format      KnowledgeGraphExportFormat
	Filename    string
	ContentType string
	Data        []byte
	Digest      string
	StateDigest string
	NodeCount   int
	EdgeCount   int
	Evidence    int
	Current     int
	Stale       int
	Missing     int
}

type KnowledgeGraphExportEvidence struct {
	ObjectType KnowledgeObjectType  `json:"object_type"`
	ObjectID   string               `json:"object_id"`
	Items      []EvidenceResolution `json:"items"`
}

type knowledgeGraphExportEnvelope struct {
	Version     int                            `json:"version"`
	Kind        string                         `json:"kind"`
	Title       string                         `json:"title"`
	Digest      string                         `json:"digest"`
	StateDigest string                         `json:"state_digest"`
	Graph       KnowledgeGraph                 `json:"graph"`
	Evidence    []KnowledgeGraphExportEvidence `json:"resolved_evidence"`
}

type knowledgeGraphExportSnapshot struct {
	Graph          KnowledgeGraph
	Resolved       []KnowledgeGraphExportEvidence
	CurrentEntries []Entry
	Pin            KnowledgeGraphExportPin
}

func (s *Store) BuildKnowledgeGraphExportPin() (KnowledgeGraphExportPin, error) {
	snapshot, err := s.buildKnowledgeGraphExportSnapshot()
	return snapshot.Pin, err
}

// ExportKnowledgeGraph renders the complete current knowledge graph without a
// model or mutation. Optional digests pin both graph content and the dynamic
// current/stale/missing state of every evidence anchor.
func (s *Store) ExportKnowledgeGraph(request KnowledgeGraphExportRequest) (KnowledgeGraphExportArtifact, error) {
	return s.exportKnowledgeGraph(context.Background(), request, nil)
}

type knowledgeGraphExportProgressFunc func(phase string, percent, completed, total int)

func (s *Store) exportKnowledgeGraph(ctx context.Context, request KnowledgeGraphExportRequest, progress knowledgeGraphExportProgressFunc) (KnowledgeGraphExportArtifact, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := func(phase string, percent, completed, total int) {
		if progress != nil {
			progress(phase, percent, completed, total)
		}
	}
	if err := ctx.Err(); err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	var err error
	request, err = normalizeKnowledgeGraphExportRequest(request)
	if err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	report("validating", 5, 0, 0)
	report("pinning", 10, 0, 0)
	snapshot, err := s.buildKnowledgeGraphSnapshotContext(ctx, true, false, func(completed, total int) {
		percent := 45
		if total > 0 {
			percent = 10 + completed*35/total
		}
		report("pinning", percent, completed, total)
	})
	if err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	report("rendering", 55, snapshot.Pin.NodeCount+snapshot.Pin.EdgeCount, snapshot.Pin.NodeCount+snapshot.Pin.EdgeCount)
	if request.ExpectedDigest != "" && request.ExpectedDigest != snapshot.Pin.Digest {
		return KnowledgeGraphExportArtifact{}, fmt.Errorf("%w: content digest mismatch", ErrKnowledgeGraphExportChanged)
	}
	if request.ExpectedStateDigest != "" && request.ExpectedStateDigest != snapshot.Pin.StateDigest {
		return KnowledgeGraphExportArtifact{}, fmt.Errorf("%w: evidence state digest mismatch", ErrKnowledgeGraphExportChanged)
	}
	envelope := knowledgeGraphExportEnvelope{
		Version: KnowledgeGraphExportVersion, Kind: "mem-tool-knowledge-graph", Title: request.Title,
		Digest: snapshot.Pin.Digest, StateDigest: snapshot.Pin.StateDigest, Graph: snapshot.Graph, Evidence: snapshot.Resolved,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return KnowledgeGraphExportArtifact{}, fmt.Errorf("encode knowledge graph export envelope: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	report("rendering", 70, snapshot.Pin.NodeCount+snapshot.Pin.EdgeCount, snapshot.Pin.NodeCount+snapshot.Pin.EdgeCount)
	data, filename, contentType, err := renderKnowledgeGraphExport(request.Format, envelope, canonical)
	if err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return KnowledgeGraphExportArtifact{}, err
	}
	report("finalizing", 90, len(data), len(data))
	if len(data) > MaxKnowledgeGraphExportBytes {
		return KnowledgeGraphExportArtifact{}, fmt.Errorf("knowledge graph export exceeds %d bytes", MaxKnowledgeGraphExportBytes)
	}
	artifact := KnowledgeGraphExportArtifact{
		Format: request.Format, Filename: filename, ContentType: contentType, Data: data,
		Digest: snapshot.Pin.Digest, StateDigest: snapshot.Pin.StateDigest,
		NodeCount: snapshot.Pin.NodeCount, EdgeCount: snapshot.Pin.EdgeCount, Evidence: snapshot.Pin.Evidence,
		Current: snapshot.Pin.Current, Stale: snapshot.Pin.Stale, Missing: snapshot.Pin.Missing,
	}
	report("completed", 100, len(data), len(data))
	return artifact, nil
}

func normalizeKnowledgeGraphExportRequest(request KnowledgeGraphExportRequest) (KnowledgeGraphExportRequest, error) {
	request.Format = KnowledgeGraphExportFormat(strings.ToLower(strings.TrimSpace(string(request.Format))))
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" {
		request.Title = "Карта знаний mem-tool"
	}
	if !utf8.ValidString(request.Title) || utf8.RuneCountInString(request.Title) > MaxKnowledgeGraphExportTitleRunes || strings.ContainsAny(request.Title, "\r\n\t") {
		return KnowledgeGraphExportRequest{}, fmt.Errorf("knowledge graph export title must be one line containing 1..%d runes", MaxKnowledgeGraphExportTitleRunes)
	}
	if !supportedKnowledgeGraphExportFormat(request.Format) {
		return KnowledgeGraphExportRequest{}, fmt.Errorf("unsupported knowledge graph export format %q", request.Format)
	}
	return request, nil
}

func supportedKnowledgeGraphExportFormat(format KnowledgeGraphExportFormat) bool {
	switch format {
	case KnowledgeGraphExportMarkdown, KnowledgeGraphExportOutline, KnowledgeGraphExportOPML,
		KnowledgeGraphExportGraphML, KnowledgeGraphExportGEXF, KnowledgeGraphExportMermaid, KnowledgeGraphExportObsidian:
		return true
	default:
		return false
	}
}

func (s *Store) buildKnowledgeGraphExportSnapshot() (knowledgeGraphExportSnapshot, error) {
	return s.buildKnowledgeGraphSnapshot(true, false)
}

func (s *Store) buildKnowledgeGraphSnapshot(enforceExportLimits, includeCurrentEntries bool) (knowledgeGraphExportSnapshot, error) {
	return s.buildKnowledgeGraphSnapshotContext(context.Background(), enforceExportLimits, includeCurrentEntries, nil)
}

func (s *Store) buildKnowledgeGraphSnapshotContext(ctx context.Context, enforceExportLimits, includeCurrentEntries bool, progress func(completed, total int)) (knowledgeGraphExportSnapshot, error) {
	if s == nil || s.db == nil {
		return knowledgeGraphExportSnapshot{}, errors.New("knowledge graph store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return knowledgeGraphExportSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("begin knowledge graph export snapshot: %w", err)
	}
	defer tx.Rollback()
	graph, err := loadKnowledgeGraphFromQuerier(tx)
	if err != nil {
		return knowledgeGraphExportSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return knowledgeGraphExportSnapshot{}, err
	}
	if enforceExportLimits && (len(graph.Nodes) > MaxKnowledgeGraphExportNodes || len(graph.Edges) > MaxKnowledgeGraphExportEdges) {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("knowledge graph export exceeds node/edge limits %d/%d", MaxKnowledgeGraphExportNodes, MaxKnowledgeGraphExportEdges)
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("encode knowledge graph digest: %w", err)
	}
	snapshot := knowledgeGraphExportSnapshot{Graph: graph}
	snapshot.Pin.Digest = prefixedSHA256(graphJSON)
	snapshot.Pin.NodeCount, snapshot.Pin.EdgeCount = len(graph.Nodes), len(graph.Edges)
	type evidenceOwner struct {
		objectType KnowledgeObjectType
		objectID   string
		anchors    []EvidenceAnchor
	}
	owners := make([]evidenceOwner, 0, len(graph.Nodes)+len(graph.Edges))
	allAnchors := make([]EvidenceAnchor, 0)
	for _, node := range graph.Nodes {
		if len(node.Evidence) > 0 {
			owners = append(owners, evidenceOwner{objectType: KnowledgeObjectNode, objectID: node.ID, anchors: node.Evidence})
			allAnchors = append(allAnchors, node.Evidence...)
		}
	}
	for _, edge := range graph.Edges {
		if len(edge.Evidence) > 0 {
			owners = append(owners, evidenceOwner{objectType: KnowledgeObjectEdge, objectID: edge.ID, anchors: edge.Evidence})
			allAnchors = append(allAnchors, edge.Evidence...)
		}
	}
	resolutions, err := resolveEvidenceAnchorsWithQueryContext(ctx, tx, allAnchors, progress)
	if err != nil {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("resolve knowledge graph export evidence batch: %w", err)
	}
	resolutionIndex := 0
	for _, owner := range owners {
		end := resolutionIndex + len(owner.anchors)
		object := KnowledgeGraphExportEvidence{
			ObjectType: owner.objectType, ObjectID: owner.objectID,
			Items: append([]EvidenceResolution(nil), resolutions[resolutionIndex:end]...),
		}
		resolutionIndex = end
		snapshot.Resolved = append(snapshot.Resolved, object)
		for _, resolution := range object.Items {
			snapshot.Pin.Evidence++
			switch resolution.State {
			case EvidenceCurrent:
				snapshot.Pin.Current++
			case EvidenceStale:
				snapshot.Pin.Stale++
			case EvidenceMissing:
				snapshot.Pin.Missing++
			}
		}
	}
	stateJSON, err := json.Marshal(snapshot.Resolved)
	if err != nil {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("encode knowledge graph evidence state: %w", err)
	}
	snapshot.Pin.StateDigest = prefixedSHA256(stateJSON)
	if includeCurrentEntries {
		if err := ctx.Err(); err != nil {
			return knowledgeGraphExportSnapshot{}, err
		}
		snapshot.CurrentEntries, err = loadKnowledgeGraphCurrentEntries(tx)
		if err != nil {
			return knowledgeGraphExportSnapshot{}, fmt.Errorf("load knowledge graph current entries: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return knowledgeGraphExportSnapshot{}, fmt.Errorf("finish knowledge graph export snapshot: %w", err)
	}
	return snapshot, nil
}

func loadKnowledgeGraphCurrentEntries(q knowledgeEvidenceQuerier) ([]Entry, error) {
	rows, err := q.Query(`SELECT text, document_id, document_revision, chunk_hash, source_path,
page, block_index, block_chunk_index, block_total_chunks
FROM entries WHERE document_id <> '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.Text, &entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash, &entry.SourcePath,
			&entry.Page, &entry.BlockIndex, &entry.BlockChunkIndex, &entry.BlockTotalChunks); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// loadKnowledgeGraphEntriesForAnchors reads only the current chunks addressed
// by a mutation/review request. Evidence freshness is keyed by stable source
// coordinates, so an updated revision is still returned and classified stale
// without materializing the text of the whole corpus.
func loadKnowledgeGraphEntriesForAnchors(q knowledgeEvidenceQuerier, anchors []EvidenceAnchor) ([]Entry, error) {
	seen := make(map[string]bool, len(anchors))
	entries := make([]Entry, 0, len(anchors))
	for _, anchor := range anchors {
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%d", anchor.DocumentID, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows, err := q.Query(`SELECT text, document_id, document_revision, chunk_hash, source_path,
page, block_index, block_chunk_index, block_total_chunks
FROM entries WHERE document_id = ? AND page = ? AND block_index = ? AND block_chunk_index = ?
ORDER BY id LIMIT 1`, anchor.DocumentID, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex)
		if err != nil {
			return nil, err
		}
		if rows.Next() {
			var entry Entry
			if err := rows.Scan(&entry.Text, &entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash,
				&entry.SourcePath, &entry.Page, &entry.BlockIndex, &entry.BlockChunkIndex,
				&entry.BlockTotalChunks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			entries = append(entries, entry)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// resolveEvidenceAnchorsFromQuerier resolves anchors against rows read through
// the caller's database snapshot. Mutations must pass their active *sql.Tx so
// an external Store cannot make a stale in-memory entry cache authorize a
// provenance-sensitive write.
func resolveEvidenceAnchorsFromQuerier(q knowledgeEvidenceQuerier, anchors []EvidenceAnchor) ([]EvidenceResolution, error) {
	entries, err := loadKnowledgeGraphEntriesForAnchors(q, anchors)
	if err != nil {
		return nil, err
	}
	resolutions := make([]EvidenceResolution, 0, len(anchors))
	for _, anchor := range anchors {
		resolutions = append(resolutions, resolveEvidenceAnchorFromEntries(anchor, entries))
	}
	return resolutions, nil
}

func renderKnowledgeGraphExport(format KnowledgeGraphExportFormat, envelope knowledgeGraphExportEnvelope, canonical []byte) ([]byte, string, string, error) {
	switch format {
	case KnowledgeGraphExportMarkdown:
		return renderKnowledgeGraphMarkdown(envelope, canonical), "mem-knowledge-map.md", "text/markdown; charset=utf-8", nil
	case KnowledgeGraphExportOutline:
		return renderKnowledgeGraphOutline(envelope, canonical), "mem-knowledge-map.txt", "text/plain; charset=utf-8", nil
	case KnowledgeGraphExportOPML:
		return renderKnowledgeGraphOPML(envelope, canonical), "mem-knowledge-map.opml", "text/x-opml; charset=utf-8", nil
	case KnowledgeGraphExportGraphML:
		return renderKnowledgeGraphGraphML(envelope, canonical), "mem-knowledge-map.graphml", "application/graphml+xml; charset=utf-8", nil
	case KnowledgeGraphExportGEXF:
		return renderKnowledgeGraphGEXF(envelope, canonical), "mem-knowledge-map.gexf", "application/gexf+xml; charset=utf-8", nil
	case KnowledgeGraphExportMermaid:
		return renderKnowledgeGraphMermaid(envelope, canonical), "mem-knowledge-map.mmd", "text/plain; charset=utf-8", nil
	case KnowledgeGraphExportObsidian:
		return renderKnowledgeGraphObsidian(envelope, canonical), "mem-knowledge-map.obsidian.md", "text/markdown; charset=utf-8", nil
	default:
		return nil, "", "", fmt.Errorf("unsupported knowledge graph export format %q", format)
	}
}

func knowledgeGraphResolvedIndex(envelope knowledgeGraphExportEnvelope) map[string][]EvidenceResolution {
	result := make(map[string][]EvidenceResolution, len(envelope.Evidence))
	for _, object := range envelope.Evidence {
		result[string(object.ObjectType)+":"+object.ObjectID] = object.Items
	}
	return result
}

func renderKnowledgeGraphMarkdown(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n", markdownInline(envelope.Title))
	appendKnowledgeGraphExportSummary(&body, envelope)
	resolved := knowledgeGraphResolvedIndex(envelope)
	for _, layer := range []KnowledgeNodeLayer{KnowledgeLayerSource, KnowledgeLayerAnalytics, KnowledgeLayerWorkspace} {
		fmt.Fprintf(&body, "## %s\n\n", knowledgeGraphLayerRU(layer))
		count := 0
		for _, node := range envelope.Graph.Nodes {
			if KnowledgeNodeLayerForKind(node.Kind) != layer {
				continue
			}
			count++
			fmt.Fprintf(&body, "### %s\n\n", markdownInline(node.Label))
			fmt.Fprintf(&body, "Тип: `%s`; статус: `%s`; происхождение: `%s`; confidence: %.3f.\n\n", node.Kind, node.Status, node.Origin, node.Confidence)
			if strings.TrimSpace(node.Body) != "" {
				appendMarkdownQuote(&body, node.Body)
			}
			appendKnowledgeGraphEvidenceMarkdown(&body, resolved["node:"+node.ID])
			fmt.Fprintf(&body, "Технический ID: `%s`.\n\n", node.ID)
		}
		if count == 0 {
			body.WriteString("_Узлов этого слоя нет._\n\n")
		}
	}
	body.WriteString("## Типизированные связи\n\n")
	nodes := knowledgeGraphNodeIndex(envelope.Graph)
	for _, edge := range envelope.Graph.Edges {
		fmt.Fprintf(&body, "- **%s** → **%s** · `%s` · `%s`", markdownInline(knowledgeGraphNodeLabel(nodes, edge.From)), markdownInline(knowledgeGraphNodeLabel(nodes, edge.To)), edge.Kind, edge.Status)
		if edge.Label != "" {
			fmt.Fprintf(&body, " — %s", markdownInline(edge.Label))
		}
		body.WriteString("\n")
		for _, evidence := range resolved["edge:"+edge.ID] {
			fmt.Fprintf(&body, "  - Источник [%s]: %s\n", evidence.State, markdownInline(formatKnowledgeGraphEvidence(evidence.Anchor)))
		}
	}
	if len(envelope.Graph.Edges) == 0 {
		body.WriteString("- Связи отсутствуют.\n")
	}
	body.WriteString("\n<!-- mem-provenance-base64-raw-std-v1:\n")
	body.WriteString(base64.RawStdEncoding.EncodeToString(canonical))
	body.WriteString("\n-->\n")
	return []byte(body.String())
}

func renderKnowledgeGraphOutline(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	fmt.Fprintf(&body, "%s\n%s\n", envelope.Title, strings.Repeat("=", max(8, utf8.RuneCountInString(envelope.Title))))
	fmt.Fprintf(&body, "Узлов: %d; связей: %d\nDigest: %s\nState: %s\n\n", len(envelope.Graph.Nodes), len(envelope.Graph.Edges), envelope.Digest, envelope.StateDigest)
	resolved := knowledgeGraphResolvedIndex(envelope)
	for _, layer := range []KnowledgeNodeLayer{KnowledgeLayerSource, KnowledgeLayerAnalytics, KnowledgeLayerWorkspace} {
		fmt.Fprintf(&body, "%s\n", strings.ToUpper(knowledgeGraphLayerRU(layer)))
		for _, node := range envelope.Graph.Nodes {
			if KnowledgeNodeLayerForKind(node.Kind) != layer {
				continue
			}
			fmt.Fprintf(&body, "- %s [%s; %s; %s]\n", singleLine(node.Label), node.Kind, node.Status, node.Origin)
			if node.Body != "" {
				fmt.Fprintf(&body, "  %s\n", singleLine(node.Body))
			}
			for _, evidence := range resolved["node:"+node.ID] {
				fmt.Fprintf(&body, "  Источник [%s]: %s\n", evidence.State, formatKnowledgeGraphEvidence(evidence.Anchor))
			}
		}
		body.WriteString("\n")
	}
	body.WriteString("СВЯЗИ\n")
	nodes := knowledgeGraphNodeIndex(envelope.Graph)
	for _, edge := range envelope.Graph.Edges {
		fmt.Fprintf(&body, "- %s -> %s [%s; %s]", knowledgeGraphNodeLabel(nodes, edge.From), knowledgeGraphNodeLabel(nodes, edge.To), edge.Kind, edge.Status)
		if edge.Label != "" {
			fmt.Fprintf(&body, ": %s", singleLine(edge.Label))
		}
		body.WriteString("\n")
	}
	body.WriteString("\nMEM-PROVENANCE-BASE64-RAW-STD-V1\n")
	body.WriteString(base64.RawStdEncoding.EncodeToString(canonical))
	body.WriteString("\n")
	return []byte(body.String())
}

func renderKnowledgeGraphOPML(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&body, "<opml version=\"2.0\" xmlns:mem=\"https://knaprus-14.github.io/mem-tool/knowledge-graph/v1\"><head><title>%s</title><ownerName>mem-tool</ownerName></head><body>\n", classicMindMapExportXMLEscape(envelope.Title))
	body.WriteString("<outline text=\"Узлы\">\n")
	for _, node := range envelope.Graph.Nodes {
		evidence, _ := json.Marshal(node.Evidence)
		fmt.Fprintf(&body, "<outline text=\"%s\" type=\"%s\" mem:id=\"%s\" mem:status=\"%s\" mem:origin=\"%s\" mem:confidence=\"%.17g\" mem:body=\"%s\" mem:evidence=\"%s\"/>\n",
			classicMindMapExportXMLAttr(node.Label), classicMindMapExportXMLAttr(string(node.Kind)), classicMindMapExportXMLAttr(node.ID), node.Status, node.Origin, node.Confidence,
			base64.RawStdEncoding.EncodeToString([]byte(node.Body)), base64.RawStdEncoding.EncodeToString(evidence))
	}
	body.WriteString("</outline>\n<outline text=\"Связи\">\n")
	for _, edge := range envelope.Graph.Edges {
		evidence, _ := json.Marshal(edge.Evidence)
		fmt.Fprintf(&body, "<outline text=\"%s\" type=\"relation\" mem:id=\"%s\" mem:from=\"%s\" mem:to=\"%s\" mem:kind=\"%s\" mem:status=\"%s\" mem:origin=\"%s\" mem:confidence=\"%.17g\" mem:evidence=\"%s\"/>\n",
			classicMindMapExportXMLAttr(firstNonEmpty(edge.Label, string(edge.Kind))), classicMindMapExportXMLAttr(edge.ID), classicMindMapExportXMLAttr(edge.From), classicMindMapExportXMLAttr(edge.To), edge.Kind, edge.Status, edge.Origin, edge.Confidence, base64.RawStdEncoding.EncodeToString(evidence))
	}
	body.WriteString("</outline>\n")
	fmt.Fprintf(&body, "<outline text=\"mem provenance\" type=\"mem-provenance\" mem:encoding=\"base64-raw-std-v1\" mem:data=\"%s\"/>\n", base64.RawStdEncoding.EncodeToString(canonical))
	body.WriteString("</body></opml>\n")
	return []byte(body.String())
}

func renderKnowledgeGraphGraphML(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<graphml xmlns=\"http://graphml.graphdrawing.org/xmlns\" xmlns:mem=\"https://knaprus-14.github.io/mem-tool/knowledge-graph/v1\">\n")
	body.WriteString("<key id=\"mem-provenance\" for=\"graph\" attr.name=\"mem-provenance\" attr.type=\"string\"/>\n")
	for _, key := range [][3]string{{"label", "all", "string"}, {"kind", "all", "string"}, {"status", "all", "string"}, {"origin", "all", "string"}, {"confidence", "all", "double"}, {"body", "node", "string"}, {"evidence", "all", "string"}} {
		fmt.Fprintf(&body, "<key id=\"%s\" for=\"%s\" attr.name=\"%s\" attr.type=\"%s\"/>\n", key[0], key[1], key[0], key[2])
	}
	fmt.Fprintf(&body, "<graph id=\"mem-knowledge-map\" edgedefault=\"directed\" parse.nodes=\"%d\" parse.edges=\"%d\"><data key=\"mem-provenance\">%s</data>\n", len(envelope.Graph.Nodes), len(envelope.Graph.Edges), classicMindMapExportXMLEscape(base64.RawStdEncoding.EncodeToString(canonical)))
	for _, node := range envelope.Graph.Nodes {
		evidence, _ := json.Marshal(node.Evidence)
		fmt.Fprintf(&body, "<node id=\"%s\"><data key=\"label\">%s</data><data key=\"kind\">%s</data><data key=\"status\">%s</data><data key=\"origin\">%s</data><data key=\"confidence\">%.17g</data><data key=\"body\">%s</data><data key=\"evidence\">%s</data></node>\n",
			classicMindMapExportXMLAttr(node.ID), classicMindMapExportXMLEscape(node.Label), node.Kind, node.Status, node.Origin, node.Confidence, classicMindMapExportXMLEscape(node.Body), base64.RawStdEncoding.EncodeToString(evidence))
	}
	for _, edge := range envelope.Graph.Edges {
		evidence, _ := json.Marshal(edge.Evidence)
		fmt.Fprintf(&body, "<edge id=\"%s\" source=\"%s\" target=\"%s\"><data key=\"label\">%s</data><data key=\"kind\">%s</data><data key=\"status\">%s</data><data key=\"origin\">%s</data><data key=\"confidence\">%.17g</data><data key=\"evidence\">%s</data></edge>\n",
			classicMindMapExportXMLAttr(edge.ID), classicMindMapExportXMLAttr(edge.From), classicMindMapExportXMLAttr(edge.To), classicMindMapExportXMLEscape(edge.Label), edge.Kind, edge.Status, edge.Origin, edge.Confidence, base64.RawStdEncoding.EncodeToString(evidence))
	}
	body.WriteString("</graph></graphml>\n")
	return []byte(body.String())
}

func renderKnowledgeGraphGEXF(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<gexf xmlns=\"http://gexf.net/1.3\" xmlns:mem=\"https://knaprus-14.github.io/mem-tool/knowledge-graph/v1\" version=\"1.3\"><meta><creator>mem-tool</creator><description>")
	body.WriteString(classicMindMapExportXMLEscape(envelope.Title))
	body.WriteString("</description></meta><graph mode=\"static\" defaultedgetype=\"directed\" mem:provenance=\"")
	body.WriteString(classicMindMapExportXMLAttr(base64.RawStdEncoding.EncodeToString(canonical)))
	body.WriteString("\"><attributes class=\"node\"><attribute id=\"kind\" title=\"kind\" type=\"string\"/><attribute id=\"status\" title=\"status\" type=\"string\"/><attribute id=\"origin\" title=\"origin\" type=\"string\"/><attribute id=\"body\" title=\"body\" type=\"string\"/><attribute id=\"evidence\" title=\"evidence\" type=\"string\"/></attributes><attributes class=\"edge\"><attribute id=\"kind\" title=\"kind\" type=\"string\"/><attribute id=\"status\" title=\"status\" type=\"string\"/><attribute id=\"origin\" title=\"origin\" type=\"string\"/><attribute id=\"evidence\" title=\"evidence\" type=\"string\"/></attributes><nodes>\n")
	for _, node := range envelope.Graph.Nodes {
		evidence, _ := json.Marshal(node.Evidence)
		fmt.Fprintf(&body, "<node id=\"%s\" label=\"%s\"><attvalues><attvalue for=\"kind\" value=\"%s\"/><attvalue for=\"status\" value=\"%s\"/><attvalue for=\"origin\" value=\"%s\"/><attvalue for=\"body\" value=\"%s\"/><attvalue for=\"evidence\" value=\"%s\"/></attvalues></node>\n",
			classicMindMapExportXMLAttr(node.ID), classicMindMapExportXMLAttr(node.Label), node.Kind, node.Status, node.Origin, classicMindMapExportXMLAttr(node.Body), base64.RawStdEncoding.EncodeToString(evidence))
	}
	body.WriteString("</nodes><edges>\n")
	for _, edge := range envelope.Graph.Edges {
		evidence, _ := json.Marshal(edge.Evidence)
		fmt.Fprintf(&body, "<edge id=\"%s\" source=\"%s\" target=\"%s\" label=\"%s\" weight=\"%.17g\"><attvalues><attvalue for=\"kind\" value=\"%s\"/><attvalue for=\"status\" value=\"%s\"/><attvalue for=\"origin\" value=\"%s\"/><attvalue for=\"evidence\" value=\"%s\"/></attvalues></edge>\n",
			classicMindMapExportXMLAttr(edge.ID), classicMindMapExportXMLAttr(edge.From), classicMindMapExportXMLAttr(edge.To), classicMindMapExportXMLAttr(edge.Label), edge.Confidence, edge.Kind, edge.Status, edge.Origin, base64.RawStdEncoding.EncodeToString(evidence))
	}
	body.WriteString("</edges></graph></gexf>\n")
	return []byte(body.String())
}

func renderKnowledgeGraphMermaid(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	body.WriteString("%% mem-tool knowledge graph\n%% digest: ")
	body.WriteString(envelope.Digest)
	body.WriteString("\n%% state-digest: ")
	body.WriteString(envelope.StateDigest)
	body.WriteString("\nflowchart LR\n")
	aliases := make(map[string]string, len(envelope.Graph.Nodes))
	for index, node := range envelope.Graph.Nodes {
		alias := fmt.Sprintf("n%d", index+1)
		aliases[node.ID] = alias
		fmt.Fprintf(&body, "  %s[\"%s\"]\n", alias, knowledgeGraphMermaidText(node.Label))
	}
	for _, edge := range envelope.Graph.Edges {
		label := firstNonEmpty(edge.Label, string(edge.Kind))
		fmt.Fprintf(&body, "  %s -- \"%s\" --> %s\n", aliases[edge.From], knowledgeGraphMermaidText(label), aliases[edge.To])
	}
	body.WriteString("%% mem-provenance-base64-raw-std-v1:")
	body.WriteString(base64.RawStdEncoding.EncodeToString(canonical))
	body.WriteString("\n")
	return []byte(body.String())
}

func renderKnowledgeGraphObsidian(envelope knowledgeGraphExportEnvelope, canonical []byte) []byte {
	var body strings.Builder
	body.WriteString("---\nmem_tool: knowledge-graph\nformat_version: 1\n")
	fmt.Fprintf(&body, "title: %s\ndigest: %s\nstate_digest: %s\nnodes: %d\nedges: %d\n---\n\n", knowledgeGraphYAMLString(envelope.Title), envelope.Digest, envelope.StateDigest, len(envelope.Graph.Nodes), len(envelope.Graph.Edges))
	fmt.Fprintf(&body, "# %s\n\n## Граф\n\n```mermaid\n", markdownInline(envelope.Title))
	body.Write(renderKnowledgeGraphMermaid(envelope, canonical))
	body.WriteString("```\n\n## Узлы\n\n")
	resolved := knowledgeGraphResolvedIndex(envelope)
	for _, node := range envelope.Graph.Nodes {
		fmt.Fprintf(&body, "### %s\n\n", markdownInline(node.Label))
		fmt.Fprintf(&body, "`%s` · `%s` · `%s`\n\n", node.Kind, node.Status, node.Origin)
		if node.Body != "" {
			body.WriteString(node.Body)
			body.WriteString("\n\n")
		}
		appendKnowledgeGraphEvidenceMarkdown(&body, resolved["node:"+node.ID])
		fmt.Fprintf(&body, "^%s\n\n", knowledgeGraphObsidianBlockID(node.ID))
	}
	body.WriteString("## Связи\n\n")
	nodes := knowledgeGraphNodeIndex(envelope.Graph)
	for _, edge := range envelope.Graph.Edges {
		fmt.Fprintf(&body, "- [[#^%s|%s]] → [[#^%s|%s]] · `%s`", knowledgeGraphObsidianBlockID(edge.From), markdownInline(knowledgeGraphNodeLabel(nodes, edge.From)), knowledgeGraphObsidianBlockID(edge.To), markdownInline(knowledgeGraphNodeLabel(nodes, edge.To)), edge.Kind)
		if edge.Label != "" {
			fmt.Fprintf(&body, " — %s", markdownInline(edge.Label))
		}
		body.WriteString("\n")
	}
	body.WriteString("\n<!-- mem-provenance-base64-raw-std-v1:\n")
	body.WriteString(base64.RawStdEncoding.EncodeToString(canonical))
	body.WriteString("\n-->\n")
	return []byte(body.String())
}

func appendKnowledgeGraphExportSummary(body *strings.Builder, envelope knowledgeGraphExportEnvelope) {
	current, stale, missing := 0, 0, 0
	for _, object := range envelope.Evidence {
		for _, evidence := range object.Items {
			switch evidence.State {
			case EvidenceCurrent:
				current++
			case EvidenceStale:
				stale++
			case EvidenceMissing:
				missing++
			}
		}
	}
	fmt.Fprintf(body, "Узлов: %d; связей: %d; evidence current/stale/missing: %d/%d/%d.\n\n", len(envelope.Graph.Nodes), len(envelope.Graph.Edges), current, stale, missing)
	fmt.Fprintf(body, "Content digest: `%s`; source-state digest: `%s`.\n\n", envelope.Digest, envelope.StateDigest)
	if stale+missing > 0 {
		body.WriteString("> Внимание: экспорт сохраняет stale/missing источники видимыми; они требуют проверки и не считаются актуальным подтверждением.\n\n")
	}
}

func appendKnowledgeGraphEvidenceMarkdown(body *strings.Builder, resolutions []EvidenceResolution) {
	if len(resolutions) == 0 {
		body.WriteString("Источник: не прикреплён.\n\n")
		return
	}
	body.WriteString("Источники:\n\n")
	for _, evidence := range resolutions {
		fmt.Fprintf(body, "- [%s] %s\n", evidence.State, markdownInline(formatKnowledgeGraphEvidence(evidence.Anchor)))
		if evidence.Anchor.Excerpt != "" {
			fmt.Fprintf(body, "  > %s\n", markdownInline(singleLine(evidence.Anchor.Excerpt)))
		}
	}
	body.WriteString("\n")
}

func formatKnowledgeGraphEvidence(anchor EvidenceAnchor) string {
	parts := []string{firstNonEmpty(anchor.SourcePath, anchor.DocumentID)}
	if anchor.Page > 0 {
		parts = append(parts, fmt.Sprintf("стр. %d", anchor.Page))
	}
	parts = append(parts, fmt.Sprintf("блок %d", anchor.BlockIndex+1), fmt.Sprintf("фрагмент %d", anchor.BlockChunkIndex+1))
	return strings.Join(parts, " · ")
}

func knowledgeGraphNodeIndex(graph KnowledgeGraph) map[string]KnowledgeNode {
	result := make(map[string]KnowledgeNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		result[node.ID] = node
	}
	return result
}

func knowledgeGraphNodeLabel(nodes map[string]KnowledgeNode, id string) string {
	if node, ok := nodes[id]; ok {
		return firstNonEmpty(node.Label, id)
	}
	return id
}

func knowledgeGraphLayerRU(layer KnowledgeNodeLayer) string {
	switch layer {
	case KnowledgeLayerSource:
		return "Исходные знания"
	case KnowledgeLayerAnalytics:
		return "Аналитика"
	case KnowledgeLayerWorkspace:
		return "Рабочий слой"
	default:
		return string(layer)
	}
}

func knowledgeGraphMermaidText(value string) string {
	value = singleLine(value)
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "[", "(")
	value = strings.ReplaceAll(value, "]", ")")
	value = strings.ReplaceAll(value, "{", "(")
	value = strings.ReplaceAll(value, "}", ")")
	return value
}

func knowledgeGraphYAMLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func knowledgeGraphObsidianBlockID(id string) string {
	var body strings.Builder
	for _, r := range strings.ToLower(id) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			body.WriteRune(r)
		} else {
			body.WriteByte('-')
		}
	}
	result := strings.Trim(body.String(), "-")
	if result == "" {
		result = "node"
	}
	return "mem-" + result
}
