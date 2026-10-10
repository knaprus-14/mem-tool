package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var ErrWebMapSourceOverviewLimit = errors.New("для одной карты выберите до 20 документов, до 512 фрагментов и 600 000 символов; разделите большой материал на несколько карт")
var ErrWebMapSourceOverviewInvalid = errors.New("выберите документы, название до 200 символов, акцент до 1200 символов и степень подробности")
var ErrWebMapSourceOverviewProvenance = errors.New("выбранные документы не содержат проверяемых фрагментов с происхождением; повторно импортируйте эти документы перед созданием карты")

type WebMapFromSources struct {
	RequestID string   `json:"request_id"`
	Title     string   `json:"title"`
	Focus     string   `json:"focus"`
	Detail    string   `json:"detail"`
	Documents []string `json:"documents"`
}

// Resolve complete documents in the same transaction as the new map and task.
// No sampling, vector search, source-path API or implicit ungrounded fallback.
func webMapDocumentPins(ctx context.Context, tx *sql.Tx, selected []string) ([]WebMapAIEvidencePin, error) {
	const source = `COALESCE(NULLIF(source_path,''),source_file,'')`
	wanted := map[string]bool{}
	found := map[string]bool{}
	for _, id := range selected {
		wanted[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+source+`,COALESCE(MIN(document_id),''),COALESCE(MAX(document_id),'') FROM entries WHERE `+source+`<>'' GROUP BY `+source+` ORDER BY `+source+` LIMIT 10001`)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	count := 0
	for rows.Next() {
		var path, lo, hi string
		if err = rows.Scan(&path, &lo, &hi); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		if count > 10000 {
			rows.Close()
			return nil, ErrWebMapSourceOverviewLimit
		}
		docID := ""
		if lo == hi {
			docID = lo
		}
		id := LibraryDocumentID(path, docID)
		if wanted[id] {
			paths = append(paths, path)
			found[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(found) != len(wanted) {
		return nil, ErrQueryStale
	}
	pins := []WebMapAIEvidencePin{}
	var bytes int64
	chars := 0
	for _, path := range paths {
		var n int
		var size int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(warnings AS BLOB))),0) FROM entries WHERE `+source+`=?`, path).Scan(&n, &size); err != nil {
			return nil, err
		}
		bytes += size
		if len(pins)+n > 512 || bytes > 64<<20 {
			return nil, ErrWebMapSourceOverviewLimit
		}
		entries, _, e := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE `+source+`=? ORDER BY page,block_index,block_chunk_index,id`, path)
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if _, e = EvidenceAnchorForEntry(entry, entry.Text); e != nil {
				return nil, ErrWebMapSourceOverviewProvenance
			}
			chars += utf8.RuneCountInString(entry.Text)
			if chars > 600000 {
				return nil, ErrWebMapSourceOverviewLimit
			}
			hit, e := queryHit(entry, 2048, DefaultAnswerLowConfidence)
			if e != nil {
				return nil, e
			}
			pins = append(pins, WebMapAIEvidencePin{ID: entry.ID, Pin: hit.Pin})
		}
	}
	if len(pins) == 0 {
		return nil, ErrWebMapSourceOverviewProvenance
	}
	return pins, nil
}

