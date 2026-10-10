package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type WebMapDocumentPassage struct {
	CitationID       string `json:"citation_id,omitempty"`
	DocumentRevision string `json:"document_revision,omitempty"`
	EntryID          int64  `json:"entry_id,omitempty"`
	Pin              string `json:"pin,omitempty"`
	Text             string `json:"text"`
	Marker           string `json:"marker,omitempty"`
	Match            bool   `json:"match"`
}
type WebMapDocument struct {
	DocumentID  string                  `json:"document_id,omitempty"`
	Title       string                  `json:"title"`
	Page        int                     `json:"page"`
	Excerpt     string                  `json:"excerpt"`
	State       string                  `json:"state"`
	OriginalPDF bool                    `json:"original_pdf"`
	Notice      string                  `json:"notice,omitempty"`
	Passages    []WebMapDocumentPassage `json:"passages"`
}

func webMapDocumentAnchor(ctx context.Context, tx *sql.Tx, mapID, nodeID, sourceID string, revision int64) (EvidenceAnchor, EvidenceState, error) {
	doc, err := readWebMapDocumentTx(ctx, tx, mapID)
	if err != nil {
		return EvidenceAnchor{}, "", err
	}
	if revision != doc.Map.Revision {
		return EvidenceAnchor{}, "", ErrClassicMindMapRevisionConflict
	}
	for _, n := range doc.Nodes {
		if n.ID == nodeID {
			for _, s := range n.Sources {
				if s.ID == sourceID && s.Kind == ClassicMindMapSourceEvidence && s.Evidence != nil {
					if s.EvidenceState == EvidenceCurrent {
						a := s.Evidence
						var count int
						// Identical document content can be imported at multiple paths.
						// Only the exact pinned file may supply this comparison.
						err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE document_id=? AND document_revision=? AND page=? AND block_index=? AND block_chunk_index=? AND chunk_hash=? AND COALESCE(NULLIF(source_path,''),source_file,'')=? AND instr(text,?)>0`, a.DocumentID, a.DocumentRevision, a.Page, a.BlockIndex, a.BlockChunkIndex, a.ChunkHash, a.SourcePath, a.Excerpt).Scan(&count)
						if err != nil {
							return EvidenceAnchor{}, "", err
						}
						if count == 0 {
							return *a, EvidenceStale, nil
						}
					}
					return *s.Evidence, s.EvidenceState, nil
				}
			}
		}
	}
	return EvidenceAnchor{}, "", ErrWebMapSourceConflict
}

// ReadWebMapDocumentIn resolves only an evidence attachment of this exact node.
// No paths supplied by the browser are accepted or returned.
func ReadWebMapDocumentIn(ctx context.Context, dir, mapID, nodeID, sourceID string, revision int64) (applogResult0 WebMapDocument, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebMapDocumentIn")(&applogResult1)

	out := WebMapDocument{Passages: []WebMapDocumentPassage{}}
	if !webMapExactID(mapID) || !webMapExactID(nodeID) || !webMapExactID(sourceID) || revision <= 0 {
		return out, ErrWebMapSourceInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return out, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	anchor, state, err := webMapDocumentAnchor(ctx, tx, mapID, nodeID, sourceID, revision)
	if err != nil {
		return out, err
	}
	out, err = readWebEvidenceDocumentTx(ctx, tx, anchor, state)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func readWebEvidenceDocumentTx(ctx context.Context, tx *sql.Tx, anchor EvidenceAnchor, state EvidenceState) (WebMapDocument, error) {
	out := WebMapDocument{Passages: []WebMapDocumentPassage{}, DocumentID: LibraryDocumentID(anchor.SourcePath, anchor.DocumentID)}
	var err error
	out.Title = filepath.Base(anchor.SourcePath)
	out.Page = anchor.Page
	out.Excerpt = anchor.Excerpt
	out.State = string(state)
	if state != EvidenceCurrent {
		out.Notice = "Источник изменён или удалён. Показан сохранённый фрагмент, использованный при создании узла; актуальная редакция не подменяет его."
		return out, nil
	}
	where := `document_id=? AND document_revision=? AND page=? AND COALESCE(NULLIF(source_path,''),source_file,'')=?`
	args := []any{anchor.DocumentID, anchor.DocumentRevision, anchor.Page, anchor.SourcePath}
	if anchor.Page == 0 {
		where += ` AND block_index BETWEEN ? AND ?`
		args = append(args, max(0, anchor.BlockIndex-2), anchor.BlockIndex+2)
		out.Notice = "У источника нет номера страницы. Показан связанный фрагмент и соседние блоки документа."
	}
	var count, size, storedSize int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))),0),COALESCE(SUM(`+annotationReaderEntrySizeSQL()+`),0) FROM entries WHERE `+where, args...).Scan(&count, &size, &storedSize); err != nil {
		return out, err
	}
	if count <= 500 && size <= 2<<20 && storedSize <= 16<<20 {
		entries, _, e := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE `+where+` ORDER BY block_index,block_chunk_index,id`, args...)
		if e != nil {
			return out, e
		}
		for _, entry := range entries {
			hit, e := queryHit(entry, 100000, DefaultAnswerLowConfidence)
			if e != nil {
				return out, e
			}
			p := WebMapDocumentPassage{EntryID: entry.ID, Pin: hit.Pin, Text: entry.Text, Marker: entry.BlockMarker, CitationID: hit.CitationID, DocumentRevision: entry.DocumentRevision}
			p.Match = entry.BlockIndex == anchor.BlockIndex && entry.BlockChunkIndex == anchor.BlockChunkIndex && entry.ChunkHash == anchor.ChunkHash
			out.Passages = append(out.Passages, p)
		}
	} else {
		out.Notice = "Страница слишком велика для текстового сравнения. Показан сохранённый фрагмент; откройте оригинал для полного просмотра."
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_source_files'`).Scan(&exists); err != nil {
		return out, err
	}
	if exists > 0 && strings.EqualFold(filepath.Ext(anchor.SourcePath), ".pdf") {
		var path, hash string
		err = tx.QueryRowContext(ctx, `SELECT source_path,file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, anchor.DocumentID, anchor.DocumentRevision).Scan(&path, &hash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		out.OriginalPDF = err == nil && coveragePathsEqual(path, anchor.SourcePath) && isSHA256ContentHash(hash)
	}
	return out, nil
}

// OpenWebMapDocumentPDFIn returns a private verified snapshot. Hosted mode opens
// through os.Root, preventing traversal/symlink escapes from the owned workspace.
func OpenWebMapDocumentPDFIn(ctx context.Context, dir, mapID, nodeID, sourceID string, revision int64, local bool) (applogResult0 *os.File, applogResult1 func(), applogResult2 error) {
	defer applog.Track("pkg.mem.OpenWebMapDocumentPDFIn")(&applogResult2)

	fail := func(e error) (*os.File, func(), error) { return nil, nil, e }
	if !webMapExactID(mapID) || !webMapExactID(nodeID) || !webMapExactID(sourceID) || revision <= 0 {
		return fail(ErrWebMapSourceInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	anchor, state, err := webMapDocumentAnchor(ctx, tx, mapID, nodeID, sourceID, revision)
	if err != nil {
		return fail(err)
	}
	if state != EvidenceCurrent || !strings.EqualFold(filepath.Ext(anchor.SourcePath), ".pdf") {
		return fail(ErrWebMapSourceConflict)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return openWebEvidencePDF(ctx, db, dir, anchor, local)
}

func openWebEvidencePDF(ctx context.Context, db *sql.DB, dir string, anchor EvidenceAnchor, local bool) (*os.File, func(), error) {
	fail := func(e error) (*os.File, func(), error) { return nil, nil, e }
	file, err := openLocatedSource(ctx, db, dir, anchor, local)
	if err != nil {
		return fail(ErrSourceFileUnverified)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return fail(ErrSourceFileUnverified)
	}
	snapshot, err := (&Store{db: db}).verifiedSourceFile(ctx, anchor, file)
	if err != nil {
		return fail(err)
	}
	header := make([]byte, 5)
	_, err = io.ReadFull(snapshot, header)
	if err != nil || string(header) != "%PDF-" {
		closeSourceSnapshot(snapshot)
		return fail(ErrSourceFileChanged)
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		closeSourceSnapshot(snapshot)
		return fail(err)
	}
	return snapshot, func() { closeSourceSnapshot(snapshot) }, nil
}
