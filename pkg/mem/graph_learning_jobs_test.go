package mem

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKnowledgeLearningJobManagerLifecycleProgressAndResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	manager := newKnowledgeLearningJobManager(ctx, func(ctx context.Context, request KnowledgeLearningGenerateRequest, progress knowledgeLearningProgressFunc) (KnowledgeLearningRun, error) {
		close(started)
		progress("grounding", 20, 2, 4)
		select {
		case <-release:
		case <-ctx.Done():
			return KnowledgeLearningRun{}, ctx.Err()
		}
		progress("verifying", 90, 1, request.Count)
		return KnowledgeLearningRun{
			ID: "learning-run-result", Model: "test-chat", ManifestDigest: request.ExpectedManifestDigest,
			Candidates: []KnowledgeLearningCandidate{{Kind: KnowledgeNodeCard, Prompt: "Q", Answer: "A"}},
		}, nil
	})
	job, err := manager.start(testKnowledgeLearningJobRequest())
	if err != nil || job.Status != KnowledgeLearningJobQueued || job.QueuePosition != 1 {
		t.Fatalf("unexpected queued job: %#v err=%v", job, err)
	}
	<-started
	running := waitKnowledgeLearningJobStatus(t, manager, job.ID, KnowledgeLearningJobRunning)
	if running.ProgressPercent != 20 || running.Phase != "grounding" || running.CompletedUnits != 2 || running.TotalUnits != 4 {
		t.Fatalf("progress was not retained: %#v", running)
	}
	close(release)
	completed := waitKnowledgeLearningJobStatus(t, manager, job.ID, KnowledgeLearningJobCompleted)
	if !completed.ResultReady || completed.ProgressPercent != 100 || completed.CandidateCount != 1 || completed.Model != "test-chat" {
		t.Fatalf("completed job is incomplete: %#v", completed)
	}
	result, err := manager.result(job.ID)
	if err != nil || result.ID != "learning-run-result" || len(result.Candidates) != 1 {
		t.Fatalf("result unavailable: %#v err=%v", result, err)
	}
	if _, err := manager.cancelJob(job.ID); !errors.Is(err, ErrKnowledgeLearningJobNotCancelable) {
		t.Fatalf("terminal job was cancellable: %v", err)
	}
}

