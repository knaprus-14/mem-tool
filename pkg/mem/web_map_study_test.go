package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWebMapStudyPersistenceIsolationAndInvalidation(t *testing.T) {
	dir, s, id, root, edit := webEditFixture(t)
	ctx := context.Background()
	if _, err := EditWebMapNodeIn(ctx, dir, "alice", id, root, edit); err != nil {
		t.Fatal(err)
	}
	view, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 2)
	if err != nil || len(view.Study.Cards) != 1 || view.DueCount != 1 {
		t.Fatal(view, err)
	}
	card := view.Study.Cards[0]
	confidence := 70
	in := WebMapStudyReview{Revision: 2, Manifest: view.Manifest.Digest, CardID: card.ID, RequestID: strings.Repeat("1", 32), Grade: KnowledgeLearningGradeGood, Response: "My independent answer", Confidence: &confidence, UsedHint: true}
	saved, err := ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in)
	if err != nil || saved.Progress.Repetitions != 1 || saved.Progress.IntervalSeconds != 86400 || saved.Progress.LastAttemptID == 0 {
		t.Fatal(saved, err)
	}
	replay, err := ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in)
	if err != nil || !replay.Replayed || replay.Progress.LastAttemptID != saved.Progress.LastAttemptID {
		t.Fatal(replay, err)
	}
	in.Response = "Changed payload"
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in); !errors.Is(err, ErrWebMapEditRequest) {
		t.Fatal(err)
	}
	in.Response = "My independent answer"
	in.RequestID = strings.Repeat("2", 32)
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal("parallel retry graded twice", err)
	}
	reloaded, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 2)
	if err != nil || reloaded.DueCount != 0 || reloaded.Progress[card.ID].LastAttemptID != saved.Progress.LastAttemptID {
		t.Fatal(reloaded, err)
	}
	bob, err := ReadWebMapStudyIn(ctx, dir, "bob", id, root, 2)
	if err != nil || bob.DueCount != 1 || bob.Progress[card.ID].LastAttemptID != 0 {
		t.Fatal("progress crossed actors", bob, err)
	}
	var response string
	var gotConfidence, hint, count int
	if err = s.db.QueryRow(`SELECT response,confidence,used_hint FROM web_map_study_attempts WHERE id=?`, saved.Progress.LastAttemptID).Scan(&response, &gotConfidence, &hint); err != nil || response != in.Response || gotConfidence != 70 || hint != 1 {
		t.Fatal(response, gotConfidence, hint, err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM web_map_study_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = s.db.Exec(`UPDATE web_map_study_attempts SET response='changed'`); err == nil {
		t.Fatal("attempts not immutable")
	}
	edit.Revision = 2
	edit.RequestID = strings.Repeat("3", 32)
	edit.Body = "Updated material"
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, root, edit); err != nil {
		t.Fatal(err)
	}
	changed, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
	if err != nil || changed.DueCount != 1 || changed.Progress[changed.Study.Cards[0].ID].LastAttemptID != 0 {
		t.Fatal("new material reused schedule", changed, err)
	}
	in.Revision = 3
	in.CardID = changed.Study.Cards[0].ID
	in.Manifest = changed.Manifest.Digest
	in.Grade = KnowledgeLearningGradeAgain
	if got, e := ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in); e != nil || got.Progress.IntervalSeconds != 600 || got.Progress.Repetitions != 0 {
		t.Fatal(got, e)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT state_json FROM web_map_study_state WHERE actor='alice'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored KnowledgeLearningScheduleState
	if err = json.Unmarshal([]byte(raw), &stored); err != nil || stored.LastAttemptID <= saved.Progress.LastAttemptID {
		t.Fatal(stored, err)
	}
	history, err := ReadWebMapStudyHistoryIn(ctx, dir, "alice", id, root, 3, changed.Study.Cards[0].ID)
	if err != nil || len(history.Items) != 2 || !history.Items[0].MatchesCurrent || history.Items[1].MatchesCurrent || history.Items[1].Answer != card.Back || history.Items[1].Confidence == nil || *history.Items[1].Confidence != 70 {
		t.Fatal(history, err)
	}
	private, err := ReadWebMapStudyHistoryIn(ctx, dir, "bob", id, root, 3, changed.Study.Cards[0].ID)
	if err != nil || len(private.Items) != 0 {
		t.Fatal("private answers leaked", private, err)
	}
}

func TestWebMapStudyStaleEvidenceAndManifest(t *testing.T) {
	dir, s, id, root, edit := webEditFixture(t)
	ctx := context.Background()
	if _, err := EditWebMapNodeIn(ctx, dir, "alice", id, root, edit); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	if err := s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapCandidatesIn(ctx, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	hit := page.Items[0]
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: 2, RequestID: strings.Repeat("a", 32), EntryID: hit.ID, Pin: hit.Pin}); err != nil {
		t.Fatal(err)
	}
	view, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
	if err != nil {
		t.Fatal(err)
	}
	in := WebMapStudyReview{Revision: 3, Manifest: view.Manifest.Digest, CardID: view.Study.Cards[0].ID, RequestID: strings.Repeat("b", 32), Grade: KnowledgeLearningGradeEasy}
	if _, err = s.db.Exec(`UPDATE entries SET text='different material' WHERE id=?`, hit.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal(err)
	}
	stale, err := ReadWebMapStudyIn(ctx, dir, "alice", id, root, 3)
	if err != nil || stale.DueCount != 0 || stale.Progress[in.CardID].Current {
		t.Fatal(stale, err)
	}
	in.Manifest = stale.Manifest.Digest
	if _, err = ReviewWebMapStudyIn(ctx, dir, "alice", id, root, in); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal("stale evidence accepted", err)
	}
}
