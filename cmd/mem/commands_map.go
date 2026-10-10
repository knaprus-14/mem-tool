package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func handleMap(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMap")(&applogResult0)

	if len(args) == 0 {
		return errors.New("использование: mem map <open|build|coverage|eval|eval-labeled|profile|diff|snapshots|corpus-diff|restore|restore-runs|extract|extract-runs|extract-run|analyze|duplicates|merge-node|merges|runs|run|prune-runs|status|approve|approve-batch|reviews|edits|export|export-html>\n  mem map open [--port N] [--title <текст>] [--no-browser]\n  mem map build <фокус> [-limit N] [-context-chars N]\n  mem map coverage [--document <путь|document-id>] [--pages N|N-M] [--tag <тег>] [--json]\n  mem map eval <manifest.json> [--json]\n  mem map eval-labeled <manifest.json> [--json]\n  mem map profile [-iterations N] [--view <имя>] [--json]\n  mem map diff [--document <путь|document-id>] [--json]\n  mem map snapshots [--document <путь|document-id>] [--json]\n  mem map corpus-diff --document <путь|document-id> [--from <snapshot-id|revision>] [--to <snapshot-id|revision|current>] [--json]\n  mem map restore --document <путь|document-id> --revision <snapshot-id|revision> [--confirm <plan-digest>] [--json]\n  mem map restore --rollback <run-id> [--confirm <plan-digest>] [--json]\n  mem map restore-runs [--json] [-limit N]\n  mem map extract <фокус> [--document <путь|document-id>] [--pages N|N-M] [--tag <тег>] [-context-chars N] [-batches N] [-resume <run-id>] [--dry-run]\n  mem map extract-runs [--json] [-limit N]\n  mem map extract-run <run-id> [--json]\n  mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]\n  mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]\n  mem map merge-node <manifest.json>\n  mem map merges [--json] [-limit N]\n  mem map runs [--json] [-limit N] [-status running|completed]\n  mem map run <run-id> [--json]\n  mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]\n  mem map status [--json]\n  mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>] [--review-digest <sha256>]\n  mem map approve-batch <manifest.json> (expected_evidence_digest + expected_review_digest)\n  mem map reviews [--json] [-limit N]\n  mem map edits [--json] [-limit N]\n  mem map export\n  mem map export --format markdown|outline|opml|graphml|gexf|mermaid|obsidian --output <путь> [--title <текст>] [--force]\n  mem map export-html <output.html> [--title <текст>] [--force]")
	}
	switch args[0] {
	case "open":
		return handleMapOpen(cfg, store, args[1:])
	case "export":
		if len(args) > 1 {
			return handleKnowledgeGraphPortableExport(store, args[1:])
		}
		graph, err := store.LoadKnowledgeGraph()
		if err != nil {
			return fmt.Errorf("map export: %w", err)
		}
		encoded, err := json.MarshalIndent(graph, "", "  ")
		if err != nil {
			return fmt.Errorf("map export: encode graph: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	case "export-html":
		return handleMapExportHTML(store, args[1:])
	case "status":
		return handleMapStatus(store, args[1:])
	case "coverage":
		return handleMapCoverage(cfg, store, args[1:])
	case "eval":
		return handleMapEval(cfg, store, args[1:])
	case "eval-labeled":
		return handleMapEvalLabeled(store, args[1:])
	case "profile":
		return handleMapProfile(store, args[1:])
	case "diff":
		return handleMapDiff(store, args[1:])
	case "snapshots":
		return handleMapSnapshots(store, args[1:])
	case "corpus-diff":
		return handleMapCorpusDiff(store, args[1:])
	case "restore":
		return handleMapRestore(store, args[1:])
	case "restore-runs":
		return handleMapRestoreRuns(store, args[1:])
	case "extract":
		return handleMapExtract(cfg, store, args[1:])
	case "extract-runs":
		return handleMapExtractionRuns(store, args[1:])
	case "extract-run":
		return handleMapExtractionRun(store, args[1:])
	case "approve":
		return handleMapApprove(store, args[1:])
	case "approve-batch":
		return handleMapApproveBatch(store, args[1:])
	case "reviews":
		return handleMapReviews(store, args[1:])
	case "edits":
		return handleMapEdits(store, args[1:])
	case "duplicates":
		return handleMapDuplicates(cfg, store, args[1:])
	case "merge-node":
		return handleMapMergeNode(store, args[1:])
	case "merges":
		return handleMapMerges(store, args[1:])
	case "runs":
		return handleMapRuns(store, args[1:])
	case "run":
		return handleMapRun(store, args[1:])
	case "prune-runs":
		return handleMapPruneRuns(store, args[1:])
	case "analyze":
		return handleMapAnalyze(cfg, store, args[1:])
	case "build":
		return handleMapBuild(cfg, store, args[1:])
	default:
		return fmt.Errorf("неизвестная подкоманда map: %s (доступны open, build, coverage, eval, eval-labeled, profile, diff, snapshots, corpus-diff, restore, restore-runs, extract, extract-runs, extract-run, analyze, duplicates, merge-node, merges, runs, run, prune-runs, status, approve, approve-batch, reviews, edits, export, export-html)", args[0])
	}
}

func handleMapCoverage(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapCoverage")(&applogResult0)

	options := mem.KnowledgeCoverageOptions{LowConfidence: cfg.Ingest.LowConfidence}
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--document":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map coverage [--document <путь|document-id>] [--pages N|N-M] [--tag <тег>] [--json]")
			}
			i++
			options.Document = strings.TrimSpace(args[i])
		case "--tag":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("map coverage: --tag требует непустой тег")
			}
			i++
			options.Tag = strings.TrimSpace(args[i])
		case "--pages":
			if i+1 >= len(args) {
				return errors.New("map coverage: --pages ожидает N или N-M")
			}
			i++
			from, to, err := parseKnowledgeCoveragePages(args[i])
			if err != nil {
				return fmt.Errorf("map coverage: --pages: %w", err)
			}
			options.PageFrom, options.PageTo = from, to
		default:
			return fmt.Errorf("неизвестный аргумент map coverage: %s", args[i])
		}
	}
	report, err := store.BuildKnowledgeCoverageReport(options)
	if err != nil {
		return fmt.Errorf("map coverage: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map coverage: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	printKnowledgeCoverageReport(report)
	return nil
}

func handleMapDiff(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapDiff")(&applogResult0)

	options := mem.KnowledgeRevisionDiffOptions{}
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--document":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map diff [--document <путь|document-id>] [--json]")
			}
			i++
			options.Document = strings.TrimSpace(args[i])
		default:
			return fmt.Errorf("неизвестный аргумент map diff: %s", args[i])
		}
	}
	report, err := store.BuildKnowledgeRevisionDiff(options)
	if err != nil {
		return fmt.Errorf("map diff: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map diff: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	printKnowledgeRevisionDiff(report)
	return nil
}

func handleMapSnapshots(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapSnapshots")(&applogResult0)

	document := ""
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--document":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map snapshots [--document <путь|document-id>] [--json]")
			}
			i++
			document = strings.TrimSpace(args[i])
		default:
			return fmt.Errorf("неизвестный аргумент map snapshots: %s", args[i])
		}
	}
	snapshots, err := store.ListDocumentHistorySnapshots(document)
	if err != nil {
		return fmt.Errorf("map snapshots: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(snapshots, "", "  ")
		if err != nil {
			return fmt.Errorf("map snapshots: encode: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(os.Stdout, "Неизменяемые снимки документов")
	fmt.Fprintln(os.Stdout, "--------------------------------")
	if len(snapshots) == 0 {
		fmt.Fprintln(os.Stdout, "Снимков пока нет. Первый снимок создаётся автоматически перед заменой ревизии документа.")
		return nil
	}
	for _, snapshot := range snapshots {
		fmt.Fprintf(os.Stdout, "- %s\n  snapshot: %s · revision: %s\n  chunks: %d · created: %s\n  map: %s · reason: %s\n",
			snapshot.SourcePath, snapshot.SnapshotID, snapshot.DocumentRevision, snapshot.ChunkCount, snapshot.Created,
			snapshot.GraphSnapshotID, snapshot.Reason)
	}
	return nil
}

func handleMapCorpusDiff(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapCorpusDiff")(&applogResult0)

	document, fromRevision, toRevision := "", "", "current"
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--document", "--from", "--to":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map corpus-diff --document <путь|document-id> [--from <snapshot-id|revision>] [--to <snapshot-id|revision|current>] [--json]")
			}
			flag := args[i]
			i++
			value := strings.TrimSpace(args[i])
			switch flag {
			case "--document":
				document = value
			case "--from":
				fromRevision = value
			case "--to":
				toRevision = value
			}
		default:
			return fmt.Errorf("неизвестный аргумент map corpus-diff: %s", args[i])
		}
	}
	if document == "" {
		return errors.New("map corpus-diff: укажи --document <путь|document-id>")
	}
	report, err := store.BuildCorpusRevisionDiff(document, fromRevision, toRevision)
	if err != nil {
		return fmt.Errorf("map corpus-diff: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map corpus-diff: encode: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	printCorpusRevisionDiff(report)
	return nil
}

func printCorpusRevisionDiff(report mem.CorpusRevisionDiff) {
	fmt.Fprintln(os.Stdout, "Полное сравнение ревизий документа")
	fmt.Fprintln(os.Stdout, "----------------------------------")
	fmt.Fprintf(os.Stdout, "%s\n%s -> %s\n", report.SourcePath, report.FromRevision, report.ToRevision)
	fmt.Fprintf(os.Stdout, "Добавлено: %d; изменено: %d; исчезло: %d; без изменений: %d\n",
		report.AddedChunks, report.ChangedChunks, report.RemovedChunks, report.UnchangedChunks)
	for _, change := range report.Changes {
		fmt.Fprintf(os.Stdout, "- %s: стр. %d, блок %d, фрагмент %d\n",
			change.State, change.Page, change.BlockIndex+1, change.BlockChunkIndex+1)
		if change.BeforeText != "" {
			fmt.Fprintln(os.Stdout, "  было: "+revisionDiffCLIText(change.BeforeText, 240))
		}
		if change.AfterText != "" {
			fmt.Fprintln(os.Stdout, "  стало: "+revisionDiffCLIText(change.AfterText, 240))
		}
	}
}

func revisionDiffCLIText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func handleMapRestore(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapRestore")(&applogResult0)

	document, revision, rollbackID, confirmDigest := "", "", "", ""
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--document", "--revision", "--rollback", "--confirm":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map restore (--document <путь|document-id> --revision <snapshot-id|revision> | --rollback <run-id>) [--confirm <plan-digest>] [--json]")
			}
			flag := args[i]
			i++
			value := strings.TrimSpace(args[i])
			switch flag {
			case "--document":
				document = value
			case "--revision":
				revision = value
			case "--rollback":
				rollbackID = value
			case "--confirm":
				confirmDigest = value
			}
		default:
			return fmt.Errorf("неизвестный аргумент map restore: %s", args[i])
		}
	}
	if rollbackID != "" && (document != "" || revision != "") {
		return errors.New("map restore: --rollback нельзя совмещать с --document/--revision")
	}
	if rollbackID == "" && (document == "" || revision == "") {
		return errors.New("map restore: укажи --document и --revision либо --rollback <run-id>")
	}
	if confirmDigest == "" {
		var plan mem.DocumentRestorePlan
		var err error
		if rollbackID != "" {
			plan, err = store.BuildDocumentRestoreRollbackPlan(rollbackID)
		} else {
			plan, err = store.BuildDocumentRestorePlan(document, revision)
		}
		if err != nil {
			return fmt.Errorf("map restore preview: %w", err)
		}
		if jsonOutput {
			encoded, err := json.MarshalIndent(plan, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(encoded))
			return nil
		}
		printDocumentRestorePlan(plan)
		return nil
	}
	var run mem.DocumentRestoreRun
	var err error
	if rollbackID != "" {
		run, err = store.ApplyDocumentRestoreRollback(rollbackID, confirmDigest)
	} else {
		run, err = store.ApplyDocumentRestore(document, revision, confirmDigest)
	}
	if err != nil {
		return fmt.Errorf("map restore apply: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(os.Stdout, "Состояние восстановлено атомарно.")
	fmt.Fprintf(os.Stdout, "Операция: %s\nДокумент: %s\nРевизия: %s -> %s\nChunks: %d\n",
		run.ID, run.SourcePath, run.FromRevision, run.TargetRevision, run.RestoredChunks)
	fmt.Fprintf(os.Stdout, "Для отката сначала выполни preview:\n  mem map restore --rollback %s\n", run.ID)
	return nil
}

func printDocumentRestorePlan(plan mem.DocumentRestorePlan) {
	fmt.Fprintln(os.Stdout, "Предпросмотр восстановления")
	fmt.Fprintln(os.Stdout, "---------------------------")
	if plan.RollbackOf != "" {
		fmt.Fprintf(os.Stdout, "Откат операции: %s\n", plan.RollbackOf)
	}
	fmt.Fprintf(os.Stdout, "Документ: %s\nРевизия: %s -> %s\nChunks: %d -> %d\n",
		plan.SourcePath, plan.CurrentRevision, plan.TargetRevision, plan.CurrentChunks, plan.TargetChunks)
	fmt.Fprintf(os.Stdout, "Изменения chunks при применении: +%d ~%d -%d\n",
		plan.ChunkDiff.AddedChunks, plan.ChunkDiff.ChangedChunks, plan.ChunkDiff.RemovedChunks)
	fmt.Fprintf(os.Stdout, "Карта: %d узл./%d связ. -> %d узл./%d связ.\n",
		plan.CurrentGraphNodes, plan.CurrentGraphEdges, plan.TargetGraphNodes, plan.TargetGraphEdges)
	fmt.Fprintln(os.Stdout, "ВНИМАНИЕ: "+plan.Warning)
	fmt.Fprintln(os.Stdout, "\nПлан привязан к текущему корпусу и digest карты:")
	fmt.Fprintln(os.Stdout, plan.PlanDigest)
	if plan.RollbackOf != "" {
		fmt.Fprintf(os.Stdout, "\nПрименить после проверки:\n  mem map restore --rollback %s --confirm %s\n", plan.RollbackOf, plan.PlanDigest)
	} else {
		fmt.Fprintf(os.Stdout, "\nПрименить после проверки:\n  mem map restore --document %q --revision %s --confirm %s\n",
			plan.SourcePath, plan.TargetSnapshotID, plan.PlanDigest)
	}
}

func handleMapRestoreRuns(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapRestoreRuns")(&applogResult0)

	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("map restore-runs: -limit требует число")
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 || value > 10000 {
				return errors.New("map restore-runs: -limit должен быть 1..10000")
			}
			limit = value
		default:
			return fmt.Errorf("неизвестный аргумент map restore-runs: %s", args[i])
		}
	}
	runs, err := store.ListDocumentRestoreRuns(limit)
	if err != nil {
		return fmt.Errorf("map restore-runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(runs, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(os.Stdout, "История восстановлений")
	fmt.Fprintln(os.Stdout, "----------------------")
	if len(runs) == 0 {
		fmt.Fprintln(os.Stdout, "Операций восстановления пока нет.")
	}
	for _, run := range runs {
		rollback := ""
		if run.RollbackOf != "" {
			rollback = " · откат " + run.RollbackOf
		}
		fmt.Fprintf(os.Stdout, "- %s · %s%s\n  %s -> %s · chunks %d · %s\n",
			run.ID, run.SourcePath, rollback, run.FromRevision, run.TargetRevision,
			run.RestoredChunks, run.Created)
	}
	return nil
}

func printKnowledgeRevisionDiff(report mem.KnowledgeRevisionDiffReport) {
	fmt.Fprintln(os.Stdout, "Изменения источников и карты")
	fmt.Fprintln(os.Stdout, "----------------------------")
	fmt.Fprintf(os.Stdout, "Документы: %d; изменены: %d; с исчезнувшими фрагментами: %d; без изменений: %d\n",
		report.Summary.Documents, report.Summary.ChangedDocuments, report.Summary.MissingDocuments, report.Summary.CurrentDocuments)
	fmt.Fprintf(os.Stdout, "Затронуто: узлов %d, связей %d; evidence изменено %d, исчезло %d\n",
		report.Summary.AffectedNodes, report.Summary.AffectedEdges, report.Summary.ChangedAnchors, report.Summary.MissingAnchors)
	if report.Summary.SnapshotDocuments > 0 {
		fmt.Fprintf(os.Stdout, "Полные снимки: документов %d; chunks добавлено %d, изменено %d, исчезло %d\n",
			report.Summary.SnapshotDocuments, report.Summary.AddedChunks,
			report.Summary.ChangedChunks, report.Summary.RemovedChunks)
	}
	if len(report.Documents) == 0 {
		fmt.Fprintln(os.Stdout, "\nВ карте пока нет versioned evidence для сравнения.")
	}
	for _, document := range report.Documents {
		state := "без изменений"
		if document.State == mem.EvidenceStale {
			state = "изменён"
		} else if document.State == mem.EvidenceMissing {
			state = "есть исчезнувшие фрагменты"
		}
		fmt.Fprintf(os.Stdout, "\n- %s [%s]\n", document.SourcePath, state)
		fmt.Fprintf(os.Stdout, "  Затронуто: узлов %d, связей %d; изменено %d, исчезло %d\n",
			document.AffectedNodes, document.AffectedEdges, document.ChangedAnchors, document.MissingAnchors)
		if document.CorpusDiff != nil {
			fmt.Fprintf(os.Stdout, "  Полный chunk-diff: +%d ~%d -%d (= %d без изменений)\n",
				document.CorpusDiff.AddedChunks, document.CorpusDiff.ChangedChunks,
				document.CorpusDiff.RemovedChunks, document.CorpusDiff.UnchangedChunks)
		}
		for _, object := range document.Objects {
			if object.EvidenceState == mem.EvidenceCurrent {
				continue
			}
			fmt.Fprintf(os.Stdout, "  - %s: %s [%s]\n", object.ObjectType, object.Label, object.EvidenceState)
			for _, evidence := range object.Evidence {
				if evidence.State != mem.EvidenceCurrent {
					fmt.Fprintf(os.Stdout, "    стр. %d, блок %d, фрагмент %d: %s\n",
						evidence.Page, evidence.BlockIndex+1, evidence.BlockChunkIndex+1, evidence.State)
				}
			}
		}
	}
	if len(report.Limitations) > 0 {
		fmt.Fprintln(os.Stdout, "\nГраницы отчёта:")
		for _, limitation := range report.Limitations {
			fmt.Fprintln(os.Stdout, "- "+limitation)
		}
	}
}
