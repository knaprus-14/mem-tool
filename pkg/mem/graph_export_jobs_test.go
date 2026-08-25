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

func TestKnowledgeMapExportManagerLifecycleProgressAndDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	manager := newKnowledgeMapExportManager(ctx, func(ctx context.Context, request KnowledgeGraphExportRequest, progress knowledgeGraphExportProgressFunc) (KnowledgeGraphExportArtifact, error) {
		close(started)
		progress("pinning", 40, 2, 5)
		select {
		case <-release:
		case <-ctx.Done():
			return KnowledgeGraphExportArtifact{}, ctx.Err()
		}
		progress("rendering", 75, 5, 5)
		return KnowledgeGraphExportArtifact{
			Format: request.Format, Filename: "map.md", ContentType: "text/markdown", Data: []byte("artifact"),
			Digest: request.ExpectedDigest, StateDigest: request.ExpectedStateDigest, NodeCount: 3, EdgeCount: 2, Evidence: 4,
		}, nil
	})
	job, err := manager.start(testKnowledgeMapExportJobRequest())
	if err != nil || job.Status != KnowledgeMapExportJobQueued || job.QueuePosition != 1 {
		t.Fatalf("unexpected queued job: %#v err=%v", job, err)
	}
	<-started
	running := waitKnowledgeMapExportJobStatus(t, manager, job.ID, KnowledgeMapExportJobRunning)
	if running.ProgressPercent != 40 || running.Phase != "pinning" || running.CompletedUnits != 2 || running.TotalUnits != 5 {
		t.Fatalf("progress was not retained: %#v", running)
	}
	close(release)
	completed := waitKnowledgeMapExportJobStatus(t, manager, job.ID, KnowledgeMapExportJobCompleted)
	if !completed.DownloadReady || completed.ProgressPercent != 100 || completed.Bytes != len("artifact") || completed.NodeCount != 3 {
		t.Fatalf("completed job is incomplete: %#v", completed)
	}
	artifact, err := manager.artifact(job.ID)
	if err != nil || string(artifact.Data) != "artifact" {
		t.Fatalf("artifact unavailable: %#v err=%v", artifact, err)
	}
	manager.markDownloaded(job.ID)
	downloaded := waitKnowledgeMapExportJobStatus(t, manager, job.ID, KnowledgeMapExportJobDownloaded)
	if downloaded.DownloadReady {
		t.Fatalf("downloaded job retained artifact: %#v", downloaded)
	}
	if _, err := manager.artifact(job.ID); !errors.Is(err, ErrKnowledgeMapExportJobNotReady) {
		t.Fatalf("downloaded artifact was returned again: %v", err)
	}
	if _, err := manager.cancelJob(job.ID); !errors.Is(err, ErrKnowledgeMapExportJobNotCancelable) {
		t.Fatalf("terminal job was cancellable: %v", err)
	}
}

