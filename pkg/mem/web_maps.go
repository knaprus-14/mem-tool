package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrWebMapInvalid = errors.New("проверьте название, описание и шаблон карты")
var ErrWebMapLimit = errors.New("достигнут лимит веб-карт: до 1000 карт, 1000 узлов и 4 МиБ текста одной карты; данные не изменены")
var ErrWebMapRequest = errors.New("эта попытка уже использована для другой карты; проверьте библиотеку и откройте новую форму")

type WebMapSummary struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Mode        ClassicMindMapMode   `json:"mode"`
	Status      ClassicMindMapStatus `json:"status"`
	RootNodeID  string               `json:"root_node_id"`
	Revision    int64                `json:"revision"`
	Updated     string               `json:"updated"`
	NodeCount   int                  `json:"node_count"`
	SourceCount int                  `json:"source_count"`
}
type WebMapPage struct {
	Items  []WebMapSummary `json:"items"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}
type WebMapNode struct {
	ID          string                 `json:"id"`
	ParentID    string                 `json:"parent_id"`
	Position    int                    `json:"position"`
	Label       string                 `json:"label"`
	Summary     string                 `json:"summary"`
	Body        string                 `json:"body"`
	Kind        ClassicMindMapNodeKind `json:"kind"`
	SourceCount int                    `json:"source_count"`
	Locked      bool                   `json:"locked"`
}
type WebMapView struct {
	Map             WebMapSummary `json:"map"`
	Nodes           []WebMapNode  `json:"nodes"`
	HasImportSource bool          `json:"has_import_source"`
}
type WebMapCreate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	TemplateID  string `json:"template_id"`
	RequestID   string `json:"request_id"`
}
type WebMapCreated struct {
	ID       string `json:"id"`
	Replayed bool   `json:"replayed"`
}

func webMapTablesExist(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('mind_maps','mind_map_nodes','mind_map_node_sources')`).Scan(&n)
	return n == 3, err
}

const webMapSelect = `SELECT m.id,substr(m.title,1,512),substr(m.description,1,4000),m.mode,m.status,m.root_node_id,m.revision,m.updated,
(SELECT COUNT(*) FROM mind_map_nodes n WHERE n.map_id=m.id AND n.deleted_at=''),
(SELECT COUNT(*) FROM mind_map_node_sources s WHERE s.map_id=m.id AND s.deleted_at='') FROM mind_maps m `

func scanWebMap(row interface{ Scan(...any) error }) (WebMapSummary, error) {
	var m WebMapSummary
	err := row.Scan(&m.ID, &m.Title, &m.Description, &m.Mode, &m.Status, &m.RootNodeID, &m.Revision, &m.Updated, &m.NodeCount, &m.SourceCount)
	return m, err
}

// ListWebMapsIn reads summaries only, without migrating the database or loading vectors.
func ListWebMapsIn(ctx context.Context, dir string, offset int, archived bool) (applogResult0 WebMapPage, applogResult1 error) {
	return ListWebMapsFilteredIn(ctx, dir, offset, archived, false)
}

