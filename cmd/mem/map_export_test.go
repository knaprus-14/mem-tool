package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestKnowledgeGraphPortableExportCLIWritesEveryFormat(t *testing.T) {
	store, anchor := cliGraphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{
		Nodes: []mem.KnowledgeNode{
			{ID: "cli-a", Kind: mem.KnowledgeNodeClaim, Label: "CLI утверждение", Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginSource, Evidence: []mem.EvidenceAnchor{anchor}},
			{ID: "cli-b", Kind: mem.KnowledgeNodeTask, Label: "CLI задача", Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginManual, Evidence: []mem.EvidenceAnchor{anchor}},
		},
		Edges: []mem.KnowledgeEdge{{ID: "cli-edge", From: "cli-b", To: "cli-a", Kind: mem.KnowledgeRelationBasedOn, Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginManual, Evidence: []mem.EvidenceAnchor{anchor}}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, format := range []mem.KnowledgeGraphExportFormat{
		mem.KnowledgeGraphExportMarkdown, mem.KnowledgeGraphExportOutline, mem.KnowledgeGraphExportOPML,
		mem.KnowledgeGraphExportGraphML, mem.KnowledgeGraphExportGEXF, mem.KnowledgeGraphExportMermaid, mem.KnowledgeGraphExportObsidian,
	} {
		t.Run(string(format), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "map."+string(format))
			stdout, stderr, err := captureCLIStreams(func() error {
				return handleMap(testCLIConfig(1000, "paragraph"), store, []string{"export", "--format", string(format), "--output", output, "--title", "CLI карта"})
			})
			if err != nil || stderr != "" {
				t.Fatalf("export failed: stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			data, err := os.ReadFile(output)
			if err != nil || len(data) == 0 {
				t.Fatalf("export file was not written: bytes=%d err=%v", len(data), err)
			}
			for _, marker := range []string{"Расширенная карта знаний экспортирована", "Узлов: 2", "current 3", "Digest: sha256:"} {
				if !strings.Contains(stdout, marker) {
					t.Fatalf("human output is missing %q: %s", marker, stdout)
				}
			}
		})
	}
}

func TestKnowledgeGraphPortableExportCLIPreservesLegacyAndRefusesOverwrite(t *testing.T) {
	store, anchor := cliGraphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "legacy", Kind: mem.KnowledgeNodeClaim, Label: "JSON", Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginSource, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	legacy, status, err := captureCLIStreams(func() error { return handleMap(testCLIConfig(1000, "paragraph"), store, []string{"export"}) })
	if err != nil || status != "" || !strings.Contains(legacy, `"id": "legacy"`) {
		t.Fatalf("legacy stdout JSON changed: stdout=%q stderr=%q err=%v", legacy, status, err)
	}
	output := filepath.Join(t.TempDir(), "map.md")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"export", "--format", "markdown", "--output", output}
	if _, _, err := captureCLIStreams(func() error { return handleMap(testCLIConfig(1000, "paragraph"), store, args) }); err == nil {
		t.Fatal("existing export target was overwritten without --force")
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "keep" {
		t.Fatalf("rejected export changed target: %q err=%v", data, err)
	}
	args = append(args, "--force")
	if _, _, err := captureCLIStreams(func() error { return handleMap(testCLIConfig(1000, "paragraph"), store, args) }); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(output); err != nil || !strings.Contains(string(data), "Карта знаний mem-tool") {
		t.Fatalf("forced export failed: bytes=%d err=%v", len(data), err)
	}
}

func TestParseKnowledgeGraphExportCLIOptionsIsStrict(t *testing.T) {
	options, err := parseKnowledgeGraphExportCLIOptions([]string{"--format", "GraphML", "--output", `D:\exports\map.graphml`, "--title", "Карта", "--force"})
	if err != nil || options.format != mem.KnowledgeGraphExportGraphML || options.outputPath != `D:\exports\map.graphml` || options.title != "Карта" || !options.force {
		t.Fatalf("valid options were rejected: %#v err=%v", options, err)
	}
	for _, args := range [][]string{
		{}, {"--format", "markdown"}, {"--output", "map.md"}, {"--format", "bad", "--output", "map"},
		{"--format", "markdown", "--format", "outline", "--output", "map"}, {"map", "--format", "markdown", "--output", "map.md"},
	} {
		if _, err := parseKnowledgeGraphExportCLIOptions(args); err == nil {
			t.Fatalf("invalid options were accepted: %#v", args)
		}
	}
}
