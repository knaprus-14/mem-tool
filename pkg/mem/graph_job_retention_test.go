package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func retainedExportFixture(now *time.Time) *knowledgeMapExportManager {
	m := newKnowledgeMapExportManager(context.Background(), nil)
	m.now = func() time.Time { return *now }
	return m
}

func retainedLearningFixture(now *time.Time) *knowledgeLearningJobManager {
	m := newKnowledgeLearningJobManager(context.Background(), nil)
	m.now = func() time.Time { return *now }
	return m
}

func seedRetainedExport(m *knowledgeMapExportManager, n int, completed time.Time, status KnowledgeMapExportJobStatus) string {
	id := fmt.Sprintf("kex-%032x", n)
	ctx, cancel := context.WithCancel(m.ctx)
	m.jobs[id] = &knowledgeMapExportJob{ctx: ctx, cancel: cancel, createdAt: completed.Add(-time.Minute), completedAt: completed,
		view:    KnowledgeMapExportJobView{Version: 1, ID: id, Status: status, Phase: string(status), Format: KnowledgeGraphExportMarkdown, CreatedAt: completed.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completed.Format(time.RFC3339Nano), DownloadReady: true, Filename: "not-retained", Bytes: 10},
		request: testKnowledgeMapExportJobRequest(), artifact: &KnowledgeGraphExportArtifact{Data: []byte("not-retained")}}
	m.order = append(m.order, id)
	return id
}

func seedRetainedLearning(m *knowledgeLearningJobManager, n int, completed time.Time, status KnowledgeLearningJobStatus) string {
	id := fmt.Sprintf("klj-%032x", n)
	ctx, cancel := context.WithCancel(m.ctx)
	m.jobs[id] = &knowledgeLearningJob{ctx: ctx, cancel: cancel, createdAt: completed.Add(-time.Minute), completedAt: completed,
		view:   KnowledgeLearningJobView{Version: 1, ID: id, Status: status, Phase: string(status), CreatedAt: completed.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completed.Format(time.RFC3339Nano), ResultReady: true, Model: "not-retained", RequestedCount: 3},
		result: &KnowledgeLearningRun{ID: "persisted-run"}}
	m.order = append(m.order, id)
	return id
}

