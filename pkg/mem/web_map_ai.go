package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

var ErrWebMapAIInvalid = errors.New("выберите действие AI, задачу до 8000 символов и до 30 актуальных источников")
var ErrWebMapAIChanged = errors.New("карта, источники или предложение AI изменились; обновите просмотр")
var ErrWebMapAILimit = errors.New("достигнут лимит заданий AI: 20 ожидающих, 1000 сохранённых или 64 МиБ предпросмотров")

type WebMapAIEvidencePin struct {
	ID  int64  `json:"id"`
	Pin string `json:"pin"`
}
type WebMapAIInput struct {
	SourceOverview  bool                   `json:"source_overview,omitempty"`
	SourceDocuments int                    `json:"source_documents,omitempty"`
	RequestID       string                 `json:"request_id"`
	Revision        int64                  `json:"revision"`
	Action          ClassicMindMapAIAction `json:"action"`
	Prompt          string                 `json:"prompt"`
	UseBranch       bool                   `json:"use_branch"`
	AllowUngrounded bool                   `json:"allow_ungrounded"`
	Entries         []WebMapAIEvidencePin  `json:"entries"`
}
type WebMapAITask struct {
	CanResume        bool                   `json:"can_resume,omitempty"`
	CompletedBatches int                    `json:"completed_batches,omitempty"`
	SavedParts       int                    `json:"saved_parts,omitempty"`
	CanReassemble    bool                   `json:"can_reassemble,omitempty"`
	Queue            *WebMapAIQueue         `json:"queue,omitempty"`
	SourceOverview   bool                   `json:"source_overview,omitempty"`
	SourceDocuments  int                    `json:"source_documents,omitempty"`
	SourceChunks     int                    `json:"source_chunks,omitempty"`
	Revision         int64                  `json:"revision"`
	ID               string                 `json:"id"`
	MapID            string                 `json:"map_id"`
	NodeID           string                 `json:"node_id"`
	Action           ClassicMindMapAIAction `json:"action"`
	Prompt           string                 `json:"prompt"`
	Status           string                 `json:"status"`
	CreatedAt        string                 `json:"created_at"`
	Phase            string                 `json:"phase"`
	Current          int                    `json:"current"`
	Total            int                    `json:"total"`
	Error            string                 `json:"error,omitempty"`
	RunID            string                 `json:"-"`
	Owner            string                 `json:"-"`
	ConfigRevision   string                 `json:"-"`
	InputDigest      string                 `json:"-"`
	Generation       int64                  `json:"-"`
	Input            WebMapAIInput          `json:"-"`
}

const webMapAISchema = `CREATE TABLE IF NOT EXISTS web_map_ai_tasks(
 id TEXT PRIMARY KEY,owner TEXT NOT NULL,map_id TEXT NOT NULL,node_id TEXT NOT NULL,payload TEXT NOT NULL,request_hash TEXT NOT NULL,
 status TEXT NOT NULL,run_id TEXT NOT NULL DEFAULT '',config_revision TEXT NOT NULL,input_digest TEXT NOT NULL,generation INTEGER NOT NULL,
 phase TEXT NOT NULL DEFAULT '',current INTEGER NOT NULL DEFAULT 0,total INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS web_map_ai_pending ON web_map_ai_tasks(status,created);`
const webAITaskColumns = `id,owner,map_id,node_id,payload,status,run_id,config_revision,input_digest,generation,phase,current,total,error,created`

func scanWebAITask(row interface{ Scan(...any) error }) (WebMapAITask, error) {
	var s WebMapAITask
	var payload string
	err := row.Scan(&s.ID, &s.Owner, &s.MapID, &s.NodeID, &payload, &s.Status, &s.RunID, &s.ConfigRevision, &s.InputDigest, &s.Generation, &s.Phase, &s.Current, &s.Total, &s.Error, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrClassicMindMapAIPreviewNotFound
	}
	if err != nil {
		return s, err
	}
	if len(payload) > 128<<10 {
		return s, ErrWebMapAILimit
	}
	if err = json.Unmarshal([]byte(payload), &s.Input); err != nil {
		return s, err
	}
	s.SourceOverview = s.Input.SourceOverview
	s.SourceDocuments = s.Input.SourceDocuments
	s.SourceChunks = len(s.Input.Entries)
	s.Revision = s.Input.Revision
	s.Action = s.Input.Action
	s.Prompt = s.Input.Prompt
	return s, nil
}
func webAITablesExist(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_map_ai_tasks'`).Scan(&n)
	return n == 1, err
}

