package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestHandleMapProfileHumanJSONAndValidation(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stdout, _, err := captureCLIStreams(func() error { return handleMapProfile(store, []string{"-iterations", "1"}) })
	if err != nil || !strings.Contains(stdout, "Профиль производительности карты знаний") ||
		!strings.Contains(stdout, "sqlite_graph_load") || !strings.Contains(stdout, "html_serialization") ||
		!strings.Contains(stdout, "Браузер") {
		t.Fatalf("human map profile failed: err=%v output=%q", err, stdout)
	}
	stdout, _, err = captureCLIStreams(func() error { return handleMapProfile(store, []string{"--iterations", "1", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var report mem.KnowledgeMapProfileReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Options.Iterations != 1 || len(report.Stages) != 4 || report.Snapshot.HTMLBytes == 0 {
		t.Fatalf("JSON map profile is invalid: err=%v report=%#v output=%q", err, report, stdout)
	}
	invalid := [][]string{
		{"-iterations", "0"}, {"-iterations", "21"}, {"-iterations"},
		{"--iterations", "1", "-iterations", "2"}, {"--view", ""}, {"--json", "--json"}, {"--unknown"},
	}
	for _, args := range invalid {
		if err := handleMapProfile(store, args); err == nil {
			t.Fatalf("invalid profile arguments accepted: %#v", args)
		}
	}
}