func TestKnowledgeMapExportManagerBoundsQueueAndCancels(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	firstStarted := make(chan struct{})
	var once sync.Once
	manager := newKnowledgeMapExportManager(ctx, func(ctx context.Context, _ KnowledgeGraphExportRequest, _ knowledgeGraphExportProgressFunc) (KnowledgeGraphExportArtifact, error) {
		once.Do(func() { close(firstStarted) })
		<-ctx.Done()
		return KnowledgeGraphExportArtifact{}, ctx.Err()
	})
	first, err := manager.start(testKnowledgeMapExportJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	waitKnowledgeMapExportJobStatus(t, manager, first.ID, KnowledgeMapExportJobRunning)
	queued := make([]KnowledgeMapExportJobView, 0, MaxKnowledgeMapExportQueuedJobs)
	for i := 0; i < MaxKnowledgeMapExportQueuedJobs; i++ {
		job, err := manager.start(testKnowledgeMapExportJobRequest())
		if err != nil {
			t.Fatalf("queue item %d: %v", i, err)
		}
		queued = append(queued, job)
	}
	if _, err := manager.start(testKnowledgeMapExportJobRequest()); !errors.Is(err, ErrKnowledgeMapExportQueueFull) {
		t.Fatalf("overflowing queue returned %v", err)
	}
	cancelled, err := manager.cancelJob(queued[1].ID)
	if err != nil || cancelled.Status != KnowledgeMapExportJobCancelled {
		t.Fatalf("queued cancellation failed: %#v err=%v", cancelled, err)
	}
	cancelling, err := manager.cancelJob(first.ID)
	if err != nil || !cancelling.CancelRequested || cancelling.Phase != "cancelling" {
		t.Fatalf("running cancellation was not acknowledged: %#v err=%v", cancelling, err)
	}
	waitKnowledgeMapExportJobStatus(t, manager, first.ID, KnowledgeMapExportJobCancelled)
	if _, err := manager.artifact(first.ID); !errors.Is(err, ErrKnowledgeMapExportJobNotReady) {
		t.Fatalf("cancelled job published an artifact: %v", err)
	}
	if _, err := manager.status("invalid"); !errors.Is(err, ErrKnowledgeMapExportJobNotFound) {
		t.Fatalf("invalid job ID returned %v", err)
	}
}

func TestKnowledgeMapExportJobWorkspaceHTTPFlow(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "background-export", Kind: KnowledgeNodeClaim, Label: "Фоновый экспорт",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "background-export-session-capability"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandlerWithSelectionContext(ctx, store, "", token, DefaultKnowledgeMapView, nil)
	page := requestKnowledgeMap(t, handler, http.MethodGet, "/", host)
	for _, marker := range []string{"portableExportProgress", "/api/export/jobs/start", "/api/export/jobs/status", "/api/export/jobs/cancel", "/api/export/jobs/download"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("background export UI is missing %q", marker)
		}
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeGraphExportRequest{
		Format: KnowledgeGraphExportMarkdown, Title: "Фоновая карта", ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest,
	}
	raw, _ := json.Marshal(request)
	if got := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/start", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized job start returned %d", got.Code)
	}
	started := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/start", host, "http://"+host, token, "same-origin", raw)
	if started.Code != http.StatusAccepted {
		t.Fatalf("job start returned %d: %s", started.Code, started.Body.String())
	}
	var job KnowledgeMapExportJobView
	if err := json.Unmarshal(started.Body.Bytes(), &job); err != nil || !validKnowledgeMapExportJobID(job.ID) {
		t.Fatalf("invalid job response: %#v err=%v", job, err)
	}
	jobBody, _ := json.Marshal(knowledgeMapExportJobRequest{JobID: job.ID})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/status", host, "http://"+host, token, "same-origin", jobBody)
		if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &job) != nil {
			t.Fatalf("job status failed: %d %s", status.Code, status.Body.String())
		}
		if job.Status == KnowledgeMapExportJobCompleted {
			break
		}
		if job.Status == KnowledgeMapExportJobFailed || job.Status == KnowledgeMapExportJobCancelled {
			t.Fatalf("job ended without artifact: %#v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.Status != KnowledgeMapExportJobCompleted || !job.DownloadReady {
		t.Fatalf("job did not complete: %#v", job)
	}
	download := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/download", host, "http://"+host, token, "same-origin", jobBody)
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "text/markdown; charset=utf-8" ||
		!strings.Contains(download.Header().Get("Content-Disposition"), "mem-knowledge-map.md") || !strings.Contains(download.Body.String(), "Фоновая карта") {
		t.Fatalf("job download failed: status=%d type=%q body=%q", download.Code, download.Header().Get("Content-Type"), download.Body.String())
	}
	status := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/status", host, "http://"+host, token, "same-origin", jobBody)
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &job) != nil || job.Status != KnowledgeMapExportJobDownloaded {
		t.Fatalf("download was not finalized: %d %#v", status.Code, job)
	}
	if got := requestKnowledgeMapMutation(t, handler, "/api/export/jobs/download", host, "http://"+host, token, "same-origin", jobBody); got.Code != http.StatusConflict {
		t.Fatalf("second download returned %d", got.Code)
	}
}

func TestExportKnowledgeGraphContextCancellationAndProgress(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "progress-export", Kind: KnowledgeNodeClaim, Label: "Прогресс",
		Status: KnowledgeStatusActive, Origin: KnowledgeOriginSource, Evidence: []EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeGraphExportRequest{Format: KnowledgeGraphExportMarkdown, ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.exportKnowledgeGraph(ctx, request, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled export returned %v", err)
	}
	var phases []string
	var percents []int
	if _, err := store.exportKnowledgeGraph(context.Background(), request, func(phase string, percent, _, _ int) {
		phases, percents = append(phases, phase), append(percents, percent)
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) < 5 || phases[0] != "validating" || phases[len(phases)-1] != "completed" || percents[len(percents)-1] != 100 {
		t.Fatalf("unexpected progress: phases=%v percents=%v", phases, percents)
	}
	for i := 1; i < len(percents); i++ {
		if percents[i] < percents[i-1] {
			t.Fatalf("progress moved backwards: %v", percents)
		}
	}
}

func testKnowledgeMapExportJobRequest() KnowledgeGraphExportRequest {
	return KnowledgeGraphExportRequest{
		Format: KnowledgeGraphExportMarkdown, Title: "Job",
		ExpectedDigest: "sha256:content", ExpectedStateDigest: "sha256:state",
	}
}

func waitKnowledgeMapExportJobStatus(t *testing.T, manager *knowledgeMapExportManager, id string, want KnowledgeMapExportJobStatus) KnowledgeMapExportJobView {
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
	return KnowledgeMapExportJobView{}
}