func TestKnowledgeLearningJobManagerBoundsQueueAndCancels(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	firstStarted := make(chan struct{})
	var once sync.Once
	manager := newKnowledgeLearningJobManager(ctx, func(ctx context.Context, _ KnowledgeLearningGenerateRequest, _ knowledgeLearningProgressFunc) (KnowledgeLearningRun, error) {
		once.Do(func() { close(firstStarted) })
		<-ctx.Done()
		return KnowledgeLearningRun{}, ctx.Err()
	})
	first, err := manager.start(testKnowledgeLearningJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	waitKnowledgeLearningJobStatus(t, manager, first.ID, KnowledgeLearningJobRunning)
	queued := make([]KnowledgeLearningJobView, 0, MaxKnowledgeLearningQueuedJobs)
	for i := 0; i < MaxKnowledgeLearningQueuedJobs; i++ {
		job, err := manager.start(testKnowledgeLearningJobRequest())
		if err != nil {
			t.Fatalf("queue item %d: %v", i, err)
		}
		queued = append(queued, job)
	}
	if _, err := manager.start(testKnowledgeLearningJobRequest()); !errors.Is(err, ErrKnowledgeLearningQueueFull) {
		t.Fatalf("overflowing queue returned %v", err)
	}
	cancelled, err := manager.cancelJob(queued[1].ID)
	if err != nil || cancelled.Status != KnowledgeLearningJobCancelled {
		t.Fatalf("queued cancellation failed: %#v err=%v", cancelled, err)
	}
	cancelling, err := manager.cancelJob(first.ID)
	if err != nil || !cancelling.CancelRequested || cancelling.Phase != "cancelling" {
		t.Fatalf("running cancellation was not acknowledged: %#v err=%v", cancelling, err)
	}
	waitKnowledgeLearningJobStatus(t, manager, first.ID, KnowledgeLearningJobCancelled)
	if _, err := manager.result(first.ID); !errors.Is(err, ErrKnowledgeLearningJobNotReady) {
		t.Fatalf("cancelled job published a result: %v", err)
	}
	if _, err := manager.status("invalid"); !errors.Is(err, ErrKnowledgeLearningJobNotFound) {
		t.Fatalf("invalid job ID returned %v", err)
	}
}

func TestKnowledgeLearningJobWorkspaceHTTPFlow(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const nodeID = "workspace-learning-background"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: nodeID, Kind: KnowledgeNodeDefinition, Label: "Закон Ома", Body: "Связь тока, напряжения и сопротивления",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	provider := &selectionAnswerProvider{answers: []string{`{"items":[{"kind":"card","prompt":"Что связывает закон Ома?","answer":"Ток, напряжение и сопротивление.","citations":["E1"]}]}`}}
	service := &KnowledgeSelectionAnswerService{
		Provider: provider,
		Config:   AnswerConfig{Model: "test-chat", BaseURL: "http://127.0.0.1:11434", TimeoutSeconds: 1, MaxTokens: 512, ContextChars: 12000, Temperature: 0.1},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "learning-background-session-capability"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandlerWithSelectionContext(ctx, store, "", token, DefaultKnowledgeMapView, service)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	for _, marker := range []string{"learning-job-progress", "/api/selection/learning/jobs/start", "/api/selection/learning/jobs/status", "/api/selection/learning/jobs/cancel", "/api/selection/learning/jobs/result"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("background learning UI is missing %q", marker)
		}
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{nodeID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 4, Focus: "определения"}
	raw, _ := json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/start", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized job start returned %d", got.Code)
	}
	started := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/start", host, "http://"+host, token, "same-origin", raw)
	if started.Code != http.StatusAccepted {
		t.Fatalf("job start returned %d: %s", started.Code, started.Body.String())
	}
	var job KnowledgeLearningJobView
	if err := json.Unmarshal(started.Body.Bytes(), &job); err != nil || !validKnowledgeLearningJobID(job.ID) {
		t.Fatalf("invalid job response: %#v err=%v", job, err)
	}
	jobBody, _ := json.Marshal(knowledgeLearningJobRequest{JobID: job.ID})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/status", host, "http://"+host, token, "same-origin", jobBody)
		if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &job) != nil {
			t.Fatalf("job status failed: %d %s", status.Code, status.Body.String())
		}
		if job.Status == KnowledgeLearningJobCompleted {
			break
		}
		if job.Status == KnowledgeLearningJobFailed || job.Status == KnowledgeLearningJobCancelled {
			t.Fatalf("job ended without result: %#v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.Status != KnowledgeLearningJobCompleted || !job.ResultReady || job.CandidateCount != 1 {
		t.Fatalf("job did not complete: %#v", job)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/result", host, "http://"+host, "wrong", "same-origin", jobBody); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized result returned %d", got.Code)
	}
	resultResponse := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/result", host, "http://"+host, token, "same-origin", jobBody)
	var run KnowledgeLearningRun
	if resultResponse.Code != http.StatusOK || json.Unmarshal(resultResponse.Body.Bytes(), &run) != nil || run.ID == "" || len(run.Candidates) != 1 || len(run.Candidates[0].Sources) != 1 {
		t.Fatalf("job result failed: status=%d run=%#v body=%q", resultResponse.Code, run, resultResponse.Body.String())
	}
	readOnly := NewKnowledgeMapLiveHandler(store, "")
	if got := requestKnowledgeMapMutation(t, readOnly, "/api/selection/learning/jobs/start", host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusNotFound {
		t.Fatalf("read-only map exposed background learning API: %d", got.Code)
	}
}

func TestKnowledgeLearningJobWorkspaceHTTPCancelsRunningGeneration(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const nodeID = "workspace-learning-cancel"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: nodeID, Kind: KnowledgeNodeDefinition, Label: "Отмена", Status: KnowledgeStatusActive,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	provider := &knowledgeLearningBlockingProvider{started: make(chan struct{})}
	service := &KnowledgeSelectionAnswerService{Provider: provider, Config: AnswerConfig{Model: "test-chat", MaxTokens: 512, ContextChars: 12000}}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	const token = "learning-cancel-session-capability"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandlerWithSelectionContext(ctx, store, "", token, DefaultKnowledgeMapView, service)
	selection := KnowledgeSelectionRequest{NodeIDs: []string{nodeID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 1})
	started := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/start", host, "http://"+host, token, "same-origin", raw)
	var job KnowledgeLearningJobView
	if started.Code != http.StatusAccepted || json.Unmarshal(started.Body.Bytes(), &job) != nil {
		t.Fatalf("job start failed: %d %s", started.Code, started.Body.String())
	}
	<-provider.started
	jobBody, _ := json.Marshal(knowledgeLearningJobRequest{JobID: job.ID})
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/cancel", host, "http://"+host, "wrong", "same-origin", jobBody); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized cancel returned %d", got.Code)
	}
	cancelled := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/cancel", host, "http://"+host, token, "same-origin", jobBody)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel returned %d: %s", cancelled.Code, cancelled.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/status", host, "http://"+host, token, "same-origin", jobBody)
		if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &job) != nil {
			t.Fatalf("status failed after cancel: %d %s", status.Code, status.Body.String())
		}
		if job.Status == KnowledgeLearningJobCancelled {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.Status != KnowledgeLearningJobCancelled || job.ResultReady {
		t.Fatalf("cancelled job published a result: %#v", job)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/jobs/result", host, "http://"+host, token, "same-origin", jobBody); got.Code != http.StatusConflict {
		t.Fatalf("cancelled result returned %d", got.Code)
	}
}

