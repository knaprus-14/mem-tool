package mem

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

const (
	MaxDocumentAnnotationTransferBytes = 8 << 20
	MaxDocumentAnnotationTransferCount = 200
	documentAnnotationTransferFormat   = "mem-tool-document-annotations"
	// This reserved locator is never resolved or automatically rebound. It
	// preserves an imported quotation when no unique local source can prove it.
	documentAnnotationUnboundPrefix = "annotation-unbound:"
)

var (
	ErrDocumentAnnotationTransferInvalid = errors.New("ожидается JSON аннотаций mem-tool версии 1: до 200 аннотаций и 8 МиБ, без файловых путей")
	ErrDocumentAnnotationTransferPreview = errors.New("содержимое или источники изменились; повторите предварительный просмотр аннотаций")
)

type DocumentAnnotationExportRequest struct {
	IDs    []string `json:"ids"`
	Format string   `json:"format"`
}
type DocumentAnnotationExportArtifact struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"-"`
	Count     int    `json:"count"`
	Digest    string `json:"digest"`
}
type DocumentAnnotationImportPreviewInput struct {
	Content string `json:"content"`
}
type DocumentAnnotationImportInput struct {
	Content   string `json:"content"`
	Digest    string `json:"digest"`
	RequestID string `json:"request_id"`
}
type DocumentAnnotationImportPreviewItem struct {
	OriginID   string        `json:"origin_id"`
	SourceName string        `json:"source_name"`
	Quote      string        `json:"quote"`
	Note       string        `json:"note"`
	State      EvidenceState `json:"state"`
	Archived   bool          `json:"archived"`
	Reason     string        `json:"reason"`
}
type DocumentAnnotationImportPreview struct {
	Digest      string                                `json:"digest"`
	Count       int                                   `json:"count"`
	Current     int                                   `json:"current"`
	Stale       int                                   `json:"stale"`
	Missing     int                                   `json:"missing"`
	Archived    int                                   `json:"archived"`
	RegionCount int                                   `json:"region_count"`
	Items       []DocumentAnnotationImportPreviewItem `json:"items"`
	Warnings    []string                              `json:"warnings"`
}
type DocumentAnnotationImportItem struct {
	OriginID string `json:"origin_id"`
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
type DocumentAnnotationImportResult struct {
	Items    []DocumentAnnotationImportItem `json:"items"`
	Imported int                            `json:"imported"`
	Current  int                            `json:"current"`
	Stale    int                            `json:"stale"`
	Missing  int                            `json:"missing"`
	Replayed bool                           `json:"replayed"`
}

// Deliberately does not embed EvidenceAnchor: SourcePath must never enter the
// portable schema. DocumentID here is the logical ingestion identity, not the
// public LibraryDocumentID (which can depend on a local locator).
type documentAnnotationTransferItem struct {
	OriginID         string                             `json:"origin_id"`
	OriginRevision   int64                              `json:"origin_revision"`
	DocumentID       string                             `json:"document_id"`
	DocumentRevision string                             `json:"document_revision"`
	CitationID       string                             `json:"citation_id"`
	ChunkHash        string                             `json:"chunk_hash"`
	EvidenceHash     string                             `json:"evidence_hash"`
	SourceName       string                             `json:"source_name"`
	Page             int                                `json:"page"`
	BlockIndex       int                                `json:"block_index"`
	BlockChunkIndex  int                                `json:"block_chunk_index"`
	Quote            string                             `json:"quote"`
	Start            int                                `json:"start"`
	End              int                                `json:"end"`
	Prefix           string                             `json:"prefix"`
	Suffix           string                             `json:"suffix"`
	Note             string                             `json:"note"`
	Color            string                             `json:"color"`
	Archived         bool                               `json:"archived"`
	Author           string                             `json:"author"`
	Created          string                             `json:"created"`
	Updated          string                             `json:"updated"`
	Regions          []DocumentAnnotationRegionTransfer `json:"regions"`
}
type documentAnnotationTransferPack struct {
	Format      string                           `json:"format"`
	Version     int                              `json:"version"`
	Annotations []documentAnnotationTransferItem `json:"annotations"`
	Digest      string                           `json:"digest"`
}

func validAnnotationTransferName(name string) bool {
	if name == "" || name == "." || !validDocumentAnnotationText(name, 256) || strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}
func validAnnotationTransferDocumentID(id string) bool {
	if id == "" || len(id) > 128 || strings.Contains(id, "..") {
		return false
	}
	if strings.HasPrefix(id, "web:") {
		return len(id) == 36 && strings.Trim(id[4:], "0123456789abcdef") == ""
	}
	if strings.HasPrefix(id, "sha256:") {
		return isSHA256ContentHash(id)
	}
	return strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") == ""
}
func (item documentAnnotationTransferItem) anchor(path string) EvidenceAnchor {
	return EvidenceAnchor{CitationID: item.CitationID, DocumentID: item.DocumentID, DocumentRevision: item.DocumentRevision, ChunkHash: item.ChunkHash, EvidenceHash: item.EvidenceHash, SourcePath: path, Page: item.Page, BlockIndex: item.BlockIndex, BlockChunkIndex: item.BlockChunkIndex, Excerpt: item.Quote}
}
func validateAnnotationTransferItem(item documentAnnotationTransferItem) error {
	_, ce := time.Parse(time.RFC3339Nano, item.Created)
	_, ue := time.Parse(time.RFC3339Nano, item.Updated)
	if !validDocumentAnnotationID(item.OriginID) || item.OriginRevision < 1 || !validAnnotationTransferDocumentID(item.DocumentID) || !isSHA256ContentHash(item.DocumentRevision) || !validAnnotationTransferName(item.SourceName) || item.Start < 0 || item.End <= item.Start || item.End > maxDocumentAnnotationChunkBytes || !validDocumentAnnotationText(item.Quote, MaxDocumentAnnotationRunes) || strings.TrimSpace(item.Quote) == "" || item.End-item.Start != utf8.RuneCountInString(item.Quote) || !validDocumentAnnotationText(item.Note, MaxDocumentAnnotationRunes) || !validDocumentAnnotationText(item.Prefix, documentAnnotationContextRunes) || !validDocumentAnnotationText(item.Suffix, documentAnnotationContextRunes) || !validDocumentAnnotationColor(item.Color) || !validDocumentAnnotationActor(item.Author) || ce != nil || ue != nil || len(item.Regions) > 100 || validateEvidenceAnchor(item.anchor(documentAnnotationUnboundPrefix+item.SourceName)) != nil {
		return ErrDocumentAnnotationTransferInvalid
	}
	seen := map[string]bool{}
	for _, area := range item.Regions {
		if validateDocumentAnnotationRegionTransfer(area) != nil || area.Page != item.Page || !strings.HasPrefix(area.OriginID, "area-") || len(area.OriginID) != 37 || strings.Trim(area.OriginID[5:], "0123456789abcdef") != "" || seen[area.OriginID] {
			return ErrDocumentAnnotationTransferInvalid
		}
		seen[area.OriginID] = true
	}
	return nil
}

func canonicalAnnotationTransferPack(pack documentAnnotationTransferPack) (documentAnnotationTransferPack, []byte, error) {
	if pack.Format != documentAnnotationTransferFormat || pack.Version != 1 || len(pack.Annotations) == 0 || len(pack.Annotations) > MaxDocumentAnnotationTransferCount {
		return pack, nil, ErrDocumentAnnotationTransferInvalid
	}
	seen := map[string]bool{}
	regions := 0
	for i := range pack.Annotations {
		item := &pack.Annotations[i]
		if seen[item.OriginID] || validateAnnotationTransferItem(*item) != nil {
			return pack, nil, ErrDocumentAnnotationTransferInvalid
		}
		seen[item.OriginID] = true
		regions += len(item.Regions)
		if regions > 10000 {
			return pack, nil, ErrDocumentAnnotationTransferInvalid
		}
		if item.Regions == nil {
			item.Regions = []DocumentAnnotationRegionTransfer{}
		}
		sort.Slice(item.Regions, func(a, b int) bool { return item.Regions[a].OriginID < item.Regions[b].OriginID })
	}
	sort.Slice(pack.Annotations, func(i, j int) bool { return pack.Annotations[i].OriginID < pack.Annotations[j].OriginID })
	pack.Digest = ""
	data, err := json.Marshal(pack)
	if err != nil {
		return pack, nil, err
	}
	pack.Digest = prefixedSHA256(data)
	data, err = json.Marshal(pack)
	if len(data) > MaxDocumentAnnotationTransferBytes {
		return pack, nil, ErrDocumentAnnotationTransferInvalid
	}
	return pack, data, err
}

// encoding/json repairs isolated UTF-16 surrogate escapes. Do not let that
// silently change the imported exact quotation or its metadata.
func validAnnotationTransferJSONUnicode(data []byte) bool {
	quoted := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			quoted = !quoted
		} else if quoted && data[i] == '\\' {
			i++
			if i >= len(data) {
				return false
			}
			if data[i] != 'u' {
				continue
			}
			if i+4 >= len(data) {
				return false
			}
			n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if n >= 0xDC00 && n <= 0xDFFF {
				return false
			}
			if n >= 0xD800 && n <= 0xDBFF {
				if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

// encoding/json accepts case-insensitive field names and null scalar values.
// Neither belongs to the versioned native schema. Check these before decoding
// the bounded, typed records; regions is the only optional item field.
func validateAnnotationTransferJSONKeys(data []byte) bool {
	object := func(raw []byte, fields string, optional string) (map[string]json.RawMessage, bool) {
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return nil, false
		}
		allowed := map[string]bool{}
		for _, field := range strings.Fields(fields) {
			allowed[field] = true
			if field != optional && values[field] == nil {
				return nil, false
			}
		}
		for key, value := range values {
			if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, false
			}
		}
		return values, true
	}
	root, ok := object(data, "format version annotations digest", "")
	if !ok {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(root["annotations"], &items) != nil || len(items) < 1 || len(items) > MaxDocumentAnnotationTransferCount {
		return false
	}
	for _, raw := range items {
		item, ok := object(raw, "origin_id origin_revision document_id document_revision citation_id chunk_hash evidence_hash source_name page block_index block_chunk_index quote start end prefix suffix note color archived author created updated regions", "regions")
		if !ok {
			return false
		}
		if item["regions"] == nil {
			continue
		}
		var areas []json.RawMessage
		if json.Unmarshal(item["regions"], &areas) != nil || len(areas) > 100 {
			return false
		}
		for _, area := range areas {
			if _, ok := object(area, "origin_id page x y width height file_hash author created", ""); !ok {
				return false
			}
		}
	}
	return true
}
func parseAnnotationTransfer(content string) (documentAnnotationTransferPack, error) {
	var pack documentAnnotationTransferPack
	if len(content) > MaxDocumentAnnotationTransferBytes || len(content) == 0 {
		return pack, ErrDocumentAnnotationTransferInvalid
	}
	data := []byte(content)
	if !validAnnotationTransferJSONUnicode(data) || validateClassicMindMapImportJSON(data) != nil || !validateAnnotationTransferJSONKeys(data) {
		return pack, ErrDocumentAnnotationTransferInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&pack); err != nil {
		return pack, ErrDocumentAnnotationTransferInvalid
	}
	digest := pack.Digest
	canonical, _, err := canonicalAnnotationTransferPack(pack)
	if err != nil || digest != canonical.Digest {
		return pack, ErrDocumentAnnotationTransferInvalid
	}
	return canonical, nil
}

func ExportDocumentAnnotationsIn(ctx context.Context, dir string, in DocumentAnnotationExportRequest) (DocumentAnnotationExportArtifact, error) {
	var out DocumentAnnotationExportArtifact
	if len(in.IDs) < 1 || len(in.IDs) > MaxDocumentAnnotationTransferCount || (in.Format != "json" && in.Format != "markdown") {
		return out, ErrDocumentAnnotationTransferInvalid
	}
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if !validDocumentAnnotationID(id) || seen[id] {
			return out, ErrDocumentAnnotationTransferInvalid
		}
		seen[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	pack := documentAnnotationTransferPack{Format: documentAnnotationTransferFormat, Version: 1}
	for _, id := range in.IDs {
		record, err := readDocumentAnnotationTx(ctx, tx, id)
		if err != nil {
			return out, err
		}
		a, anchor := record.Annotation, record.Anchor
		areas, err := exportDocumentAnnotationRegionsTx(ctx, tx, id)
		if err != nil {
			return out, err
		}
		pack.Annotations = append(pack.Annotations, documentAnnotationTransferItem{OriginID: a.ID, OriginRevision: a.Revision, DocumentID: anchor.DocumentID, DocumentRevision: anchor.DocumentRevision, CitationID: anchor.CitationID, ChunkHash: anchor.ChunkHash, EvidenceHash: anchor.EvidenceHash, SourceName: a.SourceName, Page: a.Page, BlockIndex: a.BlockIndex, BlockChunkIndex: a.BlockChunkIndex, Quote: a.Quote, Start: a.Start, End: a.End, Prefix: a.Prefix, Suffix: a.Suffix, Note: a.Note, Color: a.Color, Archived: a.Archived, Author: a.Author, Created: a.Created, Updated: a.Updated, Regions: areas})
	}
	pack, data, err := canonicalAnnotationTransferPack(pack)
	if err != nil {
		return out, err
	}
	out = DocumentAnnotationExportArtifact{Filename: "annotations-" + pack.Digest[7:19] + ".json", MediaType: "application/json; charset=utf-8", Data: data, Count: len(pack.Annotations), Digest: pack.Digest}
	if in.Format == "markdown" {
		out.Filename = "annotations-" + pack.Digest[7:19] + ".md"
		out.MediaType = "text/markdown; charset=utf-8"
		out.Data = annotationTransferMarkdown(pack)
		if len(out.Data) > MaxDocumentAnnotationTransferBytes {
			return DocumentAnnotationExportArtifact{}, ErrDocumentAnnotationTransferInvalid
		}
	}
	return out, ctx.Err()
}

func annotationTransferMarkdown(pack documentAnnotationTransferPack) []byte {
	escape := func(s string) string {
		s = html.EscapeString(s)
		return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "#", "\\#", "!", "\\!").Replace(s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Аннотации документов\n\nПакет: %s\n\nАвторство и даты сохранены как метаданные. Наличие источника проверяется при импорте.\n", pack.Digest)
	for _, a := range pack.Annotations {
		fmt.Fprintf(&b, "\n## %s\n\nДокумент: %s; редакция: %s.\n\nЦитата: %s; страница: %d; блок: %d; фрагмент блока: %d; диапазон Unicode: [%d, %d).\n\n", escape(a.SourceName), escape(a.DocumentID), a.DocumentRevision, escape(a.CitationID), a.Page, a.BlockIndex, a.BlockChunkIndex, a.Start, a.End)
		for _, line := range strings.Split(strings.ReplaceAll(a.Quote, "\r", ""), "\n") {
			fmt.Fprintf(&b, "> %s\n", escape(line))
		}
		fmt.Fprintf(&b, "\nКомментарий:\n\n%s\n\nАвтор: %s; создано: %s; изменено: %s; архив: %t.\n\nАннотация: %s, версия %d; hash фрагмента: %s; hash цитаты: %s.\n", escape(a.Note), escape(a.Author), a.Created, a.Updated, a.Archived, a.OriginID, a.OriginRevision, a.ChunkHash, a.EvidenceHash)
		for _, r := range a.Regions {
			fmt.Fprintf(&b, "\nОбласть PDF: страница %d, x=%g, y=%g, ширина=%g, высота=%g; файл %s; автор: %s; создано: %s.\n", r.Page, r.X, r.Y, r.Width, r.Height, r.FileHash, escape(r.Author), r.Created)
		}
	}
	return []byte(b.String())
}

type annotationTransferBinding struct {
	Path, Revision, ChunkHash, TextHash, FileHash string
	State                                         EvidenceState
	Reason                                        string
}
type annotationTransferPlan struct {
	Pack     documentAnnotationTransferPack
	Bindings []annotationTransferBinding
	Preview  DocumentAnnotationImportPreview
}

func resolveAnnotationTransferBindingTx(ctx context.Context, tx *sql.Tx, item documentAnnotationTransferItem, digest string) (annotationTransferBinding, error) {
	unbound := func(reason string) annotationTransferBinding {
		return annotationTransferBinding{Path: documentAnnotationUnboundPrefix + digest[7:] + "/" + item.OriginID + "/" + item.SourceName, State: EvidenceMissing, Reason: reason + " Цитата будет сохранена в архиве без привязки; источник не открывается."}
	}
	var count, size int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(NULLIF(source_path,''),source_file,'')),COALESCE(MAX(length(CAST(COALESCE(NULLIF(source_path,''),source_file,'') AS BLOB))),0) FROM entries WHERE document_id=?`, item.DocumentID).Scan(&count, &size)
	if err != nil {
		return annotationTransferBinding{}, err
	}
	if count == 0 {
		return unbound("Документ с таким идентификатором отсутствует."), nil
	}
	if count != 1 || size > 32768 {
		return unbound("Однозначная привязка к локальному документу не установлена."), nil
	}
	var path string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(source_path,''),source_file,'') FROM entries WHERE document_id=? LIMIT 1`, item.DocumentID).Scan(&path); err != nil {
		return annotationTransferBinding{}, err
	}
	if path == "" || strings.HasPrefix(path, documentAnnotationUnboundPrefix) {
		return unbound("Локальный источник недоступен."), nil
	}
	var textBytes, pinBytes int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(length(CAST(text AS BLOB))),0),COALESCE(MAX(length(CAST(document_revision AS BLOB))+length(CAST(chunk_hash AS BLOB))),0) FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=? AND COALESCE(NULLIF(source_path,''),source_file,'')=?`, item.DocumentID, item.Page, item.BlockIndex, item.BlockChunkIndex, path).Scan(&count, &textBytes, &pinBytes)
	if err != nil {
		return annotationTransferBinding{}, err
	}
	if count != 1 || textBytes > maxDocumentAnnotationChunkBytes || pinBytes > 142 {
		return unbound("Исходный фрагмент отсутствует, неоднозначен или превышает допустимый размер."), nil
	}
	var text, revision, hash string
	err = tx.QueryRowContext(ctx, `SELECT text,document_revision,chunk_hash FROM entries WHERE document_id=? AND page=? AND block_index=? AND block_chunk_index=? AND COALESCE(NULLIF(source_path,''),source_file,'')=? LIMIT 1`, item.DocumentID, item.Page, item.BlockIndex, item.BlockChunkIndex, path).Scan(&text, &revision, &hash)
	if err != nil {
		return annotationTransferBinding{}, err
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || !isSHA256ContentHash(revision) || !isSHA256ContentHash(hash) || ChunkContentHash(text) != hash {
		return unbound("Целостность локального фрагмента не подтверждена."), nil
	}
	b := annotationTransferBinding{Path: path, Revision: revision, ChunkHash: hash, TextHash: ChunkContentHash(text), State: EvidenceStale, Reason: "Документ найден, но его редакция или фрагмент изменились. Сохранены исходные цитата и координаты."}
	if revision == item.DocumentRevision && hash == item.ChunkHash {
		runes := []rune(text)
		if item.End > len(runes) || string(runes[item.Start:item.End]) != item.Quote {
			return unbound("Цитата не совпадает с указанным диапазоном."), nil
		}
		if item.Prefix != string(runes[max(0, item.Start-documentAnnotationContextRunes):item.Start]) || item.Suffix != string(runes[item.End:min(len(runes), item.End+documentAnnotationContextRunes)]) {
			return unbound("Контекст цитаты не совпадает с указанным диапазоном."), nil
		}
		b.State, b.Reason = EvidenceCurrent, "Идентификатор, редакция, hash фрагмента и точный диапазон совпадают с локальным документом."
	}
	b.FileHash, err = annotationPDFHashTx(ctx, tx, item.anchor(path))
	return b, err
}

