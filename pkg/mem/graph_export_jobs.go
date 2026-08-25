package mem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	KnowledgeMapExportJobVersion       = 1
	MaxKnowledgeMapExportQueuedJobs    = 4
	MaxKnowledgeMapExportRetainedJobs  = 12
	KnowledgeMapExportJobRetention     = 10 * time.Minute
	MaxKnowledgeMapExportRetainedBytes = MaxKnowledgeGraphExportBytes
)

type KnowledgeMapExportJobStatus string

const (
	KnowledgeMapExportJobQueued     KnowledgeMapExportJobStatus = "queued"
	KnowledgeMapExportJobRunning    KnowledgeMapExportJobStatus = "running"
	KnowledgeMapExportJobCompleted  KnowledgeMapExportJobStatus = "completed"
	KnowledgeMapExportJobFailed     KnowledgeMapExportJobStatus = "failed"
	KnowledgeMapExportJobCancelled  KnowledgeMapExportJobStatus = "cancelled"
	KnowledgeMapExportJobDownloaded KnowledgeMapExportJobStatus = "downloaded"
)

var (
	ErrKnowledgeMapExportQueueFull        = errors.New("knowledge map export queue is full")
	ErrKnowledgeMapExportJobNotFound      = errors.New("knowledge map export job not found")
	ErrKnowledgeMapExportJobNotReady      = errors.New("knowledge map export job is not ready")
	ErrKnowledgeMapExportJobNotCancelable = errors.New("knowledge map export job cannot be cancelled")
)

type KnowledgeMapExportJobView struct {
	Version         int                         `json:"version"`
	ID              string                      `json:"id"`
	Status          KnowledgeMapExportJobStatus `json:"status"`
	Phase           string                      `json:"phase"`
	ProgressPercent int                         `json:"progress_percent"`
	CompletedUnits  int                         `json:"completed_units,omitempty"`
	TotalUnits      int                         `json:"total_units,omitempty"`
	QueuePosition   int                         `json:"queue_position,omitempty"`
	Format          KnowledgeGraphExportFormat  `json:"format"`
	CreatedAt       string                      `json:"created_at"`
	StartedAt       string                      `json:"started_at,omitempty"`
	CompletedAt     string                      `json:"completed_at,omitempty"`
	ElapsedMS       int64                       `json:"elapsed_ms"`
	CancelRequested bool                        `json:"cancel_requested,omitempty"`
	ErrorCode       string                      `json:"error_code,omitempty"`
	Message         string                      `json:"message,omitempty"`
	DownloadReady   bool                        `json:"download_ready"`
	Filename        string                      `json:"filename,omitempty"`
	ContentType     string                      `json:"content_type,omitempty"`
	Bytes           int                         `json:"bytes,omitempty"`
	Digest          string                      `json:"digest,omitempty"`
	StateDigest     string                      `json:"state_digest,omitempty"`
	NodeCount       int                         `json:"node_count,omitempty"`
	EdgeCount       int                         `json:"edge_count,omitempty"`
	EvidenceCount   int                         `json:"evidence_count,omitempty"`
}

type knowledgeMapExportRunner func(context.Context, KnowledgeGraphExportRequest, knowledgeGraphExportProgressFunc) (KnowledgeGraphExportArtifact, error)

type knowledgeMapExportJob struct {
	view        KnowledgeMapExportJobView
	request     KnowledgeGraphExportRequest
	artifact    *KnowledgeGraphExportArtifact
	ctx         context.Context
	cancel      context.CancelFunc
	createdAt   time.Time
	startedAt   time.Time
	completedAt time.Time
}

type knowledgeMapExportManager struct {
	ctx       context.Context
	runner    knowledgeMapExportRunner
	mu        sync.Mutex
	jobs      map[string]*knowledgeMapExportJob
	order     []string
	queue     chan string
	startOnce sync.Once
	now       func() time.Time
}

func newKnowledgeMapExportManager(ctx context.Context, runner knowledgeMapExportRunner) *knowledgeMapExportManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &knowledgeMapExportManager{
		ctx: ctx, runner: runner, jobs: make(map[string]*knowledgeMapExportJob),
		queue: make(chan string, MaxKnowledgeMapExportQueuedJobs), now: time.Now,
	}
}

