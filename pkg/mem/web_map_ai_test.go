package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func webAIFixture(t *testing.T) (string, *Store, string, string, WebMapAIInput) {
	t.Helper()
	dir, s, id, root, _ := webEditFixture(t)
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Answer.Model = "fake-instruct"
	if err = SaveConfigIn(dir, cfg); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = filepath.Join(dir, "private-ai-folder", "manual.pdf")
	}
	if err = s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapCandidatesIn(context.Background(), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, id, root, WebMapAIInput{RequestID: strings.Repeat("b", 32), Revision: 1, Action: ClassicMindMapAIExpand, Prompt: "Добавь понятную ветвь", Entries: []WebMapAIEvidencePin{{ID: page.Items[0].ID, Pin: page.Items[0].Pin}}}
}

func TestWebMapAIPreviewSelectionApplyHistoryAndRepeat(t *testing.T) {
	dir, s, id, root, in := webAIFixture(t)
	ctx := context.Background()
	task, err := StartWebMapAIIn(ctx, dir, "alice", id, root, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := StartWebMapAIIn(ctx, dir, "alice", id, root, in)
	if err != nil || task.ID != replay.ID {
		t.Fatal(replay, err)
	}
	provider := &classicMindMapAIFakeProvider{answers: []string{`{"nodes":[{"ref":"p","parent_ref":"","label":"План","summary":"Кратко","body_markdown":"Текст <script>alert(1)</script>","kind":"subtopic","citations":["E1"]},{"ref":"c","parent_ref":"p","label":"Детали","summary":"Деталь","body_markdown":"","kind":"fact","citations":["E1"]}]}`}}
	if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	for _, r := range provider.requests {
		if strings.Contains(r.Prompt, "private-ai-folder") {
			t.Fatal("private path sent to model")
		}
	}
	view, err := ReadWebMapAIReviewIn(ctx, dir, "alice", id, task.ID)
	if err != nil || !view.Grounded || len(view.Proposals) != 2 {
		t.Fatal(view, err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "private-ai-folder") {
		t.Fatal("private preview path")
	}
	doc, err := s.LoadClassicMindMap(id)
	if err != nil || len(doc.Nodes) != 1 || doc.Map.Revision != 1 {
		t.Fatal("preview changed map", doc, err)
	}
	apply := WebMapAIApply{RequestID: strings.Repeat("c", 32), Revision: 1, Digest: view.Digest, ProposalIDs: []string{view.Proposals[1].ID}}
	if _, err = ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply); !errors.Is(err, ErrWebMapAIInvalid) {
		t.Fatal("orphan proposal accepted", err)
	}
	apply.ProposalIDs = []string{view.Proposals[0].ID}
	if _, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS web_map_actions(request_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL,revision INTEGER NOT NULL,node_id TEXT NOT NULL,created TEXT NOT NULL); CREATE TRIGGER fail_ai_receipt BEFORE INSERT ON web_map_actions BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply); err == nil {
		t.Fatal("receipt failure ignored")
	}
	doc, _ = s.LoadClassicMindMap(id)
	if doc.Map.Revision != 1 || len(doc.Nodes) != 1 {
		t.Fatal("receipt rollback lost")
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_ai_receipt`); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply)
	if err != nil || applied.Revision != 2 {
		t.Fatal(applied, err)
	}
	again, err := ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, apply)
	if err != nil || !again.Replayed {
		t.Fatal("apply replay", again, err)
	}
	doc, err = s.LoadClassicMindMap(id)
	if err != nil || len(doc.Nodes) != 2 || doc.Nodes[1].Label != "План" || len(doc.Nodes[1].Sources) != 1 {
		t.Fatal(doc, err)
	}
	if !strings.Contains(doc.Nodes[1].Sources[0].Evidence.SourcePath, "private-ai-folder") {
		t.Fatal("stored provenance was redacted")
	}
	history, err := ListWebMapHistoryIn(ctx, dir, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = UndoWebMapIn(ctx, dir, "alice", id, WebMapUndo{Revision: 2, ChangeID: history.UndoID, RequestID: strings.Repeat("d", 32)}); err != nil {
		t.Fatal("AI undo", err)
	}
	doc, _ = s.LoadClassicMindMap(id)
	if len(doc.Nodes) != 1 {
		t.Fatal("undo left AI node")
	}
}

func TestWebMapAICancelAndStaleInputs(t *testing.T) {
	for _, mode := range []string{"before", "during", "stale", "restart"} {
		t.Run(mode, func(t *testing.T) {
			dir, s, id, root, in := webAIFixture(t)
			ctx := context.Background()
			task, err := StartWebMapAIIn(ctx, dir, "alice", id, root, in)
			if err != nil {
				t.Fatal(err)
			}
			if err = CancelWebMapAIIn(ctx, dir, "bob", id, task.ID); err == nil {
				t.Fatal("foreign cancel")
			}
			if items, e := ListWebMapAIIn(ctx, dir, "bob", id); e != nil || len(items) != 0 {
				t.Fatal("foreign tasks", items, e)
			}
			calls := 0
			provider := webBranchProviderFunc(func(call context.Context, r AnswerRequest) (string, error) {
				calls++
				if mode == "during" {
					if e := CancelWebMapAIIn(ctx, dir, "alice", id, task.ID); e != nil {
						t.Fatal(e)
					}
					select {
					case <-call.Done():
						return "", call.Err()
					case <-time.After(3 * time.Second):
						t.Fatal("cancel did not interrupt provider")
					}
				}
				return "", errors.New("model must not be called")
			})
			if mode == "before" {
				if err = CancelWebMapAIIn(ctx, dir, "alice", id, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stale" {
				if _, err = s.db.Exec(`UPDATE entries SET text='changed'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "restart" {
				if _, err = s.db.Exec(`UPDATE web_map_ai_tasks SET status='running' WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = processWebMapAIIn(ctx, dir, "alice", provider); err != nil {
				t.Fatal(err)
			}
			items, err := ListWebMapAIIn(ctx, dir, "alice", id)
			if err != nil || len(items) != 1 {
				t.Fatal(items, err)
			}
			want := "failed"
			if mode == "before" || mode == "during" {
				want = "cancelled"
			}
			if items[0].Status != want {
				t.Fatal(items)
			}
			if mode != "during" && calls != 0 {
				t.Fatal("unexpected model call")
			}
			doc, _ := s.LoadClassicMindMap(id)
			if doc.Map.Revision != 1 {
				t.Fatal("failure changed map")
			}
		})
	}
}

func TestWebMapAIFillAndSourceOperations(t *testing.T) {
	for _, action := range []ClassicMindMapAIAction{ClassicMindMapAIFill, ClassicMindMapAIFindSources} {
		t.Run(string(action), func(t *testing.T) {
			dir, s, id, root, in := webAIFixture(t)
			in.Action = action
			ctx := context.Background()
			task, err := StartWebMapAIIn(ctx, dir, "alice", id, root, in)
			if err != nil {
				t.Fatal(err)
			}
			answer := `{"fill":{"summary":"Добавленный текст","body_markdown":"Подробности","citations":["E1"]}}`
			if action == ClassicMindMapAIFindSources {
				answer = `{"sources":[{"evidence_ref":"E1","reason":"Подтверждает тему"}]}`
			}
			if _, err = processWebMapAIIn(ctx, dir, "alice", &classicMindMapAIFakeProvider{answers: []string{answer}}); err != nil {
				t.Fatal(err)
			}
			view, err := ReadWebMapAIReviewIn(ctx, dir, "alice", id, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ApplyWebMapAIIn(ctx, dir, "alice", id, task.ID, WebMapAIApply{RequestID: strings.Repeat("e", 32), Revision: 1, Digest: view.Digest, ProposalIDs: []string{view.Proposals[0].ID}}); err != nil {
				t.Fatal(err)
			}
			doc, err := s.LoadClassicMindMap(id)
			if err != nil || len(doc.Nodes[0].Sources) != 1 {
				t.Fatal(doc, err)
			}
			if action == ClassicMindMapAIFill && doc.Nodes[0].Summary != "Добавленный текст" {
				t.Fatal(doc.Nodes[0])
			}
		})
	}
}

func TestWebMapAIRejectsTokenEscalation(t *testing.T) {
	dir, _, id, root, in := webAIFixture(t)
	ctx := context.Background()
	if _, err := StartWebMapAIIn(ctx, dir, "alice", id, root, in); err != nil {
		t.Fatal(err)
	}
	provider := &classicMindMapAIFakeProvider{errors: []error{&AnswerTokenLimitError{MaxTokens: 4096}}}
	if _, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || provider.requests[0].MaxTokens > 4096 {
		t.Fatal("web token cap bypassed", provider.requests)
	}
	items, err := ListWebMapAIIn(ctx, dir, "alice", id)
	if err != nil || items[0].Status != "failed" {
		t.Fatal(items, err)
	}
}

func TestWebMapAIConfiguredTokenCeilingAndSourcePublication(t *testing.T) {
	for _, budget := range []int{8128, 32768} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			ctx := context.Background()
			dir, _, in := sourceMapFixture(t)
			cfg, err := LoadConfigIn(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Answer.MaxTokens = budget
			if err = SaveConfigIn(dir, cfg); err != nil {
				t.Fatal(err)
			}
			task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
			if err != nil {
				t.Fatal(err)
			}
			expected := []int{4096, 8128}
			provider := &classicMindMapAIFakeProvider{answers: []string{"", sourceMapResponse}, errors: []error{&AnswerTokenLimitError{MaxTokens: 4096}, nil}}
			if budget > 16384 {
				expected = []int{4096, 8192, 16384}
				provider.answers = []string{"", "", sourceMapResponse}
				provider.errors = []error{&AnswerTokenLimitError{MaxTokens: 4096}, &AnswerTokenLimitError{MaxTokens: 8192}, nil}
			}
			if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); !worked || e != nil {
				t.Fatal(worked, e)
			}
			if len(provider.requests) != len(expected) {
				t.Fatal(provider.requests)
			}
			for i, r := range provider.requests {
				if r.MaxTokens != expected[i] {
					t.Fatal(i, r.MaxTokens)
				}
				if strings.Contains(r.Prompt, "private-ai-folder") {
					t.Fatal("path leak")
				}
			}
			if worked, e := processWebMapAIIn(ctx, dir, "alice", provider); !worked || e != nil {
				t.Fatal(worked, e)
			}
			view, e := ReadWebMapIn(ctx, dir, task.MapID)
			if e != nil || view.Map.Revision != 2 || len(view.Nodes) != 3 {
				t.Fatal(view, e)
			}
		})
	}
}

func TestWebMapAITokenExhaustionKeepsActionableError(t *testing.T) {
	ctx := context.Background()
	dir, _, id, root, in := webAIFixture(t)
	cfg, e := LoadConfigIn(dir)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Answer.MaxTokens = 8128
	if e = SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	if _, e = StartWebMapAIIn(ctx, dir, "alice", id, root, in); e != nil {
		t.Fatal(e)
	}
	provider := &classicMindMapAIFakeProvider{errors: []error{&AnswerTokenLimitError{MaxTokens: 4096}, &AnswerTokenLimitError{MaxTokens: 8128}}}
	if _, e = processWebMapAIIn(ctx, dir, "alice", provider); e != nil {
		t.Fatal(e)
	}
	if len(provider.requests) != 2 || provider.requests[1].MaxTokens != 8128 {
		t.Fatal(provider.requests)
	}
	tasks, e := ListWebMapAIIn(ctx, dir, "alice", id)
	if e != nil || len(tasks) != 1 {
		t.Fatal(tasks, e)
	}
	if tasks[0].Status != "failed" || !strings.Contains(tasks[0].Error, "8128") || !strings.Contains(tasks[0].Error, "Часть 1 из") {
		t.Fatal(tasks[0])
	}
	view, e := ReadWebMapIn(ctx, dir, id)
	if e != nil || view.Map.Revision != 1 {
		t.Fatal(view, e)
	}
}

func TestWebMapAIFailureMessageDoesNotExposePrivateErrors(t *testing.T) {
	message := webMapAIFailureMessage(fmt.Errorf("provider: %w", context.DeadlineExceeded), 41, 61)
	if !strings.Contains(message, "Часть 41 из 61") || !strings.Contains(message, "время ожидания") {
		t.Fatal(message)
	}
	private := errors.New("https://secret.invalid/?token=private-token C:/private-folder")
	if got := webMapAIFailureMessage(private, 1, 2); strings.Contains(got, "private") || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
}
