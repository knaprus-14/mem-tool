package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMindMapExportCLIWritesEveryPortableFormat(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := handleMindMap(nil, store, []string{"create", "Переносимая карта", "--description", "Полный снимок"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(nil, store, []string{
		"add-node", "Переносимая карта", "Переносимая карта", "Ветвь",
		"--summary", "Краткое содержание", "--body", "Полный текст узла",
	}); err != nil {
		t.Fatal(err)
	}

	for _, format := range []mem.ClassicMindMapExportFormat{
		mem.ClassicMindMapExportHTML, mem.ClassicMindMapExportSVG, mem.ClassicMindMapExportPNG,
		mem.ClassicMindMapExportJSON, mem.ClassicMindMapExportOPML, mem.ClassicMindMapExportMarkdown,
		mem.ClassicMindMapExportMermaid, mem.ClassicMindMapExportObsidian,
	} {
		t.Run(string(format), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "map."+string(format))
			stdout, _, err := captureCLIStreams(func() error {
				return handleMindMap(nil, store, []string{
					"export", "Переносимая карта", "--format", string(format), "--output", output,
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output)
			if err != nil || len(data) == 0 {
				t.Fatalf("portable artifact was not written: bytes=%d err=%v", len(data), err)
			}
			if !strings.Contains(stdout, "Формат: "+string(format)) || !strings.Contains(stdout, output) ||
				!strings.Contains(stdout, "размер:") || !strings.Contains(stdout, "digest: sha256:") {
				t.Fatalf("human export output is incomplete: %q", stdout)
			}
		})
	}
}

func TestMindMapExportCLIDoesNotOverwriteWithoutForce(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateClassicMindMap("Безопасный экспорт", ""); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "map.json")
	const original = "do not overwrite"
	if err := os.WriteFile(output, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"export", "Безопасный экспорт", "--format", "json", "--output", output}
	if err := handleMindMap(nil, store, args); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing file was not rejected: %v", err)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != original {
		t.Fatalf("rejected export changed target: data=%q err=%v", data, err)
	}
	stdout, _, err := captureCLIStreams(func() error {
		return handleMindMap(nil, store, append(args, "--force"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(output); err != nil || len(data) == 0 || string(data) == original {
		t.Fatalf("forced export did not atomically replace target: bytes=%d err=%v", len(data), err)
	}
	if !strings.Contains(stdout, "экспортирована") {
		t.Fatalf("forced export output=%q", stdout)
	}
}

func TestMindMapExportCLIRefusesActiveDatabaseAsOutput(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateClassicMindMap("Защищённая база", ""); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("store.db"+suffix, func(t *testing.T) {
			target := store.Path() + suffix
			before, beforeErr := os.ReadFile(target)
			if beforeErr != nil && !os.IsNotExist(beforeErr) {
				t.Fatal(beforeErr)
			}
			err := handleMindMap(nil, store, []string{
				"export", "Защищённая база", "--format", "json", "--output", target, "--force",
			})
			if err == nil || !strings.Contains(err.Error(), "SQLite") {
				t.Fatalf("protected database target %q was not rejected: %v", target, err)
			}
			after, afterErr := os.ReadFile(target)
			if beforeErr == nil {
				if afterErr != nil || !bytes.Equal(after, before) {
					t.Fatalf("rejected export changed %q: err=%v", target, afterErr)
				}
			} else if !os.IsNotExist(afterErr) {
				t.Fatalf("rejected export created missing SQLite sidecar %q: err=%v", target, afterErr)
			}
			if _, err := store.LoadClassicMindMap("Защищённая база"); err != nil {
				t.Fatalf("rejected export damaged active database: %v", err)
			}
		})
	}
}

func TestMindMapExportCLIRefusesHardlinkToProtectedDatabaseFile(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "store.db")
	for index, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("store.db"+suffix, func(t *testing.T) {
			protected := database + suffix
			original := []byte("sqlite fixture " + suffix)
			if err := os.WriteFile(protected, original, 0o600); err != nil {
				t.Fatal(err)
			}
			hardlink := filepath.Join(directory, fmt.Sprintf("export-%d.json", index))
			if err := os.Link(protected, hardlink); err != nil {
				t.Skipf("hard links are unavailable: %v", err)
			}
			if err := rejectClassicMindMapExportDatabaseTarget(hardlink, database); err == nil || !strings.Contains(err.Error(), "SQLite") {
				t.Fatalf("hardlink to protected SQLite file %q was not rejected: %v", protected, err)
			}
			data, err := os.ReadFile(protected)
			if err != nil || !bytes.Equal(data, original) {
				t.Fatalf("hardlink check damaged protected file: data=%q err=%v", data, err)
			}
		})
	}
}

func TestParseMindMapExportCLIOptionsIsStrict(t *testing.T) {
	options, err := parseClassicMindMapExportCLIOptions([]string{
		"Карта", "--format", "SVG", "--output", `D:\exports\map.svg`, "--force",
	})
	if err != nil || options.mapRef != "Карта" || options.format != mem.ClassicMindMapExportSVG ||
		options.outputPath != `D:\exports\map.svg` || !options.force {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	for _, args := range [][]string{
		{"Карта", "--output", "map.svg"},
		{"Карта", "--format", "svg"},
		{"Карта", "--format", "pdf", "--output", "map.pdf"},
		{"Карта", "--format", "svg", "--format", "png", "--output", "map.svg"},
		{"Карта", "--format", "svg", "--output", "map.svg", "--unknown"},
	} {
		if _, err := parseClassicMindMapExportCLIOptions(args); err == nil {
			t.Fatalf("invalid export options were accepted: %#v", args)
		}
	}
}