func TestGenerateKnowledgeLearningCandidatesReportsProgressAndHonoursCancellation(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const nodeID = "learning-progress-node"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: nodeID, Kind: KnowledgeNodeDefinition, Label: "Проверка", Status: KnowledgeStatusActive,
		Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{nodeID}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 1}
	provider := &selectionAnswerProvider{answers: []string{`{"items":[{"kind":"card","prompt":"Вопрос","answer":"Ответ","citations":["E1"]}]}`}}
	service := &KnowledgeSelectionAnswerService{Provider: provider, Config: AnswerConfig{Model: "test-chat", MaxTokens: 512, ContextChars: 12000}}
	var phases []string
	var percents []int
	if _, err := store.generateKnowledgeLearningCandidates(context.Background(), service, request, func(phase string, percent, _, _ int) {
		phases, percents = append(phases, phase), append(percents, percent)
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) < 6 || phases[0] != "validating" || phases[len(phases)-1] != "completed" || percents[len(percents)-1] != 100 {
		t.Fatalf("unexpected progress: phases=%v percents=%v", phases, percents)
	}
	for i := 1; i < len(percents); i++ {
		if percents[i] < percents[i-1] {
			t.Fatalf("progress moved backwards: %v", percents)
		}
	}

	blocking := &knowledgeLearningBlockingProvider{started: make(chan struct{})}
	blockingService := &KnowledgeSelectionAnswerService{Provider: blocking, Config: AnswerConfig{Model: "test-chat", MaxTokens: 512, ContextChars: 12000}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, generateErr := store.generateKnowledgeLearningCandidates(ctx, blockingService, request, nil)
		done <- generateErr
	}()
	<-blocking.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled generation returned %v", err)
	}
}

type knowledgeLearningBlockingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (p *knowledgeLearningBlockingProvider) Generate(ctx context.Context, _ AnswerRequest) (string, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return "", ctx.Err()
}

func testKnowledgeLearningJobRequest() KnowledgeLearningGenerateRequest {
	return KnowledgeLearningGenerateRequest{
		Selection:              KnowledgeSelectionRequest{NodeIDs: []string{"learning-node"}},
		ExpectedManifestDigest: "sha256:pinned", Count: 4,
	}
}

func waitKnowledgeLearningJobStatus(t *testing.T, manager *knowledgeLearningJobManager, id string, want KnowledgeLearningJobStatus) KnowledgeLearningJobView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.status(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	job, err := manager.status(id)
	t.Fatalf("job status=%#v err=%v, want %q", job, err, want)
	return KnowledgeLearningJobView{}
}
