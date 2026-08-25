package main

import (
	"errors"
	"fmt"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

const knowledgeGraphExportUsage = "использование: mem map export --format markdown|outline|opml|graphml|gexf|mermaid|obsidian --output <путь> [--title <текст>] [--force]\n  без флагов команда сохраняет прежнее поведение: JSON графа в stdout"

type knowledgeGraphExportCLIOptions struct {
	format     mem.KnowledgeGraphExportFormat
	outputPath string
	title      string
	force      bool
}

func handleKnowledgeGraphPortableExport(store *Store, args []string) error {
	if store == nil {
		return errors.New("активная база карты знаний недоступна")
	}
	options, err := parseKnowledgeGraphExportCLIOptions(args)
	if err != nil {
		return err
	}
	if err := rejectClassicMindMapExportDatabaseTarget(options.outputPath, store.Path()); err != nil {
		return err
	}
	pin, err := store.BuildKnowledgeGraphExportPin()
	if err != nil {
		return fmt.Errorf("map export: закрепить текущий граф: %w", err)
	}
	artifact, err := store.ExportKnowledgeGraph(mem.KnowledgeGraphExportRequest{
		Format: options.format, Title: options.title, ExpectedDigest: pin.Digest, ExpectedStateDigest: pin.StateDigest,
	})
	if err != nil {
		return fmt.Errorf("map export: %w", err)
	}
	outputPath, err := writeClassicMindMapExportArtifact(options.outputPath, artifact.Data, options.force)
	if err != nil {
		return err
	}
	fmt.Printf("%s Расширенная карта знаний экспортирована.\n", ui.Mark("ok"))
	fmt.Printf("Формат: %s · файл: %s · размер: %d байт\n", artifact.Format, outputPath, len(artifact.Data))
	fmt.Printf("Узлов: %d · связей: %d · evidence: %d (current %d, stale %d, missing %d)\n", artifact.NodeCount, artifact.EdgeCount, artifact.Evidence, artifact.Current, artifact.Stale, artifact.Missing)
	fmt.Printf("Digest: %s · state: %s\n", artifact.Digest, artifact.StateDigest)
	return nil
}

func parseKnowledgeGraphExportCLIOptions(args []string) (knowledgeGraphExportCLIOptions, error) {
	var options knowledgeGraphExportCLIOptions
	formatSet, outputSet, titleSet := false, false, false
	value := func(index *int, flag string) (string, error) {
		if *index+1 >= len(args) {
			return "", fmt.Errorf("%s ожидает значение\n%s", flag, knowledgeGraphExportUsage)
		}
		*index++
		return args[*index], nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if formatSet {
				return options, errors.New("--format указан повторно")
			}
			formatSet = true
			format, err := value(&i, "--format")
			if err != nil {
				return options, err
			}
			options.format = mem.KnowledgeGraphExportFormat(strings.ToLower(strings.TrimSpace(format)))
		case "--output":
			if outputSet {
				return options, errors.New("--output указан повторно")
			}
			outputSet = true
			output, err := value(&i, "--output")
			if err != nil {
				return options, err
			}
			options.outputPath = strings.TrimSpace(output)
		case "--title":
			if titleSet {
				return options, errors.New("--title указан повторно")
			}
			titleSet = true
			title, err := value(&i, "--title")
			if err != nil {
				return options, err
			}
			options.title = strings.TrimSpace(title)
		case "--force":
			if options.force {
				return options, errors.New("--force указан повторно")
			}
			options.force = true
		default:
			return options, fmt.Errorf("неизвестный аргумент map export %q\n%s", args[i], knowledgeGraphExportUsage)
		}
	}
	if !formatSet || !outputSet || options.outputPath == "" {
		return options, errors.New(knowledgeGraphExportUsage)
	}
	switch options.format {
	case mem.KnowledgeGraphExportMarkdown, mem.KnowledgeGraphExportOutline, mem.KnowledgeGraphExportOPML,
		mem.KnowledgeGraphExportGraphML, mem.KnowledgeGraphExportGEXF, mem.KnowledgeGraphExportMermaid, mem.KnowledgeGraphExportObsidian:
		return options, nil
	default:
		return options, fmt.Errorf("неподдерживаемый формат map export %q; доступны markdown, outline, opml, graphml, gexf, mermaid, obsidian", options.format)
	}
}