func ListWebMapsFilteredIn(ctx context.Context, dir string, offset int, archived, deleted bool) (applogResult0 WebMapPage, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapsIn")(&applogResult1)

	page := WebMapPage{Items: []WebMapSummary{}, Offset: offset, Limit: 30}
	if offset < 0 || offset > 1000000 {
		return page, ErrWebMapInvalid
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
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil || !exists {
		return page, err
	}
	where := `WHERE m.deleted_at=''`
	if deleted {
		where = `WHERE m.deleted_at!=''`
	} else if !archived {
		where += ` AND m.status!='archived'`
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_maps m `+where).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, webMapSelect+where+` ORDER BY m.updated DESC,m.id LIMIT ? OFFSET ?`, page.Limit, offset)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		m, e := scanWebMap(rows)
		if e != nil {
			rows.Close()
			return page, e
		}
		page.Items = append(page.Items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func ReadWebMapIn(ctx context.Context, dir, id string) (applogResult0 WebMapView, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebMapIn")(&applogResult1)

	view := WebMapView{Nodes: []WebMapNode{}}
	if len(id) > 128 || id == "" {
		return view, ErrWebMapInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return view, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	exists, err := webMapTablesExist(ctx, tx)
	if err != nil {
		return view, err
	}
	if !exists {
		return view, ErrClassicMindMapNotFound
	}
	var metadataSize int64
	err = tx.QueryRowContext(ctx, `SELECT length(CAST(id||title||description||mode||status||root_node_id||updated AS BLOB)) FROM mind_maps WHERE id=? AND deleted_at=''`, id).Scan(&metadataSize)
	if errors.Is(err, sql.ErrNoRows) {
		return view, ErrClassicMindMapNotFound
	}
	if err != nil {
		return view, err
	}
	if metadataSize > 4<<20 {
		return view, ErrWebMapLimit
	}
	fullSelect := strings.ReplaceAll(strings.ReplaceAll(webMapSelect, "substr(m.title,1,512)", "m.title"), "substr(m.description,1,4000)", "m.description")
	view.Map, err = scanWebMap(tx.QueryRowContext(ctx, fullSelect+`WHERE m.id=? AND m.deleted_at=''`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return view, ErrClassicMindMapNotFound
	}
	if err != nil {
		return view, err
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(id||parent_id||label||summary||body_markdown||kind AS BLOB))),0) FROM mind_map_nodes WHERE map_id=? AND deleted_at=''`, id).Scan(&size); err != nil {
		return view, err
	}
	if view.Map.NodeCount > 1000 || size+metadataSize > 4<<20 {
		return view, ErrWebMapLimit
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.parent_id,n.position,n.label,n.summary,n.body_markdown,n.kind,n.locked,(SELECT COUNT(*) FROM mind_map_node_sources s WHERE s.map_id=n.map_id AND s.node_id=n.id AND s.deleted_at='') FROM mind_map_nodes n WHERE n.map_id=? AND n.deleted_at='' ORDER BY n.parent_id,n.position,n.id`, id)
	if err != nil {
		return view, err
	}
	tree := []ClassicMindMapNode{}
	roots := 0
	for rows.Next() {
		var n WebMapNode
		if err = rows.Scan(&n.ID, &n.ParentID, &n.Position, &n.Label, &n.Summary, &n.Body, &n.Kind, &n.Locked, &n.SourceCount); err != nil {
			rows.Close()
			return view, err
		}
		view.Nodes = append(view.Nodes, n)
		tree = append(tree, ClassicMindMapNode{ID: n.ID, ParentID: n.ParentID})
		if n.ParentID == "" {
			roots++
			if n.ID != view.Map.RootNodeID {
				rows.Close()
				return view, ErrClassicMindMapRenderLimit
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return view, err
	}
	if roots != 1 {
		return view, ErrClassicMindMapRenderLimit
	}
	if err = validateClassicMindMapBrowserRender(tree); err != nil {
		return view, err
	}
	view.HasImportSource, err = hasWebMapImportSourceTx(ctx, tx, id)
	if err != nil {
		return view, err
	}
	return view, tx.Commit()
}

// CreateWebMapIn creates a normal classic map using the shared importer, with
// its creation history, initial snapshot and retry receipt in one transaction.
func CreateWebMapIn(ctx context.Context, dir, actor string, input WebMapCreate) (applogResult0 WebMapCreated, applogResult1 error) {
	defer applog.Track("pkg.mem.CreateWebMapIn")(&applogResult1)

	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 || utf8.RuneCountInString(input.Description) > 4000 || len(input.RequestID) != 32 || strings.Trim(input.RequestID, "0123456789abcdef") != "" {
		return WebMapCreated{}, ErrWebMapInvalid
	}
	draft := CreateClassicMindMapDraft(input.Title, input.Description)
	if input.TemplateID != "" {
		found := false
		for _, template := range ListClassicMindMapTemplates() {
			if template.ID != input.TemplateID {
				continue
			}
			found = true
			for i, label := range template.Branches {
				draft.Nodes = append(draft.Nodes, ClassicMindMapNodeDraft{Ref: fmt.Sprint("branch-", i), ParentRef: "root", Label: label, Kind: ClassicMindMapNodeSubtopic, Origin: ClassicMindMapNodeManual})
			}
			break
		}
		if !found {
			return WebMapCreated{}, ErrWebMapInvalid
		}
	}
	encoded, _ := json.Marshal(input)
	digest := prefixedSHA256(encoded)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapCreated{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapCreated{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_map_creates(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,map_id TEXT NOT NULL,created TEXT NOT NULL)`); err != nil {
		return WebMapCreated{}, err
	}
	var prior, hash string
	err = tx.QueryRowContext(ctx, `SELECT map_id,request_hash FROM web_map_creates WHERE request_id=?`, input.RequestID).Scan(&prior, &hash)
	if err == nil {
		if hash != digest {
			return WebMapCreated{}, ErrWebMapRequest
		}
		return WebMapCreated{ID: prior, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WebMapCreated{}, err
	}
	if _, err = tx.ExecContext(ctx, classicMindMapSchema); err != nil {
		return WebMapCreated{}, err
	}
	var maps, receipts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_maps`).Scan(&maps); err != nil {
		return WebMapCreated{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_map_creates`).Scan(&receipts); err != nil {
		return WebMapCreated{}, err
	}
	if maps >= 1000 || receipts >= 1000 {
		return WebMapCreated{}, ErrWebMapLimit
	}
	doc, err := importClassicMindMapTx(tx, draft, actor, "создано через веб-библиотеку")
	if err != nil {
		return WebMapCreated{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_creates VALUES(?,?,?,?)`, input.RequestID, digest, doc.Map.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return WebMapCreated{}, err
	}
	if err = tx.Commit(); err != nil {
		return WebMapCreated{}, err
	}
	return WebMapCreated{ID: doc.Map.ID}, nil
}
