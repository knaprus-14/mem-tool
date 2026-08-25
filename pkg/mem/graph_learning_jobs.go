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
	KnowledgeLearningJobVersion      = 1
	MaxKnowledgeLearningQueuedJobs   = 4
	MaxKnowledgeLearningRetainedJobs = 12
	KnowledgeLearningJobRetention    = 10 * time.Minute
)

type KnowledgeLearningJobStatus string

const (
	KnowledgeLearningJobQueued    KnowledgeLearningJobStatus = "queued"
	KnowledgeLearningJobRunning   KnowledgeLearningJobStatus = "running"
	KnowledgeLearningJobCompleted KnowledgeLearningJobStatus = "completed"
	KnowledgeLearningJobFailed    KnowledgeLearningJobStatus = "failed"
	KnowledgeLearningJobCancelled KnowledgeLearningJobStatus = "cancelled"
)

var (
	ErrKnowledgeLearningQueueFull        = errors.New("knowledge learning queue is full")
	ErrKnowledgeLearningJobNotFound      = errors.New("knowledge learning job not found")
	ErrKnowledgeLearningJobNotReady      = errors.New("knowledge learning job is not ready")
	ErrKnowledgeLearningJobNotCancelable = errors.New("knowledge learning job cannot be cancelled")
)

// KnowledgeLearningJobView is the compact, polling-safe state exposed by the
// loopback workspace. The generated candidates are returned only by the result
// endpoint after the job reaches completed.
type KnowledgeLearningJobView struct {
	Version         int                        `json:"version"`
	ID              string                     `json:"id"`
	Status          KnowledgeLearningJobStatus `json:"status"`
	Phase           string                     `json:"phase"`
	ProgressPercent int                        `json:"progress_percent"`
	CompletedUnits  int                        `json:"completed_units,omitempty"`
	TotalUnits      int                        `json:"total_units,omitempty"`
	QueuePosition   int                        `json:"queue_position,omitempty"`
	RequestedCount  int                        `json:"requested_count"`
	CandidateCount  int                        `json:"candidate_count,omitempty"`
	Model           string                     `json:"model,omitempty"`
	CreatedAt       string                     `json:"created_at"`
	StartedAt       string                     `json:"started_at,omitempty"`
	CompletedAt     string                     `json:"completed_at,omitempty"`
	ElapsedMS       int64                      `json:"elapsed_ms"`
	CancelRequested bool                       `json:"cancel_requested,omitempty"`
	ErrorCode       string                     `json:"error_code,omitempty"`
	Message         string                     `json:"message,omitempty"`
	ResultReady     bool                       `json:"result_ready"`
}

type knowledgeLearningRunner func(context.Context, KnowledgeLearningGenerateRequest, knowledgeLearningProgressFunc) (KnowledgeLearningRun, error)

type knowledgeLearningJob struct {
	view        KnowledgeLearningJobView
	request     KnowledgeLearningGenerateRequest
	result      *KnowledgeLearningRun
	ctx         context.Context
	cancel      context.CancelFunc
	createdAt   time.Time
	startedAt   time.Time
	completedAt time.Time
}

type knowledgeLearningJobManager struct {
	ctx       context.Context
	runner    knowledgeLearningRunner
	mu        sync.Mutex
	jobs      map[string]*knowledgeLearningJob
	order     []string
	queue     chan string
	startOnce sync.Once
	now       func() time.Time
}

func newKnowledgeLearningJobManager(ctx context.Context, runner knowledgeLearningRunner) *knowledgeLearningJobManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &knowledgeLearningJobManager{
		ctx: ctx, runner: runner, jobs: make(map[string]*knowledgeLearningJob),
		queue: make(chan string, MaxKnowledgeLearningQueuedJobs), now: time.Now,
	}
}