func TestGraphJobRetentionExpiredStatusAndFixedDeadline(t *testing.T) {
	initial := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	for _, status := range []KnowledgeMapExportJobStatus{KnowledgeMapExportJobCompleted, KnowledgeMapExportJobFailed, KnowledgeMapExportJobCancelled, KnowledgeMapExportJobDownloaded} {
		t.Run("export/"+string(status), func(t *testing.T) {
			now := initial
			m := retainedExportFixture(&now)
			id := seedRetainedExport(m, 1, initial, status)
			now = initial.Add(KnowledgeMapExportJobRetention + time.Second)
			view, err := m.status(id)
			if err != nil || view.Status != KnowledgeMapExportJobFailed || view.Phase != "expired" || view.ErrorCode != "expired" || view.DownloadReady || view.Bytes != 0 || view.Filename != "" || view.Message == "" || view.ElapsedMS != 60000 {
				t.Fatal(view, err)
			}
			if len(m.jobs) != 0 || len(m.order) != 0 || len(m.expired.items) != 1 {
				t.Fatal("large record retained", m)
			}
			if _, err = m.artifact(id); !errors.Is(err, ErrKnowledgeMapExportJobNotReady) {
				t.Fatal(err)
			}
			if _, err = m.cancelJob(id); !errors.Is(err, ErrKnowledgeMapExportJobNotCancelable) {
				t.Fatal(err)
			}
			now = initial.Add(KnowledgeMapExportJobRetention + KnowledgeJobTombstoneRetention - time.Nanosecond)
			again, err := m.status(id)
			if err != nil || again != view {
				t.Fatal("poll changed tombstone", again, err)
			}
			now = now.Add(time.Nanosecond)
			if _, err = m.status(id); !errors.Is(err, ErrKnowledgeMapExportJobNotFound) {
				t.Fatal("poll renewed lifetime", err)
			}
			if len(m.expired.items) != 0 || len(m.expired.order) != 0 {
				t.Fatal("tombstone memory retained")
			}
		})
	}
	for _, status := range []KnowledgeLearningJobStatus{KnowledgeLearningJobCompleted, KnowledgeLearningJobFailed, KnowledgeLearningJobCancelled} {
		t.Run("learning/"+string(status), func(t *testing.T) {
			now := initial
			m := retainedLearningFixture(&now)
			id := seedRetainedLearning(m, 1, initial, status)
			now = initial.Add(KnowledgeLearningJobRetention + time.Second)
			view, err := m.status(id)
			if err != nil || view.Status != KnowledgeLearningJobFailed || view.Phase != "expired" || view.ErrorCode != "expired" || view.ResultReady || view.Model != "" || view.Message == "" || view.ElapsedMS != 60000 {
				t.Fatal(view, err)
			}
			if len(m.jobs) != 0 || len(m.order) != 0 || len(m.expired.items) != 1 {
				t.Fatal("large record retained", m)
			}
			if _, err = m.result(id); !errors.Is(err, ErrKnowledgeLearningJobNotReady) {
				t.Fatal(err)
			}
			if _, err = m.cancelJob(id); !errors.Is(err, ErrKnowledgeLearningJobNotCancelable) {
				t.Fatal(err)
			}
			now = initial.Add(KnowledgeLearningJobRetention + KnowledgeJobTombstoneRetention - time.Nanosecond)
			again, err := m.status(id)
			if err != nil || again != view {
				t.Fatal("poll changed tombstone", again, err)
			}
			now = now.Add(time.Nanosecond)
			if _, err = m.status(id); !errors.Is(err, ErrKnowledgeLearningJobNotFound) {
				t.Fatal("poll renewed lifetime", err)
			}
		})
	}
}

func TestGraphJobRetentionCannotBypassWithResultOrCancel(t *testing.T) {
	for _, action := range []string{"result", "cancel"} {
		t.Run(action, func(t *testing.T) {
			now := time.Now().UTC()
			export, learning := retainedExportFixture(&now), retainedLearningFixture(&now)
			eid := seedRetainedExport(export, 1, now, KnowledgeMapExportJobCompleted)
			lid := seedRetainedLearning(learning, 1, now, KnowledgeLearningJobCompleted)
			if _, err := export.artifact(eid); err != nil {
				t.Fatal(err)
			}
			if _, err := learning.result(lid); err != nil {
				t.Fatal(err)
			}
			now = now.Add(KnowledgeLearningJobRetention + time.Second)
			if action == "result" {
				if _, err := export.artifact(eid); !errors.Is(err, ErrKnowledgeMapExportJobNotReady) {
					t.Fatal("download bypassed timeout", err)
				}
				if _, err := learning.result(lid); !errors.Is(err, ErrKnowledgeLearningJobNotReady) {
					t.Fatal("result bypassed timeout", err)
				}
			} else {
				if _, err := export.cancelJob(eid); !errors.Is(err, ErrKnowledgeMapExportJobNotCancelable) {
					t.Fatal(err)
				}
				if _, err := learning.cancelJob(lid); !errors.Is(err, ErrKnowledgeLearningJobNotCancelable) {
					t.Fatal(err)
				}
			}
			if e, err := export.status(eid); err != nil || e.Phase != "expired" {
				t.Fatal(e, err)
			}
			if l, err := learning.status(lid); err != nil || l.Phase != "expired" {
				t.Fatal(l, err)
			}
		})
	}
}

