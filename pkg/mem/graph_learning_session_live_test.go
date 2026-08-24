package mem

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestKnowledgeMapLearningSessionAPIsAreAuthorizedAndFunctional(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	const token = "learning-session-capability-with-enough-entropy"
	const host = "127.0.0.1:8765"
	handler := NewKnowledgeMapWorkspaceHandler(store, "", token, DefaultKnowledgeMapView)

	startRequest := KnowledgeLearningSessionStartRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Limit: 1}
	raw, _ := json.Marshal(startRequest)
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/session/start", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized learning session returned %d", got.Code)
	}
	started := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/session/start", host, "http://"+host, token, "same-origin", raw)
	if started.Code != http.StatusOK {
		t.Fatalf("learning session start failed: status=%d body=%q", started.Code, started.Body.String())
	}
	var session KnowledgeLearningSession
	if err := json.Unmarshal(started.Body.Bytes(), &session); err != nil || session.ID == "" || len(session.Items) != 1 {
		t.Fatalf("learning session response is invalid: session=%#v err=%v", session, err)
	}

	gradeRequest := KnowledgeLearningGradeRequest{SessionID: session.ID, NodeID: session.Items[0].NodeID, Grade: KnowledgeLearningGradeGood}
	raw, _ = json.Marshal(gradeRequest)
	graded := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/session/grade", host, "http://"+host, token, "same-origin", raw)
	if graded.Code != http.StatusOK {
		t.Fatalf("learning grade failed: status=%d body=%q", graded.Code, graded.Body.String())
	}
	var grade KnowledgeLearningGradeResult
	if err := json.Unmarshal(graded.Body.Bytes(), &grade); err != nil || grade.Attempt.ID == 0 || grade.Completed != 1 || grade.Total != 1 {
		t.Fatalf("learning grade response is invalid: result=%#v err=%v", grade, err)
	}
	if duplicate := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/session/grade", host, "http://"+host, token, "same-origin", raw); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate API grade returned %d", duplicate.Code)
	}

	historyRequest := KnowledgeLearningHistoryRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest}
	raw, _ = json.Marshal(historyRequest)
	historyResponse := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/history", host, "http://"+host, token, "same-origin", raw)
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("learning history failed: status=%d body=%q", historyResponse.Code, historyResponse.Body.String())
	}
	var history KnowledgeLearningHistory
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil || len(history.Items) != 2 || history.Scheduled != 1 {
		t.Fatalf("learning history response is invalid: history=%#v err=%v", history, err)
	}

	exportRequest := KnowledgeLearningExportRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedRouteDigest: history.RouteDigest, Format: KnowledgeLearningExportAnki, Title: "API learning"}
	raw, _ = json.Marshal(exportRequest)
	if got := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/export", host, "http://"+host, "wrong", "same-origin", raw); got.Code != http.StatusForbidden {
		t.Fatalf("unauthorized learning export returned %d", got.Code)
	}
	exported := requestKnowledgeMapMutation(t, handler, "/api/selection/learning/export", host, "http://"+host, token, "same-origin", raw)
	if exported.Code != http.StatusOK || !strings.Contains(exported.Body.String(), "#deck:API learning") ||
		exported.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		!strings.Contains(exported.Header().Get("Content-Disposition"), "mem-learning-anki.txt") {
		t.Fatalf("learning export failed: status=%d type=%q disposition=%q body=%q", exported.Code, exported.Header().Get("Content-Type"), exported.Header().Get("Content-Disposition"), exported.Body.String())
	}

	readOnly := NewKnowledgeMapLiveHandler(store, "")
	for _, path := range []string{"/api/selection/learning/session/start", "/api/selection/learning/session/grade", "/api/selection/learning/history", "/api/selection/learning/export"} {
		if got := requestKnowledgeMapMutation(t, readOnly, path, host, "http://"+host, token, "same-origin", raw); got.Code != http.StatusNotFound {
			t.Fatalf("read-only map exposed %s: %d", path, got.Code)
		}
	}
}
