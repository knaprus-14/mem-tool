package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrWebMapSourceInvalid = errors.New("выберите фрагмент базы или укажите ссылку http/https и название до 512 символов")
var ErrWebMapSourceConflict = errors.New("источник уже прикреплён, изменился или больше недоступен; обновите список")

type WebMapSource struct {
	Attachment  bool                     `json:"attachment,omitempty"`
	GraphNodeID string                   `json:"graph_node_id,omitempty"`
	ID          string                   `json:"id"`
	Kind        ClassicMindMapSourceKind `json:"kind"`
	Title       string                   `json:"title"`
	URL         string                   `json:"url,omitempty"`
	Excerpt     string                   `json:"excerpt,omitempty"`
	CitationID  string                   `json:"citation_id,omitempty"`
	Page        int                      `json:"page,omitempty"`
	State       string                   `json:"state"`
}
type WebMapSourcePage struct {
	Revision int64          `json:"revision"`
	Items    []WebMapSource `json:"items"`
}
type WebMapSourceInput struct {
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	EntryID   int64  `json:"entry_id,omitempty"`
	Pin       string `json:"pin,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	Position  int    `json:"position"`
}

func ActWebMapSourceIn(ctx context.Context, dir, actor, mapID, nodeID, action string, in WebMapSourceInput) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.ActWebMapSourceIn")(&applogResult1)

	if !webMapExactID(nodeID) {
		return WebMapNodeEdited{}, ErrWebMapSourceInvalid
	}
	in.URL = strings.TrimSpace(in.URL)
	in.Title = strings.TrimSpace(in.Title)
	var normalized ClassicMindMapSource
	switch action {
	case "add":
		if in.SourceID != "" || utf8.RuneCountInString(in.Title) > 512 || !utf8.ValidString(in.Title) {
			return WebMapNodeEdited{}, ErrWebMapSourceInvalid
		}
		if in.EntryID > 0 {
			if !validSHA256Digest(in.Pin) || in.URL != "" || in.Title != "" {
				return WebMapNodeEdited{}, ErrWebMapSourceInvalid
			}
		} else {
			if in.EntryID != 0 || in.Pin != "" || len(in.URL) > 8192 {
				return WebMapNodeEdited{}, ErrWebMapSourceInvalid
			}
			parsed, err := validateClassicMindMapHTTPURL(in.URL)
			if err != nil {
				return WebMapNodeEdited{}, ErrWebMapSourceInvalid
			}
			title := in.Title
			if title == "" {
				title = parsed.Host
			}
			normalized = ClassicMindMapSource{Kind: ClassicMindMapSourceURL, Title: title, URL: in.URL}
		}
	case "remove", "move":
		if !webMapExactID(in.SourceID) || in.EntryID != 0 || in.URL != "" || in.Pin != "" || in.Title != "" || in.Position < -1 || in.Position > 2000 {
			return WebMapNodeEdited{}, ErrWebMapSourceInvalid
		}
	default:
		return WebMapNodeEdited{}, ErrWebMapSourceInvalid
	}
	historyAction := map[string]string{"add": "attach_source:url", "remove": "detach_source", "move": "move_source"}[action]
	if action == "add" && in.EntryID > 0 {
		historyAction = "attach_evidence"
	}
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, struct {
		Node, Action string
		Input        WebMapSourceInput
	}{nodeID, action, in}, historyAction, func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		n, err := exactWebMapNode(tx, mapID, nodeID)
		if err != nil {
			return "", err
		}
		if n.Locked {
			return "", ErrClassicMindMapLocked
		}
		if action != "add" {
			var count int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources WHERE id=? AND node_id=? AND map_id=? AND deleted_at=''`, in.SourceID, nodeID, mapID).Scan(&count); err != nil {
				return "", err
			}
			if count != 1 {
				return "", ErrWebMapSourceConflict
			}
			if action == "remove" {
				return detachClassicMindMapSourceTx(tx, item, nodeID, in.SourceID)
			}
			return moveClassicMindMapSourceTx(tx, item, nodeID, in.SourceID, in.Position)
		}
		var attached ClassicMindMapSource
		if in.EntryID > 0 {
			if _, err = readQueryEvidence(ctx, tx, in.EntryID, in.Pin); err != nil {
				return "", err
			}
			entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, in.EntryID)
			if err != nil {
				return "", err
			}
			if len(entries) != 1 {
				return "", ErrWebMapSourceConflict
			}
			anchor, err := EvidenceAnchorForEntry(entries[0], "")
			if err != nil {
				return "", ErrWebMapSourceInvalid
			}
			var count int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' AND kind='evidence' AND citation_id=? AND evidence_hash=?`, mapID, nodeID, anchor.CitationID, anchor.EvidenceHash).Scan(&count); err != nil {
				return "", err
			}
			if count > 0 {
				return "", ErrWebMapSourceConflict
			}
			if err = boundWebMapEvidence(tx, anchor); err != nil {
				return "", err
			}
			return attachClassicMindMapEvidenceTx(tx, item, nodeID, anchor, &attached)
		}
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM mind_map_node_sources WHERE map_id=? AND node_id=? AND deleted_at='' AND kind='url' AND url=?`, mapID, nodeID, normalized.URL).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return "", ErrWebMapSourceConflict
		}
		return attachClassicMindMapSourceTx(tx, item, nodeID, normalized, &attached)
	})
}

func readWebMapDocument(ctx context.Context, dir, id string) (ClassicMindMapDocument, error) {
	if !webMapExactID(id) {
		return ClassicMindMapDocument{}, ErrWebMapInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClassicMindMapDocument{}, err
	}
	defer tx.Rollback()
	doc, err := readWebMapDocumentTx(ctx, tx, id)
	if err != nil {
		return doc, err
	}
	return doc, tx.Commit()
}

func readWebMapDocumentTx(ctx context.Context, tx *sql.Tx, id string) (ClassicMindMapDocument, error) {
	if _, err := webMapRevision(ctx, tx, id); err != nil {
		return ClassicMindMapDocument{}, err
	}
	if err := validateWebMapMutationBounds(ctx, tx, id); err != nil {
		return ClassicMindMapDocument{}, err
	}
	doc, err := loadClassicMindMapDocument(tx, id)
	if err != nil {
		return doc, err
	}
	doc, _, err = finalizeClassicMindMapDocument(doc)
	if err != nil {
		return doc, err
	}
	for i := range doc.Nodes {
		for j := range doc.Nodes[i].Sources {
			source := &doc.Nodes[i].Sources[j]
			switch source.Kind {
			case ClassicMindMapSourceEvidence:
				source.EvidenceState = EvidenceMissing
				if source.Evidence != nil {
					if err = boundWebMapEvidence(tx, *source.Evidence); err != nil {
						return doc, err
					}
					resolution, e := resolveClassicMindMapEvidenceWithQuery(tx, *source.Evidence)
					if e == nil {
						source.EvidenceState = resolution.State
					}
				}
			case ClassicMindMapSourceURL:
				source.EvidenceState = EvidenceMissing
				if _, e := validateClassicMindMapHTTPURL(source.URL); e == nil {
					source.EvidenceState = EvidenceCurrent
				}
			case ClassicMindMapSourceKnowledgeNode:
				var count int
				err = tx.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, source.KnowledgeNodeID).Scan(&count)
				if err != nil {
					return doc, err
				}
				source.EvidenceState = EvidenceMissing
				if count == 1 {
					source.EvidenceState = EvidenceCurrent
				}
			default:
				source.EvidenceState = EvidenceMissing // No filesystem access from a stored locator.
			}
		}
	}
	doc.StateDigest = classicMindMapResolvedStateDigest(doc)
	return doc, nil
}

func boundWebMapEvidence(tx *sql.Tx, a EvidenceAnchor) error {
	var count, size int64
	err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=?`, a.DocumentID, a.Page, a.BlockIndex, a.BlockChunkIndex).Scan(&count, &size)
	if err != nil {
		return err
	}
	if count > 10000 || size > 16<<20 {
		return ErrWebMapEditLimit
	}
	return nil
}