func (m *knowledgeLearningJobManager) start(request KnowledgeLearningGenerateRequest) (KnowledgeLearningJobView, error) {
	if m == nil || m.runner == nil {
		return KnowledgeLearningJobView{}, errors.New("knowledge learning worker is unavailable")
	}
	if err := m.ctx.Err(); err != nil {
		return KnowledgeLearningJobView{}, err
	}
	normalized, err := normalizeKnowledgeLearningGenerateRequest(request)
	if err != nil {
		return KnowledgeLearningJobView{}, err
	}
	now := m.now().UTC()
	m.mu.Lock()
	m.cleanupLocked(now)
	active := 0
	for _, job := range m.jobs {
		if job.view.Status == KnowledgeLearningJobQueued || job.view.Status == KnowledgeLearningJobRunning {
			active++
		}
	}
	if active >= MaxKnowledgeLearningQueuedJobs+1 {
		m.mu.Unlock()
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningQueueFull
	}
	id := ""
	for attempt := 0; attempt < 4; attempt++ {
		candidate, idErr := newKnowledgeLearningJobID()
		if idErr != nil {
			m.mu.Unlock()
			return KnowledgeLearningJobView{}, idErr
		}
		if _, exists := m.jobs[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		m.mu.Unlock()
		return KnowledgeLearningJobView{}, errors.New("could not allocate a unique knowledge learning job ID")
	}
	jobCtx, cancel := context.WithCancel(m.ctx)
	job := &knowledgeLearningJob{
		view: KnowledgeLearningJobView{
			Version: KnowledgeLearningJobVersion, ID: id, Status: KnowledgeLearningJobQueued,
			Phase: "queued", RequestedCount: normalized.Count, CreatedAt: now.Format(time.RFC3339Nano),
			Message: "Генерация ожидает свободный worker.",
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
		return KnowledgeLearningJobView{}, m.ctx.Err()
	default:
		m.mu.Lock()
		m.removeLocked(id)
		m.mu.Unlock()
		cancel()
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningQueueFull
	}
}

func (m *knowledgeLearningJobManager) worker() {
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

func (m *knowledgeLearningJobManager) janitor() {
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

func (m *knowledgeLearningJobManager) run(id string) {
	now := m.now().UTC()
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil || job.view.Status != KnowledgeLearningJobQueued {
		m.mu.Unlock()
		return
	}
	job.view.Status, job.view.Phase = KnowledgeLearningJobRunning, "validating"
	job.view.Message = "Проверяю закреплённую выбранную область."
	job.startedAt, job.view.StartedAt = now, now.Format(time.RFC3339Nano)
	request, ctx := job.request, job.ctx
	m.mu.Unlock()

	result, err := m.runner(ctx, request, func(phase string, percent, completed, total int) {
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
	// A successfully committed preview wins a cancellation race at the final
	// boundary. This prevents a durable learning run from becoming hidden.
	if err == nil {
		job.result = &result
		job.view.Status, job.view.Phase, job.view.ProgressPercent = KnowledgeLearningJobCompleted, "completed", 100
		job.view.Message, job.view.ResultReady = "Кандидаты готовы к проверке.", true
		job.view.CandidateCount, job.view.Model = len(result.Candidates), result.Model
		return
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		job.view.Status, job.view.Phase, job.view.ProgressPercent = KnowledgeLearningJobCancelled, "cancelled", 0
		job.view.ErrorCode, job.view.Message = "cancelled", "Генерация отменена; кандидаты не опубликованы."
		job.result = nil
		return
	}
	job.view.Status, job.view.Phase = KnowledgeLearningJobFailed, "failed"
	job.view.ErrorCode, job.view.Message = publicKnowledgeLearningJobError(err)
	job.result = nil
}

func (m *knowledgeLearningJobManager) updateProgress(id, phase string, percent, completed, total int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.view.Status != KnowledgeLearningJobRunning || job.view.CancelRequested {
		return
	}
	if percent >= job.view.ProgressPercent {
		job.view.ProgressPercent = percent
	}
	job.view.Phase, job.view.CompletedUnits, job.view.TotalUnits = phase, completed, total
	job.view.Message = knowledgeLearningPhaseMessage(phase, completed, total)
}

func (m *knowledgeLearningJobManager) status(id string) (KnowledgeLearningJobView, error) {
	if !validKnowledgeLearningJobID(id) {
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningJobNotFound
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	job := m.jobs[id]
	if job == nil {
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningJobNotFound
	}
	return m.viewLocked(job, now), nil
}

func (m *knowledgeLearningJobManager) cancelJob(id string) (KnowledgeLearningJobView, error) {
	if !validKnowledgeLearningJobID(id) {
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningJobNotFound
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningJobNotFound
	}
	switch job.view.Status {
	case KnowledgeLearningJobQueued:
		job.cancel()
		job.completedAt, job.view.CompletedAt = now, now.Format(time.RFC3339Nano)
		job.view.Status, job.view.Phase = KnowledgeLearningJobCancelled, "cancelled"
		job.view.ErrorCode, job.view.Message = "cancelled", "Генерация удалена из очереди."
	case KnowledgeLearningJobRunning:
		job.view.CancelRequested, job.view.Phase = true, "cancelling"
		job.view.Message = "Отмена запрошена; результат модели не будет опубликован."
		job.cancel()
	default:
		return KnowledgeLearningJobView{}, ErrKnowledgeLearningJobNotCancelable
	}
	return m.viewLocked(job, now), nil
}

func (m *knowledgeLearningJobManager) result(id string) (KnowledgeLearningRun, error) {
	if !validKnowledgeLearningJobID(id) {
		return KnowledgeLearningRun{}, ErrKnowledgeLearningJobNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return KnowledgeLearningRun{}, ErrKnowledgeLearningJobNotFound
	}
	if job.view.Status != KnowledgeLearningJobCompleted || job.result == nil {
		return KnowledgeLearningRun{}, ErrKnowledgeLearningJobNotReady
	}
	return *job.result, nil
}

func (m *knowledgeLearningJobManager) cancelAll() {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.view.Status != KnowledgeLearningJobQueued && job.view.Status != KnowledgeLearningJobRunning {
			continue
		}
		job.cancel()
		job.result = nil
		job.completedAt, job.view.CompletedAt = now, now.Format(time.RFC3339Nano)
		job.view.Status, job.view.Phase = KnowledgeLearningJobCancelled, "cancelled"
		job.view.ErrorCode, job.view.Message = "shutdown", "Сервер карты остановлен; генерация отменена."
	}
}

func (m *knowledgeLearningJobManager) viewLocked(job *knowledgeLearningJob, now time.Time) KnowledgeLearningJobView {
	view := job.view
	if view.Status == KnowledgeLearningJobQueued {
		position := 1
		for _, id := range m.order {
			candidate := m.jobs[id]
			if candidate == nil || candidate.view.Status != KnowledgeLearningJobQueued {
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

func (m *knowledgeLearningJobManager) cleanupLocked(now time.Time) {
	for _, id := range append([]string(nil), m.order...) {
		job := m.jobs[id]
		if job == nil || job.completedAt.IsZero() || now.Sub(job.completedAt) <= KnowledgeLearningJobRetention {
			continue
		}
		m.removeLocked(id)
	}
	for len(m.jobs) >= MaxKnowledgeLearningRetainedJobs {
		removed := false
		for _, id := range append([]string(nil), m.order...) {
			job := m.jobs[id]
			if job == nil || job.view.Status == KnowledgeLearningJobQueued || job.view.Status == KnowledgeLearningJobRunning {
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

func (m *knowledgeLearningJobManager) removeLocked(id string) {
	job := m.jobs[id]
	if job != nil {
		job.cancel()
		job.result = nil
	}
	delete(m.jobs, id)
	for i, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

func newKnowledgeLearningJobID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create knowledge learning job ID: %w", err)
	}
	return "klj-" + hex.EncodeToString(raw[:]), nil
}

func validKnowledgeLearningJobID(id string) bool {
	if len(id) != 36 || id[:4] != "klj-" {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil
}

func knowledgeLearningPhaseMessage(phase string, completed, total int) string {
	switch phase {
	case "validating":
		return "Проверяю параметры генерации."
	case "grounding":
		return "Закрепляю выбранные узлы и актуальные источники."
	case "generating":
		if total > 0 {
			return fmt.Sprintf("Модель обрабатывает %d из %d evidence-фрагментов.", completed, total)
		}
		return "Модель создаёт карточки и открытые вопросы."
	case "validating-response":
		return "Проверяю строгий JSON, цитаты и отсутствие неподтверждённых фактов."
	case "correcting":
		return "Исправляющий повтор: модель возвращает полный проверяемый JSON."
	case "verifying":
		return fmt.Sprintf("Повторно проверяю выбор и источники; кандидатов: %d из %d.", completed, total)
	default:
		return "Генерация учебных кандидатов выполняется."
	}
}

func publicKnowledgeLearningJobError(err error) (string, string) {
	switch {
	case errors.Is(err, ErrKnowledgeSelectionChanged), errors.Is(err, ErrKnowledgeSelectionNotCurrent):
		return "changed", "Выбор или evidence изменились. Закрепите выбранную область заново."
	case errors.Is(err, ErrKnowledgeSelectionUnavailable):
		return "unavailable", "Answer-модель недоступна. Проверьте настройки и перезапустите карту."
	default:
		return "rejected", "Модель не сформировала строгий проверяемый набор кандидатов."
	}
}
