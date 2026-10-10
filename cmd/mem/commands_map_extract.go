package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func handleMapExtract(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapExtract")(&applogResult0)

	focusParts := make([]string, 0, len(args))
	scope := mem.KnowledgeCoverageOptions{LowConfidence: cfg.Ingest.LowConfidence}
	contextBudget := cfg.Answer.WithDefaults().ContextChars
	maxBatches := 8
	resumeID := ""
	dryRun := false
	allChunks := false
	documentSet, tagSet, pagesSet := false, false, false
	contextSet, batchesSet := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all-chunks":
			allChunks = true
		case "--document":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("map extract: --document требует путь или document-id")
			}
			i++
			scope.Document = strings.TrimSpace(args[i])
			documentSet = true
		case "--tag":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("map extract: --tag требует непустой тег")
			}
			i++
			scope.Tag = strings.TrimSpace(args[i])
			tagSet = true
		case "--pages":
			if i+1 >= len(args) {
				return errors.New("map extract: --pages ожидает N или N-M")
			}
			i++
			from, to, err := parseKnowledgeCoveragePages(args[i])
			if err != nil {
				return fmt.Errorf("map extract: --pages: %w", err)
			}
			scope.PageFrom, scope.PageTo = from, to
			pagesSet = true
		case "-context-chars":
			if i+1 >= len(args) {
				return errors.New("map extract: -context-chars требует число")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxAnswerContextChars {
				return fmt.Errorf("map extract: -context-chars должен быть от 1 до %d", mem.MaxAnswerContextChars)
			}
			contextBudget = parsed
			contextSet = true
		case "-batches":
			if i+1 >= len(args) {
				return errors.New("map extract: -batches требует число")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxKnowledgeExtractionJobBatches {
				return fmt.Errorf("map extract: -batches должен быть от 1 до %d", mem.MaxKnowledgeExtractionJobBatches)
			}
			maxBatches = parsed
			batchesSet = true
		case "-resume":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || resumeID != "" {
				return errors.New("map extract: -resume требует один run-id")
			}
			i++
			resumeID = strings.TrimSpace(args[i])
		case "--dry-run":
			dryRun = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("неизвестный аргумент map extract: %s", args[i])
			}
			focusParts = append(focusParts, args[i])
		}
	}
	if len(focusParts) == 0 {
		return errors.New("укажи цель извлечения\nПример: mem map extract \"полный разбор документа\" --document .\\manual.pdf -batches 16")
	}
	focus := strings.Join(focusParts, " ")
	if allChunks {
		if !documentSet || tagSet || pagesSet || contextSet || batchesSet {
			return errors.New("map extract --all-chunks: укажите --document; размер запросов выбирается автоматически, --tag/--pages/-context-chars/-batches не применяются")
		}
		if dryRun {
			report, err := store.BuildKnowledgeCoverageReport(scope)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Документ: %d фрагментов. Будут сохранены структура и текст, затем выполнен смысловой разбор с продолжением незавершённых мест.\n", report.Summary.ChunksWithText)
			return nil
		}
		var provider mem.AnswerProvider
		if p, err := newAnswerProvider(cfg.Answer.WithMapGenerationDefaults()); err == nil {
			provider = p
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		defer cancel()
		return store.RunKnowledgeDocumentGraph(ctx, cfg, provider, scope.Document, focus, resumeID, func(stage string, current, total int) error {
			fmt.Fprintf(os.Stderr, "[MAP DOCUMENT] %d/%d: %s\n", current, total, stage)
			return nil
		})
	}
	var plan mem.KnowledgeExtractionJobPlan
	var err error
	if resumeID != "" {
		stored, loadErr := store.LoadKnowledgeExtractionRun(resumeID)
		if loadErr != nil {
			return fmt.Errorf("map extract resume: %w", loadErr)
		}
		if (documentSet && !strings.EqualFold(scope.Document, stored.Scope.Document)) ||
			(tagSet && !strings.EqualFold(scope.Tag, stored.Scope.Tag)) ||
			(pagesSet && (scope.PageFrom != stored.Scope.PageFrom || scope.PageTo != stored.Scope.PageTo)) {
			return errors.New("map extract: область --document/--tag/--pages не совпадает с сохранённым run")
		}
		if contextSet && contextBudget != stored.ContextChars {
			return errors.New("map extract: -context-chars не совпадает с сохранённым run")
		}
		if batchesSet && maxBatches != stored.MaxBatches {
			return errors.New("map extract: -batches не совпадает с сохранённым run")
		}
		contextBudget, maxBatches = stored.ContextChars, stored.MaxBatches
		plan, err = store.RebuildKnowledgeExtractionRunPlan(resumeID, focus)
	} else {
		plan, err = store.BuildKnowledgeExtractionJobPlan(focus, scope, contextBudget, maxBatches)
	}
	if err != nil {
		return fmt.Errorf("map extract plan: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[MAP EXTRACT] scope chunks=%d; covered=%d; processed=%d; eligible=%d; selected=%d; remaining=%d; batches=%d\n",
		plan.ScopedChunks, plan.AlreadyCoveredChunks, plan.PreviouslyProcessedChunks,
		plan.EligibleChunks, plan.SelectedChunks, plan.RemainingChunks, len(plan.Batches))
	if len(plan.Batches) == 0 {
		fmt.Fprintln(os.Stdout, "Задание не требуется: в выбранной области нет новых непокрытых chunks. Используй mem map coverage для текущего отчёта.")
		return nil
	}
	if dryRun {
		fmt.Fprintf(os.Stdout, "План извлечения: chunks=%d batches=%d remaining=%d documents=%d pages=%d. База и модель не изменялись.\n",
			plan.SelectedChunks, len(plan.Batches), plan.RemainingChunks, plan.Documents, plan.Pages)
		return nil
	}
	if plan.RemainingChunks > 0 {
		fmt.Fprintf(os.Stderr, "[MAP EXTRACT] лимит пакетов оставит %d chunks на следующий запуск; после завершения повтори ту же команду\n", plan.RemainingChunks)
	}
	configuredAnswerCfg := cfg.Answer.WithDefaults()
	answerCfg := cfg.Answer.WithMapGenerationDefaults()
	if answerCfg.MaxTokens != configuredAnswerCfg.MaxTokens {
		fmt.Fprintf(os.Stderr, "[MAP EXTRACT] output budget: %d tokens (answer.max_tokens=%d)\n", answerCfg.MaxTokens, configuredAnswerCfg.MaxTokens)
	}
	run, err := store.PrepareKnowledgeExtractionRun(plan, focus, contextBudget, maxBatches, answerCfg, resumeID)
	if err != nil {
		return fmt.Errorf("map extract run: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[MAP EXTRACT] run=%s status=%s; checkpoints сохраняются после каждого пакета\n", run.ID, run.Status)
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for i, batch := range plan.Batches {
		stored := run.Batches[i]
		switch stored.Status {
		case mem.KnowledgeExtractionBatchCompleted:
			fmt.Fprintf(os.Stderr, "[MAP EXTRACT] batch=%d/%d id=%s: восстановлен проверенный checkpoint\n", i+1, len(plan.Batches), batch.BatchID)
			continue
		case mem.KnowledgeExtractionBatchInsufficient:
			fmt.Fprintf(os.Stderr, "[MAP EXTRACT] batch=%d/%d id=%s: восстановлен insufficient checkpoint\n", i+1, len(plan.Batches), batch.BatchID)
			continue
		}
		fmt.Fprintf(os.Stderr, "[MAP EXTRACT] batch=%d/%d id=%s chunks=%d; extraction через %s...\n",
			i+1, len(plan.Batches), batch.BatchID, len(batch.Prompt.Evidence), answerCfg.Model)
		for _, evidence := range batch.Prompt.Evidence {
			for _, warning := range evidence.Warnings {
				fmt.Fprintf(os.Stderr, "[MAP EXTRACT] warning %s: %s\n", evidence.CitationLabel, strings.Join(strings.Fields(warning), " "))
			}
		}
		extracted, rejected, batchErr := extractKnowledgeExtractionBatch(rootCtx, provider, answerCfg, batch, i+1, len(plan.Batches))
		if batchErr != nil {
			if saveErr := store.SaveKnowledgeExtractionBatchFailure(run.ID, batch.BatchID, batchErr); saveErr != nil {
				return fmt.Errorf("map extract batch %d, run=%s: %v; save failure: %w", i+1, run.ID, batchErr, saveErr)
			}
			if rejected {
				return fmt.Errorf("map extract batch %d rejected after correction retries, run=%s: %w", i+1, run.ID, batchErr)
			}
			return fmt.Errorf("map extract batch %d, run=%s: %w", i+1, run.ID, batchErr)
		}
		if extracted.Insufficient {
			if err := store.SaveKnowledgeExtractionBatchInsufficient(run.ID, batch.BatchID, extracted.Reason); err != nil {
				return fmt.Errorf("map extract checkpoint, run=%s: %w", run.ID, err)
			}
			fmt.Fprintf(os.Stderr, "[MAP EXTRACT] batch=%d/%d: insufficient evidence (%s)\n", i+1, len(plan.Batches), extracted.Reason)
			continue
		}
		if err := store.SaveKnowledgeExtractionBatchGraph(run.ID, batch.BatchID, extracted.Graph); err != nil {
			return fmt.Errorf("map extract checkpoint, run=%s: %w", run.ID, err)
		}
		fmt.Fprintf(os.Stderr, "[MAP EXTRACT] batch=%d/%d: checkpoint nodes=%d edges=%d\n",
			i+1, len(plan.Batches), len(extracted.Graph.Nodes), len(extracted.Graph.Edges))
	}
	merged, err := store.FinalizeKnowledgeExtractionRun(run.ID, plan)
	if err != nil {
		return fmt.Errorf("map extract finalize, run=%s: %w", run.ID, err)
	}
	fmt.Fprintf(os.Stdout, "Задание извлечения завершено: run=%s processed=%d nodes=%d edges=%d remaining=%d. Результаты сохранены как draft.\n",
		run.ID, plan.SelectedChunks, len(merged.Nodes), len(merged.Edges), plan.RemainingChunks)
	return nil
}

const maxAutomaticKnowledgeExtractionTokens = 16384

func extractKnowledgeExtractionBatch(ctx context.Context, provider mem.AnswerProvider, cfg mem.AnswerConfig, batch mem.KnowledgeExtractionJobBatch, ordinal, total int) (mem.KnowledgeExtractionResult, bool, error) {
	return mem.ExtractKnowledgeExtractionBatch(ctx, provider, cfg, batch, ordinal, total)
}
func generateKnowledgeExtractionBatch(ctx context.Context, provider mem.AnswerProvider, cfg mem.AnswerConfig, batch mem.KnowledgeExtractionJobBatch, ordinal, total int) (string, error) {
	return mem.GenerateKnowledgeExtractionBatch(ctx, provider, cfg, batch, ordinal, total)
}

func handleMapExtractionRuns(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapExtractionRuns")(&applogResult0)

	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("map extract-runs: -limit требует число")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map extract-runs: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map extract-runs: %s", args[i])
		}
	}
	runs, err := store.ListKnowledgeExtractionRuns(limit)
	if err != nil {
		return fmt.Errorf("map extract-runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(runs, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Extraction runs: records=%d limit=%d\n", len(runs), limit)
	for _, run := range runs {
		fmt.Fprintf(os.Stdout, "- %s [%s] chunks=%d/%d remaining=%d batches=%d completed=%d insufficient=%d failed=%d pending=%d updated=%s\n",
			run.ID, run.Status, run.SelectedChunks, run.EligibleChunks, run.RemainingChunks, run.BatchCount,
			run.CompletedBatches, run.InsufficientBatches, run.FailedBatches, run.PendingBatches, run.Updated)
		fmt.Fprintf(os.Stdout, "  focus %s\n", strings.Join(strings.Fields(run.Focus), " "))
	}
	return nil
}

func handleMapExtractionRun(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapExtractionRun")(&applogResult0)

	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return errors.New("использование: mem map extract-run <run-id> [--json]")
	}
	run, err := store.LoadKnowledgeExtractionRun(args[0])
	if err != nil {
		return fmt.Errorf("map extract-run: %w", err)
	}
	if len(args) == 2 {
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Extraction run %s [%s]\n", run.ID, run.Status)
	fmt.Fprintf(os.Stdout, "Focus: %s\nScope chunks: %d; selected: %d; remaining: %d\n",
		strings.Join(strings.Fields(run.Focus), " "), run.ScopedChunks, run.SelectedChunks, run.RemainingChunks)
	for _, batch := range run.Batches {
		fmt.Fprintf(os.Stdout, "- batch %d %s [%s] chunks=%d nodes=%d edges=%d updated=%s\n",
			batch.Ordinal+1, batch.BatchID, batch.Status, batch.EvidenceCount, len(batch.Graph.Nodes), len(batch.Graph.Edges), batch.Updated)
		if batch.Reason != "" {
			fmt.Fprintf(os.Stdout, "  reason %s\n", strings.Join(strings.Fields(batch.Reason), " "))
		}
	}
	return nil
}

func parseKnowledgeCoveragePages(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, errors.New("ожидается N или N-M")
	}
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return 0, 0, errors.New("ожидается N или N-M")
	}
	from, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || from < 1 {
		return 0, 0, errors.New("номер страницы должен быть положительным")
	}
	to := from
	if len(parts) == 2 {
		to, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || to < from {
			return 0, 0, errors.New("конец диапазона должен быть не меньше начала")
		}
	}
	return from, to, nil
}

