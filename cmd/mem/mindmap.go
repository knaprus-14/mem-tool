package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

const mindMapUsage = `использование: mem mindmap <open|create|list|show|add-node|edit-node|move-node|delete-node|source-add|source-remove|source-move|history|undo|redo|snapshot|snapshots|ai-new|ai-expand|ai-fill|ai-sources|ai-show|ai-apply|templates|template-create|ask-branch|compare|study|export>
  mem mindmap open [--port N] [--no-browser]
  mem mindmap create <название> [--description <текст>] [--json]
  mem mindmap list [--all] [--json]
  mem mindmap show <карта> [--json]
  mem mindmap add-node <карта> <родитель> <название> [--kind <тип>] [--summary <текст>] [--body <markdown>] [--position N] [--expect N] [--json]
  mem mindmap edit-node <карта> <узел> [--label <текст>] [--summary <текст>] [--body <markdown>] [--kind <тип>] [--lock|--unlock] [--expect N] [--json]
  mem mindmap move-node <карта> <узел> --parent <родитель> [--position N] [--expect N] [--json]
  mem mindmap delete-node <карта> <узел> [--branch|--promote-children] [--expect N] [--json]
  mem mindmap source-add <карта> <узел> (--entry N [--excerpt <текст>] | --file <путь> | --url <адрес> | --knowledge-node <ID>) [--title <название>] [--expect N] [--json]
  mem mindmap source-remove <карта> <узел> --source <ID> [--expect N] [--json]
  mem mindmap source-move <карта> <узел> --source <ID> --position N [--expect N] [--json]
  mem mindmap history <карта> [-limit N] [--json]
	mem mindmap undo <карта> [--change N] [--expect N] [--json]
	mem mindmap redo <карта> [--expect N] [--json]
  mem mindmap snapshot <карта> --reason <текст> [--expect N]
  mem mindmap snapshots <карта> [-limit N] [--json]
  mem mindmap ai-new <запрос> [AI scope flags] [--title <название>] [--json]
  mem mindmap ai-expand <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-fill <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-sources <карта> <узел> <запрос> [AI scope flags] [--expect N] [--json]
  mem mindmap ai-show <preview-id> [--json]
  mem mindmap ai-apply <preview-id> [--select <id,id>] [--expect N] [--json]
  mem mindmap templates [--json]
  mem mindmap template-create <шаблон> <название> [--description <текст>] [--json]
  mem mindmap ask-branch <карта> <узел> <вопрос> [--json]
  mem mindmap compare <левая-карта> <правая-карта> [--json]
  mem mindmap study <карта> <узел> [--output <путь>] [--force] [--json]
  mem mindmap export <карта> --format html|svg|png|json|opml|markdown|mermaid|obsidian --output <путь> [--force]

AI scope flags: --document <путь|document-id> [--page-from N] [--page-to N],
  --query <текст>, повторяемый --entry N, --node-sources, --without-sources,
  --limit N (1..10000), --json. Без evidence-флагов используются все current
  chunks активной базы. Без --limit безопасный автоматический порог — 512;
  большая область отклоняется до вызова модели. Явный --limit детерминированно
  берёт первые N chunks и не гарантирует полный корпус.
  --without-sources несовместим с выбором evidence; ai-sources всегда требует
  current versioned evidence. Генерирующие команды сохраняют только preview;
  карту меняет лишь ai-apply.`

type mindMapCLIOptions struct {
	positional      []string
	description     string
	summary         string
	body            string
	kind            string
	label           string
	summarySet      bool
	bodySet         bool
	kindSet         bool
	labelSet        bool
	parent          string
	excerpt         string
	comment         string
	reason          string
	expect          int64
	position        int
	entryID         int64
	sourceID        string
	filePath        string
	sourceURL       string
	knowledgeNodeID string
	sourceTitle     string
	changeID        int64
	limit           int
	jsonOutput      bool
	includeArchived bool
	deleteMode      mem.ClassicMindMapDeleteMode
	lock            *bool
}

