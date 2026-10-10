package mem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/applog"
)

func TestSourceMapLoggingCorrelatesTaskPlanAndModelRetry(t *testing.T) {
	dir, _, in := sourceMapFixture(t)
	logDir := t.TempDir()
	t.Chdir(logDir)
	var task WebMapAITask
	if code := applog.Run("test", "1.71.0-alpha.26", func() int {
		cfg, err := LoadConfigIn(dir)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Answer.MaxTokens = 8192
		if err = SaveConfigIn(dir, cfg); err != nil {
			t.Fatal(err)
		}
		task, err = CreateWebMapFromSourcesIn(context.Background(), dir, "alice", in)
		if err != nil {
			t.Fatal(err)
		}
		provider := &classicMindMapAIFakeProvider{answers: []string{"", sourceMapResponse}, errors: []error{&AnswerTokenLimitError{MaxTokens: 4096}, nil}}
		for i := 0; i < 2; i++ {
			if _, err = processWebMapAIIn(context.Background(), dir, "alice", provider); err != nil {
				t.Fatal(err)
			}
		}
		return 0
	}); code != 0 {
		t.Fatal(code)
	}
	data, err := os.ReadFile(filepath.Join(logDir, applog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, s := range []string{"ai.task.started", "budget_seconds", "ai.progress", "ai.model.attempt_failed", "token_ceiling", task.ID, "process.exit"} {
		if !strings.Contains(text, s) {
			t.Fatal("missing log evidence", s)
		}
	}
	for _, s := range []string{"private-ai-folder", "chunk-0", "chunk-1", in.Title, sourceMapResponse} {
		if strings.Contains(text, s) {
			t.Fatal("private payload in log", s)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid JSONL", line)
		}
	}
}
