package mem

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestClassicMindMapAIWorkspacePreviewThenPublish(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"AI карта","summary":"Проверенный preview","body_markdown":"","kind":"topic","citations":[]}]}`}}
	workspace := NewClassicMindMapAIWorkspace(context.Background(), classicMindMapAITestService(store, provider))
	handler := NewClassicMindMapWorkspaceHandlerWithAI(store, "session", workspace)

	startedResponse := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/start", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"action": "new_map",
		"prompt": "Построй карту",
		"title":  "AI карта",
		"scope": map[string]any{
			"allow_ungrounded": true,
		},
	})
	var started classicMindMapAIWorkspaceJob
	decodeClassicMindMapTestResponse(t, startedResponse, http.StatusOK, &started)
	if started.ID == "" || started.Status != "running" {
		t.Fatalf("unexpected start response: %#v", started)
	}

	preview := waitClassicMindMapAIWorkspacePreview(t, handler, started.ID)
	if preview.RunID == "" || preview.Status != ClassicMindMapAIPreviewReady || len(preview.Proposals) != 1 {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if maps, err := store.ListClassicMindMaps(true); err != nil || len(maps) != 0 {
		t.Fatalf("browser preview mutated maps: %#v, %v", maps, err)
	}

	publishResponse := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/publish", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"preview_id":              preview.RunID,
		"proposal_ids":            []string{preview.Proposals[0].ID},
		"expected_preview_digest": preview.ProposalDigest,
	})
	var published ClassicMindMapAIApplyResult
	decodeClassicMindMapTestResponse(t, publishResponse, http.StatusOK, &published)
	if published.Document.Map.Title != "AI карта" || published.Document.Map.Mode != ClassicMindMapModeGenerated || published.Document.Map.Revision != 1 {
		t.Fatalf("unexpected browser publication: %#v", published.Document.Map)
	}

	repeat := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/publish", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"preview_id":   preview.RunID,
		"proposal_ids": []string{preview.Proposals[0].ID},
	})
	if repeat.Code != http.StatusConflict {
		t.Fatalf("repeated browser publication status=%d body=%q", repeat.Code, repeat.Body.String())
	}
}

func TestClassicMindMapAIWorkspaceBlankScopeUsesActiveBase(t *testing.T) {
	store, entries := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Методы защиты СПС","summary":"Проверено по базе","body_markdown":"","kind":"topic","citations":["E1"]}]}`}}
	workspace := NewClassicMindMapAIWorkspace(context.Background(), classicMindMapAITestService(store, provider))
	handler := NewClassicMindMapWorkspaceHandlerWithAI(store, "session", workspace)

	startedResponse := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/start", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"action": "new_map", "prompt": "Методы защиты СПС", "title": "Методы защиты СПС", "scope": map[string]any{},
	})
	var started classicMindMapAIWorkspaceJob
	decodeClassicMindMapTestResponse(t, startedResponse, http.StatusOK, &started)
	preview := waitClassicMindMapAIWorkspacePreview(t, handler, started.ID)
	if !preview.Grounded || preview.EvidenceCount != len(entries) || len(preview.Proposals) != 1 {
		t.Fatalf("browser blank scope did not use active base: preview=%#v entries=%d", preview, len(entries))
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls=%d, want one", len(provider.requests))
	}
}

