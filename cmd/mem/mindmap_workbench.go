package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func isMindMapWorkbenchCommand(command string) bool {
	switch command {
	case "templates", "template-create", "ask-branch", "compare", "study":
		return true
	default:
		return false
	}
}

func handleMindMapWorkbench(cfg *Config, store *Store, args []string) error {
	if len(args) == 0 {
		return errors.New("mindmap workbench command is empty")
	}
	command := args[0]
	positional, flags, err := parseMindMapWorkbenchArgs(args[1:])
	if err != nil {
		return err
	}
	switch command {
	case "templates":
		if len(positional) != 0 {
			return errors.New("использование: mem mindmap templates [--json]")
		}
		items := mem.ListClassicMindMapTemplates()
		if flags.jsonOutput {
			return printMindMapJSON(items)
		}
		fmt.Println("Шаблоны классических карт")
		fmt.Println("--------------------------")
		for _, item := range items {
			fmt.Printf("- %s · %s\n  %s\n", item.ID, item.Title, strings.Join(item.Branches, " · "))
		}
		return nil
	case "template-create":
		if len(positional) != 2 {
			return errors.New("использование: mem mindmap template-create <шаблон> <название> [--description <текст>] [--json]")
		}
		doc, err := store.CreateClassicMindMapFromTemplate(mem.ClassicMindMapTemplateRequest{
			TemplateID: positional[0], Title: positional[1], Description: flags.description,
		}, "cli")
		if err != nil {
			return err
		}
		if flags.jsonOutput {
			return printMindMapJSON(doc)
		}
		fmt.Printf("Карта %q создана из шаблона %s: %d узлов, ревизия %d.\n", doc.Map.Title, positional[0], len(doc.Nodes), doc.Map.Revision)
		return nil
	case "ask-branch":
		if len(positional) != 3 {
			return errors.New("использование: mem mindmap ask-branch <карта> <узел> <вопрос> [--json]")
		}
		if cfg == nil {
			return errors.New("mindmap ask-branch: конфигурация answer-модели недоступна")
		}
		answerCfg := cfg.Answer.WithDefaults()
		provider, err := newAnswerProvider(answerCfg)
		if err != nil {
			return err
		}
		doc, err := store.LoadClassicMindMap(positional[0])
		if err != nil {
			return err
		}
		branch := mem.ClassicMindMapBranchRequest{MapRef: doc.Map.ID, NodeRef: positional[1], ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest}
		manifest, err := store.BuildClassicMindMapBranchManifest(branch)
		if err != nil {
			return err
		}
		if !flags.jsonOutput {
			fmt.Fprintf(os.Stderr, "[MINDMAP ASK] ветвь: %s · узлов: %d · current evidence: %d\n", manifest.RootLabel, len(manifest.NodeIDs), len(manifest.Evidence))
			fmt.Fprintf(os.Stderr, "[MINDMAP ASK] grounded generation через %s...\n", answerCfg.Model)
		}
		result, err := store.AnswerClassicMindMapBranch(context.Background(), &mem.ClassicMindMapAIService{Store: store, Provider: provider, Config: answerCfg}, mem.ClassicMindMapBranchQuestionRequest{
			ClassicMindMapBranchRequest: branch, Question: positional[2], ExpectedManifestDigest: manifest.Digest,
		})
		if err != nil {
			return err
		}
		if flags.jsonOutput {
			return printMindMapJSON(result)
		}
		fmt.Println(result.Answer)
		printMindMapBranchSources(result.Sources)
		return nil
	case "compare":
		if len(positional) != 2 {
			return errors.New("использование: mem mindmap compare <левая-карта> <правая-карта> [--json]")
		}
		result, err := store.CompareClassicMindMaps(mem.ClassicMindMapCompareRequest{LeftMapRef: positional[0], RightMapRef: positional[1]})
		if err != nil {
			return err
		}
		if flags.jsonOutput {
			return printMindMapJSON(result)
		}
		fmt.Printf("Сравнение: %s → %s\n", result.Left.Title, result.Right.Title)
		fmt.Printf("Добавлено: %d · удалено: %d · изменено: %d · перемещено: %d · без изменений: %d\n", result.Added, result.Removed, result.Changed, result.Moved, result.Unchanged)
		for _, item := range result.Items {
			if item.Status == "unchanged" {
				continue
			}
			line := fmt.Sprintf("- %s · %s", humanClassicMindMapCompareStatus(item.Status), item.Path)
			if item.OtherPath != "" {
				line += " → " + item.OtherPath
			}
			if len(item.Changes) > 0 {
				line += " · " + strings.Join(item.Changes, ", ")
			}
			fmt.Println(line)
		}
		return nil
	case "study":
		if len(positional) != 2 {
			return errors.New("использование: mem mindmap study <карта> <узел> [--output <путь>] [--force] [--json]")
		}
		doc, err := store.LoadClassicMindMap(positional[0])
		if err != nil {
			return err
		}
		pack, err := store.BuildClassicMindMapStudyPack(mem.ClassicMindMapBranchRequest{MapRef: doc.Map.ID, NodeRef: positional[1], ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest})
		if err != nil {
			return err
		}
		if flags.jsonOutput {
			return printMindMapJSON(pack)
		}
		if flags.output != "" {
			path, err := writeClassicMindMapExportArtifact(flags.output, []byte(pack.Markdown), flags.force)
			if err != nil {
				return err
			}
			fmt.Printf("Учебный набор сохранён: %s · %d карточек.\n", path, len(pack.Cards))
			return nil
		}
		fmt.Print(pack.Markdown)
		return nil
	}
	return fmt.Errorf("неизвестная команда mindmap workbench %q", command)
}

func humanClassicMindMapCompareStatus(status string) string {
	switch status {
	case "added":
		return "добавлено"
	case "removed":
		return "удалено"
	case "changed":
		return "изменено"
	case "moved":
		return "перемещено"
	case "unchanged":
		return "без изменений"
	default:
		return status
	}
}

type mindMapWorkbenchFlags struct {
	description string
	output      string
	jsonOutput  bool
	force       bool
}

func parseMindMapWorkbenchArgs(args []string) ([]string, mindMapWorkbenchFlags, error) {
	var positional []string
	var flags mindMapWorkbenchFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			flags.jsonOutput = true
		case "--force":
			flags.force = true
		case "--description", "--output":
			if i+1 >= len(args) {
				return nil, flags, fmt.Errorf("флаг %s требует значение", args[i])
			}
			key := args[i]
			i++
			if key == "--description" {
				flags.description = args[i]
			} else {
				flags.output = args[i]
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return nil, flags, fmt.Errorf("неизвестный флаг %q", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if flags.jsonOutput && flags.output != "" {
		return nil, flags, errors.New("--json нельзя сочетать с --output")
	}
	return positional, flags, nil
}

func printMindMapBranchSources(sources []mem.GroundedEvidence) {
	if len(sources) == 0 {
		return
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].SourcePath != sources[j].SourcePath {
			return sources[i].SourcePath < sources[j].SourcePath
		}
		return sources[i].Page < sources[j].Page
	})
	fmt.Println("\nИсточники:")
	for _, source := range sources {
		fmt.Printf("- %s · стр. %d · блок %d · фрагмент %d\n", source.SourcePath, source.Page, source.BlockIndex, source.BlockChunkIndex)
	}
}
