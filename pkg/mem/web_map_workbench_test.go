package mem

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type webBranchProviderFunc func(context.Context, AnswerRequest) (string, error)

func (f webBranchProviderFunc) Generate(ctx context.Context, r AnswerRequest) (string, error) {
	return f(ctx, r)
}

func TestWebMapWorkbenchSnapshotEvidenceAndModel(t *testing.T) {
	dir, s, id, root, edit := webEditFixture(t)
	ctx := context.Background()
	if _, err := EditWebMapNodeIn(ctx, dir, "alice", id, root, edit); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private-folder", "manual.pdf")
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
	}
	if err := s.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapCandidatesIn(ctx, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	hit := page.Items[0]
	if _, err = ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: 2, RequestID: strings.Repeat("c", 32), EntryID: hit.ID, Pin: hit.Pin}); err != nil {
		t.Fatal(err)
	}
	view, err := ReadWebMapBranchIn(ctx, dir, id, root, 3)
	if err != nil || len(view.Study.Cards) != 1 || len(view.Manifest.Evidence) != 1 || view.Manifest.Digest == "" {
		t.Fatal(view, err)
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), "private-folder") {
		t.Fatal("server path leak", string(data))
	}
	if view.Study.Cards[0].Back != edit.Summary+"\n\n"+edit.Body {
		t.Fatal("study material changed", view.Study.Cards)
	}
	if _, err = ReadWebMapBranchIn(ctx, dir, id, "Edited", 3); !errors.Is(err, ErrClassicMindMapNodeNotFound) {
		t.Fatal("label accepted", err)
	}
	if _, err = ReadWebMapBranchIn(ctx, dir, id, root, 2); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal("revision", err)
	}
	calls := 0
	service := &ClassicMindMapAIService{Config: AnswerConfig{Model: "fixture", ContextChars: 12000}, Provider: webBranchProviderFunc(func(ctx context.Context, r AnswerRequest) (string, error) {
		calls++
		if strings.Contains(r.Prompt, "private-folder") {
			t.Fatal("prompt leaked path")
		}
		return `{"claims":[{"text":"Ответ по ветви","citations":["E1"]}]}`, nil
	})}
	in := WebMapBranchInput{Revision: 3, Question: "Что известно?", Manifest: view.Manifest.Digest}
	answer, err := answerWebMapBranchWithService(ctx, dir, id, root, in, service)
	if err != nil || !strings.Contains(answer.Answer, "Ответ по ветви") || len(answer.Sources) != 1 || calls != 1 {
		t.Fatal(answer, err, calls)
	}
	in.Manifest = strings.Repeat("0", 64)
	if _, err = answerWebMapBranchWithService(ctx, dir, id, root, in, service); !errors.Is(err, ErrWebMapBranchChanged) || calls != 1 {
		t.Fatal("stale preview called model", err, calls)
	}
	in.Manifest = view.Manifest.Digest
	service.Provider = webBranchProviderFunc(func(ctx context.Context, r AnswerRequest) (string, error) {
		_, e := s.db.Exec(`UPDATE entries SET text='replacement' WHERE id=?`, hit.ID)
		if e != nil {
			t.Fatal(e)
		}
		return `{"claims":[{"text":"Ответ","citations":["E1"]}]}`, nil
	})
	if _, err = answerWebMapBranchWithService(ctx, dir, id, root, in, service); !errors.Is(err, ErrWebMapBranchChanged) {
		t.Fatal("changed during generation", err)
	}
	view, err = ReadWebMapBranchIn(ctx, dir, id, root, 3)
	if err != nil || len(view.Manifest.Evidence) != 0 || view.Unavailable != 1 {
		t.Fatal("stale source", view, err)
	}
	in.Manifest = view.Manifest.Digest
	if _, err = answerWebMapBranchWithService(ctx, dir, id, root, in, service); !errors.Is(err, ErrWebMapBranchEmpty) {
		t.Fatal(err)
	}
}

func TestWebMapComparisonMatchesCoreWithoutWrites(t *testing.T) {
	dir, s, id, root, edit := webEditFixture(t)
	ctx := context.Background()
	other, err := s.DuplicateClassicMindMap(id, "Copy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EditWebMapNodeIn(ctx, dir, "alice", id, root, edit); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListClassicMindMapChanges(id, 100)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.CompareClassicMindMaps(ClassicMindMapCompareRequest{LeftMapRef: id, RightMapRef: other.Map.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CompareWebMapsIn(ctx, dir, id, WebMapCompareInput{Revision: 2, OtherID: other.Map.ID, OtherRevision: 1})
	if err != nil || got.Digest != want.Digest || len(got.Items) != len(want.Items) {
		t.Fatal(got, want, err)
	}
	after, _ := s.ListClassicMindMapChanges(id, 100)
	if len(before) != len(after) {
		t.Fatal("read wrote history")
	}
	if _, err = CompareWebMapsIn(ctx, dir, id, WebMapCompareInput{Revision: 2, OtherID: other.Map.ID, OtherRevision: 2}); !errors.Is(err, ErrClassicMindMapRevisionConflict) {
		t.Fatal(err)
	}
}
