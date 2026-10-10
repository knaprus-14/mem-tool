package mem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// The web resolver follows versioned document anchors only. Graph text is a
// dependency pin, never evidence; URL/file/attachment locators are not opened.
// All queries share the caller's snapshot and have aggregate allocation limits.
const maxWebMapResolvedEvidence = 2000

type webMapEvidencePin struct {
	Digest  string
	Current bool
}

type webMapResolvedEvidence struct {
	Evidence    []GroundedEvidence
	Digest      string
	Nodes       map[string]webMapEvidencePin
	Unavailable int
	Other       int
}

type webMapResolvedAnchor struct {
	Resolution EvidenceResolution
	Evidence   *GroundedEvidence
	Quality    *webMapEvidenceQuality
}

// Pin the stored quality metadata as well as the derived model warnings. A
// method change, or a stored warning equal to an auto-generated OCR warning,
// must invalidate a preview even when its excerpt and derived warnings match.
type webMapEvidenceQuality struct {
	ExtractionMethod string
	OCRConfidence    float64
	Warnings         []string
}

type webMapGraphEvidence struct {
	Digest                                         string `json:"-"`
	ID, Kind, Label, Body, Status, Origin, Updated string
	Confidence                                     float64
	Exists                                         bool
	Anchors                                        []webMapResolvedAnchor
}

type webMapEvidenceResolver struct {
	ctx         context.Context
	tx          *sql.Tx
	bytes       int64
	entryCount  int64
	anchorCount int64
	entries     map[evidenceResolutionCoordinate][]Entry
	anchors     map[string]webMapResolvedAnchor
	graph       map[string]webMapGraphEvidence
}

func (r *webMapEvidenceResolver) reserve(size int64) error {
	r.bytes += size
	if size < 0 || r.bytes > 16<<20 {
		return ErrWebMapEditLimit
	}
	return r.ctx.Err()
}

