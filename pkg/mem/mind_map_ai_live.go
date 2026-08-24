package mem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxClassicMindMapAIWorkspaceJobs       = 32
	maxClassicMindMapAIWorkspaceConcurrent = 4
	maxClassicMindMapAIHTTPExcerptRunes    = 240
)

// ClassicMindMapAIWorkspace owns only ephemeral progress/cancellation state.
// Validated previews and publications themselves are durable SQLite records.
type ClassicMindMapAIWorkspace struct {
	Context context.Context
	Service *ClassicMindMapAIService

	mu   sync.RWMutex
	jobs map[string]*classicMindMapAIWorkspaceJob
}

type classicMindMapAIWorkspaceJob struct {
	ID      string                       `json:"id"`
	RunID   string                       `json:"run_id,omitempty"`
	Status  string                       `json:"status"`
	Phase   string                       `json:"phase,omitempty"`
	Current int                          `json:"current,omitempty"`
	Total   int                          `json:"total,omitempty"`
	Batch   int                          `json:"batch,omitempty"`
	Batches int                          `json:"batches,omitempty"`
	Message string                       `json:"message,omitempty"`
	Error   string                       `json:"error,omitempty"`
	Preview *classicMindMapAIHTTPPreview `json:"preview,omitempty"`
	Started string                       `json:"started"`
	Updated string                       `json:"updated"`
	cancel  context.CancelFunc
}

// classicMindMapAIHTTPPreview is intentionally not the durable preview. The
// browser needs proposal text, selectable IDs, publication guards and short
// human-readable citations; it must not receive the full evidence manifest or
// repeat complete chunk excerpts in every proposal anchor.
type classicMindMapAIHTTPPreview struct {
	Version           int                            `json:"version"`
	RunID             string                         `json:"run_id"`
	Status            ClassicMindMapAIRunStatus      `json:"status"`
	Action            ClassicMindMapAIAction         `json:"action"`
	TargetMapID       string                         `json:"target_map_id,omitempty"`
	TargetNodeID      string                         `json:"target_node_id,omitempty"`
	BaseRevision      int64                          `json:"base_revision,omitempty"`
	Grounded          bool                           `json:"grounded"`
	Proposals         []classicMindMapAIHTTPProposal `json:"proposals"`
	ProposalDigest    string                         `json:"proposal_digest"`
	Title             string                         `json:"title,omitempty"`
	Description       string                         `json:"description,omitempty"`
	EvidenceCount     int                            `json:"evidence_count"`
	CitationCount     int                            `json:"citation_count"`
	BatchCount        int                            `json:"batch_count"`
	CorrectionRetries int                            `json:"correction_retries,omitempty"`
	Created           string                         `json:"created"`
}

type classicMindMapAIHTTPProposal struct {
	ID               string                         `json:"id"`
	ParentProposalID string                         `json:"parent_proposal_id,omitempty"`
	Label            string                         `json:"label,omitempty"`
	Summary          string                         `json:"summary,omitempty"`
	BodyMarkdown     string                         `json:"body_markdown,omitempty"`
	Kind             ClassicMindMapNodeKind         `json:"kind,omitempty"`
	Evidence         []classicMindMapAIHTTPCitation `json:"evidence,omitempty"`
	EvidenceCount    int                            `json:"evidence_count"`
	Reason           string                         `json:"reason,omitempty"`
}