func (m *knowledgeMapExportManager) start(request KnowledgeGraphExportRequest) (KnowledgeMapExportJobView, error) {
	if m == nil || m.runner == nil {
		return KnowledgeMapExportJobView{}, errors.New("knowledge map export worker is unavailable")
	}
	if err := m.ctx.Err(); err != nil {
		return KnowledgeMapExportJobView{}, err
	}
	normalized, err := normalizeKnowledgeGraphExportRequest(request)
	if err != nil {
		return KnowledgeMapExportJobView{}, err
	}
	if normalized.ExpectedDigest == "" || normalized.ExpectedStateDigest == "" {
		return KnowledgeMapExportJobView{}, errors.New("knowledge map export job requires content and source-state pins")
	}
	now := m.now().UTC()
	m.mu.Lock()
	m.cleanupLocked(now)
	active := 0
	for _, job := range m.jobs {
		if job.view.Status == KnowledgeMapExportJobQueued || job.view.Status == KnowledgeMapExportJobRunning {
			active++
		}
	}
	if active >= MaxKnowledgeMapExportQueuedJobs+1 {
		m.mu.Unlock()
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportQueueFull
	}
	id := ""
	for attempt := 0; attempt < 4; attempt++ {
		candidate, idErr := newKnowledgeMapExportJobID()
		if idErr != nil {
			m.mu.Unlock()
			return KnowledgeMapExportJobView{}, idErr
		}
		if _, exists := m.jobs[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		m.mu.Unlock()
		return KnowledgeMapExportJobView{}, errors.New("could not allocate a unique knowledge map export job ID")
	}
	jobCtx, cancel := context.WithCancel(m.ctx)
	job := &knowledgeMapExportJob{
		view: KnowledgeMapExportJobView{
			Version: KnowledgeMapExportJobVersion, ID: id, Status: KnowledgeMapExportJobQueued,
			Phase: "queued", Format: normalized.Format, CreatedAt: now.Format(time.RFC3339Nano),
			Message: "Экспорт ожидает свободный worker.",
		},
		request: normalized, ctx: jobCtx, cancel: cancel, createdAt: now,
	}
	m.jobs[id] = job
	m.order = append(m.order, id)
	view := m.viewLocked(job, now)
	m.mu.Unlock()

	m.startOnce.Do(func() {
		go m.worker()
		go m.janitor()
	})
	select {
	case m.queue <- id:
		return view, nil
	case <-m.ctx.Done():
		m.mu.Lock()
		m.removeLocked(id)
		m.mu.Unlock()
		cancel()
		return KnowledgeMapExportJobView{}, m.ctx.Err()
	default:
		m.mu.Lock()
		m.removeLocked(id)
		m.mu.Unlock()
		cancel()
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportQueueFull
	}
}

func (m *knowledgeMapExportManager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			m.cancelAll()
			return
		case id := <-m.queue:
			m.run(id)
		}
	}
}

func (m *knowledgeMapExportManager) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.mu.Lock()
			m.cleanupLocked(now.UTC())
			m.mu.Unlock()
		}
	}
}

func (m *knowledgeMapExportManager) run(id string) {
	now := m.now().UTC()
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil || job.view.Status != KnowledgeMapExportJobQueued {
		m.mu.Unlock()
		return
	}
	job.view.Status, job.view.Phase = KnowledgeMapExportJobRunning, "validating"
	job.view.Message = "Проверяю закреплённое состояние карты."
	job.startedAt, job.view.StartedAt = now, now.Format(time.RFC3339Nano)
	request, ctx := job.request, job.ctx
	m.mu.Unlock()

	artifact, err := m.runner(ctx, request, func(phase string, percent, completed, total int) {
		m.updateProgress(id, phase, percent, completed, total)
	})
	finished := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	job = m.jobs[id]
	if job == nil {
		return
	}
	job.completedAt, job.view.CompletedAt = finished, finished.Format(time.RFC3339Nano)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		job.view.Status, job.view.Phase, job.view.ProgressPercent = KnowledgeMapExportJobCancelled, "cancelled", 0
		job.view.ErrorCode, job.view.Message = "cancelled", "Экспорт отменён; файл не был опубликован."
		job.artifact = nil
		return
	}
	if err != nil {
		job.view.Status, job.view.Phase = KnowledgeMapExportJobFailed, "failed"
		job.view.ErrorCode, job.view.Message = publicKnowledgeMapExportJobError(err)
		job.artifact = nil
		return
	}
	m.ensureArtifactCapacityLocked(len(artifact.Data), id)
	job.artifact = &artifact
	job.view.Status, job.view.Phase, job.view.ProgressPercent = KnowledgeMapExportJobCompleted, "completed", 100
	job.view.Message, job.view.DownloadReady = "Файл готов к сохранению.", true
	job.view.Filename, job.view.ContentType, job.view.Bytes = artifact.Filename, artifact.ContentType, len(artifact.Data)
	job.view.Digest, job.view.StateDigest = artifact.Digest, artifact.StateDigest
	job.view.NodeCount, job.view.EdgeCount, job.view.EvidenceCount = artifact.NodeCount, artifact.EdgeCount, artifact.Evidence
}

