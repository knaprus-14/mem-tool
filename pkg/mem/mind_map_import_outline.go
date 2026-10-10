package mem

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

const (
	MaxOutlineImportBytes  = 8 << 20
	MaxOutlineImportNodes  = 1000
	MaxOutlineImportDepth  = 256
	maxOutlineArchiveBytes = 128 << 20
)

var (
	ErrOutlineImportInvalid        = errors.New("некорректная карта: выберите поддержанный формат OPML, FreeMind, XMind или Markdown и проверьте структуру файла")
	ErrOutlineImportLimit          = errors.New("превышен предел импорта: файл до 8 МиБ, 1000 узлов, глубина 256 и 4 МиБ данных карты")
	ErrOutlineImportSourceNotFound = errors.New("сохранённый исходный файл импорта не найден")
)

// Format adapters return only a tree and ordinary external links. Imported
// third-party IDs, paths and arbitrary metadata never become evidence anchors.
type OutlineImportDraft struct {
	Title    string
	Nodes    []ClassicMindMapNodeDraft
	Warnings []string
}
type OutlineImportInput struct {
	Format        string `json:"format"`
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
}
type OutlineImportApplyInput struct {
	OutlineImportInput
	Digest    string `json:"digest"`
	RequestID string `json:"request_id"`
}
type OutlineImportPreviewNode struct {
	Ref       string `json:"ref"`
	ParentRef string `json:"parent_ref"`
	Label     string `json:"label"`
	Depth     int    `json:"depth"`
}
type OutlineImportPreview struct {
	Digest       string                     `json:"digest"`
	Format       string                     `json:"format"`
	Title        string                     `json:"title"`
	NodeCount    int                        `json:"node_count"`
	SourceCount  int                        `json:"source_count"`
	SourceSHA256 string                     `json:"source_sha256"`
	SourceBytes  int                        `json:"source_bytes"`
	Warnings     []string                   `json:"warnings"`
	Nodes        []OutlineImportPreviewNode `json:"nodes"`
}
type OutlineImportSource struct {
	Filename string
	Format   string
	SHA256   string
	Data     []byte
}
type outlineImportPlan struct {
	Preview OutlineImportPreview
	Draft   ClassicMindMapDraft
	Source  OutlineImportSource
}