func handleMindMap(cfg *Config, store *Store, args []string) error {
	if len(args) == 0 {
		return errors.New(mindMapUsage)
	}
	if args[0] == "open" {
		return handleClassicMindMapOpen(cfg, store, args[1:])
	}
	if strings.HasPrefix(args[0], "ai-") {
		return handleMindMapAI(cfg, store, args)
	}
	if isMindMapWorkbenchCommand(args[0]) {
		return handleMindMapWorkbench(cfg, store, args)
	}
	if args[0] == "export" {
		return handleClassicMindMapExport(store, args[1:])
	}
	options, err := parseMindMapCLIOptions(args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap create <название> [--description <текст>] [--json]")
		}
		doc, err := store.CreateClassicMindMap(options.positional[0], options.description)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(doc)
		}
		fmt.Printf("%s Карта мысли %s создана. Ревизия: %d.\n", ui.Mark("ok"), ui.Value(doc.Map.Title), doc.Map.Revision)
		fmt.Printf("Добавить первую ветвь: mem mindmap add-node %q %q %q\n", doc.Map.Title, doc.Map.Title, "Новая ветвь")
		return nil
	case "list":
		if len(options.positional) != 0 {
			return errors.New("использование: mem mindmap list [--all] [--json]")
		}
		items, err := store.ListClassicMindMaps(options.includeArchived)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(items)
		}
		if len(items) == 0 {
			fmt.Println("Классических карт мыслей пока нет. Создание: mem mindmap create \"Название\"")
			return nil
		}
		fmt.Println("Классические карты мыслей")
		fmt.Println("-------------------------")
		for _, item := range items {
			fmt.Printf("- %s · %s · %d узлов · %d источников · ревизия %d\n",
				item.Title, item.Mode, item.NodeCount, item.SourceCount, item.Revision)
		}
		return nil
	case "show":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap show <карта> [--json]")
		}
		doc, err := store.LoadClassicMindMap(options.positional[0])
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(doc)
		}
		printClassicMindMapTree(doc)
		return nil
	case "add-node":
		if len(options.positional) != 3 {
			return errors.New("использование: mem mindmap add-node <карта> <родитель> <название> [--kind <тип>] [--summary <текст>] [--body <markdown>] [--position N] [--expect N] [--json]")
		}
		doc, node, err := store.AddClassicMindMapNode(options.positional[0], options.positional[1], options.positional[2],
			options.position, mem.ClassicMindMapNodeKind(options.kind), options.summary, options.body,
			options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map  mem.ClassicMindMapDocument `json:"map"`
				Node mem.ClassicMindMapNode     `json:"node"`
			}{doc, node})
		}
		fmt.Printf("%s Узел %s добавлен в карту %s. Ревизия: %d.\n", ui.Mark("ok"), ui.Value(node.Label), ui.Value(doc.Map.Title), doc.Map.Revision)
		return nil
	case "edit-node":
		if len(options.positional) != 2 {
			return errors.New("использование: mem mindmap edit-node <карта> <узел> [--label <текст>] [--summary <текст>] [--body <markdown>] [--kind <тип>] [--lock|--unlock] [--expect N] [--json]")
		}
		patch := mem.ClassicMindMapNodePatch{Locked: options.lock}
		if options.labelSet {
			patch.Label = &options.label
		}
		if options.summarySet {
			patch.Summary = &options.summary
		}
		if options.bodySet {
			patch.BodyMarkdown = &options.body
		}
		if options.kindSet {
			kind := mem.ClassicMindMapNodeKind(options.kind)
			patch.Kind = &kind
		}
		doc, node, err := store.EditClassicMindMapNode(options.positional[0], options.positional[1], patch,
			options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map  mem.ClassicMindMapDocument `json:"map"`
				Node mem.ClassicMindMapNode     `json:"node"`
			}{doc, node})
		}
		fmt.Printf("%s Узел %s обновлён. Ревизия карты: %d.\n", ui.Mark("ok"), ui.Value(node.Label), doc.Map.Revision)
		return nil
	case "move-node":
		if len(options.positional) != 2 || options.parent == "" {
			return errors.New("использование: mem mindmap move-node <карта> <узел> --parent <родитель> [--position N] [--expect N] [--json]")
		}
		doc, node, err := store.MoveClassicMindMapNode(options.positional[0], options.positional[1], options.parent,
			options.position, options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map  mem.ClassicMindMapDocument `json:"map"`
				Node mem.ClassicMindMapNode     `json:"node"`
			}{doc, node})
		}
		fmt.Printf("%s Узел %s перемещён. Ревизия карты: %d.\n", ui.Mark("ok"), ui.Value(node.Label), doc.Map.Revision)
		return nil
	case "delete-node":
		if len(options.positional) != 2 {
			return errors.New("использование: mem mindmap delete-node <карта> <узел> [--branch|--promote-children] [--expect N] [--json]")
		}
		doc, err := store.DeleteClassicMindMapNode(options.positional[0], options.positional[1], options.deleteMode,
			options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(doc)
		}
		fmt.Printf("%s Узел удалён безопасно. Ревизия карты: %d. Для отмены: mem mindmap undo %q --expect %d\n",
			ui.Mark("ok"), doc.Map.Revision, doc.Map.Title, doc.Map.Revision)
		return nil
	case "source-add":
		if len(options.positional) != 2 {
			return errors.New("использование: mem mindmap source-add <карта> <узел> (--entry N|--file <путь>|--url <адрес>|--knowledge-node <ID>) [--title <название>] [--expect N] [--json]")
		}
		selected := 0
		for _, ok := range []bool{options.entryID > 0, options.filePath != "", options.sourceURL != "", options.knowledgeNodeID != ""} {
			if ok {
				selected++
			}
		}
		if selected != 1 {
			return errors.New("source-add требует ровно один из флагов --entry, --file, --url или --knowledge-node")
		}
		var doc mem.ClassicMindMapDocument
		var source mem.ClassicMindMapSource
		var err error
		if options.entryID > 0 {
			entry, getErr := store.GetByID(options.entryID)
			if getErr != nil {
				return getErr
			}
			excerpt := options.excerpt
			if strings.TrimSpace(excerpt) == "" {
				excerpt = entry.Text
			}
			anchor, anchorErr := mem.EvidenceAnchorForEntry(*entry, excerpt)
			if anchorErr != nil {
				return anchorErr
			}
			doc, source, err = store.AttachClassicMindMapEvidence(options.positional[0], options.positional[1], anchor,
				options.expect, "cli", options.comment)
		} else {
			candidate := mem.ClassicMindMapSource{Title: options.sourceTitle}
			switch {
			case options.filePath != "":
				candidate.Kind, candidate.Locator = mem.ClassicMindMapSourceExternalFile, options.filePath
			case options.sourceURL != "":
				candidate.Kind, candidate.URL = mem.ClassicMindMapSourceURL, options.sourceURL
			case options.knowledgeNodeID != "":
				candidate.Kind, candidate.KnowledgeNodeID = mem.ClassicMindMapSourceKnowledgeNode, options.knowledgeNodeID
			}
			doc, source, err = store.AttachClassicMindMapSource(options.positional[0], options.positional[1], candidate,
				options.expect, "cli", options.comment)
		}
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map    mem.ClassicMindMapDocument `json:"map"`
				Source mem.ClassicMindMapSource   `json:"source"`
			}{doc, source})
		}
		fmt.Printf("%s Источник привязан: %s", ui.Mark("ok"), source.Title)
		if reference := firstMindMapValue(source.Locator, source.URL, source.KnowledgeNodeID); reference != "" {
			fmt.Printf(", %s", reference)
		}
		fmt.Printf(". Ревизия карты: %d.\n", doc.Map.Revision)
		return nil
	case "source-remove":
		if len(options.positional) != 2 || options.sourceID == "" {
			return errors.New("использование: mem mindmap source-remove <карта> <узел> --source <ID> [--expect N] [--json]")
		}
		doc, err := store.DetachClassicMindMapSource(options.positional[0], options.positional[1], options.sourceID,
			options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(doc)
		}
		fmt.Printf("%s Источник отвязан. Ревизия карты: %d.\n", ui.Mark("ok"), doc.Map.Revision)
		return nil
	case "source-move":
		if len(options.positional) != 2 || options.sourceID == "" || options.position < 0 {
			return errors.New("использование: mem mindmap source-move <карта> <узел> --source <ID> --position N [--expect N] [--json]")
		}
		doc, err := store.MoveClassicMindMapSource(options.positional[0], options.positional[1], options.sourceID,
			options.position, options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(doc)
		}
		fmt.Printf("%s Порядок источников изменён. Ревизия карты: %d.\n", ui.Mark("ok"), doc.Map.Revision)
		return nil
	case "history":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap history <карта> [-limit N] [--json]")
		}
		changes, err := store.ListClassicMindMapChanges(options.positional[0], options.limit)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(changes)
		}
		if len(changes) == 0 {
			fmt.Println("История карты пуста.")
			return nil
		}
		fmt.Println("История карты")
		fmt.Println("-------------")
		for _, change := range changes {
			undo := ""
			if change.RevertsChangeID > 0 {
				undo = fmt.Sprintf(" · отменяет изменение %d", change.RevertsChangeID)
			}
			fmt.Printf("- #%d · %s · ревизия %d → %d%s · %s\n", change.ID, humanMindMapAction(change.Action),
				change.BaseRevision, change.NewRevision, undo, change.Created)
		}
		return nil
	case "undo":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap undo <карта> [--change N] [--expect N] [--json]")
		}
		doc, change, err := store.UndoClassicMindMapChange(options.positional[0], options.changeID,
			options.expect, "cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map    mem.ClassicMindMapDocument `json:"map"`
				Change mem.ClassicMindMapChange   `json:"change"`
			}{doc, change})
		}
		fmt.Printf("%s Последнее изменение отменено. Новая ревизия: %d.\n", ui.Mark("ok"), doc.Map.Revision)
		return nil
	case "redo":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap redo <карта> [--expect N] [--json]")
		}
		doc, change, err := store.RedoClassicMindMapChange(options.positional[0], options.expect,
			"cli", options.comment)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(struct {
				Map    mem.ClassicMindMapDocument `json:"map"`
				Change mem.ClassicMindMapChange   `json:"change"`
			}{doc, change})
		}
		fmt.Printf("%s Отменённое изменение повторено. Новая ревизия: %d.\n", ui.Mark("ok"), doc.Map.Revision)
		return nil
	case "snapshot":
		if len(options.positional) != 1 || strings.TrimSpace(options.reason) == "" {
			return errors.New("использование: mem mindmap snapshot <карта> --reason <текст> [--expect N]")
		}
		digest, err := store.CreateClassicMindMapSnapshot(options.positional[0], options.reason, options.expect)
		if err != nil {
			return err
		}
		fmt.Printf("%s Контрольная точка сохранена: %s.\n", ui.Mark("ok"), digest)
		return nil
	case "snapshots":
		if len(options.positional) != 1 {
			return errors.New("использование: mem mindmap snapshots <карта> [-limit N] [--json]")
		}
		snapshots, err := store.ListClassicMindMapSnapshots(options.positional[0], options.limit)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(snapshots)
		}
		if len(snapshots) == 0 {
			fmt.Println("Контрольных точек пока нет.")
			return nil
		}
		fmt.Println("Контрольные точки карты")
		fmt.Println("------------------------")
		for _, snapshot := range snapshots {
			fmt.Printf("- ревизия %d · %s · %s\n", snapshot.Revision, snapshot.Reason, snapshot.Created)
		}
		return nil
	default:
		return fmt.Errorf("неизвестная подкоманда mindmap %q\n%s", args[0], mindMapUsage)
	}
}