func TestGraphJobRetentionCapacityEvictionAndTies(t *testing.T) {
	now := time.Now().UTC()
	export, learning := retainedExportFixture(&now), retainedLearningFixture(&now)
	// Identical completion times still evict in the deterministic creation order.
	for i := 1; i <= MaxKnowledgeJobTombstones+MaxKnowledgeMapExportRetainedJobs+9; i++ {
		seedRetainedExport(export, i, now, KnowledgeMapExportJobCompleted)
		seedRetainedLearning(learning, i, now, KnowledgeLearningJobCompleted)
		export.cleanupLocked(now)
		learning.cleanupLocked(now)
		if len(export.jobs) >= MaxKnowledgeMapExportRetainedJobs || len(learning.jobs) >= MaxKnowledgeLearningRetainedJobs || len(export.expired.items) > MaxKnowledgeJobTombstones || len(learning.expired.items) > MaxKnowledgeJobTombstones {
			t.Fatal("capacity exceeded")
		}
	}
	if _, err := export.status(fmt.Sprintf("kex-%032x", 1)); !errors.Is(err, ErrKnowledgeMapExportJobNotFound) {
		t.Fatal("oldest tombstone not bounded", err)
	}
	if _, err := learning.status(fmt.Sprintf("klj-%032x", 1)); !errors.Is(err, ErrKnowledgeLearningJobNotFound) {
		t.Fatal(err)
	}
	if v, err := export.status(fmt.Sprintf("kex-%032x", 20)); err != nil || v.Phase != "expired" {
		t.Fatal(v, err)
	}
	if v, err := learning.status(fmt.Sprintf("klj-%032x", 20)); err != nil || v.Phase != "expired" {
		t.Fatal(v, err)
	}
	if len(export.expired.order) != MaxKnowledgeJobTombstones || len(learning.expired.order) != MaxKnowledgeJobTombstones {
		t.Fatal("order cap")
	}
	// A cold access well beyond both deadlines must not grant a fresh hour.
	now = now.Add(KnowledgeMapExportJobRetention + KnowledgeJobTombstoneRetention + time.Second)
	export.cleanupLocked(now)
	learning.cleanupLocked(now)
	if len(export.jobs)+len(export.expired.items)+len(learning.jobs)+len(learning.expired.items) != 0 {
		t.Fatal("late cleanup renewed lifetime")
	}
}

func TestGraphJobRetentionByteEvictionMatchesTimedExpiry(t *testing.T) {
	now := time.Now().UTC()
	m := retainedExportFixture(&now)
	first := seedRetainedExport(m, 1, now, KnowledgeMapExportJobCompleted)
	second := seedRetainedExport(m, 2, now, KnowledgeMapExportJobCompleted)
	// No giant allocation is necessary to exercise the byte reservation boundary.
	m.ensureArtifactCapacityLocked(MaxKnowledgeMapExportRetainedBytes-len("not-retained"), "incoming")
	if v, err := m.status(first); err != nil || v.Phase != "expired" || v.DownloadReady {
		t.Fatal(v, err)
	}
	if _, err := m.artifact(first); !errors.Is(err, ErrKnowledgeMapExportJobNotReady) {
		t.Fatal(err)
	}
	if _, err := m.artifact(second); err != nil {
		t.Fatal("wrong equal-time victim", err)
	}
	if _, err := m.cancelJob(first); !errors.Is(err, ErrKnowledgeMapExportJobNotCancelable) {
		t.Fatal(err)
	}
	if _, ok := m.jobs[first]; ok {
		t.Fatal("evicted artifact metadata retained")
	}
}

