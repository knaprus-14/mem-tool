package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

type mindMapAIOptions struct {
	document      string
	query         string
	title         string
	selection     string
	pageFrom      int
	pageTo        int
	limit         int
	expect        int64
	entryIDs      []int64
	nodeSources   bool
	withoutSource bool
	jsonOutput    bool
	positional    []string
}

func handleMindMapAI(cfg *Config, store *Store, args []string) error {
	if store == nil || len(args) == 0 {
		return errors.New(mindMapUsage)
	}
	command := args[0]
	options, err := parseMindMapAIOptions(args[1:])
	if err != nil {
		return err
	}
	switch command {
	case "ai-show":
		if len(options.positional) != 1 || options.hasGenerationScope() || options.selection != "" || options.expect != 0 {
			return errors.New("использование: mem mindmap ai-show <preview-id> [--json]")
		}
		preview, err := store.LoadClassicMindMapAIPreview(options.positional[0])
		if err != nil {
			return err
		}
		return printClassicMindMapAIPreview(preview, options.jsonOutput)
	case "ai-apply":
		if len(options.positional) != 1 || options.hasGenerationScope() {
			return errors.New("использование: mem mindmap ai-apply <preview-id> [--select <id,id>] [--expect N] [--json]")
		}
		preview, err := store.LoadClassicMindMapAIPreview(options.positional[0])
		if err != nil {
			return err
		}
		selected, err := parseMindMapAISelection(options.selection)
		if err != nil {
			return err
		}
		result, err := store.ApplyClassicMindMapAIPreview(mem.ClassicMindMapAIApplyRequest{
			RunID: preview.RunID, ProposalIDs: selected, ExpectedRevision: options.expect,
			ExpectedPreviewDigest: preview.ProposalDigest, Actor: "cli",
		})
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(result)
		}
		fmt.Printf("%s AI-preview %s опубликован атомарно.\n", ui.Mark("ok"), result.RunID)
		fmt.Printf("Карта: %s · ревизия %d · применено предложений: %d.\n",
			result.Document.Map.Title, result.Document.Map.Revision, len(result.ProposalIDs))
		if preview.Action == mem.ClassicMindMapAINewMap {
			fmt.Printf("Открыть созданную карту: mem mindmap show %q\n", result.Document.Map.Title)
		} else {
			fmt.Printf("Для отмены: mem mindmap undo %q --expect %d\n", result.Document.Map.Title, result.Document.Map.Revision)
		}
		return nil
	}

	request, err := buildMindMapAIPreviewRequest(command, options)
	if err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("конфигурация AI недоступна")
	}
	answerCfg := cfg.Answer.WithMapGenerationDefaults()
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	preview, err := mem.PrepareClassicMindMapAIPreview(ctx, &mem.ClassicMindMapAIService{
		Store: store, Provider: provider, Config: answerCfg,
	}, request, printClassicMindMapAIProgress)
	if err != nil {
		return err
	}
	return printClassicMindMapAIPreview(preview, options.jsonOutput)
}

func buildMindMapAIPreviewRequest(command string, options mindMapAIOptions) (mem.ClassicMindMapAIPreviewRequest, error) {
	request := mem.ClassicMindMapAIPreviewRequest{
		ExpectedRevision: options.expect,
		Scope: mem.ClassicMindMapAIScope{
			Document: options.document, PageFrom: options.pageFrom, PageTo: options.pageTo,
			Query: options.query, EntryIDs: options.entryIDs, UseNodeSources: options.nodeSources,
			AllowUngrounded: options.withoutSource, Limit: options.limit,
		},
	}
	if options.withoutSource && (options.document != "" || options.query != "" || len(options.entryIDs) != 0 || options.nodeSources || options.pageFrom != 0 || options.pageTo != 0) {
		return request, errors.New("--without-sources нельзя совмещать с выбором document/query/entry/node-sources/pages")
	}
	switch command {
	case "ai-new":
		if len(options.positional) == 0 || options.nodeSources || options.expect != 0 || options.selection != "" {
			return request, errors.New("использование: mem mindmap ai-new <запрос> [AI scope flags] [--title <название>] [--json]")
		}
		request.Action = mem.ClassicMindMapAINewMap
		request.Prompt = strings.Join(options.positional, " ")
		request.Title = options.title
	case "ai-expand", "ai-fill", "ai-sources":
		if len(options.positional) < 3 || options.title != "" || options.selection != "" {
			return request, fmt.Errorf("использование: mem mindmap %s <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]", command)
		}
		request.MapRef, request.NodeRef = options.positional[0], options.positional[1]
		request.Prompt = strings.Join(options.positional[2:], " ")
		switch command {
		case "ai-expand":
			request.Action = mem.ClassicMindMapAIExpand
		case "ai-fill":
			request.Action = mem.ClassicMindMapAIFill
		case "ai-sources":
			request.Action = mem.ClassicMindMapAIFindSources
		}
	default:
		return request, fmt.Errorf("неизвестная AI-подкоманда mindmap %q\n%s", command, mindMapUsage)
	}
	return request, nil
}