func TestClassicMindMapAIWorkspaceCancelAndUnavailable(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	provider := classicMindMapAIBlockingProvider{}
	workspace := NewClassicMindMapAIWorkspace(context.Background(), classicMindMapAITestService(store, provider))
	handler := NewClassicMindMapWorkspaceHandlerWithAI(store, "session", workspace)

	startedResponse := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/start", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{
		"action": "new_map", "prompt": "Ждать", "title": "Не публиковать",
		"scope": map[string]any{"allow_ungrounded": true},
	})
	var started classicMindMapAIWorkspaceJob
	decodeClassicMindMapTestResponse(t, startedResponse, http.StatusOK, &started)
	deadline := time.Now().Add(3 * time.Second)
	for started.RunID == "" && time.Now().Before(deadline) {
		if current, ok := workspace.get(started.ID); ok {
			started = current
		}
		time.Sleep(5 * time.Millisecond)
	}
	if started.RunID == "" {
		t.Fatal("AI job did not persist its durable run before cancellation")
	}
	cancelResponse := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/cancel", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{"job_id": started.ID})
	var cancelled classicMindMapAIWorkspaceJob
	decodeClassicMindMapTestResponse(t, cancelResponse, http.StatusOK, &cancelled)
	if cancelled.Status != "cancelled" {
		t.Fatalf("unexpected cancellation response: %#v", cancelled)
	}
	if maps, err := store.ListClassicMindMaps(true); err != nil || len(maps) != 0 {
		t.Fatalf("cancelled job mutated maps: %#v, %v", maps, err)
	}
	var durableStatus string
	if err := store.db.QueryRow(`SELECT status FROM mind_map_generation_runs WHERE id=?`, started.RunID).Scan(&durableStatus); err != nil {
		t.Fatal(err)
	}
	if durableStatus != string(ClassicMindMapAICancelled) {
		t.Fatalf("durable run status=%q, want cancelled", durableStatus)
	}
	var previews int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_previews WHERE run_id=?`, started.RunID).Scan(&previews); err != nil || previews != 0 {
		t.Fatalf("cancelled run stored %d hidden previews: %v", previews, err)
	}

	withoutAI := NewClassicMindMapWorkspaceHandler(store, "session")
	unavailable := classicMindMapWorkspaceRequest(t, withoutAI, "/api/assistant/start", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{})
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("assistant without provider status=%d body=%q", unavailable.Code, unavailable.Body.String())
	}
}

func TestClassicMindMapAIWorkspaceCancelDoesNotHideCommittedPreview(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"n1","parent_ref":"","label":"Готовая карта","summary":"Preview уже сохранён","body_markdown":"","kind":"topic","citations":[]}]}`}}
	service := classicMindMapAITestService(store, provider)
	preview, err := PrepareClassicMindMapAIPreview(context.Background(), service, ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "Построй карту", Title: "Готовая карта",
		Scope: ClassicMindMapAIScope{AllowUngrounded: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	workspace := NewClassicMindMapAIWorkspace(context.Background(), service)
	jobID := "job-at-preview-commit-boundary"
	workspace.jobs[jobID] = &classicMindMapAIWorkspaceJob{
		ID: jobID, RunID: preview.RunID, Status: "running", Phase: "validating",
		Started: preview.Created, Updated: preview.Created, cancel: func() {},
	}
	handler := NewClassicMindMapWorkspaceHandlerWithAI(store, "session", workspace)
	response := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/cancel", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{"job_id": jobID})
	if response.Code != http.StatusConflict {
		t.Fatalf("cancel-vs-preview status=%d body=%q", response.Code, response.Body.String())
	}

	current, ok := workspace.get(jobID)
	if !ok || current.Status != string(ClassicMindMapAIPreviewReady) || current.Preview == nil {
		t.Fatalf("committed preview was hidden by cancellation: %#v, found=%v", current, ok)
	}
	loaded, err := store.LoadClassicMindMapAIPreview(preview.RunID)
	if err != nil || loaded.Status != ClassicMindMapAIPreviewReady {
		t.Fatalf("durable preview status=%q err=%v", loaded.Status, err)
	}
	published, err := store.ApplyClassicMindMapAIPreview(ClassicMindMapAIApplyRequest{
		RunID: preview.RunID, ExpectedPreviewDigest: preview.ProposalDigest, Actor: "test",
	})
	if err != nil || published.Document.Map.Revision != 1 {
		t.Fatalf("preview committed before cancellation was not publishable: %#v, %v", published, err)
	}
}

func TestClassicMindMapAIWorkspaceRejectsUnboundedConcurrentJobs(t *testing.T) {
	store, _ := newClassicMindMapAITestStore(t)
	workspace := NewClassicMindMapAIWorkspace(context.Background(), classicMindMapAITestService(store, classicMindMapAIBlockingProvider{}))
	for i := 0; i < maxClassicMindMapAIWorkspaceJobs; i++ {
		id := fmt.Sprintf("job-%03d", i)
		workspace.jobs[id] = &classicMindMapAIWorkspaceJob{ID: id, Status: "running"}
	}
	if _, err := workspace.start(ClassicMindMapAIPreviewRequest{
		Action: ClassicMindMapAINewMap, Prompt: "overflow", Scope: ClassicMindMapAIScope{AllowUngrounded: true},
	}); err == nil {
		t.Fatal("workspace accepted a job after reaching its hard concurrent limit")
	}
	if len(workspace.jobs) != maxClassicMindMapAIWorkspaceJobs {
		t.Fatalf("workspace job count=%d, want %d", len(workspace.jobs), maxClassicMindMapAIWorkspaceJobs)
	}
}

func TestClassicMindMapAIHTTPPreviewBoundsAndSanitizesEvidence(t *testing.T) {
	sentinel := "FULL-EXCERPT-SENTINEL-MUST-NOT-LEAK"
	fullExcerpt := strings.Repeat("длинный фрагмент ", maxClassicMindMapAIHTTPExcerptRunes) + sentinel
	preview := ClassicMindMapAIPreview{
		Version: ClassicMindMapAIPreviewVersion, RunID: "mmg-test", Status: ClassicMindMapAIPreviewReady,
		Action: ClassicMindMapAIExpand, TargetMapID: "mm-test", TargetNodeID: "mmn-test",
		BaseRevision: 7, Grounded: true, ProposalDigest: "sha256:proposal", Title: "Preview",
		Evidence: []GroundedEvidence{{EvidenceRef: "E1"}, {EvidenceRef: "E2"}},
		Proposals: []ClassicMindMapAIProposal{{
			ID: "proposal-1", Label: "Узел", Kind: ClassicMindMapNodeSubtopic,
			Evidence: []EvidenceAnchor{{
				CitationID: "cite-human", DocumentID: "secret-document-id",
				DocumentRevision: "secret-document-revision", ChunkHash: "secret-chunk-hash",
				EvidenceHash: "secret-evidence-hash", SourcePath: "C:/docs/manual.pdf",
				Page: 12, BlockIndex: 3, BlockChunkIndex: 2, Excerpt: fullExcerpt,
			}},
		}},
	}
	httpPreview := classicMindMapAIHTTPPreviewFrom(preview, 4)
	if httpPreview.EvidenceCount != 2 || httpPreview.CitationCount != 1 || httpPreview.BatchCount != 4 {
		t.Fatalf("preview counts=%d/%d/%d", httpPreview.EvidenceCount, httpPreview.CitationCount, httpPreview.BatchCount)
	}
	if len(httpPreview.Proposals) != 1 || len(httpPreview.Proposals[0].Evidence) != 1 || httpPreview.Proposals[0].EvidenceCount != 1 {
		t.Fatalf("sanitized proposals=%#v", httpPreview.Proposals)
	}
	citation := httpPreview.Proposals[0].Evidence[0]
	if citation.DocumentTitle != "manual.pdf" || citation.Page != 12 || citation.BlockIndex != 3 || citation.BlockChunkIndex != 2 {
		t.Fatalf("human citation lost coordinates: %#v", citation)
	}
	if utf8.RuneCountInString(citation.Excerpt) > maxClassicMindMapAIHTTPExcerptRunes || strings.Contains(citation.Excerpt, sentinel) {
		t.Fatalf("excerpt was not bounded: runes=%d value=%q", utf8.RuneCountInString(citation.Excerpt), citation.Excerpt)
	}
	encoded, err := json.Marshal(classicMindMapAIWorkspaceJob{ID: "job", Status: "preview", Preview: &httpPreview})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{sentinel, "secret-document-id", "secret-document-revision", "secret-chunk-hash", "secret-evidence-hash", `"manifest_digest"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("HTTP preview leaked %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"evidence_count":2`) || !strings.Contains(string(encoded), `"citation_count":1`) {
		t.Fatalf("HTTP preview omitted useful counts: %s", encoded)
	}
}

type classicMindMapAIBlockingProvider struct{}

func (classicMindMapAIBlockingProvider) Generate(ctx context.Context, _ AnswerRequest) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func waitClassicMindMapAIWorkspacePreview(t *testing.T, handler http.Handler, jobID string) classicMindMapAIHTTPPreview {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := classicMindMapWorkspaceRequest(t, handler, "/api/assistant/status", "127.0.0.1:9000", "http://127.0.0.1:9000", "session", map[string]any{"job_id": jobID})
		var job classicMindMapAIWorkspaceJob
		decodeClassicMindMapTestResponse(t, response, http.StatusOK, &job)
		if job.Status == "failed" || job.Status == "cancelled" {
			t.Fatalf("AI workspace job stopped before preview: %#v", job)
		}
		if job.Preview != nil {
			return *job.Preview
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for AI workspace preview")
	return classicMindMapAIHTTPPreview{}
}