func ListWebMapSourcesIn(ctx context.Context, dir, mapID, nodeID string) (applogResult0 WebMapSourcePage, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapSourcesIn")(&applogResult1)

	page := WebMapSourcePage{Items: []WebMapSource{}}
	doc, err := readWebMapDocument(ctx, dir, mapID)
	if err != nil {
		return page, err
	}
	page.Revision = doc.Map.Revision
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		for _, s := range n.Sources {
			item := WebMapSource{ID: s.ID, Kind: s.Kind, Title: s.Title, State: string(s.EvidenceState)}
			if s.Kind == ClassicMindMapSourceURL {
				if _, e := validateClassicMindMapHTTPURL(s.URL); e == nil {
					item.URL = s.URL
				}
			}
			if s.Kind == ClassicMindMapSourceExternalFile {
				item.State = "local_file"
				item.Title = "Локальный файл"
				if strings.HasPrefix(s.Locator, attachmentPrefix) {
					item.Attachment = true
					item.Title = s.Title
					item.State = "attachment"
				}
			}
			if s.Kind == ClassicMindMapSourceKnowledgeNode {
				item.GraphNodeID = s.KnowledgeNodeID
			}
			if s.Evidence != nil {
				item.Excerpt = s.Evidence.Excerpt
				item.CitationID = s.Evidence.CitationID
				item.Page = s.Evidence.Page
			}
			page.Items = append(page.Items, item)
		}
		return page, nil
	}
	return page, ErrClassicMindMapNodeNotFound
}

type WebMapCandidates struct {
	Items      []QueryHit `json:"items"`
	NextOffset int        `json:"next_offset"`
}

// A bounded, model-free picker of document fragments. It carries a content pin
// so importing a new document revision cannot attach an unseen replacement.
func ListWebMapCandidatesIn(ctx context.Context, dir string, offset int) (applogResult0 WebMapCandidates, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapCandidatesIn")(&applogResult1)

	page := WebMapCandidates{Items: []QueryHit{}, NextOffset: -1}
	if offset < 0 || offset > 1000000 {
		return page, ErrWebMapSourceInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return page, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	const where = ` FROM entries WHERE document_id!='' AND document_revision!='' AND chunk_hash!='' AND source_path!='' ORDER BY id DESC LIMIT 31 OFFSET ?`
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(warnings AS BLOB))),0) FROM (SELECT text,embedding,tags,warnings`+where+`)`, offset).Scan(&size)
	if err != nil {
		return page, err
	}
	if size > 32<<20 {
		return page, ErrQueryLimit
	}
	entries, _, err := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+where, offset)
	if err != nil {
		return page, err
	}
	if len(entries) > 30 {
		page.NextOffset = offset + 30
		entries = entries[:30]
	}
	for _, entry := range entries {
		hit, e := queryHit(entry, 2048, DefaultAnswerLowConfidence)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, hit)
	}
	return page, tx.Commit()
}