func (r *webMapEvidenceResolver) resolve(a EvidenceAnchor) (webMapResolvedAnchor, error) {
	if err := r.ctx.Err(); err != nil {
		return webMapResolvedAnchor{}, err
	}
	key, err := classicMindMapAIDigest(a)
	if err != nil {
		return webMapResolvedAnchor{}, err
	}
	if cached, ok := r.anchors[key]; ok {
		return cached, nil
	}
	if len(r.anchors) >= maxWebMapResolvedEvidence {
		return webMapResolvedAnchor{}, ErrWebMapEditLimit
	}
	resolved := webMapResolvedAnchor{Resolution: EvidenceResolution{Anchor: a, State: EvidenceMissing}}
	if validateEvidenceAnchor(a) != nil {
		r.anchors[key] = resolved
		return resolved, nil
	}
	coordinate := evidenceResolutionCoordinate{a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex}
	entries, ok := r.entries[coordinate]
	if !ok {
		var count, size int64
		err = r.tx.QueryRowContext(r.ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(CAST(source_path AS BLOB))+length(CAST(source_file AS BLOB))+length(CAST(document_id AS BLOB))+length(CAST(document_revision AS BLOB))+length(CAST(chunk_hash AS BLOB))+length(CAST(extraction_method AS BLOB))+length(CAST(warnings AS BLOB))+8),0) FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=?`, a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex).Scan(&count, &size)
		if err != nil {
			return resolved, err
		}
		r.entryCount += count
		if r.entryCount > 10000 {
			return resolved, ErrWebMapEditLimit
		}
		if err = r.reserve(size); err != nil {
			return resolved, err
		}
		rows, err := r.tx.QueryContext(r.ctx, `SELECT id,text,document_id,document_revision,chunk_hash,source_file,source_path,page,block_index,block_chunk_index,block_total_chunks,chunk_index,extraction_method,ocr_confidence,warnings FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=? ORDER BY id`, a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex)
		if err != nil {
			return resolved, err
		}
		defer rows.Close()
		for rows.Next() {
			var e Entry
			var warningsJSON string
			if err = rows.Scan(&e.ID, &e.Text, &e.DocumentID, &e.DocumentRevision, &e.ChunkHash, &e.SourceFile, &e.SourcePath, &e.Page, &e.BlockIndex, &e.BlockChunkIndex, &e.BlockTotalChunks, &e.ChunkIndex, &e.ExtractionMethod, &e.OCRConfidence, &warningsJSON); err != nil {
				return resolved, err
			}
			if err = json.Unmarshal([]byte(warningsJSON), &e.Warnings); err != nil {
				return resolved, fmt.Errorf("запись #%d: повреждены warnings: %w", e.ID, err)
			}
			entries = append(entries, e)
		}
		if err = rows.Err(); err != nil {
			return resolved, err
		}
		r.entries[coordinate] = entries
	}
	resolved.Resolution = resolveEvidenceAnchorFromEntries(a, entries)
	if resolved.Resolution.State == EvidenceCurrent {
		for _, e := range entries {
			if candidate := resolveEvidenceAnchorFromEntries(a, []Entry{e}); candidate.State == EvidenceCurrent {
				g := groundedEvidenceForEntry(e, a.Excerpt, DefaultAnswerLowConfidence)
				g.CitationID, g.EvidenceHash = a.CitationID, a.EvidenceHash
				resolved.Evidence = &g
				resolved.Quality = &webMapEvidenceQuality{ExtractionMethod: e.ExtractionMethod, OCRConfidence: e.OCRConfidence, Warnings: append([]string{}, e.Warnings...)}
				break
			}
		}
	}
	r.anchors[key] = resolved
	return resolved, nil
}

func (r *webMapEvidenceResolver) knowledge(id string) (webMapGraphEvidence, error) {
	if cached, ok := r.graph[id]; ok {
		return cached, nil
	}
	g := webMapGraphEvidence{ID: id}
	var size int64
	err := r.tx.QueryRowContext(r.ctx, `SELECT length(CAST(kind AS BLOB))+length(CAST(label AS BLOB))+length(CAST(body AS BLOB))+length(CAST(status AS BLOB))+length(CAST(origin AS BLOB))+length(CAST(updated AS BLOB)) FROM knowledge_nodes WHERE id=?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return r.rememberKnowledge(g)
	}
	if err != nil {
		return g, err
	}
	if err = r.reserve(size); err != nil {
		return g, err
	}
	err = r.tx.QueryRowContext(r.ctx, `SELECT kind,label,body,status,origin,confidence,updated FROM knowledge_nodes WHERE id=?`, id).Scan(&g.Kind, &g.Label, &g.Body, &g.Status, &g.Origin, &g.Confidence, &g.Updated)
	if err != nil {
		return g, err
	}
	g.Exists = true
	var count int64
	err = r.tx.QueryRowContext(r.ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(citation_id AS BLOB))+length(CAST(document_id AS BLOB))+length(CAST(document_revision AS BLOB))+length(CAST(chunk_hash AS BLOB))+length(CAST(evidence_hash AS BLOB))+length(CAST(source_path AS BLOB))+length(CAST(excerpt AS BLOB))),0) FROM knowledge_node_evidence WHERE node_id=?`, id).Scan(&count, &size)
	if err != nil {
		return g, err
	}
	r.anchorCount += count
	if r.anchorCount > maxWebMapResolvedEvidence {
		return g, ErrWebMapEditLimit
	}
	if err = r.reserve(size); err != nil {
		return g, err
	}
	anchors, err := loadKnowledgeEvidence(contextEntryReader{r.ctx, r.tx}, "knowledge_node_evidence", "node_id", id)
	if err != nil {
		return g, err
	}
	for _, a := range anchors {
		resolved, err := r.resolve(a)
		if err != nil {
			return g, err
		}
		g.Anchors = append(g.Anchors, resolved)
	}
	return r.rememberKnowledge(g)
}

func (r *webMapEvidenceResolver) rememberKnowledge(g webMapGraphEvidence) (webMapGraphEvidence, error) {
	var err error
	g.Digest, err = classicMindMapAIDigest(g)
	if err != nil {
		return g, err
	}
	r.graph[g.ID] = g
	return g, r.ctx.Err()
}

func resolveWebMapBranchEvidenceTx(ctx context.Context, tx *sql.Tx, nodes []ClassicMindMapNode, redact bool) (webMapResolvedEvidence, error) {
	result := webMapResolvedEvidence{Evidence: []GroundedEvidence{}, Nodes: map[string]webMapEvidencePin{}}
	r := webMapEvidenceResolver{ctx: ctx, tx: tx, entries: map[evidenceResolutionCoordinate][]Entry{}, anchors: map[string]webMapResolvedAnchor{}, graph: map[string]webMapGraphEvidence{}}
	seen := map[string]bool{}
	for i := range nodes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pins := []string{}
		current := true
		for j := range nodes[i].Sources {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			source := &nodes[i].Sources[j]
			var anchors []webMapResolvedAnchor
			var dependency any
			switch source.Kind {
			case ClassicMindMapSourceEvidence:
				if source.Evidence != nil {
					a, err := r.resolve(*source.Evidence)
					if err != nil {
						return result, err
					}
					anchors = []webMapResolvedAnchor{a}
				}
				dependency = anchors
			case ClassicMindMapSourceKnowledgeNode:
				g, err := r.knowledge(source.KnowledgeNodeID)
				if err != nil {
					return result, err
				}
				anchors, dependency = g.Anchors, g.Digest
			default:
				result.Other++
			}
			if source.Kind == ClassicMindMapSourceEvidence || source.Kind == ClassicMindMapSourceKnowledgeNode {
				source.EvidenceState = EvidenceCurrent
				if len(anchors) == 0 {
					source.EvidenceState = EvidenceMissing
				}
				for _, a := range anchors {
					if a.Resolution.State != EvidenceCurrent {
						// Missing takes precedence over stale for a mixed graph source.
						if source.EvidenceState != EvidenceMissing {
							source.EvidenceState = a.Resolution.State
						}
						continue
					}
					if a.Evidence != nil {
						g := *a.Evidence
						// Different excerpts of one citation remain distinct evidence.
						key := g.CitationID + "\x00" + g.EvidenceHash
						if !seen[key] {
							seen[key] = true
							if redact {
								name := sourceName(Entry{SourcePath: g.SourcePath})
								g.SourcePath = name
								_, g.CitationLabel = CitationForEntry(Entry{DocumentID: g.DocumentID, SourcePath: name, Page: g.Page, BlockIndex: g.BlockIndex, BlockChunkIndex: g.BlockChunkIndex, BlockTotalChunks: g.BlockTotalChunks})
							}
							result.Evidence = append(result.Evidence, g)
						}
					}
				}
				if source.EvidenceState != EvidenceCurrent {
					result.Unavailable++
					current = false
				}
			}
			pin, err := classicMindMapAIDigest(struct {
				Source     ClassicMindMapSource
				Dependency any
			}{*source, dependency})
			if err != nil {
				return result, err
			}
			pins = append(pins, pin)
		}
		digest, err := classicMindMapAIDigest(pins)
		if err != nil {
			return result, err
		}
		result.Nodes[nodes[i].ID] = webMapEvidencePin{Digest: digest, Current: current}
	}
	sort.Slice(result.Evidence, func(i, j int) bool {
		a, b := result.Evidence[i], result.Evidence[j]
		if a.CitationID != b.CitationID {
			return a.CitationID < b.CitationID
		}
		return a.EvidenceHash < b.EvidenceHash
	})
	for i := range result.Evidence {
		result.Evidence[i].EvidenceRef = fmt.Sprintf("E%d", i+1)
	}
	var err error
	result.Digest, err = classicMindMapAIDigest(result.Nodes)
	return result, err
}
