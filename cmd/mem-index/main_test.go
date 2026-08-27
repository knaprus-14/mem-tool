package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/knaprus-14/mem-tool/pkg/fileindex"
	"github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestReadCommandsDoNotCreateUninitializedFileIndex(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name string
		run  func() int
	}{
		{"list", func() int { return handleList(nil) }},
		{"show", func() int { return handleShow([]string{"1"}) }},
		{"stats", func() int { return handleStats(nil) }},
		{"rm", func() int { return handleRm([]string{"1"}) }},
		{"find", func() int { return handleFind([]string{"query"}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if code := test.run(); code == 0 {
				t.Fatalf("%s succeeded without an initialized index", test.name)
			}
			if _, err := os.Stat(fileindex.FileIndexDirName); !os.IsNotExist(err) {
				t.Fatalf("%s created %s: err=%v", test.name, fileindex.FileIndexDirName, err)
			}
		})
	}
}

func TestRunRejectsMissingDirAndAcceptsColorAuto(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	t.Chdir(t.TempDir())

	for _, args := range [][]string{{"mem-index", "--dir"}, {"mem-index", "--dir="}} {
		os.Args = args
		if code := run(); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	os.Args = []string{"mem-index", "help", "--color=auto"}
	if code := run(); code != 0 {
		t.Fatalf("--color=auto exit code = %d", code)
	}
}

func TestTopLevelCommandsRejectExtraArguments(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	t.Chdir(t.TempDir())

	for _, args := range [][]string{
		{"mem-index", "version", "unexpected"},
		{"mem-index", "help", "unexpected"},
	} {
		os.Args = args
		if code := run(); code == 0 {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	if _, err := os.Stat(fileindex.FileIndexDirName); !os.IsNotExist(err) {
		t.Fatalf("rejected command created %s: %v", fileindex.FileIndexDirName, err)
	}
}

func TestFileIndexInitializedRequiresConfigMetaAndStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), fileindex.FileIndexDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if fileIndexInitialized(dir) {
		t.Fatal("store.db alone was treated as an initialized file index")
	}
	if err := os.WriteFile(fileindex.FileIndexConfigPathIn(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileindex.FileIndexMetaPathIn(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileIndexInitialized(dir) {
		t.Fatal("config.json, meta.json and store.db were not recognized as a complete index")
	}
}

func TestReadCommandsDoNotCreateStoreInsideIncompleteIndex(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(fileindex.FileIndexDirName, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() int{
		func() int { return handleList(nil) },
		func() int { return handleShow([]string{"1"}) },
		func() int { return handleStats(nil) },
		func() int { return handleRm([]string{"1"}) },
		func() int { return handleFind([]string{"query"}) },
	} {
		if code := run(); code == 0 {
			t.Fatal("read command succeeded against incomplete index")
		}
		if _, err := os.Stat(filepath.Join(fileindex.FileIndexDirName, "store.db")); !os.IsNotExist(err) {
			t.Fatalf("read command created store.db: %v", err)
		}
	}
}

func TestReadCommandsDoNotCreateMissingStoreFromPartialMarkers(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	dir := fileindex.FileIndexDirName
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileindex.FileIndexConfigPathIn(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileindex.FileIndexMetaPathIn(dir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() int{
		func() int { return handleList(nil) },
		func() int { return handleShow([]string{"1"}) },
		func() int { return handleStats(nil) },
		func() int { return handleRm([]string{"1"}) },
		func() int { return handleFind([]string{"query"}) },
	} {
		if code := run(); code == 0 {
			t.Fatal("read command succeeded against partial markers")
		}
		if _, err := os.Stat(fileindex.FileIndexStorePathIn(dir)); !os.IsNotExist(err) {
			t.Fatalf("read command created store.db: %v", err)
		}
	}
}

func TestInitRollsBackDirectoryWhenInitialScanFails(t *testing.T) {
	root := t.TempDir()
	originalScan := scanFileIndex
	defer func() { scanFileIndex = originalScan }()
	scanFileIndex = func(fileindex.ScanOptions, *fileindex.Store, *mem.Config) (fileindex.ScanReport, error) {
		return fileindex.ScanReport{}, errors.New("forced initial scan failure")
	}
	if code := handleInit([]string{root}); code == 0 {
		t.Fatal("init succeeded despite failed initial scan")
	}
	if _, err := os.Stat(filepath.Join(root, fileindex.FileIndexDirName)); !os.IsNotExist(err) {
		t.Fatalf("failed init left a poisoned .fileindex: %v", err)
	}
}

func TestParseFileIndexFlagsRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--bogus"},
		{"-limit"},
		{"-limit", "oops"},
		{"-limit", "0"},
		{"-format", "json"},
	} {
		if _, _, _, _, _, err := parseFileIndexFlags(args); err == nil {
			t.Errorf("parseFileIndexFlags(%q) accepted invalid arguments", args)
		}
	}
}

func TestParseFileIndexFlagsHonorsArgumentTerminator(t *testing.T) {
	positional, _, _, _, _, err := parseFileIndexFlags([]string{"--", "--literal-root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(positional) != 1 || positional[0] != "--literal-root" {
		t.Fatalf("positional = %q", positional)
	}
}

func TestCommandsRejectKnownButInapplicableFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, run := range map[string]func() int{
		"init-limit":       func() int { return handleInit([]string{"-limit", "1"}) },
		"scan-limit":       func() int { return handleScan([]string{"-limit", "1"}) },
		"enrich-limit":     func() int { return handleEnrich([]string{"-limit", "1"}) },
		"find-no-embed":    func() int { return handleFind([]string{"query", "-no-embed"}) },
		"list-no-embed":    func() int { return handleList([]string{"-no-embed"}) },
		"stats-unexpected": func() int { return handleStats([]string{"-limit", "1"}) },
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(); code == 0 {
				t.Fatalf("%s accepted an inapplicable flag", name)
			}
		})
	}
}
