package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestCommonCLIFlagsRejectUnknownMissingAndInvalidValues(t *testing.T) {
	tests := [][]string{
		{"--bogus"},
		{"-limit"},
		{"-limit", "oops"},
		{"-limit", "0"},
		{"-min-score", "1.1"},
		{"-tags", "-important"},
	}
	for _, args := range tests {
		if err := validateCommonFlags(args); err == nil {
			t.Errorf("validateCommonFlags(%q) accepted invalid arguments", args)
		}
	}
}

func TestAskAndMapBuildArgumentsHonorTerminator(t *testing.T) {
	searchArgs, contextBudget, err := parseAskArgs([]string{"focus", "--", "-context-chars", "literal-value"})
	if err != nil {
		t.Fatal(err)
	}
	if contextBudget != 0 {
		t.Fatalf("context budget after terminator = %d", contextBudget)
	}
	if err := validateCommonFlags(searchArgs, "-limit"); err != nil {
		t.Fatal(err)
	}
	positional, _, _, _, _, _, _, _, _, _ := parseFlags(searchArgs)
	want := []string{"focus", "-context-chars", "literal-value"}
	if strings.Join(positional, "|") != strings.Join(want, "|") {
		t.Fatalf("literal tail was lost: got=%q want=%q", positional, want)
	}
}

func TestRunRejectsMissingDirAndAcceptsColorAuto(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	t.Chdir(t.TempDir())

	for _, args := range [][]string{{"mem", "--dir"}, {"mem", "--dir="}} {
		os.Args = args
		if code := run(); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	os.Args = []string{"mem", "help", "--color=auto"}
	if code := run(); code != 0 {
		t.Fatalf("--color=auto exit code = %d", code)
	}
}

func TestTopLevelCommandsRejectExtraArguments(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	t.Chdir(t.TempDir())

	for _, args := range [][]string{
		{"mem", "init", "unexpected"},
		{"mem", "version", "unexpected"},
		{"mem", "--version", "unexpected"},
		{"mem", "-v", "unexpected"},
		{"mem", "help", "unexpected"},
		{"mem", "--help", "unexpected"},
		{"mem", "-h", "unexpected"},
		{"mem", "stats", "unexpected"},
		{"mem", "sources", "unexpected"},
		{"mem", "where", "unexpected"},
		{"mem", "current", "unexpected"},
		{"mem", "repl", "unexpected"},
		{"mem", "source", "1", "unexpected"},
		{"mem", "show", "1", "unexpected"},
		{"mem", "get", "1", "unexpected"},
		{"mem", "view", "--from-file", "notes.md", "unexpected"},
	} {
		os.Args = args
		if code := run(); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	if _, err := os.Stat(mem.MemDirName); !os.IsNotExist(err) {
		t.Fatalf("rejected init created %s: %v", mem.MemDirName, err)
	}
}

func TestInvalidIndexArityDoesNotCreateOrChangeDatabase(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	t.Run("absent database", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		os.Args = []string{"mem", "index", ".", "unexpected"}
		if code := run(); code == 0 {
			t.Fatal("invalid index command succeeded")
		}
		if _, err := os.Stat(filepath.Join(root, mem.MemDirName)); !os.IsNotExist(err) {
			t.Fatalf("invalid index command created %s: %v", mem.MemDirName, err)
		}
	})

	t.Run("existing database", func(t *testing.T) {
		root := t.TempDir()
		memPath := filepath.Join(root, mem.MemDirName)
		if err := os.Mkdir(memPath, 0o700); err != nil {
			t.Fatal(err)
		}
		storePath := filepath.Join(memPath, "store.db")
		before := []byte("sentinel database bytes")
		if err := os.WriteFile(storePath, before, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		os.Args = []string{"mem", "index", ".", "unexpected"}
		if code := run(); code == 0 {
			t.Fatal("invalid index command succeeded")
		}
		after, err := os.ReadFile(storePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("invalid index command changed store.db: before=%q after=%q", before, after)
		}
		for _, unexpected := range []string{"config.json", "meta.json", "store.db-wal", "store.db-shm"} {
			if _, err := os.Stat(filepath.Join(memPath, unexpected)); !os.IsNotExist(err) {
				t.Fatalf("invalid index command created %s: %v", unexpected, err)
			}
		}
	})
}

func TestInvalidAutocreateCommandsLeaveNoDatabase(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	tests := []struct {
		name string
		args []string
	}{
		{name: "add missing text", args: []string{"add"}},
		{name: "add unknown flag", args: []string{"add", "--bogus"}},
		{name: "add-file extra path", args: []string{"add-file", "one.txt", "two.txt"}},
		{name: "import extra path", args: []string{"import", "one.pdf", "two.pdf"}},
		{name: "config extra value", args: []string{"config", "set-backend", "ollama", "extra"}},
		{name: "config unknown setter", args: []string{"config", "set-unknown", "value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			os.Args = append([]string{"mem"}, test.args...)
			if code := run(); code == 0 {
				t.Fatalf("run(%q) succeeded", os.Args)
			}
			if _, err := os.Stat(filepath.Join(root, mem.MemDirName)); !os.IsNotExist(err) {
				t.Fatalf("rejected command created %s: %v", mem.MemDirName, err)
			}
		})
	}
}

func TestInvalidAutocreatePreflightRunsBeforeDirSwitch(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	start := t.TempDir()
	target := t.TempDir()
	t.Chdir(start)

	os.Args = []string{"mem", "--dir", target, "add-file", "one.txt", "two.txt"}
	if code := run(); code == 0 {
		t.Fatal("invalid add-file command succeeded")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(start)) {
		t.Fatalf("invalid command changed cwd: got=%q want=%q", cwd, start)
	}
	if _, err := os.Stat(filepath.Join(target, mem.MemDirName)); !os.IsNotExist(err) {
		t.Fatalf("invalid command created target %s: %v", mem.MemDirName, err)
	}
}

func TestExactArityPreservesArgumentTerminator(t *testing.T) {
	positionals, err := exactCommandPositionals("index", []string{"--", "-notes"}, 1, "mem index <файл|каталог>")
	if err != nil {
		t.Fatal(err)
	}
	if len(positionals) != 1 || positionals[0] != "-notes" {
		t.Fatalf("literal path after -- = %q", positionals)
	}
	if err := validateTopLevelCommandArgs("stats", []string{"--"}); err != nil {
		t.Fatalf("empty tail after -- was rejected: %v", err)
	}
	if err := validateTopLevelCommandArgs("stats", []string{"--", "literal"}); err == nil {
		t.Fatal("literal extra argument after -- was ignored")
	}
	if request, err := parseShowRequest([]string{"--", "#12"}); err != nil || request.idArg != "#12" {
		t.Fatalf("show terminator parse = %#v, %v", request, err)
	}
	configArgs, err := parseConfigCommandArgs([]string{"set-polza-key", "--", "--secret"})
	if err != nil || len(configArgs) != 2 || configArgs[1] != "--secret" {
		t.Fatalf("config terminator parse = %q, %v", configArgs, err)
	}
}

func TestIndexSourceAndShowRejectIgnoredTailsBeforeStoreAccess(t *testing.T) {
	if err := handleIndex(nil, nil, []string{".", "unexpected"}); err == nil {
		t.Fatal("index accepted an extra path")
	}
	if err := handleSource(nil, []string{"1", "unexpected"}); err == nil {
		t.Fatal("source accepted an extra argument")
	}
	for _, args := range [][]string{
		{"1", "unexpected"},
		{"--from-file", "notes.md", "unexpected"},
		{"--from-file"},
		{"--from-file", "notes.md", "--file", "other.md"},
	} {
		if err := handleShow(nil, args); err == nil {
			t.Errorf("show accepted invalid arguments %q", args)
		}
	}
}

func TestHandleRecentReturnsParserErrorsBeforeStoreAccess(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"-limit", "oops"}, {"-title", "ignored-before-fix"}, {"unexpected"}} {
		err := handleRecent(nil, args)
		if err == nil || (!strings.Contains(err.Error(), "флаг") && !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "аргумент")) {
			t.Fatalf("handleRecent(%q) error = %v", args, err)
		}
	}
}