func (m *knowledgeMapExportManager) updateProgress(id, phase string, percent, completed, total int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.view.Status != KnowledgeMapExportJobRunning || job.view.CancelRequested {
		return
	}
	if percent >= job.view.ProgressPercent {
		job.view.ProgressPercent = percent
	}
	job.view.Phase, job.view.CompletedUnits, job.view.TotalUnits = phase, completed, total
	job.view.Message = knowledgeMapExportPhaseMessage(phase, completed, total)
}

func (m *knowledgeMapExportManager) status(id string) (KnowledgeMapExportJobView, error) {
	if !validKnowledgeMapExportJobID(id) {
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportJobNotFound
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	job := m.jobs[id]
	if job == nil {
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportJobNotFound
	}
	return m.viewLocked(job, now), nil
}

func (m *knowledgeMapExportManager) cancelJob(id string) (KnowledgeMapExportJobView, error) {
	if !validKnowledgeMapExportJobID(id) {
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportJobNotFound
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportJobNotFound
	}
	switch job.view.Status {
	case KnowledgeMapExportJobQueued:
		job.cancel()
		job.completedAt, job.view.CompletedAt = now, now.Format(time.RFC3339Nano)
		job.view.Status, job.view.Phase = KnowledgeMapExportJobCancelled, "cancelled"
		job.view.ErrorCode, job.view.Message = "cancelled", "Экспорт удалён из очереди."
	case KnowledgeMapExportJobRunning:
		job.view.CancelRequested, job.view.Phase = true, "cancelling"
		job.view.Message = "Отмена запрошена; текущая атомарная фаза будет безопасно завершена."
		job.cancel()
	default:
		return KnowledgeMapExportJobView{}, ErrKnowledgeMapExportJobNotCancelable
	}
	return m.viewLocked(job, now), nil
}

func (m *knowledgeMapExportManager) artifact(id string) (KnowledgeGraphExportArtifact, error) {
	if !validKnowledgeMapExportJobID(id) {
		return KnowledgeGraphExportArtifact{}, ErrKnowledgeMapExportJobNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return KnowledgeGraphExportArtifact{}, ErrKnowledgeMapExportJobNotFound
	}
	if job.view.Status != KnowledgeMapExportJobCompleted || job.artifact == nil {
		return KnowledgeGraphExportArtifact{}, ErrKnowledgeMapExportJobNotReady
	}
	return *job.artifact, nil
}

func (m *knowledgeMapExportManager) markDownloaded(id string) {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.view.Status != KnowledgeMapExportJobCompleted {
		return
	}
	job.artifact = nil
	job.view.Status, job.view.Phase = KnowledgeMapExportJobDownloaded, "downloaded"
	job.view.DownloadReady, job.view.Message = false, "Файл передан браузеру; память освобождена."
	job.completedAt, job.view.CompletedAt = now, now.Format(time.RFC3339Nano)
}

func (m *knowledgeMapExportManager) cancelAll() {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.view.Status != KnowledgeMapExportJobQueued && job.view.Status != KnowledgeMapExportJobRunning {
			continue
		}
		job.cancel()
		job.artifact = nil
		job.completedAt, job.view.CompletedAt = now, now.Format(time.RFC3339Nano)
		job.view.Status, job.view.Phase = KnowledgeMapExportJobCancelled, "cancelled"
		job.view.ErrorCode, job.view.Message = "shutdown", "Сервер карты остановлен; незавершённый экспорт отменён."
	}
}