func previewAnnotationTransferTx(ctx context.Context, tx *sql.Tx, pack documentAnnotationTransferPack) (annotationTransferPlan, error) {
	plan := annotationTransferPlan{Pack: pack, Preview: DocumentAnnotationImportPreview{Count: len(pack.Annotations), Items: []DocumentAnnotationImportPreviewItem{}, Warnings: []string{"Создаются независимые копии с новыми ID; исходные аннотации, связи с картами и их история не заменяются.", "Авторство и даты аннотаций и областей PDF взяты из файла и не подтверждены. Фактический импортёр записывается в локальную историю."}}}
	for _, a := range pack.Annotations {
		binding, err := resolveAnnotationTransferBindingTx(ctx, tx, a, pack.Digest)
		if err != nil {
			return plan, err
		}
		plan.Bindings = append(plan.Bindings, binding)
		archived := a.Archived || binding.State == EvidenceMissing
		if archived {
			plan.Preview.Archived++
		}
		switch binding.State {
		case EvidenceCurrent:
			plan.Preview.Current++
		case EvidenceStale:
			plan.Preview.Stale++
		default:
			plan.Preview.Missing++
		}
		plan.Preview.RegionCount += len(a.Regions)
		plan.Preview.Items = append(plan.Preview.Items, DocumentAnnotationImportPreviewItem{OriginID: a.OriginID, SourceName: a.SourceName, Quote: a.Quote, Note: a.Note, State: binding.State, Archived: archived, Reason: binding.Reason})
	}
	if plan.Preview.Missing > 0 {
		plan.Preview.Warnings = append(plan.Preview.Warnings, "Отсутствующие или неоднозначные источники сохраняются как архивные цитаты без привязки. Автоматическая привязка при появлении документа не выполняется.")
	}
	if plan.Preview.RegionCount > 0 {
		plan.Preview.Warnings = append(plan.Preview.Warnings, "Области PDF сохраняют исходные координаты и hash файла. Несовпавший hash отмечается как устаревший; распознавание текста по изображению не выполняется.")
	}
	raw, err := json.Marshal(struct {
		Package  string
		Bindings []annotationTransferBinding
	}{pack.Digest, plan.Bindings})
	plan.Preview.Digest = prefixedSHA256(raw)
	return plan, err
}