func TestGraphJobRetentionNeverExpiresActiveOrResurrectsTerminal(t *testing.T) {
	now := time.Now().UTC()
	export, learning := retainedExportFixture(&now), retainedLearningFixture(&now)
	eid := seedRetainedExport(export, 1, now.Add(-24*time.Hour), KnowledgeMapExportJobRunning)
	lid := seedRetainedLearning(learning, 1, now.Add(-24*time.Hour), KnowledgeLearningJobRunning)
	export.jobs[eid].completedAt = time.Time{}
	learning.jobs[lid].completedAt = time.Time{}
	export.cleanupLocked(now)
	learning.cleanupLocked(now)
	if v, err := export.status(eid); err != nil || v.Status != KnowledgeMapExportJobRunning {
		t.Fatal(v, err)
	}
	if v, err := learning.status(lid); err != nil || v.Status != KnowledgeLearningJobRunning {
		t.Fatal(v, err)
	}
	export.cancelAll()
	learning.cancelAll()
	now = now.Add(KnowledgeLearningJobRetention + time.Second)
	export.cleanupLocked(now)
	learning.cleanupLocked(now)
	export.updateProgress(eid, "rendering", 99, 1, 1)
	learning.updateProgress(lid, "generating", 99, 1, 1)
	export.run(eid)
	learning.run(lid)
	if v, err := export.status(eid); err != nil || v.Phase != "expired" {
		t.Fatal(v, err)
	}
	if v, err := learning.status(lid); err != nil || v.Phase != "expired" {
		t.Fatal(v, err)
	}
}

func TestGraphJobRetentionHTTPExpiredDiffersFromUnknown(t *testing.T) {
	now := time.Now().UTC()
	export, learning := retainedExportFixture(&now), retainedLearningFixture(&now)
	eid := seedRetainedExport(export, 1, now, KnowledgeMapExportJobCompleted)
	lid := seedRetainedLearning(learning, 1, now, KnowledgeLearningJobCompleted)
	now = now.Add(KnowledgeLearningJobRetention + time.Second)
	const host, token = "127.0.0.1:8765", "test-retained-job-session"
	workspace := &KnowledgeMapWorkspace{SessionToken: token}
	for _, tc := range []struct {
		name, id string
		serve    func(http.ResponseWriter, *http.Request)
	}{
		{"export", eid, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/status":
				serveKnowledgeMapExportJobStatus(w, r, workspace, export)
			case "/cancel":
				serveKnowledgeMapExportJobCancel(w, r, workspace, export)
			default:
				serveKnowledgeMapExportJobDownload(w, r, workspace, export)
			}
		}},
		{"learning", lid, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/status":
				serveKnowledgeMapLearningJobStatus(w, r, workspace, learning)
			case "/cancel":
				serveKnowledgeMapLearningJobCancel(w, r, workspace, learning)
			default:
				serveKnowledgeMapLearningJobResult(w, r, workspace, learning)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"job_id": tc.id})
			handler := http.HandlerFunc(tc.serve)
			for repeat := 0; repeat < 2; repeat++ {
				response := requestKnowledgeMapMutation(t, handler, "/status", host, "http://"+host, token, "same-origin", body)
				if response.Code != 200 || !strings.Contains(response.Body.String(), `"phase":"expired"`) || !strings.Contains(response.Body.String(), `"error_code":"expired"`) {
					t.Fatal(response.Code, response.Body)
				}
			}
			for _, route := range []string{"/cancel", "/result"} {
				response := requestKnowledgeMapMutation(t, handler, route, host, "http://"+host, token, "same-origin", body)
				if response.Code != 409 {
					t.Fatal(route, response.Code, response.Body)
				}
			}
			unknown, _ := json.Marshal(map[string]string{"job_id": tc.id[:4] + strings.Repeat("f", 32)})
			for _, route := range []string{"/status", "/cancel", "/result"} {
				response := requestKnowledgeMapMutation(t, handler, route, host, "http://"+host, token, "same-origin", unknown)
				if response.Code != 404 {
					t.Fatal(route, response.Code, response.Body)
				}
			}
			if response := requestKnowledgeMapMutation(t, handler, "/status", host, "http://"+host, "wrong", "same-origin", body); response.Code != 403 {
				t.Fatal(response.Code)
			}
		})
	}
}
