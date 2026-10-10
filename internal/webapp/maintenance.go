package webapp

import (
	"encoding/json"
	"errors"
	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) maintenanceRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, p []string) {
	base, err := a.manager.Resolve(r.Context(), actor, p[3])
	if err != nil {
		a.fail(w, err)
		return
	}
	dir := filepath.Join(base.Root, mem.MemDirName)
	if len(p) == 7 && p[6] == "result" && r.Method == "GET" {
		result, e := mem.ReadWebOperationResultIn(r.Context(), dir, actor.ID, p[5])
		if e != nil {
			a.maintenanceError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if len(p) == 5 {
		if r.Method == "GET" {
			items, e := mem.ListWebOperationsIn(r.Context(), dir, actor.ID)
			if e != nil {
				a.fail(w, e)
				return
			}
			writeJSON(w, 200, items)
			return
		}
		if r.Method == "POST" {
			var in mem.WebOperationInput
			if !decode(w, r, &in) {
				return
			}
			op, e := mem.StartWebOperationIn(r.Context(), dir, actor.ID, in, a.cfg.Local)
			if e != nil {
				a.maintenanceError(w, e)
				return
			}
			writeJSON(w, 202, op)
			return
		}
	}
	if len(p) == 7 && r.Method == "POST" && (p[6] == "cancel" || p[6] == "retry") {
		if err = mem.ActWebOperationIn(r.Context(), dir, actor.ID, p[5], p[6]); err != nil {
			a.maintenanceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if len(p) == 6 && r.Method == "POST" {
		a.graphTool(w, r, dir, actor.ID, p[5])
		return
	}
	writeError(w, 405, "Метод не поддерживается")
}
func (a *App) maintenanceError(w http.ResponseWriter, err error) {
	applog.Error("web.maintenance_failed", err)
	if errors.Is(err, mem.ErrKnowledgeReviewPinRequired) {
		writeError(w, 400, "План не закрепляет проверенную формулировку. Получите свежий отчёт состояния графа и добавьте expected_review_digest для каждого объекта.")
		return
	}
	if errors.Is(err, mem.ErrKnowledgeContentChanged) || errors.Is(err, mem.ErrKnowledgeEvidenceChanged) || errors.Is(err, mem.ErrKnowledgeReviewChanged) {
		writeError(w, 409, "Текст, источники или состояние проверки изменились. Обновите отчёт и заново проверьте план подтверждения.")
		return
	}
	if errors.Is(err, mem.ErrPortableBusy) || errors.Is(err, mem.ErrDocumentUpdateStateChanged) || errors.Is(err, mem.ErrSourceFileChanged) {
		writeError(w, 409, "Данные изменились или обработка ещё выполняется. Обновите страницу и повторите после завершения заданий.")
		return
	}
	if errors.Is(err, mem.ErrWebGraphAnswerModel) || errors.Is(err, mem.ErrWebOperationInvalid) || errors.Is(err, mem.ErrPortableInvalid) || errors.Is(err, mem.ErrSourceFileUnverified) {
		writeError(w, 400, err.Error())
		return
	}
	// Core error details go to the structured log, not to a hosted client's paths.
	a.mapError(w, err)
}

type graphToolInput struct {
	Document string          `json:"document"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	RunID    string          `json:"run_id"`
	Digest   string          `json:"digest"`
	Confirm  bool            `json:"confirm"`
	Manifest json.RawMessage `json:"manifest"`
	Days     int             `json:"days"`
}

func (a *App) graphTool(w http.ResponseWriter, r *http.Request, dir, actor, action string) {
	var in graphToolInput
	if !decodeLimit(w, r, &in, 160<<10) {
		return
	}
	if len(in.Document) > 512 || len(in.From) > 256 || len(in.To) > 256 || len(in.RunID) > 256 || len(in.Digest) > 128 {
		writeError(w, 400, "Слишком длинный параметр")
		return
	}
	release, ok := a.reserveQuery(w, workspace.Actor{ID: actor})
	if !ok {
		return
	}
	defer release()
	s, err := mem.OpenWebGraphIn(r.Context(), dir)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer s.Close()
	var out any
	switch action {
	case "coverage":
		out, err = s.BuildKnowledgeCoverageReport(mem.KnowledgeCoverageOptions{Document: in.Document})
	case "status":
		out, err = s.ReviewKnowledgeGraph()
	case "diff":
		out, err = s.BuildKnowledgeRevisionDiff(mem.KnowledgeRevisionDiffOptions{Document: in.Document})
	case "snapshots":
		out, err = s.ListDocumentHistorySnapshots(in.Document)
	case "corpus-diff":
		out, err = s.BuildCorpusRevisionDiff(in.Document, in.From, in.To)
	case "restore":
		if in.Confirm {
			out, err = s.ApplyDocumentRestore(in.Document, in.To, in.Digest)
		} else {
			out, err = s.BuildDocumentRestorePlan(in.Document, in.To)
		}
	case "rollback":
		if in.Confirm {
			out, err = s.ApplyDocumentRestoreRollback(in.RunID, in.Digest)
		} else {
			out, err = s.BuildDocumentRestoreRollbackPlan(in.RunID)
		}
	case "restore-runs":
		out, err = s.ListDocumentRestoreRuns(100)
	case "extract-runs":
		out, err = s.ListKnowledgeExtractionRuns(100)
	case "extract-run":
		out, err = s.LoadKnowledgeExtractionRun(in.RunID)
	case "analysis-runs":
		out, err = s.ListCorpusAnalysisRuns(100, "")
	case "analysis-run":
		out, err = s.LoadCorpusAnalysisRun(in.RunID)
	case "reviews":
		out, err = s.ListKnowledgeReviews(100)
	case "edits":
		out, err = s.ListKnowledgeEdits(100)
	case "merges":
		out, err = s.ListKnowledgeNodeMerges(100)
	case "profile":
		out, err = s.ProfileKnowledgeMap(mem.KnowledgeMapProfileOptions{Iterations: 1})
	case "eval":
		manifest, e := mem.ParseKnowledgeQualityManifest(in.Manifest)
		err = e
		if err == nil {
			out, err = s.EvaluateKnowledgeQuality(manifest, 0.6)
		}
	case "eval-labeled":
		manifest, e := mem.ParseKnowledgeLabeledEvalManifest(in.Manifest)
		err = e
		if err == nil {
			out, err = s.EvaluateKnowledgeLabeledQuality(manifest)
		}
	case "merge":
		var request mem.KnowledgeNodeMergeRequest
		err = json.Unmarshal(in.Manifest, &request)
		if err == nil {
			out, err = s.MergeKnowledgeDuplicate(request)
		}
	case "approve-batch":
		var requests []mem.KnowledgeApprovalRequest
		err = json.Unmarshal(in.Manifest, &requests)
		if err == nil {
			out, err = s.ApproveKnowledgeObjects(requests)
		}
	case "prune-runs":
		if in.Days < 1 || in.Days > 3650 {
			writeError(w, 400, "Срок хранения: от 1 до 3650 дней")
			return
		}
		out, err = s.PruneCompletedCorpusAnalysisRuns(time.Now().Add(-time.Duration(in.Days)*24*time.Hour), 10, !in.Confirm)
	default:
		writeError(w, 404, "Операция не найдена")
		return
	}
	if err != nil {
		a.maintenanceError(w, err)
		return
	}
	if strings.HasSuffix(action, "runs") && out == nil {
		out = []any{}
	}
	writeJSON(w, 200, out)
}