func PreviewDocumentAnnotationImportIn(ctx context.Context, dir string, in DocumentAnnotationImportPreviewInput) (DocumentAnnotationImportPreview, error) {
	pack, err := parseAnnotationTransfer(in.Content)
	if err != nil {
		return DocumentAnnotationImportPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return DocumentAnnotationImportPreview{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentAnnotationImportPreview{}, err
	}
	defer tx.Rollback()
	plan, err := previewAnnotationTransferTx(ctx, tx, pack)
	return plan.Preview, err
}

const annotationTransferReceiptSchema = `CREATE TABLE IF NOT EXISTS document_annotation_import_receipts(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,result_json TEXT NOT NULL,result_hash TEXT NOT NULL,created TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS document_annotation_import_receipt_no_update BEFORE UPDATE ON document_annotation_import_receipts BEGIN SELECT RAISE(ABORT,'receipt is immutable'); END;
CREATE TRIGGER IF NOT EXISTS document_annotation_import_receipt_no_delete BEFORE DELETE ON document_annotation_import_receipts BEGIN SELECT RAISE(ABORT,'receipt is immutable'); END;`
const annotationTransferReceiptSizeSQL = `length(CAST(request_id AS BLOB))+length(CAST(request_hash AS BLOB))+length(CAST(result_json AS BLOB))+length(CAST(result_hash AS BLOB))+length(CAST(created AS BLOB))`

func readAnnotationTransferReceiptTx(ctx context.Context, tx *sql.Tx, request, digest string) (DocumentAnnotationImportResult, bool, error) {
	var out DocumentAnnotationImportResult
	exists, err := annotationTableExists(ctx, tx, "document_annotation_import_receipts")
	if err != nil || !exists {
		return out, false, err
	}
	var size int
	err = tx.QueryRowContext(ctx, `SELECT `+annotationTransferReceiptSizeSQL+` FROM document_annotation_import_receipts WHERE request_id=?`, request).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if size > maxDocumentAnnotationHistoryRowBytes {
		return out, false, ErrDocumentAnnotationLimit
	}
	var old, raw, hash string
	if err = tx.QueryRowContext(ctx, `SELECT request_hash,result_json,result_hash FROM document_annotation_import_receipts WHERE request_id=?`, request).Scan(&old, &raw, &hash); err != nil {
		return out, false, err
	}
	if old != digest {
		return out, false, ErrDocumentAnnotationRequest
	}
	if hash != prefixedSHA256([]byte(raw)) || json.Unmarshal([]byte(raw), &out) != nil || len(out.Items) == 0 || len(out.Items) > MaxDocumentAnnotationTransferCount || len(out.Items) != out.Imported {
		return out, false, ErrDocumentAnnotationInvalid
	}
	for _, item := range out.Items {
		if !validDocumentAnnotationID(item.ID) || !validDocumentAnnotationID(item.OriginID) || item.Revision != 1 {
			return out, false, ErrDocumentAnnotationInvalid
		}
	}
	out.Replayed = true
	return out, true, nil
}

type annotationTransferHistory struct {
	documentAnnotationRecord
	TransferOrigin struct {
		PackageDigest string `json:"package_digest"`
		ID            string `json:"id"`
		Revision      int64  `json:"revision"`
		Updated       string `json:"updated"`
	} `json:"transfer_origin"`
}

func ImportDocumentAnnotationsIn(ctx context.Context, dir, actor string, in DocumentAnnotationImportInput) (DocumentAnnotationImportResult, error) {
	var out DocumentAnnotationImportResult
	if !validDocumentAnnotationActor(actor) || !validDocumentAnnotationRequest(in.RequestID) || !isSHA256ContentHash(in.Digest) {
		return out, ErrDocumentAnnotationTransferInvalid
	}
	pack, err := parseAnnotationTransfer(in.Content)
	if err != nil {
		return out, err
	}
	requestHash, err := documentAnnotationDigest(actor, "import-annotations-v1", pack.Digest, in.Digest)
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if prior, found, e := readAnnotationTransferReceiptTx(ctx, tx, in.RequestID, requestHash); e != nil || found {
		return prior, e
	}
	plan, err := previewAnnotationTransferTx(ctx, tx, pack)
	if err != nil {
		return out, err
	}
	if plan.Preview.Digest != in.Digest {
		return out, ErrDocumentAnnotationTransferPreview
	}
	if err = ensureDocumentAnnotationsTx(ctx, tx); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, annotationTransferReceiptSchema); err != nil {
		return out, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	out = DocumentAnnotationImportResult{Items: []DocumentAnnotationImportItem{}, Imported: len(pack.Annotations), Current: plan.Preview.Current, Stale: plan.Preview.Stale, Missing: plan.Preview.Missing}
	var count int
	var liveBytes, historyBytes, receiptBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(`+documentAnnotationSizeSQL+`),0) FROM document_annotations`).Scan(&count, &liveBytes); err != nil {
		return out, err
	}
	if count+len(pack.Annotations) > maxDocumentAnnotationRows {
		return out, ErrDocumentAnnotationLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+documentAnnotationHistorySizeSQL+`),0) FROM document_annotation_history`).Scan(&historyBytes); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(`+annotationTransferReceiptSizeSQL+`),0) FROM document_annotation_import_receipts`).Scan(&count, &receiptBytes); err != nil {
		return out, err
	}
	if count >= maxDocumentAnnotationReceipts {
		return out, ErrDocumentAnnotationLimit
	}
	for i, source := range pack.Annotations {
		id, err := newClassicMindMapID("ann-")
		if err != nil {
			return out, err
		}
		binding := plan.Bindings[i]
		anchor := source.anchor(binding.Path)
		a := DocumentAnnotation{ID: id, Revision: 1, DocumentID: LibraryDocumentID(binding.Path, source.DocumentID), DocumentRevision: source.DocumentRevision, CitationID: source.CitationID, SourceName: sourceName(Entry{SourcePath: binding.Path}), Page: source.Page, BlockIndex: source.BlockIndex, BlockChunkIndex: source.BlockChunkIndex, Quote: source.Quote, Start: source.Start, End: source.End, Prefix: source.Prefix, Suffix: source.Suffix, Note: source.Note, Color: source.Color, Archived: source.Archived || binding.State == EvidenceMissing, Author: source.Author, Created: source.Created, Updated: source.Updated}
		anchorJSON, _ := json.Marshal(anchor)
		rowBytes := int64(len(a.ID) + len(a.DocumentID) + len(anchorJSON) + len(a.Prefix) + len(a.Suffix) + len(a.Note) + len(a.Color) + len(a.Author) + len(a.Created) + len(a.Updated) + 64)
		liveBytes += rowBytes
		history := annotationTransferHistory{documentAnnotationRecord: documentAnnotationRecord{Anchor: anchor, Annotation: a}}
		history.TransferOrigin.PackageDigest, history.TransferOrigin.ID, history.TransferOrigin.Revision, history.TransferOrigin.Updated = pack.Digest, source.OriginID, source.OriginRevision, source.Updated
		historyJSON, err := json.Marshal(history)
		if err != nil {
			return out, err
		}
		historySize := int64(len(historyJSON) + len(id) + len("import") + len(actor) + len(now) + 103)
		historyBytes += historySize
		if rowBytes > maxDocumentAnnotationRowBytes || liveBytes > maxDocumentAnnotationBytes || historySize > maxDocumentAnnotationHistoryRowBytes || historyBytes > maxDocumentAnnotationHistoryBytes {
			return out, ErrDocumentAnnotationLimit
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO document_annotations(id,revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,archived,author,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Revision, a.DocumentID, string(anchorJSON), a.Start, a.End, a.Prefix, a.Suffix, a.Note, a.Color, a.Archived, a.Author, a.Created, a.Updated)
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_history(annotation_id,revision,action,actor,created,payload,payload_hash) VALUES(?,?,?,?,?,?,?)`, id, 1, "import", actor, now, string(historyJSON), prefixedSHA256(historyJSON))
		if err != nil {
			return out, err
		}
		if err = importDocumentAnnotationRegionsTx(ctx, tx, id, source.Regions, actor); err != nil {
			return out, err
		}
		out.Items = append(out.Items, DocumentAnnotationImportItem{OriginID: source.OriginID, ID: id, Revision: 1})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if receiptBytes+int64(len(raw)+len(in.RequestID)+len(requestHash)+71+len(now)) > maxDocumentAnnotationReceiptBytes {
		return out, ErrDocumentAnnotationLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_annotation_import_receipts(request_id,request_hash,result_json,result_hash,created) VALUES(?,?,?,?,?)`, in.RequestID, requestHash, string(raw), prefixedSHA256(raw), now); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return DocumentAnnotationImportResult{}, err
	}
	return out, nil
}