func webAIInputs(ctx context.Context, dir, mapID, nodeID string, in WebMapAIInput) (ClassicMindMapDocument, ClassicMindMapNode, []GroundedEvidence, int64, string, error) {
	fail := func(err error) (ClassicMindMapDocument, ClassicMindMapNode, []GroundedEvidence, int64, string, error) {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, 0, "", err
	}
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
	return webAIInputsTx(ctx, tx, mapID, nodeID, in)
}

func webAIInputsTx(ctx context.Context, tx *sql.Tx, mapID, nodeID string, in WebMapAIInput) (ClassicMindMapDocument, ClassicMindMapNode, []GroundedEvidence, int64, string, error) {
	var doc ClassicMindMapDocument
	var target ClassicMindMapNode
	var evidence []GroundedEvidence
	fail := func(err error) (ClassicMindMapDocument, ClassicMindMapNode, []GroundedEvidence, int64, string, error) {
		return doc, target, nil, 0, "", err
	}
	var err error
	doc, err = readWebMapDocumentTx(ctx, tx, mapID)
	if err != nil {
		return fail(err)
	}
	if doc.Map.Revision != in.Revision {
		return fail(ErrClassicMindMapRevisionConflict)
	}
	for _, n := range doc.Nodes {
		if n.ID == nodeID {
			target = n
			break
		}
	}
	if target.ID == "" {
		return fail(ErrClassicMindMapNodeNotFound)
	}
	if target.Locked {
		return fail(ErrClassicMindMapLocked)
	}
	byEvidence := map[string]GroundedEvidence{}
	branchDigest := ""
	if in.UseBranch {
		_, _, nodes, e := classicMindMapBranchFromDocument(doc, ClassicMindMapBranchRequest{NodeRef: nodeID})
		if e != nil {
			return fail(e)
		}
		resolved, e := resolveWebMapBranchEvidenceTx(ctx, tx, nodes, false)
		if e != nil {
			return fail(e)
		}
		branchDigest = resolved.Digest
		for _, g := range resolved.Evidence {
			byEvidence[g.CitationID+"\x00"+g.EvidenceHash] = g
		}
	}
	var size int64
	for _, pin := range in.Entries {
		var rowSize int64
		if err = tx.QueryRowContext(ctx, `SELECT length(CAST(text AS BLOB))+length(embedding)+length(CAST(tags AS BLOB))+length(CAST(warnings AS BLOB)) FROM entries WHERE id=?`, pin.ID).Scan(&rowSize); err != nil {
			return fail(ErrQueryStale)
		}
		size += rowSize
		if size > 64<<20 || (!in.SourceOverview && size > 16<<20) {
			return fail(ErrWebMapAILimit)
		}
		entries, _, e := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, pin.ID)
		if e != nil {
			return fail(e)
		}
		if len(entries) != 1 {
			return fail(ErrQueryStale)
		}
		entry := entries[0]
		hit, e := queryHit(entry, 2048, DefaultAnswerLowConfidence)
		if e != nil {
			return fail(e)
		}
		if hit.Pin != pin.Pin {
			return fail(ErrQueryStale)
		}
		if _, e = EvidenceAnchorForEntry(entry, entry.Text); e != nil {
			return fail(ErrWebMapAIInvalid)
		}
		g := groundedEvidenceForEntry(entry, entry.Text, DefaultAnswerLowConfidence)
		byEvidence[g.CitationID+"\x00"+g.EvidenceHash] = g
	}
	maxEvidence, maxChars, maxBytes := 30, 60000, int64(16<<20)
	if in.SourceOverview {
		maxEvidence, maxChars, maxBytes = 512, 600000, 64<<20
	}
	if size > maxBytes {
		return fail(ErrWebMapSourceOverviewLimit)
	}
	if len(byEvidence) > maxEvidence {
		return fail(ErrWebMapAIInvalid)
	}
	total := 0
	for _, g := range byEvidence {
		total += utf8.RuneCountInString(g.Text)
		evidence = append(evidence, g)
	}
	if total > maxChars {
		return fail(ErrWebMapAIInvalid)
	}
	if len(evidence) == 0 && (!in.AllowUngrounded || in.Action == ClassicMindMapAIFindSources) {
		return fail(ErrWebMapBranchEmpty)
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].CitationID != evidence[j].CitationID {
			return evidence[i].CitationID < evidence[j].CitationID
		}
		return evidence[i].EvidenceHash < evidence[j].EvidenceHash
	})
	for i := range evidence {
		evidence[i].EvidenceRef = fmt.Sprintf("E%d", i+1)
	}
	g, err := loadEntryCacheGeneration(tx)
	if err != nil {
		return fail(err)
	}
	digest, err := classicMindMapAIDigest(struct {
		Map      string
		Evidence []GroundedEvidence
		Branch   string `json:",omitempty"`
	}{doc.Digest, evidence, branchDigest})
	if err != nil {
		return fail(err)
	}
	return doc, target, evidence, g, digest, nil
}

