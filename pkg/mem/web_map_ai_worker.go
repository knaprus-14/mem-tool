package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

func ProcessWebMapAIIn(ctx context.Context, dir, owner string) (bool, error) {
	return processWebMapAIIn(ctx, dir, owner, nil)
}

func processWebMapAIIn(parent context.Context, dir, owner string, provider AnswerProvider) (bool, error) {
	db, err := openQueryDB(parent, dir)
	if err != nil {
		return false, err
	}
	exists, err := webAITablesExist(parent, db)
	db.Close()
	if err != nil || !exists {
		return false, err
	}
	lock, err := filelock.Try(filepath.Join(dir, "web-map-ai.lock"))
	if errors.Is(err, filelock.ErrBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	db, err = sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return false, err
	}
	defer db.Close()
	task, err := scanWebAITask(db.QueryRowContext(parent, `SELECT `+webAITaskColumns+` FROM web_map_ai_tasks WHERE owner=? AND `+webMapAIProcessable+` ORDER BY created,id LIMIT 1`, owner))
	if errors.Is(err, ErrClassicMindMapAIPreviewNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	applog.Event("INFO", "ai.task.claimed", applog.Fields{"task_id": task.ID, "map_id": task.MapID, "status": task.Status, "source_overview": task.SourceOverview})
	finish := func(status, message string) error {
		level := "INFO"
		if status == "failed" {
			level = "ERROR"
		}
		applog.Event(level, "ai.task.finish", applog.Fields{"task_id": task.ID, "status": status, "current": task.Current, "total": task.Total})
		_, e := db.ExecContext(parent, `UPDATE web_map_ai_tasks SET status=?,error=? WHERE id=? AND status IN ('queued','running')`, status, message, task.ID)
		return e
	}
	if task.Status == "ready" && task.SourceOverview {
		return true, applyWebSourceOverview(parent, dir, owner, task, db)
	}
	if task.Status == "running" {
		// The OS lock is available, so a persisted running task has lost its worker.
		// Recover a committed preview, or resume source extraction in the same run.
		var status string
		if task.RunID != "" && db.QueryRowContext(parent, `SELECT r.status FROM mind_map_generation_runs r JOIN mind_map_generation_previews p ON p.run_id=r.id WHERE r.id=?`, task.RunID).Scan(&status) == nil && status == string(ClassicMindMapAIPreviewReady) {
			return true, finish("ready", "")
		}
		if !task.SourceOverview {
			return true, finish("failed", "Сервис был остановлен во время генерации. Запустите новое предложение; карта не изменена.")
		}
		if _, err = db.ExecContext(parent, `UPDATE web_map_ai_tasks SET status='queued',phase='resuming' WHERE id=? AND status='running'`, task.ID); err != nil {
			return true, err
		}
		task.Status, task.Phase = "queued", "resuming"
	}
	budget := 180 * time.Second
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	doc, target, evidence, g, digest, err := webAIInputs(ctx, dir, task.MapID, task.NodeID, task.Input)
	if err != nil || g != task.Generation || digest != task.InputDigest {
		return true, finish("failed", "Карта или выбранные источники изменились. Создайте новое предложение.")
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil || cfg.Revision() != task.ConfigRevision {
		return true, finish("failed", "Настройки изменились. Создайте новое предложение с текущей моделью.")
	}
	var stored int64
	var runs int
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(manifest_json AS BLOB))+length(CAST(proposals_json AS BLOB))),0) FROM mind_map_generation_previews`).Scan(&stored); err != nil {
		return true, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mind_map_generation_runs`).Scan(&runs); err != nil {
		return true, err
	}
	resuming := task.SourceOverview && task.Phase == "resuming" && task.RunID != ""
	if stored > 48<<20 || (runs >= 1000 && !resuming) {
		return true, finish("failed", "Достигнут лимит сохранённых предложений AI.")
	}
	answer := cfg.Answer.WithMapGenerationDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 90)
	tokenCeiling := min(answer.MaxTokens, maxClassicMindMapAITokens)
	answer.MaxTokens = min(tokenCeiling, DefaultMapGenerationTokens)
	answer.ContextChars = min(answer.ContextChars, 24000)
	request := ClassicMindMapAIPreviewRequest{Action: task.Input.Action, Prompt: task.Input.Prompt, MapRef: task.MapID, NodeRef: task.NodeID, ExpectedRevision: task.Input.Revision, Scope: ClassicMindMapAIScope{AllowUngrounded: task.Input.AllowUngrounded}}
	if task.SourceOverview {
		// Use the same deterministic plan as generation, including evidence refs.
		// The input-preflight deadline must not cap the subsequent long job.
		for i := range evidence {
			evidence[i].EvidenceRef = fmt.Sprintf("E%d", i+1)
		}
		_, e := fitClassicMindMapAIEvidence(request, doc, target, evidence, answer.ContextChars)
		if e != nil {
			return true, finish("failed", webMapAIFailureMessage(e, 0, 0))
		}
		if e = ctx.Err(); e != nil {
			return true, finish("failed", "Истекло время подготовки источников. Попробуйте новое задание.")
		}
		budget = 0 // Source jobs persist across sessions; only individual calls expire.
		cancel()
		ctx, cancel = context.WithCancel(parent)
		defer cancel()
	}
	if provider == nil {
		provider, err = NewOllamaAnswerProvider(answer)
		if err != nil {
			return true, finish("failed", "Модель ответов недоступна. Проверьте настройки.")
		}
	}
	result, err := db.ExecContext(ctx, `UPDATE web_map_ai_tasks SET status='running',phase='planned' WHERE id=? AND status='queued'`, task.ID)
	if err != nil {
		return true, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return true, nil
	}
	applog.Event("INFO", "ai.task.started", applog.Fields{"task_id": task.ID, "map_id": task.MapID, "budget_seconds": int(budget.Seconds()), "request_timeout_seconds": answer.TimeoutSeconds, "token_ceiling": tokenCeiling, "model_id": applog.Ref(answer.Model)})
	// Abort the provider request promptly when a browser cancels the durable task.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var status string
				if db.QueryRowContext(ctx, `SELECT status FROM web_map_ai_tasks WHERE id=?`, task.ID).Scan(&status) == nil && status == "cancelled" {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	store := &Store{db: db, path: StorePathIn(dir)}
	service := &ClassicMindMapAIService{Store: store, Provider: webAIPrivatePathProvider(provider, doc, evidence, tokenCeiling), Config: answer}
	reuseRunID := ""
	if task.SourceOverview && (task.Phase == "reassembling" || resuming) {
		reuseRunID = task.RunID
	}
	preview, err := store.prepareClassicMindMapAIFromCheckpoints(ctx, service, request, doc, target, evidence, func(p ClassicMindMapAIProgress) {
		task.Current, task.Total, task.RunID = p.Current, p.Total, p.RunID
		task.Phase = p.Phase
		_, e := db.ExecContext(ctx, `UPDATE web_map_ai_tasks SET run_id=?,phase=?,current=?,total=? WHERE id=? AND status='running'`, p.RunID, p.Phase, p.Current, p.Total, task.ID)
		if e != nil {
			cancel()
		}
	}, reuseRunID, resuming)
	if parent.Err() != nil {
		return true, parent.Err()
	}
	if err != nil {
		applog.Error("ai.task.error", err)
		applog.Event("ERROR", "ai.task.timeout_state", applog.Fields{"task_id": task.ID, "run_id": task.RunID, "current": task.Current, "total": task.Total, "job_deadline": errors.Is(ctx.Err(), context.DeadlineExceeded), "request_deadline": errors.Is(err, context.DeadlineExceeded), "budget_seconds": int(budget.Seconds())})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &webMapAIJobTimeout{Budget: budget}
		}
		return true, finish("failed", webMapAIFailureAtPhase(err, task.Phase, task.Current, task.Total))
	}
	if len(preview.Proposals) == 0 {
		return true, finish("failed", "Модель не предложила изменений по выбранным материалам.")
	}
	return true, finish("ready", "")
}

