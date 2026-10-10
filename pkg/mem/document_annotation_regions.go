package mem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

var ErrDocumentAnnotationPreviewBusy = errors.New("просмотр PDF занят; повторите через несколько секунд")

// Coordinates refer to the complete rendered physical PDF page, independent of
// display zoom. Regions do not claim that OCR recognized the selected pixels.
type DocumentAnnotationRegionTransfer struct {
	OriginID string  `json:"origin_id,omitempty"`
	Page     int     `json:"page"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	FileHash string  `json:"file_hash"`
	Author   string  `json:"author"`
	Created  string  `json:"created"`
}

type DocumentAnnotationRegion struct {
	DocumentAnnotationRegionTransfer
	ID                 string        `json:"id"`
	AnnotationID       string        `json:"annotation_id"`
	AnnotationRevision int64         `json:"annotation_revision"`
	ImportedBy         string        `json:"imported_by,omitempty"`
	State              EvidenceState `json:"state"`
}

type DocumentAnnotationRegionCreate struct {
	Revision  int64   `json:"revision"`
	FileHash  string  `json:"file_hash"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	RequestID string  `json:"request_id"`
}
type DocumentAnnotationRegionResult struct {
	Region   DocumentAnnotationRegion `json:"region"`
	Replayed bool                     `json:"replayed"`
}
type DocumentAnnotationRegions struct {
	Items []DocumentAnnotationRegion `json:"items"`
}

const annotationRegionsSchema = `CREATE TABLE IF NOT EXISTS document_annotation_regions(
 id TEXT PRIMARY KEY,annotation_id TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS document_annotation_regions_annotation ON document_annotation_regions(annotation_id,id);
CREATE TRIGGER IF NOT EXISTS document_annotation_region_no_update BEFORE UPDATE ON document_annotation_regions BEGIN SELECT RAISE(ABORT,'region is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_region_no_delete BEFORE DELETE ON document_annotation_regions BEGIN SELECT RAISE(ABORT,'region is immutable'); END;
CREATE TABLE IF NOT EXISTS document_annotation_region_receipts(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,region_id TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS document_annotation_region_receipt_no_update BEFORE UPDATE ON document_annotation_region_receipts BEGIN SELECT RAISE(ABORT,'receipt is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_region_receipt_no_delete BEFORE DELETE ON document_annotation_region_receipts BEGIN SELECT RAISE(ABORT,'receipt is immutable'); END;`

func annotationTableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count)
	return count > 0, err
}
func annotationPDFHashTx(ctx context.Context, tx *sql.Tx, anchor EvidenceAnchor) (string, error) {
	exists, err := annotationTableExists(ctx, tx, "document_source_files")
	if err != nil || !exists {
		return "", err
	}
	var path, hash string
	err = tx.QueryRowContext(ctx, `SELECT source_path,file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, anchor.DocumentID, anchor.DocumentRevision).Scan(&path, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !coveragePathsEqual(path, anchor.SourcePath) || !isSHA256ContentHash(hash) {
		return "", nil
	}
	return hash, nil
}
func validAnnotationRegionRect(x, y, w, h float64) bool {
	for _, n := range []float64{x, y, w, h} {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return false
		}
	}
	return x >= 0 && y >= 0 && w > 0 && h > 0 && x+w <= 1 && y+h <= 1
}
func validateDocumentAnnotationRegionTransfer(r DocumentAnnotationRegionTransfer) error {
	_, err := time.Parse(time.RFC3339Nano, r.Created)
	if !validAnnotationRegionRect(r.X, r.Y, r.Width, r.Height) || r.Page < 1 || !isSHA256ContentHash(r.FileHash) || !validDocumentAnnotationActor(r.Author) || !validDocumentAnnotationText(r.OriginID, 128) || err != nil {
		return ErrDocumentAnnotationInvalid
	}
	return nil
}
func readAnnotationRegionsTx(ctx context.Context, tx *sql.Tx, id string) ([]DocumentAnnotationRegion, error) {
	items := []DocumentAnnotationRegion{}
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return items, err
	}
	exists, err := annotationTableExists(ctx, tx, "document_annotation_regions")
	if err != nil || !exists {
		return items, err
	}
	var count, size int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))),0) FROM document_annotation_regions WHERE annotation_id=?`, id).Scan(&count, &size); err != nil {
		return items, err
	}
	if count > 100 || size > 256<<10 {
		return items, ErrDocumentAnnotationLimit
	}
	hash, err := annotationPDFHashTx(ctx, tx, record.Anchor)
	if err != nil {
		return items, err
	}
	// Append order keeps visible region numbers stable when a new random ID is
	// inserted. Imported timestamps are metadata, not local insertion order.
	rows, err := tx.QueryContext(ctx, `SELECT id,payload,payload_hash FROM document_annotation_regions WHERE annotation_id=? ORDER BY rowid`, id)
	if err != nil {
		return items, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw, digest string
		var item DocumentAnnotationRegion
		if err = rows.Scan(&key, &raw, &digest); err != nil {
			return items, err
		}
		if len(raw) > 8192 || prefixedSHA256([]byte(raw)) != digest || json.Unmarshal([]byte(raw), &item) != nil || item.ID != key || item.AnnotationID != id || validateDocumentAnnotationRegionTransfer(item.DocumentAnnotationRegionTransfer) != nil {
			return items, ErrDocumentAnnotationInvalid
		}
		item.State = record.Annotation.State
		if item.State == EvidenceCurrent && (item.Page != record.Anchor.Page || item.FileHash != hash) {
			item.State = EvidenceStale
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func ListDocumentAnnotationRegionsIn(ctx context.Context, dir, id string) (DocumentAnnotationRegions, error) {
	out := DocumentAnnotationRegions{Items: []DocumentAnnotationRegion{}}
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
	out.Items, err = readAnnotationRegionsTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func insertAnnotationRegionTx(ctx context.Context, tx *sql.Tx, item DocumentAnnotationRegion) error {
	var total, one, receipts, receiptBytes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN annotation_id=? THEN 1 ELSE 0 END),0) FROM document_annotation_regions`, item.AnnotationID).Scan(&total, &one); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(request_id)+length(request_hash)+length(region_id)),0) FROM document_annotation_region_receipts`).Scan(&receipts, &receiptBytes); err != nil {
		return err
	}
	if total >= 10000 || one >= 100 || receipts >= 100000 || receiptBytes+256 > 16<<20 {
		return ErrDocumentAnnotationLimit
	}
	item.State = ""
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(raw) > 8192 {
		return ErrDocumentAnnotationLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_regions(id,annotation_id,payload,payload_hash) VALUES(?,?,?,?)`, item.ID, item.AnnotationID, string(raw), prefixedSHA256(raw))
	return err
}
func readAnnotationRegionReceiptTx(ctx context.Context, tx *sql.Tx, id, request, digest string) (DocumentAnnotationRegionResult, bool, error) {
	out := DocumentAnnotationRegionResult{}
	exists, err := annotationTableExists(ctx, tx, "document_annotation_region_receipts")
	if err != nil || !exists {
		return out, false, err
	}
	var old, regionID string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,region_id FROM document_annotation_region_receipts WHERE request_id=?`, request).Scan(&old, &regionID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if old != digest {
		return out, false, ErrDocumentAnnotationRequest
	}
	items, err := readAnnotationRegionsTx(ctx, tx, id)
	if err != nil {
		return out, false, err
	}
	for _, item := range items {
		if item.ID == regionID {
			return DocumentAnnotationRegionResult{item, true}, true, nil
		}
	}
	return out, false, ErrDocumentAnnotationNotFound
}
func CreateDocumentAnnotationRegionIn(ctx context.Context, dir, actor, id string, in DocumentAnnotationRegionCreate, local bool) (DocumentAnnotationRegionResult, error) {
	out := DocumentAnnotationRegionResult{}
	if !validDocumentAnnotationActor(actor) || !validDocumentAnnotationID(id) || in.Revision < 1 || !validDocumentAnnotationRequest(in.RequestID) || !isSHA256ContentHash(in.FileHash) || !validAnnotationRegionRect(in.X, in.Y, in.Width, in.Height) {
		return out, ErrDocumentAnnotationInvalid
	}
	digest, err := documentAnnotationDigest(actor, "region", id, in)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return out, err
	}
	defer db.Close()
	// Replay is available even after the original file disappears.
	probe, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	prior, found, err := readAnnotationRegionReceiptTx(ctx, probe, id, in.RequestID, digest)
	probe.Rollback()
	if err != nil || found {
		return prior, err
	}
	file, release, err := OpenDocumentAnnotationPDFIn(ctx, dir, id, in.Revision, local)
	if err != nil {
		return out, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	release()
	if err != nil {
		return out, err
	}
	if fmt.Sprintf("sha256:%x", hash.Sum(nil)) != in.FileHash {
		return out, ErrDocumentAnnotationStale
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, annotationRegionsSchema); err != nil {
		return out, err
	}
	if prior, found, err = readAnnotationRegionReceiptTx(ctx, tx, id, in.RequestID, digest); err != nil || found {
		return prior, err
	}
	record, state, err := documentAnnotationResolvedAnchorTx(ctx, tx, id, in.Revision)
	if err != nil {
		return out, err
	}
	if state != EvidenceCurrent || record.Annotation.Archived || record.Anchor.Page < 1 {
		return out, ErrDocumentAnnotationStale
	}
	fileHash, err := annotationPDFHashTx(ctx, tx, record.Anchor)
	if err != nil {
		return out, err
	}
	if fileHash != in.FileHash {
		return out, ErrDocumentAnnotationStale
	}
	regionID, err := newClassicMindMapID("area-")
	if err != nil {
		return out, err
	}
	region := DocumentAnnotationRegion{ID: regionID, AnnotationID: id, AnnotationRevision: in.Revision, State: EvidenceCurrent, DocumentAnnotationRegionTransfer: DocumentAnnotationRegionTransfer{Page: record.Anchor.Page, X: in.X, Y: in.Y, Width: in.Width, Height: in.Height, FileHash: in.FileHash, Author: actor, Created: time.Now().UTC().Format(time.RFC3339Nano)}}
	if err = insertAnnotationRegionTx(ctx, tx, region); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_region_receipts VALUES(?,?,?)`, in.RequestID, digest, regionID); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return DocumentAnnotationRegionResult{Region: region}, nil
}
func exportDocumentAnnotationRegionsTx(ctx context.Context, tx *sql.Tx, id string) ([]DocumentAnnotationRegionTransfer, error) {
	items, err := readAnnotationRegionsTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	out := []DocumentAnnotationRegionTransfer{}
	for _, item := range items {
		part := item.DocumentAnnotationRegionTransfer
		part.OriginID = item.ID
		out = append(out, part)
	}
	return out, nil
}
func importDocumentAnnotationRegionsTx(ctx context.Context, tx *sql.Tx, id string, items []DocumentAnnotationRegionTransfer, actor string) error {
	if len(items) == 0 {
		return nil
	}
	if len(items) > 100 || !validDocumentAnnotationActor(actor) {
		return ErrDocumentAnnotationLimit
	}
	record, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, annotationRegionsSchema); err != nil {
		return err
	}
	for _, item := range items {
		if err = validateDocumentAnnotationRegionTransfer(item); err != nil {
			return err
		}
		if item.Page != record.Anchor.Page {
			return ErrDocumentAnnotationInvalid
		}
		key, err := newClassicMindMapID("area-")
		if err != nil {
			return err
		}
		region := DocumentAnnotationRegion{DocumentAnnotationRegionTransfer: item, ID: key, AnnotationID: id, AnnotationRevision: record.Annotation.Revision, ImportedBy: actor}
		if err = insertAnnotationRegionTx(ctx, tx, region); err != nil {
			return err
		}
	}
	return nil
}

type DocumentAnnotationPageImage struct {
	PNG      []byte
	FileHash string
	Page     int
}

// Render only an independently verified private PDF snapshot. Model output and
// client-provided paths never enter the renderer.
func RenderDocumentAnnotationPageIn(ctx context.Context, dir, id string, revision int64, local bool) (DocumentAnnotationPageImage, error) {
	out := DocumentAnnotationPageImage{}
	select {
	case knowledgeSourcePreviewSlots <- struct{}{}:
		defer func() { <-knowledgeSourcePreviewSlots }()
	default:
		return out, ErrDocumentAnnotationPreviewBusy
	}
	a, err := ReadDocumentAnnotationIn(ctx, dir, id)
	if err != nil {
		return out, err
	}
	if a.Page < 1 {
		return out, ErrDocumentAnnotationInvalid
	}
	file, release, err := OpenDocumentAnnotationPDFIn(ctx, dir, id, revision, local)
	if err != nil {
		return out, err
	}
	defer release()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return out, err
	}
	out.FileHash = fmt.Sprintf("sha256:%x", hash.Sum(nil))
	out.Page = a.Page
	cfg, err := LoadConfigIn(dir)
	if errors.Is(err, os.ErrNotExist) {
		cfg = DefaultLocalConfig()
		err = nil
	}
	if err != nil {
		return out, err
	}
	out.PNG, err = ingest.RenderPDFPage(ctx, file.Name(), a.Page, ingest.ToolConfig{PDFToPPM: cfg.Ingest.PDFToPPM, MuTool: cfg.Ingest.MuTool, Python: cfg.Ingest.Python})
	if err != nil {
		return out, err
	}
	info, err := png.DecodeConfig(bytes.NewReader(out.PNG))
	if err != nil || info.Width < 1 || info.Height < 1 || !strings.HasPrefix(out.FileHash, "sha256:") {
		return out, ErrDocumentAnnotationInvalid
	}
	return out, nil
}