func parseMindMapCLIOptions(args []string) (mindMapCLIOptions, error) {
	options := mindMapCLIOptions{position: -1, limit: 50, deleteMode: mem.ClassicMindMapDeleteBranch}
	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s ожидает значение", name)
		}
		*i = *i + 1
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			options.jsonOutput = true
		case "--all":
			options.includeArchived = true
		case "--branch":
			options.deleteMode = mem.ClassicMindMapDeleteBranch
		case "--promote-children":
			options.deleteMode = mem.ClassicMindMapDeletePromoteChildren
		case "--lock":
			locked := true
			options.lock = &locked
		case "--unlock":
			locked := false
			options.lock = &locked
		case "--description", "--summary", "--body", "--kind", "--label", "--parent", "--excerpt", "--comment", "--reason", "--source", "--file", "--url", "--knowledge-node", "--title":
			name := args[i]
			v, err := value(&i, name)
			if err != nil {
				return options, err
			}
			switch name {
			case "--description":
				options.description = v
			case "--summary":
				options.summary = v
				options.summarySet = true
			case "--body":
				options.body = v
				options.bodySet = true
			case "--kind":
				options.kind = v
				options.kindSet = true
			case "--label":
				options.label = v
				options.labelSet = true
			case "--parent":
				options.parent = v
			case "--excerpt":
				options.excerpt = v
			case "--comment":
				options.comment = v
			case "--reason":
				options.reason = v
			case "--source":
				options.sourceID = v
			case "--file":
				options.filePath = v
			case "--url":
				options.sourceURL = v
			case "--knowledge-node":
				options.knowledgeNodeID = v
			case "--title":
				options.sourceTitle = v
			}
		case "--expect", "--position", "--entry", "--change", "-limit":
			name := args[i]
			v, err := value(&i, name)
			if err != nil {
				return options, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return options, fmt.Errorf("%s ожидает неотрицательное целое число", name)
			}
			switch name {
			case "--expect":
				options.expect = n
			case "--position":
				options.position = int(n)
			case "--entry":
				options.entryID = n
			case "--change":
				options.changeID = n
			case "-limit":
				options.limit = int(n)
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return options, fmt.Errorf("неизвестный флаг mindmap %q", args[i])
			}
			options.positional = append(options.positional, args[i])
		}
	}
	return options, nil
}

func printMindMapJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return nil
}

func printClassicMindMapTree(doc mem.ClassicMindMapDocument) {
	fmt.Printf("Карта мысли: %s\n", doc.Map.Title)
	fmt.Printf("Режим: %s · Статус: %s · Ревизия: %d · Узлов: %d\n", doc.Map.Mode, doc.Map.Status, doc.Map.Revision, len(doc.Nodes))
	if doc.Map.Description != "" {
		fmt.Printf("Описание: %s\n", doc.Map.Description)
	}
	fmt.Println(strings.Repeat("-", 72))
	byID := make(map[string]mem.ClassicMindMapNode, len(doc.Nodes))
	children := make(map[string][]mem.ClassicMindMapNode)
	for _, node := range doc.Nodes {
		byID[node.ID] = node
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			if children[parent][i].Position != children[parent][j].Position {
				return children[parent][i].Position < children[parent][j].Position
			}
			return children[parent][i].Label < children[parent][j].Label
		})
	}
	root, found := byID[doc.Map.RootNodeID]
	if !found {
		fmt.Println("[ОШИБКА] Корневой узел не найден.")
		return
	}
	printClassicMindMapNode(root, children, "", true, true)
}

func printClassicMindMapNode(node mem.ClassicMindMapNode, children map[string][]mem.ClassicMindMapNode, prefix string, last, root bool) {
	connector := ""
	childPrefix := "   "
	if !root {
		if last {
			connector = "└─ "
			childPrefix = prefix + "   "
		} else {
			connector = "├─ "
			childPrefix = prefix + "│  "
		}
	}
	lock := ""
	if node.Locked {
		lock = " [закреплён]"
	}
	fmt.Printf("%s%s%s · %s%s\n", prefix, connector, node.Label, node.Kind, lock)
	if node.Summary != "" {
		fmt.Printf("%s   %s\n", childPrefix, node.Summary)
	}
	for _, source := range node.Sources {
		if source.Kind == mem.ClassicMindMapSourceEvidence && source.Evidence != nil {
			state := source.EvidenceState
			fmt.Printf("%s   Источник: %s", childPrefix, source.Title)
			if source.Locator != "" {
				fmt.Printf(", %s", source.Locator)
			}
			fmt.Printf(" [%s]\n", state)
			if excerpt := strings.TrimSpace(source.Evidence.Excerpt); excerpt != "" {
				if len([]rune(excerpt)) > 180 {
					excerpt = string([]rune(excerpt)[:180]) + "…"
				}
				fmt.Printf("%s   «%s»\n", childPrefix, strings.ReplaceAll(excerpt, "\n", " "))
			}
			continue
		}
		fmt.Printf("%s   Источник: %s", childPrefix, source.Title)
		if reference := firstMindMapValue(source.Locator, source.URL, source.KnowledgeNodeID); reference != "" {
			fmt.Printf(", %s", reference)
		}
		if source.EvidenceState != "" {
			fmt.Printf(" [%s]", source.EvidenceState)
		}
		fmt.Println()
	}
	items := children[node.ID]
	for i, child := range items {
		printClassicMindMapNode(child, children, childPrefix, i == len(items)-1, false)
	}
}

func humanMindMapAction(action string) string {
	switch action {
	case "create_map":
		return "создание карты"
	case "add_node":
		return "добавление узла"
	case "edit_node":
		return "редактирование узла"
	case "edit_map":
		return "изменение параметров карты"
	case "move_node":
		return "перемещение узла"
	case "delete_node:branch":
		return "удаление ветки"
	case "delete_node:promote_children":
		return "удаление узла с переносом дочерних"
	case "attach_evidence":
		return "привязка источника"
	case "attach_source:external_file":
		return "привязка файла"
	case "attach_source:url":
		return "привязка веб-ссылки"
	case "attach_source:knowledge_node":
		return "привязка узла графа"
	case "detach_source":
		return "отвязка источника"
	case "move_source":
		return "изменение порядка источников"
	default:
		if strings.HasPrefix(action, "undo:") {
			return "отмена: " + humanMindMapAction(strings.TrimPrefix(action, "undo:"))
		}
		if strings.HasPrefix(action, "redo:") {
			return "повтор: " + humanMindMapAction(strings.TrimPrefix(action, "redo:"))
		}
		return action
	}
}

func firstMindMapValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