func printKnowledgeCoverageReport(report mem.KnowledgeCoverageReport) {
	summary := report.Summary
	fmt.Fprintln(os.Stdout, "Покрытие карты знаний")
	fmt.Fprintln(os.Stdout, "-----------------------")
	scope := "весь текущий корпус"
	if report.Scope.Document != "" {
		scope = "документ " + report.Scope.Document
	}
	if report.Scope.PageFrom > 0 {
		if report.Scope.PageFrom == report.Scope.PageTo {
			scope += fmt.Sprintf(", страница %d", report.Scope.PageFrom)
		} else {
			scope += fmt.Sprintf(", страницы %d-%d", report.Scope.PageFrom, report.Scope.PageTo)
		}
	}
	if report.Scope.Tag != "" {
		scope += ", тег " + report.Scope.Tag
	}
	fmt.Fprintf(os.Stdout, "Область: %s\n", scope)
	fmt.Fprintf(os.Stdout, "Документы: %d\n", summary.Documents)
	if summary.ManifestDocuments > 0 {
		fmt.Fprintf(os.Stdout, "Импорт физических страниц: сохранено %d из %d (%.1f%%), пусто %d, ошибок %d; манифесты %d/%d документов\n",
			summary.StoredPhysicalPages, summary.PhysicalPagesInScope, summary.ImportCoveragePercent,
			summary.EmptyPhysicalPages, summary.FailedPhysicalPages, summary.ManifestDocuments, summary.Documents)
	}
	if summary.DocumentsWithoutManifest > 0 {
		fmt.Fprintf(os.Stdout, "Без манифеста импорта: %d документов — для учёта пустых страниц требуется повторный import\n",
			summary.DocumentsWithoutManifest)
	}
	fmt.Fprintf(os.Stdout, "Обработка заданий: обработано %d из %d chunks (%.1f%%), ожидают обработки %d\n",
		summary.ProcessedChunks, summary.ChunksWithText, summary.ProcessingPercent, summary.UnprocessedChunks)
	fmt.Fprintf(os.Stdout, "Chunks с текстом: покрыто %d из %d (%.1f%%), не покрыто %d\n",
		summary.CoveredChunks, summary.ChunksWithText, summary.CoveragePercent, summary.UncoveredChunks)
	fmt.Fprintf(os.Stdout, "Страницы с сохранённым текстом: %d; полностью покрыто %d, частично %d, не покрыто %d\n",
		summary.PagesWithText, summary.FullyCoveredPages, summary.PartiallyCoveredPages, summary.UncoveredPages)
	fmt.Fprintf(os.Stdout, "Объекты карты по текущим источникам: узлы %d, связи %d; draft %d, active %d, rejected %d, resolved %d\n",
		summary.ExtractedNodes, summary.ExtractedRelations, summary.DraftObjects, summary.ActiveObjects,
		summary.RejectedObjects, summary.ResolvedObjects)
	fmt.Fprintf(os.Stdout, "Качество источников: low-confidence OCR chunks %d на %d стр.; chunks с предупреждениями %d; без координаты страницы %d\n",
		summary.LowConfidenceOCRChunks, summary.LowConfidenceOCRPages, summary.WarningChunks, summary.UnlocatedChunks)
	if summary.StaleEvidenceObjects > 0 || summary.MissingEvidenceObjects > 0 {
		fmt.Fprintf(os.Stdout, "Устаревшие/отсутствующие evidence: объектов %d/%d (они не засчитаны как текущее покрытие)\n",
			summary.StaleEvidenceObjects, summary.MissingEvidenceObjects)
	}
	for _, document := range report.Documents {
		fmt.Fprintf(os.Stdout, "\n- %s\n", document.Title)
		fmt.Fprintf(os.Stdout, "  Источник: %s\n", document.SourcePath)
		if document.ImportManifestAvailable {
			fmt.Fprintf(os.Stdout, "  Импорт страниц: %d/%d в области (%.1f%%); всего в документе %d; пустые %s; ошибки %s\n",
				document.StoredPhysicalPages, document.PhysicalPagesInScope, document.ImportCoveragePercent,
				document.PhysicalPageCount, formatCoveragePages(document.EmptyPhysicalPages, 24),
				formatCoveragePages(document.FailedPhysicalPages, 24))
			for _, issue := range document.ImportPageIssues {
				reason := strings.Join(strings.Fields(strings.Join(issue.Warnings, "; ")), " ")
				if reason == "" {
					reason = "причина не указана"
				}
				fmt.Fprintf(os.Stdout, "  Страница %d [%s]: %s\n", issue.Page, issue.Status, reason)
			}
		} else {
			fmt.Fprintln(os.Stdout, "  Манифест физических страниц отсутствует: повторите import этой ревизии новой версией mem.")
		}
		fmt.Fprintf(os.Stdout, "  Chunks: %d/%d (%.1f%%); страницы: всего %d, полностью %d, частично %d, не покрыто %d\n",
			document.CoveredChunks, document.ChunksWithText, document.CoveragePercent,
			document.PagesWithText, document.FullyCoveredPages, document.PartiallyCoveredPages, len(document.UncoveredPages))
		if len(document.UncoveredPages) > 0 {
			fmt.Fprintf(os.Stdout, "  Непокрытые страницы: %s\n", formatCoveragePages(document.UncoveredPages, 24))
		}
		if len(document.LowConfidenceOCRPages) > 0 {
			fmt.Fprintf(os.Stdout, "  Слабый OCR на страницах: %s\n", formatCoveragePages(document.LowConfidenceOCRPages, 24))
		}
		if document.ProcessedChunks != document.ChunksWithText {
			fmt.Fprintf(os.Stdout, "  Задания: обработано %d/%d chunks (%.1f%%), ожидают %d\n",
				document.ProcessedChunks, document.ChunksWithText, document.ProcessingPercent, document.UnprocessedChunks)
		}
		for index, warning := range document.Warnings {
			if index >= 10 {
				fmt.Fprintf(os.Stdout, "  Предупреждения: … и ещё %d\n", len(document.Warnings)-index)
				break
			}
			location := "без номера страницы"
			if warning.Page > 0 {
				location = fmt.Sprintf("страница %d", warning.Page)
			}
			fmt.Fprintf(os.Stdout, "  Предупреждение, %s, блок %d, фрагмент %d: %s\n", location,
				warning.BlockIndex, warning.BlockChunkIndex, strings.Join(strings.Fields(strings.Join(warning.Messages, "; ")), " "))
		}
	}
	fmt.Fprintln(os.Stdout, "\nВажно: graph coverage считается по versioned chunks; import coverage отдельно учитывает все физические страницы из сохранённого манифеста.")
}

func formatCoveragePages(pages []int, limit int) string {
	if len(pages) == 0 {
		return "—"
	}
	if limit < 1 || limit > len(pages) {
		limit = len(pages)
	}
	values := make([]string, 0, limit)
	for _, page := range pages[:limit] {
		values = append(values, strconv.Itoa(page))
	}
	result := strings.Join(values, ", ")
	if limit < len(pages) {
		result += fmt.Sprintf(" … и ещё %d", len(pages)-limit)
	}
	return result
}