func validOutlineFilename(name string) bool {
	return name != "" && name != "." && name != ".." && strings.TrimSpace(name) == name && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 200 && !strings.ContainsAny(name, "/\\:\x00") && !strings.ContainsFunc(name, unicode.IsControl)
}
func parseOutlineImport(in OutlineImportInput) (outlineImportPlan, error) {
	var p outlineImportPlan
	if !validOutlineFilename(in.Filename) {
		return p, ErrOutlineImportInvalid
	}
	switch in.Format {
	case "opml", "freemind", "xmind", "markdown":
	default:
		return p, ErrOutlineImportInvalid
	}
	if len(in.ContentBase64) > base64.StdEncoding.EncodedLen(MaxOutlineImportBytes) {
		return p, ErrOutlineImportLimit
	}
	data, err := base64.StdEncoding.Strict().DecodeString(in.ContentBase64)
	if err != nil || len(data) == 0 || base64.StdEncoding.EncodeToString(data) != in.ContentBase64 {
		return p, ErrOutlineImportInvalid
	}
	if len(data) > MaxOutlineImportBytes {
		return p, ErrOutlineImportLimit
	}
	content := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if in.Format != "xmind" && (!utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0) {
		return p, ErrOutlineImportInvalid
	}
	var parsed OutlineImportDraft
	switch in.Format {
	case "opml", "freemind":
		parsed, err = parseOutlineXML(content, in.Format)
	case "xmind":
		parsed, err = parseOutlineXMind(data)
	case "markdown":
		parsed, err = parseOutlineMarkdown(content, in.Filename)
	}
	if err != nil {
		return p, err
	}
	if len(parsed.Nodes) == 0 {
		return p, ErrOutlineImportInvalid
	}
	if len(parsed.Nodes) > MaxOutlineImportNodes {
		return p, ErrOutlineImportLimit
	}
	if strings.TrimSpace(parsed.Title) == "" {
		parsed.Title = parsed.Nodes[0].Label
	}
	p.Draft = ClassicMindMapDraft{Title: parsed.Title, Mode: ClassicMindMapModeManual, Status: ClassicMindMapStatusDraft, Nodes: parsed.Nodes}
	p.Preview = OutlineImportPreview{Format: in.Format, NodeCount: len(parsed.Nodes), SourceSHA256: prefixedSHA256(data), SourceBytes: len(data), Nodes: []OutlineImportPreviewNode{}, Warnings: []string{"Будет создана отдельная карта с новыми идентификаторами. История, учебный прогресс и исходные IDs не переносятся.", "Исходный файл сохраняется без изменений и доступен для скачивания из новой карты. Ссылки и вложения не загружаются; импортированный текст не считается подтверждённым основанием документа."}}
	depths := map[string]int{}
	roots := 0
	for i := range p.Draft.Nodes {
		n := &p.Draft.Nodes[i]
		if n.Ref == "" || strings.TrimSpace(n.Ref) != n.Ref {
			return p, ErrOutlineImportInvalid
		}
		if _, ok := depths[n.Ref]; ok {
			return p, ErrOutlineImportInvalid
		}
		depth := 0
		if n.ParentRef != "" {
			parent, ok := depths[n.ParentRef]
			if !ok {
				return p, ErrOutlineImportInvalid
			}
			depth = parent + 1
		} else {
			roots++
			if i != 0 {
				return p, ErrOutlineImportInvalid
			}
		}
		if depth >= MaxOutlineImportDepth {
			return p, ErrOutlineImportLimit
		}
		depths[n.Ref] = depth
		n.Origin = ClassicMindMapNodeImported
		for _, s := range n.Sources {
			if s.Kind != ClassicMindMapSourceURL || s.Evidence != nil || s.KnowledgeNodeID != "" || s.Locator != "" {
				return p, ErrOutlineImportInvalid
			}
			if _, e := validateClassicMindMapHTTPURL(s.URL); e != nil {
				return p, ErrOutlineImportInvalid
			}
		}
		p.Preview.SourceCount += len(n.Sources)
	}
	if roots != 1 {
		return p, ErrOutlineImportInvalid
	}
	if p.Preview.SourceCount > 2000 {
		return p, ErrOutlineImportLimit
	}
	p.Draft, _, err = normalizeClassicMindMapDraft(p.Draft)
	if err != nil {
		return p, fmt.Errorf("%w: %v", ErrOutlineImportInvalid, err)
	}
	encoded, err := json.Marshal(p.Draft)
	if err != nil {
		return p, err
	}
	if len(encoded)+len(p.Draft.Nodes)*512+p.Preview.SourceCount*512 > 4<<20 {
		return p, ErrOutlineImportLimit
	}
	p.Preview.Title = p.Draft.Title
	for _, n := range p.Draft.Nodes {
		p.Preview.Nodes = append(p.Preview.Nodes, OutlineImportPreviewNode{n.Ref, n.ParentRef, n.Label, depths[n.Ref]})
	}
	seen := map[string]bool{}
	for _, warning := range parsed.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		if len(p.Preview.Warnings) < 100 {
			p.Preview.Warnings = append(p.Preview.Warnings, truncateRunes(warning, 512))
		}
	}
	if len(seen) > 98 {
		p.Preview.Warnings = append(p.Preview.Warnings, "Дополнительные неподдержанные свойства сохранены только в исходном файле.")
	}
	p.Source = OutlineImportSource{Filename: in.Filename, Format: in.Format, SHA256: p.Preview.SourceSHA256, Data: data}
	pinned, _ := json.Marshal(struct {
		Version  int
		Filename string
		Preview  OutlineImportPreview
		Draft    ClassicMindMapDraft
	}{1, in.Filename, p.Preview, p.Draft})
	p.Preview.Digest = prefixedSHA256(pinned)
	return p, nil
}

// Preview is read-only and independent of the target corpus: third-party links
// are not promoted to current evidence and no paths or URLs are dereferenced.
func PreviewMindMapOutline(in OutlineImportInput) (OutlineImportPreview, error) {
	p, err := parseOutlineImport(in)
	return p.Preview, err
}

const outlineImportSourceSchema = `CREATE TABLE IF NOT EXISTS web_map_import_sources(
 map_id TEXT PRIMARY KEY,format TEXT NOT NULL,filename TEXT NOT NULL,sha256 TEXT NOT NULL,content BLOB NOT NULL,created TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS web_map_import_sources_no_update BEFORE UPDATE ON web_map_import_sources BEGIN SELECT RAISE(ABORT,'import source is immutable'); END;`

