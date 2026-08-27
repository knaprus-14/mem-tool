package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestShouldSaveReplHistoryRejectsPolzaKey(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"/config set-polza-key super-secret", false},
		{"  /CONFIG SET-POLZA-KEY super-secret  ", false},
		{"/config \"set-polza-key\" super-secret", false},
		{"/config 'set-polza-key' super-secret", false},
		{"/config \"set-polza-key super-secret", false},
		{"/config set-polza-model model", true},
		{"/search set-polza-key", true},
		{"ordinary search", true},
	}
	for _, test := range tests {
		if got := shouldSaveReplHistory(test.line); got != test.want {
			t.Errorf("shouldSaveReplHistory(%q) = %v, want %v", test.line, got, test.want)
		}
	}
}

func TestSplitReplArgumentsSupportsQuotesEscapesAndEmptyArguments(t *testing.T) {
	parts, err := splitReplArguments(`add "two word body" -title "say \"hello\"" -tags "" "C:\Docs\notes.md" "C:\\escaped\\path"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"add", "two word body", "-title", `say "hello"`, "-tags", "", `C:\Docs\notes.md`, `C:\\escaped\\path`}
	if len(parts) != len(want) {
		t.Fatalf("parts=%q want=%q", parts, want)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("parts[%d]=%q want=%q; all=%q", i, parts[i], want[i], parts)
		}
	}

	parts, err = splitReplArguments(`search escaped\ space escaped\"quote escaped\\slash`)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"search", "escaped space", `escaped"quote`, `escaped\\slash`}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("outside escape parts[%d]=%q want=%q", i, parts[i], want[i])
		}
	}
}

func TestSplitReplArgumentsPreservesQuotedAndUnquotedUNCPaths(t *testing.T) {
	for _, line := range []string{
		`index "\\server\share\folder with spaces\notes.md"`,
		`index \\server\share\notes.md`,
	} {
		parts, err := splitReplArguments(line)
		if err != nil {
			t.Fatalf("splitReplArguments(%q): %v", line, err)
		}
		if len(parts) != 2 || !strings.HasPrefix(parts[1], `\\server\share\`) {
			t.Fatalf("splitReplArguments(%q) collapsed UNC path: %q", line, parts)
		}
	}
}

func TestSplitReplArgumentsPreservesQuotedTrailingBackslash(t *testing.T) {
	for _, test := range []struct {
		line string
		want []string
	}{
		{`index "C:\folder\"`, []string{`index`, `C:\folder\`}},
		{`index "\\server\share\"`, []string{`index`, `\\server\share\`}},
		{`index "C:\folder\" -title "x"`, []string{`index`, `C:\folder\`, `-title`, `x`}},
	} {
		parts, err := splitReplArguments(test.line)
		if err != nil {
			t.Fatalf("splitReplArguments(%q): %v", test.line, err)
		}
		if !slices.Equal(parts, test.want) {
			t.Fatalf("splitReplArguments(%q) = %q, want %q", test.line, parts, test.want)
		}
	}
}

func TestParseReplCommandLineRejectsMalformedQuote(t *testing.T) {
	if _, _, err := parseReplCommandLine(`/show --from-file "unfinished path`); err == nil || !strings.Contains(err.Error(), "незакрытая кавычка") {
		t.Fatalf("malformed quote error = %v", err)
	}
	_, stderr, err := captureCLIStreams(func() error {
		if quit := dispatchReplLine(nil, nil, `/show --from-file "unfinished path`); quit {
			t.Fatal("malformed command ended REPL")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Ошибка разбора команды") {
		t.Fatalf("malformed command was not reported: %q", stderr)
	}
}

func TestDispatchReplAddPreservesQuotedTitle(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	getEmbedding = func(_ *Config, _ string) ([]float32, error) { return []float32{1, 0}, nil }
	cfg := mem.DefaultLocalConfig()

	_, stderr, err := captureCLIStreams(func() error {
		if quit := dispatchReplLine(cfg, store, `/add "body with spaces" -title "two words"`); quit {
			t.Fatal("add unexpectedly ended REPL")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("quoted add failed: %q", stderr)
	}
	entries, err := store.Recent(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "body with spaces" || entries[0].Title != "two words" {
		t.Fatalf("quoted add result = %#v", entries)
	}
}

func TestDispatchReplShowPreservesQuotedFromFilePath(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(t.TempDir(), "folder with spaces", "manual.md")
	text := "quoted path evidence"
	if err := store.ReplaceDocumentChunks(source, []mem.DocumentChunk{{
		Text: text, Backend: "test", Embedding: []float32{1}, ChunkIndex: 0, TotalChunks: 1,
		Provenance: mem.Provenance{
			DocumentID: "repl-quoted-path", DocumentRevision: mem.ChunkContentHash("revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown",
			BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		},
	}}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := captureCLIStreams(func() error {
		line := `/show --from-file "` + source + `"`
		if quit := dispatchReplLine(mem.DefaultLocalConfig(), store, line); quit {
			t.Fatal("show unexpectedly ended REPL")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, text) || !strings.Contains(stdout, source) {
		t.Fatalf("quoted show failed: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestDispatchReplNoArgumentCommandsRejectTailsBeforeAccess(t *testing.T) {
	for _, line := range []string{"/stats unexpected", "/sources unexpected", "/where unexpected", "/help unexpected", "/exit unexpected"} {
		_, stderr, err := captureCLIStreams(func() error {
			if quit := dispatchReplLine(nil, nil, line); quit {
				t.Fatalf("%s unexpectedly ended REPL", line)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr, "не принимает аргументы") {
			t.Errorf("%s tail was not rejected: %q", line, stderr)
		}
	}
}

func TestSanitizeReplHistoryMigratesPreviouslyStoredSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.txt")
	input := strings.Join([]string{
		"/search retained query",
		"/config set-polza-key first-secret",
		"  /CONFIG SET-POLZA-KEY second-secret  ",
		"/config set-polza-model retained-model",
		"ordinary retained text",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(input), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := sanitizeReplHistory(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, secret := range []string{"first-secret", "second-secret", "set-polza-key"} {
		if strings.Contains(strings.ToLower(output), strings.ToLower(secret)) {
			t.Fatalf("sanitized history retained %q: %q", secret, output)
		}
	}
	for _, retained := range []string{"/search retained query", "/config set-polza-model retained-model", "ordinary retained text"} {
		if !strings.Contains(output, retained) {
			t.Fatalf("sanitized history lost %q: %q", retained, output)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".history-sanitize-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary migration files remain: matches=%q err=%v", matches, err)
	}
}

func TestSanitizeReplHistoryMissingFileIsNoop(t *testing.T) {
	if err := sanitizeReplHistory(filepath.Join(t.TempDir(), "missing-history.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchReplConfigRefreshesSession(t *testing.T) {
	originalLoadConfig, originalSaveConfig := loadConfig, saveConfig
	defer func() { loadConfig, saveConfig = originalLoadConfig, originalSaveConfig }()

	diskConfig := mem.DefaultLocalConfig()
	sessionConfig := mem.DefaultLocalConfig()
	sessionConfig.Ollama.Model = "old-session-model"
	var persisted *Config
	loadConfig = func() (*Config, error) {
		if persisted != nil {
			copy := *persisted
			return &copy, nil
		}
		return diskConfig, nil
	}
	saveConfig = func(cfg *Config) error {
		copy := *cfg
		persisted = &copy
		return nil
	}

	if quit := dispatchReplLine(sessionConfig, nil, "/config set-ollama-model current-session-model"); quit {
		t.Fatal("config command unexpectedly ended the REPL")
	}
	if persisted == nil || persisted.Ollama.Model != "current-session-model" {
		t.Fatalf("config was not persisted: %#v", persisted)
	}
	if sessionConfig.Ollama.Model != "current-session-model" {
		t.Fatalf("REPL retained stale config: %q", sessionConfig.Ollama.Model)
	}
}

func TestHandleConfigRedactsPolzaKey(t *testing.T) {
	originalLoadConfig := loadConfig
	defer func() { loadConfig = originalLoadConfig }()
	cfg := mem.DefaultLocalConfig()
	cfg.Polza.APIKey = "do-not-print-this-key"
	loadConfig = func() (*Config, error) { return cfg, nil }

	stdout, _, err := captureCLIStreams(func() error { return handleConfig(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, cfg.Polza.APIKey) || !strings.Contains(stdout, "[скрыт]") {
		t.Fatalf("config output did not redact key: %q", stdout)
	}
}
