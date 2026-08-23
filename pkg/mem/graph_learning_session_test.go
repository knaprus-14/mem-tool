package mem

import (
	"errors"
	"testing"
	"time"
)

func TestKnowledgeLearningSessionSchedulesOnlyDueReviewedItems(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	request := KnowledgeLearningSessionStartRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest}

	first, err := store.startKnowledgeLearningSessionAt(request, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || len(first.Items) != 2 || first.Summary.NewItems != 2 || first.Summary.ReviewItems != 0 {
		t.Fatalf("unexpected first session: %#v", first)
	}
	if first.Items[0].NodeID != "session-card" || first.Items[1].NodeID != "session-question" {
		t.Fatalf("route order was not preserved: %#v", first.Items)
	}
	graded, err := store.gradeKnowledgeLearningItemAt(KnowledgeLearningGradeRequest{SessionID: first.ID, NodeID: first.Items[0].NodeID, Grade: KnowledgeLearningGradeGood}, now)
	if err != nil {
		t.Fatal(err)
	}
	if graded.Completed != 1 || graded.Total != 2 || graded.Attempt.NextState.IntervalSeconds != int64(24*time.Hour/time.Second) || graded.Attempt.NextState.DueAt != now.Add(24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected first schedule: %#v", graded)
	}
	if _, err := store.gradeKnowledgeLearningItemAt(KnowledgeLearningGradeRequest{SessionID: first.ID, NodeID: first.Items[0].NodeID, Grade: KnowledgeLearningGradeEasy}, now); !errors.Is(err, ErrKnowledgeLearningAlreadyGraded) {
		t.Fatalf("duplicate grade was accepted: %v", err)
	}

	second, err := store.startKnowledgeLearningSessionAt(request, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].NodeID != "session-question" || second.Summary.NewItems != 1 || second.Summary.Deferred != 1 || second.Summary.NextDue == "" {
		t.Fatalf("future review was not deferred: %#v", second)
	}

	history, err := store.buildKnowledgeLearningHistoryAt(KnowledgeLearningHistoryRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if history.Due != 1 || history.New != 1 || history.Scheduled != 1 || len(history.Items) != 2 || history.Items[0].Attempts != 1 {
		t.Fatalf("learning history is incomplete: %#v", history)
	}

	third, err := store.startKnowledgeLearningSessionAt(request, now.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Items) != 2 || third.Summary.NewItems != 1 || third.Summary.ReviewItems != 1 {
		t.Fatalf("due review did not return: %#v", third)
	}
}

func TestKnowledgeLearningSessionFailsClosedWhenItemChanges(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	session, err := store.startKnowledgeLearningSessionAt(KnowledgeLearningSessionStartRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_nodes SET body = 'Изменённый ответ' WHERE id = ?`, session.Items[0].NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.gradeKnowledgeLearningItemAt(KnowledgeLearningGradeRequest{SessionID: session.ID, NodeID: session.Items[0].NodeID, Grade: KnowledgeLearningGradeGood}, now.Add(2*time.Minute)); !errors.Is(err, ErrKnowledgeLearningItemChanged) {
		t.Fatalf("changed item was graded: %v", err)
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_learning_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("failed grade left audit history: attempts=%d err=%v", attempts, err)
	}
}

func TestKnowledgeLearningAttemptHistoryIsAppendOnly(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	session, err := store.startKnowledgeLearningSessionAt(KnowledgeLearningSessionStartRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Limit: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.gradeKnowledgeLearningItemAt(KnowledgeLearningGradeRequest{SessionID: session.ID, NodeID: session.Items[0].NodeID, Grade: KnowledgeLearningGradeHard}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_learning_attempts SET grade = 'easy' WHERE id = ?`, result.Attempt.ID); err == nil {
		t.Fatal("learning attempt update was not blocked")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_learning_attempts WHERE id = ?`, result.Attempt.ID); err == nil {
		t.Fatal("learning attempt delete was not blocked")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_learning_sessions WHERE id = ?`, session.ID); err == nil {
		t.Fatal("learning session delete was not blocked")
	}
}

func TestKnowledgeLearningSchedulerIntervalsAreDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	again := scheduleKnowledgeLearningReview("n", "c", "e", KnowledgeLearningScheduleState{}, false, KnowledgeLearningGradeAgain, now)
	if again.IntervalSeconds != 600 || again.Repetitions != 0 || again.Lapses != 1 || again.EasePermille != 2300 {
		t.Fatalf("unexpected again schedule: %#v", again)
	}
	good := scheduleKnowledgeLearningReview("n", "c", "e", KnowledgeLearningScheduleState{}, false, KnowledgeLearningGradeGood, now)
	good2 := scheduleKnowledgeLearningReview("n", "c", "e", good, true, KnowledgeLearningGradeGood, now.Add(24*time.Hour))
	if good.IntervalSeconds != 86400 || good2.IntervalSeconds != 6*86400 || good2.Repetitions != 2 {
		t.Fatalf("unexpected good sequence: first=%#v second=%#v", good, good2)
	}
	easy := scheduleKnowledgeLearningReview("n", "c", "e", KnowledgeLearningScheduleState{}, false, KnowledgeLearningGradeEasy, now)
	if easy.IntervalSeconds != 4*86400 || easy.EasePermille != 2650 {
		t.Fatalf("unexpected easy schedule: %#v", easy)
	}
}

func knowledgeLearningSessionFixture(t *testing.T) (*Store, KnowledgeSelectionRequest, KnowledgeSelectionManifest) {
	t.Helper()
	store, anchor := graphStoreAndAnchor(t)
	nodes := []KnowledgeNode{
		{ID: "session-card", Kind: KnowledgeNodeCard, Label: "Формула", Body: "I = U / R", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
		{ID: "session-question", Kind: KnowledgeNodeQuestion, Label: "Как найти ток?", Body: "Ожидаемый ответ для проверки:\nРазделить напряжение на сопротивление.", Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}},
	}
	edges := []KnowledgeEdge{{ID: "session-order", From: "session-card", To: "session-question", Kind: KnowledgeRelationPrerequisite, Status: KnowledgeStatusActive, Origin: KnowledgeOriginManual, Evidence: []EvidenceAnchor{anchor}}}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: nodes, Edges: edges}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{"session-card", "session-question"}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, selection, manifest
}