// Admission is checked only for a new import, after recovering an existing
// receipt. A lost successful response remains recoverable at storage capacity.
func ImportWebMapOutlineIn(ctx context.Context, dir, actor string, in OutlineImportApplyInput, admit func() error) (WebMapCreated, error) {
	var out WebMapCreated
	if !validDocumentAnnotationActor(actor) || strings.ContainsFunc(actor, unicode.IsControl) || !validDocumentAnnotationRequest(in.RequestID) || !validSHA256Digest(in.Digest) {
		return out, ErrOutlineImportInvalid
	}
	p, err := parseOutlineImport(in.OutlineImportInput)
	if err != nil {
		return out, err
	}
	request, _ := json.Marshal(struct {
		Action, Actor string
		Input         OutlineImportApplyInput
	}{"import-outline-v1", actor, in})
	hash := prefixedSHA256(request)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_creates(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL,created TEXT NOT NULL)`); err != nil {
		return out, err
	}
	var priorHash string
	err = tx.QueryRowContext(ctx, `SELECT map_id,request_hash FROM web_map_creates WHERE request_id=?`, in.RequestID).Scan(&out.ID, &priorHash)
	if err == nil {
		if priorHash != hash {
			return WebMapCreated{}, ErrWebMapRequest
		}
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if p.Preview.Digest != in.Digest {
		return out, ErrClassicMindMapImportPreview
	}
	if admit != nil {
		if err = admit(); err != nil {
			return out, err
		}
	}
	if _, err = tx.ExecContext(ctx, classicMindMapSchema); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, outlineImportSourceSchema); err != nil {
		return out, err
	}
	var maps, receipts int
	var archiveBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_maps`).Scan(&maps); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_creates`).Scan(&receipts); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(content)),0) FROM web_map_import_sources`).Scan(&archiveBytes); err != nil {
		return out, err
	}
	if maps >= 1000 || receipts >= 1000 {
		return out, ErrWebMapLimit
	}
	if archiveBytes+int64(len(p.Source.Data)) > maxOutlineArchiveBytes {
		return out, ErrOutlineImportLimit
	}
	doc, err := importClassicMindMapTx(tx, p.Draft, actor, "импорт "+in.Format+" с сохранением исходного файла")
	if err != nil {
		return out, err
	}
	if err = validateWebMapMutationBounds(ctx, tx, doc.Map.ID); err != nil {
		return out, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_import_sources(map_id,format,filename,sha256,content,created) VALUES(?,?,?,?,?,?)`, doc.Map.ID, in.Format, in.Filename, p.Source.SHA256, p.Source.Data, now); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_creates VALUES(?,?,?,?)`, in.RequestID, hash, doc.Map.ID, now); err != nil {
		return out, err
	}
	return WebMapCreated{ID: doc.Map.ID}, tx.Commit()
}

func hasWebMapImportSourceTx(ctx context.Context, tx *sql.Tx, mapID string) (bool, error) {
	exists, err := annotationTableExists(ctx, tx, "web_map_import_sources")
	if err != nil || !exists {
		return false, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_import_sources WHERE map_id=?`, mapID).Scan(&count)
	return count == 1, err
}
func ReadWebMapImportSourceIn(ctx context.Context, dir, mapID string) (OutlineImportSource, error) {
	var out OutlineImportSource
	if !webMapExactID(mapID) {
		return out, ErrOutlineImportInvalid
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
	exists, err := hasWebMapImportSourceTx(ctx, tx, mapID)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, ErrOutlineImportSourceNotFound
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT length(s.content) FROM web_map_import_sources s JOIN mind_maps m ON m.id=s.map_id AND m.deleted_at='' WHERE s.map_id=?`, mapID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrOutlineImportSourceNotFound
	}
	if err != nil {
		return out, err
	}
	if size < 1 || size > MaxOutlineImportBytes {
		return out, ErrOutlineImportLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT format,filename,sha256,content FROM web_map_import_sources WHERE map_id=?`, mapID).Scan(&out.Format, &out.Filename, &out.SHA256, &out.Data); err != nil {
		return out, err
	}
	if !validOutlineFilename(out.Filename) || out.SHA256 != prefixedSHA256(out.Data) {
		return OutlineImportSource{}, ErrOutlineImportInvalid
	}
	return out, tx.Commit()
}