func TestCommonCLIFlagsHonorArgumentTerminator(t *testing.T) {
	args := []string{"--", "--literal-text"}
	if err := validateCommonFlags(args); err != nil {
		t.Fatal(err)
	}
	positional, _, _, _, _, _, _, _, _, _ := parseFlags(args)
	if len(positional) != 1 || positional[0] != "--literal-text" {
		t.Fatalf("positional = %q", positional)
	}
}

func TestSingleTargetCommandsRejectExtraPositionalsBeforeStoreAccess(t *testing.T) {
	if err := handleAddFile(nil, nil, []string{"one.txt", "two.txt"}); err == nil {
		t.Fatal("add-file accepted two paths")
	}
	if err := handleRetag(nil, []string{"1", "unexpected", "-tags", "tag"}); err == nil {
		t.Fatal("retag accepted an extra positional argument")
	}
}

func TestDeleteAndImportantRejectExtraArgumentsWithoutMutation(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry, err := store.Add("keep me", "", nil, "test", []float32{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(entry.ID, 10)
	if err := handleDelete(store, []string{id, "extra"}); err == nil {
		t.Fatal("delete accepted an extra argument")
	}
	if _, err := store.GetByID(entry.ID); err != nil {
		t.Fatalf("delete mutated store despite invalid arity: %v", err)
	}
	if err := handleImportant(store, []string{id, "extra"}); err == nil {
		t.Fatal("important accepted an extra argument")
	}
	unchanged, err := store.GetByID(entry.ID)
	if err != nil || unchanged.Important {
		t.Fatalf("important mutated store despite invalid arity: entry=%#v err=%v", unchanged, err)
	}
}

func TestConfigSettersRequireExactArity(t *testing.T) {
	originalLoadConfig, originalSaveConfig := loadConfig, saveConfig
	defer func() { loadConfig, saveConfig = originalLoadConfig, originalSaveConfig }()
	cfg := mem.DefaultLocalConfig()
	loadConfig = func() (*Config, error) { return cfg, nil }
	saves := 0
	saveConfig = func(*Config) error { saves++; return nil }

	setters := []string{
		"set-backend", "set-polza-key", "set-polza-model", "set-ollama-model",
		"set-answer-model", "set-answer-base-url", "set-answer-timeout",
		"set-answer-max-tokens", "set-answer-context-chars", "set-chunk-size",
		"set-chunk-overlap", "set-chunk-strategy",
	}
	for _, setter := range setters {
		if err := handleConfig([]string{setter, "value", "extra"}); err == nil {
			t.Errorf("%s accepted extra argument", setter)
		}
	}
	if saves != 0 {
		t.Fatalf("invalid config setters persisted %d change(s)", saves)
	}
}