func StartWebMapAIIn(ctx context.Context, dir, owner, mapID, nodeID string, in WebMapAIInput) (applogResult0 WebMapAITask, applogResult1 error) {
	defer applog.Track("pkg.mem.StartWebMapAIIn")(&applogResult1)

	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.SourceOverview || in.SourceDocuments != 0 || !webMapExactID(mapID) || !webMapExactID(nodeID) || in.Revision <= 0 || len(in.RequestID) != 32 || strings.Trim(in.RequestID, "0123456789abcdef") != "" || !utf8.ValidString(in.Prompt) || in.Prompt == "" || utf8.RuneCountInString(in.Prompt) > 8000 || len(in.Entries) > 30 || (in.Action != ClassicMindMapAIExpand && in.Action != ClassicMindMapAIFill && in.Action != ClassicMindMapAIFindSources) {
		return WebMapAITask{}, ErrWebMapAIInvalid
	}
	for _, pin := range in.Entries {
		if pin.ID <= 0 || !validSHA256Digest(pin.Pin) {
			return WebMapAITask{}, ErrWebMapAIInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	payload, _ := json.Marshal(in)
	encoded, _ := json.Marshal(struct {
		Owner, Map, Node string
		Input            WebMapAIInput
	}{owner, mapID, nodeID, in})
	hash := prefixedSHA256(encoded)
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return WebMapAITask{}, err
	}
	defer db.Close()
	// Resolve a lost response before rechecking inputs that may have changed.
	if exists, e := webAITablesExist(ctx, db); e != nil {
		return WebMapAITask{}, e
	} else if exists {
		var prior string
		e = db.QueryRowContext(ctx, `SELECT request_hash FROM web_map_ai_tasks WHERE id=?`, in.RequestID).Scan(&prior)
		if e == nil {
			if prior != hash {
				return WebMapAITask{}, ErrWebMapAIChanged
			}
			return scanWebAITask(db.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=? AND owner=?`, in.RequestID, owner))
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return WebMapAITask{}, e
		}
	}
	_, _, _, generation, digest, err := webAIInputs(ctx, dir, mapID, nodeID, in)
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WebMapAITask{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, classicMindMapAISchema+webMapAISchema); err != nil {
		return WebMapAITask{}, err
	}
	var tasks, pending, runs int
	var stored int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status IN ('queued','running')),0) FROM web_map_ai_tasks`).Scan(&tasks, &pending); err != nil {
		return WebMapAITask{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_runs`).Scan(&runs); err != nil {
		return WebMapAITask{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(manifest_json AS BLOB))+length(CAST(proposals_json AS BLOB))),0) FROM mind_map_generation_previews`).Scan(&stored); err != nil {
		return WebMapAITask{}, err
	}
	if tasks >= 1000 || runs >= 1000 || pending >= 20 || stored > 48<<20 {
		return WebMapAITask{}, ErrWebMapAILimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_map_ai_tasks(id,owner,map_id,node_id,payload,request_hash,status,config_revision,input_digest,generation,created) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`, in.RequestID, owner, mapID, nodeID, string(payload), hash, cfg.Revision(), digest, generation, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return WebMapAITask{}, err
	}
	result, err := scanWebAITask(tx.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=?`, in.RequestID))
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func ListWebMapAIIn(ctx context.Context, dir, owner, mapID string) (applogResult0 []WebMapAITask, applogResult1 error) {
	defer applog.Track("pkg.mem.ListWebMapAIIn")(&applogResult1)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	items := []WebMapAITask{}
	if !webMapExactID(mapID) {
		return items, ErrWebMapAIInvalid
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return items, err
	}
	defer db.Close()
	if exists, e := webAITablesExist(ctx, db); e != nil || !exists {
		return items, e
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return items, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE owner=? AND map_id=? ORDER BY created DESC,id DESC LIMIT 30`, owner, mapID)
	if err != nil {
		return items, err
	}
	defer rows.Close()
	for rows.Next() {
		s, e := scanWebAITask(rows)
		if e != nil {
			return items, e
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return items, err
	}
	if err := rows.Close(); err != nil {
		return items, err
	}
	if err := addWebMapAIQueue(ctx, tx, owner, items); err != nil {
		return items, err
	}
	for i := range items {
		if items[i].CanResume, items[i].CompletedBatches, err = canResumeWebMapAI(ctx, tx, items[i]); err != nil {
			return items, err
		}
		items[i].SavedParts = items[i].CompletedBatches
		if items[i].CanResume {
			var exists, saved int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mind_map_generation_parts'`).Scan(&exists); err != nil {
				return items, err
			}
			if exists > 0 {
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_parts WHERE run_id=? AND status IN ('completed','insufficient')`, items[i].RunID).Scan(&saved); err != nil {
					return items, err
				}
				items[i].SavedParts = max(saved, items[i].CompletedBatches)
			}
		}
		if items[i].CanReassemble, err = canReassembleWebMapAI(ctx, tx, items[i]); err != nil {
			return items, err
		}
		if items[i].CanReassemble && strings.HasPrefix(items[i].Error, "Часть ") && strings.Contains(items[i].Error, "ответ модели не получен или не прошёл проверку") {
			items[i].Error = "Все части обработаны и сохранены. Ошибка возникла при итоговой сборке карты. Можно повторить сборку из готовых частей."
		}
	}
	return items, tx.Commit()
}

func CancelWebMapAIIn(ctx context.Context, dir, owner, mapID, id string) (applogResult0 error) {
	defer applog.Track("pkg.mem.CancelWebMapAIIn")(&applogResult0)

	if !webMapExactID(mapID) || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return ErrWebMapAIInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := scanWebAITask(tx.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=? AND owner=? AND map_id=?`, id, owner, mapID))
	if err != nil {
		return err
	}
	if task.Status == "cancelled" {
		return nil
	}
	if task.Status != "queued" && task.Status != "running" && task.Status != "ready" {
		return ErrWebMapAIChanged
	}
	if task.RunID != "" {
		var status string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM mind_map_generation_runs WHERE id=?`, task.RunID).Scan(&status); err != nil {
			return err
		}
		if status == string(ClassicMindMapAIPublished) {
			return ErrWebMapAIChanged
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mind_map_generation_runs SET status=? WHERE id=? AND status IN (?,?)`, ClassicMindMapAICancelled, task.RunID, ClassicMindMapAIRunning, ClassicMindMapAIPreviewReady); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='cancelled' WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

type WebMapAIReview struct {
	Task      WebMapAITask               `json:"task"`
	Digest    string                     `json:"digest"`
	Grounded  bool                       `json:"grounded"`
	Proposals []ClassicMindMapAIProposal `json:"proposals"`
	Synthesis *ClassicMindMapAISynthesis `json:"synthesis,omitempty"`
}

func loadWebMapAIPreview(ctx context.Context, db *sql.DB, owner, mapID, id string) (WebMapAITask, ClassicMindMapAIPreview, error) {
	task, err := scanWebAITask(db.QueryRowContext(ctx, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE id=? AND owner=? AND map_id=?`, id, owner, mapID))
	if err != nil {
		return task, ClassicMindMapAIPreview{}, err
	}
	if task.Status != "ready" && task.Status != "applied" {
		return task, ClassicMindMapAIPreview{}, ErrWebMapAIChanged
	}
	var size int64
	if err = db.QueryRowContext(ctx, `SELECT length(CAST(manifest_json AS BLOB))+length(CAST(proposals_json AS BLOB)) FROM mind_map_generation_previews WHERE run_id=?`, task.RunID).Scan(&size); err != nil {
		return task, ClassicMindMapAIPreview{}, err
	}
	if size > 4<<20 {
		return task, ClassicMindMapAIPreview{}, ErrWebMapAILimit
	}
	store := &Store{db: db}
	preview, err := store.LoadClassicMindMapAIPreview(task.RunID)
	if err != nil {
		return task, preview, err
	}
	if preview.TargetMapID != mapID || preview.TargetNodeID != task.NodeID || preview.BaseRevision != task.Input.Revision {
		return task, preview, ErrWebMapAIChanged
	}
	return task, preview, nil
}
func ReadWebMapAIReviewIn(ctx context.Context, dir, owner, mapID, id string) (applogResult0 WebMapAIReview, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebMapAIReviewIn")(&applogResult1)

	if !webMapExactID(mapID) || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return WebMapAIReview{}, ErrWebMapAIInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return WebMapAIReview{}, err
	}
	defer db.Close()
	task, preview, err := loadWebMapAIPreview(ctx, db, owner, mapID, id)
	if err != nil {
		return WebMapAIReview{}, err
	}
	for i := range preview.Proposals {
		for j := range preview.Proposals[i].Evidence {
			a := &preview.Proposals[i].Evidence[j]
			a.SourcePath = sourceName(Entry{SourcePath: a.SourcePath})
		}
	}
	return WebMapAIReview{Task: task, Digest: preview.ProposalDigest, Grounded: preview.Grounded, Proposals: preview.Proposals, Synthesis: preview.Synthesis}, nil
}

type WebMapAIApply struct {
	RequestID   string   `json:"request_id"`
	Revision    int64    `json:"revision"`
	Digest      string   `json:"digest"`
	ProposalIDs []string `json:"proposal_ids"`
}

func ApplyWebMapAIIn(ctx context.Context, dir, owner, mapID, id string, in WebMapAIApply) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.ApplyWebMapAIIn")(&applogResult1)

	if !webMapExactID(mapID) || len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" || len(in.ProposalIDs) == 0 || len(in.ProposalIDs) > 1000 || len(in.Digest) > 128 {
		return WebMapNodeEdited{}, ErrWebMapAIInvalid
	}
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return WebMapNodeEdited{}, err
	}
	task, preview, err := loadWebMapAIPreview(ctx, db, owner, mapID, id)
	db.Close()
	if err != nil {
		return WebMapNodeEdited{}, err
	}
	if in.Digest != preview.ProposalDigest || in.Revision != preview.BaseRevision {
		return WebMapNodeEdited{}, ErrWebMapAIChanged
	}
	selected, ids, err := selectClassicMindMapAIProposals(preview, in.ProposalIDs)
	if err != nil {
		return WebMapNodeEdited{}, ErrWebMapAIInvalid
	}
	return runWebMapMutation(ctx, dir, owner, mapID, in.Revision, in.RequestID, struct {
		ID    string
		Input WebMapAIApply
	}{id, in}, "ai:"+string(preview.Action), func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		// Graph dependencies can change without changing the classic map revision.
		// Recheck inside the publication transaction, including the ready interval.
		if task.Input.UseBranch {
			_, _, _, _, digest, e := webAIInputsTx(ctx, tx, mapID, task.NodeID, task.Input)
			if e != nil {
				return "", e
			}
			if digest != task.InputDigest {
				return "", ErrWebMapAIChanged
			}
		}
		if task.SourceOverview {
			generation, e := loadEntryCacheGeneration(tx)
			if e != nil {
				return "", e
			}
			if generation != task.Generation {
				return "", ErrWebMapAIChanged
			}
		}
		for _, g := range preview.Evidence {
			a, e := evidenceAnchorFromGrounded(g)
			if e != nil {
				return "", e
			}
			if e = boundWebMapEvidence(tx, a); e != nil {
				return "", e
			}
		}
		var status string
		if e := tx.QueryRowContext(ctx, `SELECT status FROM web_map_ai_tasks WHERE id=? AND owner=? AND run_id=?`, id, owner, task.RunID).Scan(&status); e != nil {
			return "", e
		}
		if status != "ready" {
			return "", ErrWebMapAIChanged
		}
		var publication int64
		nodeID, e := publishExistingClassicMindMapPreviewTx(tx, item, preview, selected, ids, owner, "применено через веб", &publication)
		if e != nil {
			return "", e
		}
		_, e = tx.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='applied' WHERE id=?`, id)
		return nodeID, e
	})
}