type webMapAIJobTimeout struct{ Budget time.Duration }

func (e *webMapAIJobTimeout) Error() string { return "web map AI job time budget exhausted" }
func (e *webMapAIJobTimeout) Unwrap() (applogResult0 error) {
	defer applog.Track("pkg.mem.webMapAIJobTimeout.Unwrap")(&applogResult0)
	return context.DeadlineExceeded
}

type webAIProvider struct {
	provider  AnswerProvider
	replace   *strings.Replacer
	maxTokens int
}

func (p webAIProvider) MaxOutputTokens() int { return p.maxTokens }

func webMapAIFailureAtPhase(err error, phase string, current, total int) string {
	if phase != "merging" && phase != "synthesizing" && phase != "validating" {
		return webMapAIFailureMessage(err, current, total)
	}
	var limit *classicMindMapAISynthesisContextError
	if errors.As(err, &limit) {
		return fmt.Sprintf("Сборка карты: группировка не укладывается в контекст модели (%d символов при лимите %d). Готовые части сохранены. Карта не изменена.", limit.Needed, limit.Budget)
	}
	if errors.Is(err, ErrClassicMindMapAIResourceLimit) {
		return "Сборка карты: превышен лимит размера или глубины карты. Готовые части сохранены; карта не изменена."
	}
	return "Сборка карты: " + webMapAIFailureMessage(err, 0, 0) + " Готовые части сохранены; доступен повтор сборки."
}