func parseMindMapAIOptions(args []string) (mindMapAIOptions, error) {
	var options mindMapAIOptions
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s ожидает значение", flag)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			options.jsonOutput = true
		case "--node-sources":
			options.nodeSources = true
		case "--without-sources":
			options.withoutSource = true
		case "--document", "--query", "--title", "--select":
			flag := args[i]
			text, err := value(&i, flag)
			if err != nil {
				return options, err
			}
			switch flag {
			case "--document":
				options.document = text
			case "--query":
				options.query = text
			case "--title":
				options.title = text
			case "--select":
				options.selection = text
			}
		case "--page-from", "--page-to", "--limit", "--entry", "--expect":
			flag := args[i]
			text, err := value(&i, flag)
			if err != nil {
				return options, err
			}
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil || (flag != "--expect" && n <= 0) || n < 0 {
				return options, fmt.Errorf("%s ожидает положительное целое число", flag)
			}
			switch flag {
			case "--page-from":
				options.pageFrom = int(n)
			case "--page-to":
				options.pageTo = int(n)
			case "--limit":
				options.limit = int(n)
			case "--entry":
				options.entryIDs = append(options.entryIDs, n)
			case "--expect":
				options.expect = n
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return options, fmt.Errorf("неизвестный AI-флаг mindmap %q", args[i])
			}
			options.positional = append(options.positional, args[i])
		}
	}
	return options, nil
}

func (o mindMapAIOptions) hasGenerationScope() bool {
	return o.document != "" || o.query != "" || o.title != "" || o.pageFrom != 0 || o.pageTo != 0 ||
		o.limit != 0 || len(o.entryIDs) != 0 || o.nodeSources || o.withoutSource
}

func parseMindMapAISelection(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return nil, errors.New("--select содержит пустой или повторяющийся ID")
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func printClassicMindMapAIProgress(progress mem.ClassicMindMapAIProgress) {
	counter := ""
	if progress.Total > 0 {
		counter = fmt.Sprintf(" %d/%d", progress.Current, progress.Total)
	}
	fmt.Fprintf(os.Stderr, "[MINDMAP AI] %s%s", progress.Phase, counter)
	if progress.RunID != "" {
		fmt.Fprintf(os.Stderr, " · run=%s", progress.RunID)
	}
	if strings.TrimSpace(progress.Message) != "" {
		fmt.Fprintf(os.Stderr, " · %s", strings.Join(strings.Fields(progress.Message), " "))
	}
	fmt.Fprintln(os.Stderr)
}

func printClassicMindMapAIPreview(preview mem.ClassicMindMapAIPreview, jsonOutput bool) error {
	if jsonOutput {
		return printMindMapJSON(preview)
	}
	fmt.Printf("AI-preview: %s · %s · предложений: %d\n", preview.RunID, preview.Status, len(preview.Proposals))
	fmt.Printf("Digest: %s · источники: %s", preview.ProposalDigest, map[bool]string{true: "grounded", false: "без локальных источников"}[preview.Grounded])
	if preview.BaseRevision > 0 {
		fmt.Printf(" · базовая ревизия: %d", preview.BaseRevision)
	}
	fmt.Fprintln(os.Stdout)
	for i, proposal := range preview.Proposals {
		label := strings.TrimSpace(proposal.Label)
		if label == "" {
			label = firstMindMapValue(proposal.Summary, proposal.Reason, "изменение узла")
		}
		fmt.Printf("%d. %s", i+1, label)
		if proposal.Kind != "" {
			fmt.Printf(" · %s", proposal.Kind)
		}
		fmt.Printf("\n   ID выбора: %s\n", proposal.ID)
		if summary := strings.TrimSpace(proposal.Summary); summary != "" && summary != label {
			printClassicMindMapAIPreviewText("   Кратко: ", summary)
		}
		if body := strings.TrimSpace(proposal.BodyMarkdown); body != "" {
			printClassicMindMapAIPreviewText("   Текст: ", body)
		}
		if reason := strings.TrimSpace(proposal.Reason); reason != "" && reason != label {
			printClassicMindMapAIPreviewText("   Почему: ", reason)
		}
		for sourceIndex, anchor := range proposal.Evidence {
			printClassicMindMapAIPreviewSource(sourceIndex+1, anchor)
		}
	}
	fmt.Fprintln(os.Stdout, "Карта не изменена.")
	if preview.Status == mem.ClassicMindMapAIPreviewReady && len(preview.Proposals) > 0 {
		fmt.Printf("Опубликовать всё после проверки: mem mindmap ai-apply %q", preview.RunID)
		if preview.BaseRevision > 0 {
			fmt.Printf(" --expect %d", preview.BaseRevision)
		}
		fmt.Fprintln(os.Stdout)
	}
	return nil
}

func printClassicMindMapAIPreviewText(prefix, value string) {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if i == 0 {
			fmt.Printf("%s%s\n", prefix, line)
			continue
		}
		fmt.Printf("%s%s\n", strings.Repeat(" ", len([]rune(prefix))), line)
	}
}

func printClassicMindMapAIPreviewSource(index int, anchor mem.EvidenceAnchor) {
	name := strings.TrimSpace(anchor.SourcePath)
	if cut := strings.LastIndexAny(name, `\/`); cut >= 0 {
		name = name[cut+1:]
	}
	if name == "" {
		name = firstMindMapValue(anchor.DocumentID, "локальный документ")
	}
	location := make([]string, 0, 3)
	if anchor.Page > 0 {
		location = append(location, fmt.Sprintf("стр. %d", anchor.Page))
	}
	location = append(location, fmt.Sprintf("блок %d", anchor.BlockIndex+1))
	location = append(location, fmt.Sprintf("фрагмент %d", anchor.BlockChunkIndex+1))
	fmt.Printf("   Источник %d: %s · %s\n", index, name, strings.Join(location, " · "))
	if path := strings.TrimSpace(anchor.SourcePath); path != "" {
		fmt.Printf("      Файл: %s\n", path)
	}
	if excerpt := strings.TrimSpace(strings.Join(strings.Fields(anchor.Excerpt), " ")); excerpt != "" {
		const excerptRunes = 240
		runes := []rune(excerpt)
		if len(runes) > excerptRunes {
			excerpt = string(runes[:excerptRunes]) + "…"
		}
		fmt.Printf("      «%s»\n", excerpt)
	}
}