func (m *knowledgeMapExportManager) viewLocked(job *knowledgeMapExportJob, now time.Time) KnowledgeMapExportJobView {
	view := job.view
	if view.Status == KnowledgeMapExportJobQueued {
		position := 1
		for _, id := range m.order {
			candidate := m.jobs[id]
			if candidate == nil || candidate.view.Status != KnowledgeMapExportJobQueued {
				continue
			}
			if id == job.view.ID {
				break
			}
			position++
		}
		view.QueuePosition = position
	}
	started := job.startedAt
	if started.IsZero() {
		started = job.createdAt
	}
	finished := now
	if !job.completedAt.IsZero() {
		finished = job.completedAt
	}
	if !started.IsZero() && finished.After(started) {
		view.ElapsedMS = finished.Sub(started).Milliseconds()
	}
	return view
}

func (m *knowledgeMapExportManager) cleanupLocked(now time.Time) {
	for _, id := range append([]string(nil), m.order...) {
		job := m.jobs[id]
		if job == nil || job.completedAt.IsZero() || now.Sub(job.completedAt) <= KnowledgeMapExportJobRetention {
			continue
		}
		m.removeLocked(id)
	}
	for len(m.jobs) >= MaxKnowledgeMapExportRetainedJobs {
		removed := false
		for _, id := range append([]string(nil), m.order...) {
			job := m.jobs[id]
			if job == nil || job.view.Status == KnowledgeMapExportJobQueued || job.view.Status == KnowledgeMapExportJobRunning {
				continue
			}
			m.removeLocked(id)
			removed = true
			break
		}
		if !removed {
			break
		}
	}
}

func (m *knowledgeMapExportManager) ensureArtifactCapacityLocked(incoming int, keepID string) {
	total := incoming
	for id, job := range m.jobs {
		if id != keepID && job.artifact != nil {
			total += len(job.artifact.Data)
		}
	}
	for total > MaxKnowledgeMapExportRetainedBytes {
		removed := false
		for _, id := range m.order {
			job := m.jobs[id]
			if id == keepID || job == nil || job.artifact == nil {
				continue
			}
			total -= len(job.artifact.Data)
			job.artifact = nil
			job.view.Status, job.view.Phase, job.view.DownloadReady = KnowledgeMapExportJobFailed, "expired", false
			job.view.ErrorCode, job.view.Message = "expired", "Предыдущий файл удалён из памяти; запустите экспорт повторно."
			removed = true
			break
		}
		if !removed {
			break
		}
	}
}

func (m *knowledgeMapExportManager) removeLocked(id string) {
	job := m.jobs[id]
	if job != nil {
		job.cancel()
		job.artifact = nil
	}
	delete(m.jobs, id)
	for i, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

func newKnowledgeMapExportJobID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create knowledge map export job ID: %w", err)
	}
	return "kex-" + hex.EncodeToString(raw[:]), nil
}

func validKnowledgeMapExportJobID(id string) bool {
	if len(id) != 36 || id[:4] != "kex-" {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil
}

func knowledgeMapExportPhaseMessage(phase string, completed, total int) string {
	switch phase {
	case "validating":
		return "Проверяю параметры экспорта."
	case "pinning":
		if total > 0 {
			return fmt.Sprintf("Проверяю источники: %d из %d уникальных координат.", completed, total)
		}
		return "Закрепляю граф и состояния источников."
	case "rendering":
		return "Формирую переносимый файл и provenance."
	case "finalizing":
		return "Проверяю размер и целостность готового файла."
	default:
		return "Экспорт выполняется."
	}
}

func publicKnowledgeMapExportJobError(err error) (string, string) {
	if errors.Is(err, ErrKnowledgeGraphExportChanged) {
		return "changed", "Карта или состояние источников изменились. Обновите страницу и повторите экспорт."
	}
	return "rejected", "Экспорт отклонён после проверки; обновите страницу и повторите попытку."
}