func webMapAIFailureMessage(err error, current, total int) string {
	prefix := ""
	if current > 0 && total > 0 {
		prefix = fmt.Sprintf("Часть %d из %d: ", current, total)
	}
	var jobTimeout *webMapAIJobTimeout
	if errors.As(err, &jobTimeout) {
		return prefix + fmt.Sprintf("истёк общий лимит задания (%d мин.). Готовые части сохранены в журнале; карта не изменена.", int(jobTimeout.Budget/time.Minute))
	}
	if limit, ok := AnswerTokenLimit(err); ok {
		return prefix + fmt.Sprintf("модели не хватило %d токенов для полного ответа на порцию. Готовые части сохранены; карта не изменена. Проверьте модель и её лимит ответа перед продолжением.", limit)
	}
	var transient *AnswerTransientError
	if errors.As(err, &transient) {
		return prefix + "модель временно недоступна или ограничила частоту запросов. Готовые части сохранены; продолжите с сохранённого места позже. Карта не изменена."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return prefix + "истекло время ожидания одного ответа модели (для веб-карт — не более 90 секунд). Проверьте доступность модели либо выберите более быструю модель. Карта не изменена."
	}
	if errors.Is(err, ErrAnswerContextLimit) {
		return prefix + "минимальная порция не помещается в контекст модели. Готовые части сохранены; карта не изменена."
	}
	return prefix + "ответ модели не получен или не прошёл проверку. Карта не изменена; готовые части сохранены. Проверьте доступность модели."
}

func (p webAIProvider) Generate(ctx context.Context, in AnswerRequest) (applogResult0 string, applogResult1 error) {
	defer applog.Track("pkg.mem.webAIProvider.Generate")(&applogResult1)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	if in.MaxTokens > p.maxTokens {
		return "", &AnswerTokenLimitError{MaxTokens: p.maxTokens}
	}
	in.Prompt = p.replace.Replace(in.Prompt)
	in.System = p.replace.Replace(in.System)
	return p.provider.Generate(ctx, in)
}

// Keep real provenance in the immutable preview, but remove filesystem paths
// from the outgoing model prompt, including JSON-escaped Windows paths.
func webAIPrivatePathProvider(provider AnswerProvider, doc ClassicMindMapDocument, evidence []GroundedEvidence, tokenCeiling int) AnswerProvider {
	paths := map[string]string{}
	for _, e := range evidence {
		if e.SourcePath != "" {
			paths[e.SourcePath] = sourceName(Entry{SourcePath: e.SourcePath})
		}
	}
	for _, n := range doc.Nodes {
		for _, s := range n.Sources {
			if s.Kind == ClassicMindMapSourceExternalFile && s.Locator != "" {
				paths[s.Locator] = "Локальный файл"
			}
			if s.Evidence != nil && s.Evidence.SourcePath != "" {
				paths[s.Evidence.SourcePath] = sourceName(Entry{SourcePath: s.Evidence.SourcePath})
			}
		}
	}
	keys := make([]string, 0, len(paths))
	for path := range paths {
		keys = append(keys, path)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := []string{}
	for _, path := range keys {
		encoded, _ := json.Marshal(path)
		value, _ := json.Marshal(paths[path])
		pairs = append(pairs, string(encoded[1:len(encoded)-1]), string(value[1:len(value)-1]), path, paths[path])
	}
	return webAIProvider{provider: provider, replace: strings.NewReplacer(pairs...), maxTokens: tokenCeiling}
}

// Publishing is automatic only for an explicitly requested new source map.
// Existing-map AI proposals always retain their manual review contract.
func applyWebSourceOverview(ctx context.Context, dir, owner string, task WebMapAITask, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	review, err := ReadWebMapAIReviewIn(ctx, dir, owner, task.MapID, task.ID)
	if err == nil {
		ids := make([]string, 0, len(review.Proposals))
		for _, p := range review.Proposals {
			ids = append(ids, p.ID)
		}
		nonce := prefixedSHA256([]byte("source-map-publication:" + task.ID))[7:39]
		_, err = ApplyWebMapAIIn(ctx, dir, owner, task.MapID, task.ID, WebMapAIApply{RequestID: nonce, Revision: review.Task.Revision, Digest: review.Digest, ProposalIDs: ids})
	}
	if err == nil {
		applog.Event("INFO", "ai.task.published", applog.Fields{"task_id": task.ID, "map_id": task.MapID, "run_id": task.RunID})
	} else {
		applog.Error("ai.task.publication_failed", err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, saveErr := db.ExecContext(ctx, `UPDATE web_map_ai_tasks SET error=? WHERE id=? AND status='ready'`, "Предложение сохранено, но автоматическое применение не удалось: карта или источники могли измениться либо достигнут размер карты. Откройте предложения AI для просмотра.", task.ID)
		return saveErr
	}
	return nil
}