type classicMindMapAIHTTPCitation struct {
	CitationID      string `json:"citation_id,omitempty"`
	DocumentTitle   string `json:"document_title,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
	Page            int    `json:"page"`
	BlockIndex      int    `json:"block_index"`
	BlockChunkIndex int    `json:"block_chunk_index"`
	Excerpt         string `json:"excerpt,omitempty"`
}

type classicMindMapAIStartRequest struct {
	Action           ClassicMindMapAIAction `json:"action"`
	MapID            string                 `json:"map_id"`
	TargetNodeID     string                 `json:"target_node_id"`
	ExpectedRevision int64                  `json:"expected_revision"`
	Prompt           string                 `json:"prompt"`
	Title            string                 `json:"title"`
	Description      string                 `json:"description"`
	Scope            ClassicMindMapAIScope  `json:"scope"`
}

type classicMindMapAIJobRequest struct {
	JobID string `json:"job_id"`
}

type classicMindMapAIPublishRequest struct {
	PreviewID        string   `json:"preview_id"`
	ProposalIDs      []string `json:"proposal_ids"`
	ExpectedRevision int64    `json:"expected_revision"`
	ExpectedDigest   string   `json:"expected_preview_digest"`
}

var errClassicMindMapAICancelTooLate = errors.New("AI-задание уже завершило подготовку предпросмотра; отмена не выполнена")

func NewClassicMindMapAIWorkspace(ctx context.Context, service *ClassicMindMapAIService) *ClassicMindMapAIWorkspace {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ClassicMindMapAIWorkspace{Context: ctx, Service: service, jobs: make(map[string]*classicMindMapAIWorkspaceJob)}
}

func (w *ClassicMindMapAIWorkspace) start(request ClassicMindMapAIPreviewRequest) (classicMindMapAIWorkspaceJob, error) {
	if w == nil || w.Service == nil || w.Service.Store == nil || w.Service.Provider == nil {
		return classicMindMapAIWorkspaceJob{}, errors.New("AI-помощник недоступен: настройте локальную answer-модель и перезапустите редактор")
	}
	id, err := newClassicMindMapID("mmj-")
	if err != nil {
		return classicMindMapAIWorkspaceJob{}, err
	}
	ctx, cancel := context.WithCancel(w.Context)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := &classicMindMapAIWorkspaceJob{ID: id, Status: "running", Phase: "queued", Message: "Запрос поставлен в очередь", Started: now, Updated: now, cancel: cancel}
	w.mu.Lock()
	running := 0
	for _, current := range w.jobs {
		if current.Status == "running" {
			running++
		}
	}
	if running >= maxClassicMindMapAIWorkspaceConcurrent {
		w.mu.Unlock()
		cancel()
		return classicMindMapAIWorkspaceJob{}, fmt.Errorf("AI-помощник уже выполняет %d задания; дождитесь завершения или отмените одно из них", maxClassicMindMapAIWorkspaceConcurrent)
	}
	if len(w.jobs) >= maxClassicMindMapAIWorkspaceJobs {
		w.dropOldestFinishedLocked()
	}
	if len(w.jobs) >= maxClassicMindMapAIWorkspaceJobs {
		w.mu.Unlock()
		cancel()
		return classicMindMapAIWorkspaceJob{}, fmt.Errorf("AI-помощник уже выполняет предельное число заданий (%d); дождитесь завершения или отмените одно из них", maxClassicMindMapAIWorkspaceJobs)
	}
	w.jobs[id] = job
	w.mu.Unlock()
	accepted := cloneClassicMindMapAIWorkspaceJob(job)

	go func() {
		preview, runErr := PrepareClassicMindMapAIPreview(ctx, w.Service, request, func(progress ClassicMindMapAIProgress) {
			w.mu.Lock()
			defer w.mu.Unlock()
			current := w.jobs[id]
			if current == nil || current.Status == "cancelled" {
				return
			}
			current.RunID = progress.RunID
			current.Phase = progress.Phase
			current.Current, current.Total = progress.Current, progress.Total
			current.Batch, current.Batches = progress.Current, progress.Total
			current.Message = progress.Message
			current.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		})
		w.mu.Lock()
		defer w.mu.Unlock()
		current := w.jobs[id]
		if current == nil || current.Status == "cancelled" {
			return
		}
		current.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				current.Status, current.Phase, current.Message = "cancelled", "cancelled", "Операция отменена"
			} else {
				current.Status, current.Phase, current.Error = "failed", "failed", runErr.Error()
			}
			return
		}
		current.RunID = preview.RunID
		current.Status, current.Phase = string(preview.Status), string(preview.Status)
		current.Message = "Предпросмотр готов"
		httpPreview := classicMindMapAIHTTPPreviewFrom(preview, current.Total)
		current.Preview = &httpPreview
	}()
	return accepted, nil
}

func (w *ClassicMindMapAIWorkspace) get(id string) (classicMindMapAIWorkspaceJob, bool) {
	if w == nil {
		return classicMindMapAIWorkspaceJob{}, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	job := w.jobs[strings.TrimSpace(id)]
	if job == nil {
		return classicMindMapAIWorkspaceJob{}, false
	}
	return cloneClassicMindMapAIWorkspaceJob(job), true
}

func (w *ClassicMindMapAIWorkspace) cancelJob(id string) (classicMindMapAIWorkspaceJob, bool, error) {
	if w == nil {
		return classicMindMapAIWorkspaceJob{}, false, nil
	}
	w.mu.Lock()
	job := w.jobs[strings.TrimSpace(id)]
	if job == nil {
		w.mu.Unlock()
		return classicMindMapAIWorkspaceJob{}, false, nil
	}
	if job.Status != "running" {
		status := job.Status
		result := cloneClassicMindMapAIWorkspaceJob(job)
		w.mu.Unlock()
		if status == "cancelled" {
			return result, true, nil
		}
		return result, true, errClassicMindMapAICancelTooLate
	}
	runID := job.RunID
	cancel := job.cancel
	if cancel != nil {
		cancel()
	}
	if runID == "" {
		job.Status, job.Phase, job.Message = "cancelled", "cancelled", "Операция отменена пользователем"
		job.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		result := cloneClassicMindMapAIWorkspaceJob(job)
		w.mu.Unlock()
		return result, true, nil
	}
	w.mu.Unlock()
	if w.Service == nil || w.Service.Store == nil {
		return classicMindMapAIWorkspaceJob{}, true, errors.New("AI-помощник не может сохранить отмену: хранилище недоступно")
	}
	durableStatus, _, err := w.Service.Store.cancelClassicMindMapAIRun(runID)
	if err != nil {
		return classicMindMapAIWorkspaceJob{}, true, fmt.Errorf("сохранить отмену AI-задания: %w", err)
	}

	var durablePreview *classicMindMapAIHTTPPreview
	if durableStatus == ClassicMindMapAIPreviewReady || durableStatus == ClassicMindMapAIInsufficient || durableStatus == ClassicMindMapAIPublished {
		preview, loadErr := w.Service.Store.LoadClassicMindMapAIPreview(runID)
		if loadErr != nil {
			return classicMindMapAIWorkspaceJob{}, true, fmt.Errorf("восстановить завершённый AI-preview после отмены: %w", loadErr)
		}
		httpPreview := classicMindMapAIHTTPPreviewFrom(preview, preview.BatchCount)
		durablePreview = &httpPreview
	}

	w.mu.Lock()
	job = w.jobs[strings.TrimSpace(id)]
	if job == nil {
		w.mu.Unlock()
		return classicMindMapAIWorkspaceJob{}, false, nil
	}
	job.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	if durableStatus == ClassicMindMapAICancelled {
		job.Status, job.Phase, job.Message = "cancelled", "cancelled", "Операция отменена пользователем"
		job.Preview = nil
		result := cloneClassicMindMapAIWorkspaceJob(job)
		w.mu.Unlock()
		return result, true, nil
	}
	job.Status, job.Phase = string(durableStatus), string(durableStatus)
	job.Message = "AI-задание уже завершилось; отмена не применена"
	job.Preview = durablePreview
	result := cloneClassicMindMapAIWorkspaceJob(job)
	w.mu.Unlock()
	return result, true, fmt.Errorf("%w (status %s)", errClassicMindMapAICancelTooLate, durableStatus)
}

func (w *ClassicMindMapAIWorkspace) dropOldestFinishedLocked() {
	oldestID, oldest := "", ""
	for id, job := range w.jobs {
		if job.Status == "running" {
			continue
		}
		if oldest == "" || job.Updated < oldest {
			oldestID, oldest = id, job.Updated
		}
	}
	if oldestID != "" {
		delete(w.jobs, oldestID)
	}
}

func cloneClassicMindMapAIWorkspaceJob(source *classicMindMapAIWorkspaceJob) classicMindMapAIWorkspaceJob {
	result := *source
	result.cancel = nil
	if source.Preview != nil {
		preview := *source.Preview
		preview.Proposals = append([]classicMindMapAIHTTPProposal(nil), source.Preview.Proposals...)
		for i := range preview.Proposals {
			preview.Proposals[i].Evidence = append([]classicMindMapAIHTTPCitation(nil), source.Preview.Proposals[i].Evidence...)
		}
		result.Preview = &preview
	}
	return result
}

func classicMindMapAIHTTPPreviewFrom(preview ClassicMindMapAIPreview, batchCount int) classicMindMapAIHTTPPreview {
	result := classicMindMapAIHTTPPreview{
		Version: preview.Version, RunID: preview.RunID, Status: preview.Status, Action: preview.Action,
		TargetMapID: preview.TargetMapID, TargetNodeID: preview.TargetNodeID, BaseRevision: preview.BaseRevision,
		Grounded: preview.Grounded, ProposalDigest: preview.ProposalDigest, Title: preview.Title,
		Description: preview.Description, EvidenceCount: len(preview.Evidence), BatchCount: batchCount,
		CorrectionRetries: preview.CorrectionRetries, Created: preview.Created,
		Proposals: make([]classicMindMapAIHTTPProposal, 0, len(preview.Proposals)),
	}
	for _, proposal := range preview.Proposals {
		item := classicMindMapAIHTTPProposal{
			ID: proposal.ID, ParentProposalID: proposal.ParentProposalID, Label: proposal.Label,
			Summary: proposal.Summary, BodyMarkdown: proposal.BodyMarkdown, Kind: proposal.Kind,
			EvidenceCount: len(proposal.Evidence), Reason: proposal.Reason,
			Evidence: make([]classicMindMapAIHTTPCitation, 0, len(proposal.Evidence)),
		}
		for _, anchor := range proposal.Evidence {
			item.Evidence = append(item.Evidence, classicMindMapAIHTTPCitation{
				CitationID: anchor.CitationID, DocumentTitle: filepath.Base(anchor.SourcePath),
				SourcePath: anchor.SourcePath, Page: anchor.Page, BlockIndex: anchor.BlockIndex,
				BlockChunkIndex: anchor.BlockChunkIndex,
				Excerpt:         compactClassicMindMapAIHTTPExcerpt(anchor.Excerpt),
			})
			result.CitationCount++
		}
		result.Proposals = append(result.Proposals, item)
	}
	return result
}

func compactClassicMindMapAIHTTPExcerpt(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxClassicMindMapAIHTTPExcerptRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxClassicMindMapAIHTTPExcerptRunes-1])) + "…"
}

func serveClassicMindMapAIStart(response http.ResponseWriter, request *http.Request, workspace *ClassicMindMapAIWorkspace) {
	if workspace == nil {
		http.Error(response, "AI-помощник не подключён к этому редактору", http.StatusServiceUnavailable)
		return
	}
	var payload classicMindMapAIStartRequest
	if !decodeClassicMindMapJSON(response, request, &payload) {
		return
	}
	job, err := workspace.start(ClassicMindMapAIPreviewRequest{
		Action: payload.Action, Prompt: payload.Prompt, Title: payload.Title, Description: payload.Description,
		MapRef: payload.MapID, NodeRef: payload.TargetNodeID, ExpectedRevision: payload.ExpectedRevision, Scope: payload.Scope,
	})
	if err != nil {
		http.Error(response, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeClassicMindMapResult(response, job, nil)
}

func serveClassicMindMapAIStatus(response http.ResponseWriter, request *http.Request, workspace *ClassicMindMapAIWorkspace) {
	if workspace == nil {
		http.Error(response, "AI-помощник не подключён к этому редактору", http.StatusServiceUnavailable)
		return
	}
	var payload classicMindMapAIJobRequest
	if !decodeClassicMindMapJSON(response, request, &payload) {
		return
	}
	job, ok := workspace.get(payload.JobID)
	if !ok {
		http.Error(response, "AI job не найден", http.StatusNotFound)
		return
	}
	writeClassicMindMapResult(response, job, nil)
}

func serveClassicMindMapAICancel(response http.ResponseWriter, request *http.Request, workspace *ClassicMindMapAIWorkspace) {
	if workspace == nil {
		http.Error(response, "AI-помощник не подключён к этому редактору", http.StatusServiceUnavailable)
		return
	}
	var payload classicMindMapAIJobRequest
	if !decodeClassicMindMapJSON(response, request, &payload) {
		return
	}
	job, ok, err := workspace.cancelJob(payload.JobID)
	if !ok {
		http.Error(response, "AI job не найден", http.StatusNotFound)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errClassicMindMapAICancelTooLate) {
			status = http.StatusConflict
		}
		http.Error(response, err.Error(), status)
		return
	}
	writeClassicMindMapResult(response, job, nil)
}

func serveClassicMindMapAIPublish(response http.ResponseWriter, request *http.Request, workspace *ClassicMindMapAIWorkspace) {
	if workspace == nil || workspace.Service == nil || workspace.Service.Store == nil {
		http.Error(response, "AI-помощник не подключён к этому редактору", http.StatusServiceUnavailable)
		return
	}
	var payload classicMindMapAIPublishRequest
	if !decodeClassicMindMapJSON(response, request, &payload) {
		return
	}
	result, err := workspace.Service.Store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{
		RunID: payload.PreviewID, ProposalIDs: payload.ProposalIDs, ExpectedRevision: payload.ExpectedRevision,
		ExpectedPreviewDigest: payload.ExpectedDigest, Actor: "browser", Comment: "AI preview published",
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrClassicMindMapRevisionConflict) || errors.Is(err, ErrClassicMindMapAIChanged) || errors.Is(err, ErrClassicMindMapLocked) || errors.Is(err, ErrClassicMindMapAIAlreadyApplied) {
			status = http.StatusConflict
		} else if errors.Is(err, ErrClassicMindMapAIPreviewNotFound) {
			status = http.StatusNotFound
		}
		http.Error(response, err.Error(), status)
		return
	}
	writeClassicMindMapResult(response, result, nil)
}