func CreateWebMapFromSourcesIn(ctx context.Context, dir, owner string, in WebMapFromSources) (applogResult0 WebMapAITask, applogResult1 error) {
	defer applog.Track("pkg.mem.CreateWebMapFromSourcesIn")(&applogResult1)

	in.Title = strings.TrimSpace(in.Title)
	in.Focus = strings.TrimSpace(in.Focus)
	if len(in.Documents) == 0 || len(in.Documents) > 20 {
		return WebMapAITask{}, ErrWebMapSourceOverviewLimit
	}
	if len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || !utf8.ValidString(in.Title) || !utf8.ValidString(in.Focus) || utf8.RuneCountInString(in.Title) > 200 || utf8.RuneCountInString(in.Focus) > 1200 {
		return WebMapAITask{}, ErrWebMapSourceOverviewInvalid
	}
	levels := map[string]string{"brief": "1–2 уровня подтем, только главные идеи", "standard": "2–3 уровня подтем с понятными пояснениями", "detailed": "3–4 уровня подтем с ключевыми фактами и примерами"}
	if in.Detail == "" {
		in.Detail = "standard"
	}
	level, ok := levels[in.Detail]
	if !ok {
		return WebMapAITask{}, ErrWebMapSourceOverviewInvalid
	}
	in.Documents = append([]string(nil), in.Documents...)
	sort.Strings(in.Documents)
	for i, id := range in.Documents {
		valid := len(id) == 64 && strings.Trim(id, "0123456789abcdef") == "" || len(id) == 36 && strings.HasPrefix(id, "web:") && strings.Trim(id[4:], "0123456789abcdef") == ""
		if !valid || i > 0 && id == in.Documents[i-1] {
			return WebMapAITask{}, ErrWebMapSourceOverviewInvalid
		}
	}
	if in.Title == "" {
		in.Title = "Карта по источникам"
	}
	encoded, _ := json.Marshal(struct {
		Owner string
		Input WebMapFromSources
	}{owner, in})
	hash := prefixedSHA256(encoded)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapAITask{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapAITask{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, classicMindMapSchema+classicMindMapAISchema+webMapAISchema); err != nil {
		return WebMapAITask{}, err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT request_hash FROM web_map_ai_tasks WHERE id=?`, in.RequestID).Scan(&prior)
	if err == nil {
		if prior != hash {
			return WebMapAITask{}, ErrWebMapAIChanged
		}
		return scanWebAITask(tx.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=? AND owner=?`, in.RequestID, owner))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WebMapAITask{}, err
	}
	pins, err := webMapDocumentPins(ctx, tx, in.Documents)
	if err != nil {
		return WebMapAITask{}, err
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return WebMapAITask{}, err
	}
	if _, err = NewOllamaAnswerProvider(cfg.Answer); err != nil {
		return WebMapAITask{}, ErrQueryAnswer
	}
	var maps, tasks, pending, runs int
	var stored int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_maps`).Scan(&maps); err != nil {
		return WebMapAITask{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status IN ('queued','running')),0) FROM web_map_ai_tasks`).Scan(&tasks, &pending); err != nil {
		return WebMapAITask{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_runs`).Scan(&runs); err != nil {
		return WebMapAITask{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(manifest_json AS BLOB))+length(CAST(proposals_json AS BLOB))),0) FROM mind_map_generation_previews`).Scan(&stored); err != nil {
		return WebMapAITask{}, err
	}
	if maps >= 1000 || tasks >= 1000 || pending >= 20 || runs >= 1000 || stored > 48<<20 {
		return WebMapAITask{}, ErrWebMapAILimit
	}
	prompt := "Создай понятную обзорную ментальную карту по предоставленным источникам на русском языке. Организуй материал по смыслу: главные темы → подтемы → факты. Стремись к 4–7 главным темам, объединяй связанные идеи из разных документов. Не делай отдельную ветвь для каждого фрагмента или файла. Используй короткие названия (2–6 слов), пояснения в summary, подробности в body_markdown. Каждое утверждение должно опираться на citations. Не добавляй неподтверждённых знаний. Уровень подробности: " + level + ". Корень уже создан; верни ветви для него."
	if in.Focus != "" {
		prompt += "\nАкцент пользователя: " + in.Focus
	}
	doc, err := importClassicMindMapTx(tx, CreateClassicMindMapDraft(in.Title, "Автоматическая карта по выбранным источникам. "+in.Focus), owner, "карта по источникам")
	if err != nil {
		return WebMapAITask{}, err
	}
	input := WebMapAIInput{RequestID: in.RequestID, Revision: doc.Map.Revision, Action: ClassicMindMapAIExpand, Prompt: prompt, Entries: pins, SourceOverview: true, SourceDocuments: len(in.Documents)}
	_, _, _, g, digest, err := webAIInputsTx(ctx, tx, doc.Map.ID, doc.Map.RootNodeID, input)
	if err != nil {
		return WebMapAITask{}, err
	}
	payload, _ := json.Marshal(input)
	if len(payload) > 128<<10 {
		return WebMapAITask{}, ErrWebMapSourceOverviewLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_ai_tasks(id,owner,map_id,node_id,payload,request_hash,status,config_revision,input_digest,generation,created) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`, in.RequestID, owner, doc.Map.ID, doc.Map.RootNodeID, string(payload), hash, cfg.Revision(), digest, g, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return WebMapAITask{}, err
	}
	task, err := scanWebAITask(tx.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=?`, in.RequestID))
	if err != nil {
		return task, err
	}
	return task, tx.Commit()
}
