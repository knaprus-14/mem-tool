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

func handleMapRuns(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapRuns")(&applogResult0)

	jsonOutput, limit := false, 100
	var status mem.CorpusAnalysisRunStatus
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map runs [--json] [-limit N] [-status running|completed]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map runs: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		case "-status":
			if i+1 >= len(args) {
				return errors.New("использование: mem map runs [--json] [-limit N] [-status running|completed]")
			}
			i++
			status = mem.CorpusAnalysisRunStatus(strings.TrimSpace(args[i]))
			if status != mem.CorpusAnalysisRunRunning && status != mem.CorpusAnalysisRunCompleted {
				return errors.New("map runs: -status должен быть running или completed")
			}
		default:
			return fmt.Errorf("неизвестный аргумент map runs: %s", args[i])
		}
	}
	runs, err := store.ListCorpusAnalysisRuns(limit, status)
	if err != nil {
		return fmt.Errorf("map runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(runs, "", "  ")
		if err != nil {
			return fmt.Errorf("map runs: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis runs: records=%d limit=%d", len(runs), limit)
	if status != "" {
		fmt.Fprintf(os.Stdout, " status=%s", status)
	}
	fmt.Fprintln(os.Stdout)
	for _, run := range runs {
		fmt.Fprintf(os.Stdout, "- %s [%s] batches=%d completed=%d insufficient=%d failed=%d pending=%d updated=%s\n",
			run.ID, run.Status, run.BatchCount, run.CompletedBatches, run.InsufficientBatches,
			run.FailedBatches, run.PendingBatches, run.Updated)
		fmt.Fprintf(os.Stdout, "  focus %s\n", strings.Join(strings.Fields(run.Focus), " "))
		fmt.Fprintf(os.Stdout, "  coverage claims=%d/%d documents=%d/%d context=%d max_batches=%d\n",
			run.CoveredClaims, run.EligibleClaims, run.CoveredDocuments, run.EligibleDocuments,
			run.ContextChars, run.MaxBatches)
	}
	return nil
}

func handleMapRun(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapRun")(&applogResult0)

	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return errors.New("использование: mem map run <run-id> [--json]")
	}
	run, err := store.LoadCorpusAnalysisRun(args[0])
	if err != nil {
		return fmt.Errorf("map run: %w", err)
	}
	if len(args) == 2 {
		encoded, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return fmt.Errorf("map run: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis run %s [%s]\n", run.ID, run.Status)
	fmt.Fprintf(os.Stdout, "Focus: %s\n", strings.Join(strings.Fields(run.Focus), " "))
	fmt.Fprintf(os.Stdout, "Coverage: claims=%d/%d documents=%d/%d context=%d max_batches=%d\n",
		run.CoveredClaims, run.EligibleClaims, run.CoveredDocuments, run.EligibleDocuments,
		run.ContextChars, run.MaxBatches)
	fmt.Fprintf(os.Stdout, "Created: %s\nUpdated: %s\n", run.Created, run.Updated)
	for _, batch := range run.Batches {
		fmt.Fprintf(os.Stdout, "- batch %d %s [%s] findings=%d relations=%d updated=%s\n",
			batch.Ordinal+1, batch.BatchID, batch.Status, len(batch.Graph.Nodes), len(batch.Graph.Edges), batch.Updated)
		if batch.Reason != "" {
			fmt.Fprintf(os.Stdout, "  reason %s\n", strings.Join(strings.Fields(batch.Reason), " "))
		}
	}
	return nil
}

func parseAnalysisRunRetention(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days < 1 || days > 36500 {
			return 0, errors.New("число дней должно быть от 1 до 36500")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("ожидается положительная duration, например 720h или 30d")
	}
	return duration, nil
}

func handleMapPruneRuns(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapPruneRuns")(&applogResult0)

	var olderThan time.Duration
	keepLatest := 20
	dryRun, yes, jsonOutput := false, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-older-than":
			if i+1 >= len(args) {
				return errors.New("использование: mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]")
			}
			i++
			parsed, err := parseAnalysisRunRetention(args[i])
			if err != nil {
				return fmt.Errorf("map prune-runs: -older-than: %w", err)
			}
			olderThan = parsed
		case "-keep":
			if i+1 >= len(args) {
				return errors.New("использование: mem map prune-runs -older-than <duration> [-keep N] [--dry-run|--yes] [--json]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 0 || parsed > 10000 {
				return errors.New("map prune-runs: -keep должен быть от 0 до 10000")
			}
			keepLatest = parsed
		case "--dry-run":
			dryRun = true
		case "--yes":
			yes = true
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("неизвестный аргумент map prune-runs: %s", args[i])
		}
	}
	if olderThan == 0 {
		return errors.New("map prune-runs: обязателен -older-than, например 30d")
	}
	if dryRun && yes {
		return errors.New("map prune-runs: --dry-run и --yes несовместимы")
	}
	preview := !yes
	result, err := store.PruneCompletedCorpusAnalysisRuns(time.Now().UTC().Add(-olderThan), keepLatest, preview)
	if err != nil {
		return fmt.Errorf("map prune-runs: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("map prune-runs: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	if preview {
		fmt.Fprintf(os.Stdout, "Analysis run cleanup preview: candidates=%d completed_before=%s keep_latest=%d\n",
			len(result.Runs), result.CompletedBefore, result.KeepLatest)
		for _, run := range result.Runs {
			fmt.Fprintf(os.Stdout, "- %s [completed] batches=%d updated=%s focus=%s\n",
				run.ID, run.BatchCount, run.Updated, strings.Join(strings.Fields(run.Focus), " "))
		}
		fmt.Fprintln(os.Stdout, "Изменений нет. Для удаления повтори команду с --yes.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Analysis run cleanup complete: runs=%d batches=%d completed_before=%s keep_latest=%d\n",
		result.DeletedRuns, result.DeletedBatches, result.CompletedBefore, result.KeepLatest)
	return nil
}

func handleMapAnalyze(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapAnalyze")(&applogResult0)

	focusParts := make([]string, 0, len(args))
	contextBudget := cfg.Answer.WithDefaults().ContextChars
	maxBatches := 1
	resumeID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-context-chars":
			if i+1 >= len(args) {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxAnswerContextChars {
				return fmt.Errorf("-context-chars должен быть от 1 до %d", mem.MaxAnswerContextChars)
			}
			contextBudget = parsed
		case "-batches":
			if i+1 >= len(args) {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > mem.MaxCorpusAnalysisBatches {
				return fmt.Errorf("-batches должен быть от 1 до %d", mem.MaxCorpusAnalysisBatches)
			}
			maxBatches = parsed
		case "-resume":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("использование: mem map analyze <фокус> [-context-chars N] [-batches N] [-resume <run-id>]")
			}
			if resumeID != "" {
				return errors.New("-resume указан более одного раза")
			}
			i++
			resumeID = strings.TrimSpace(args[i])
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("неизвестный аргумент map analyze: %s", args[i])
			}
			focusParts = append(focusParts, args[i])
		}
	}
	if len(focusParts) == 0 {
		return errors.New("укажи фокус междокументного анализа\nПример: mem map analyze \"требования к давлению\"")
	}
	focus := strings.Join(focusParts, " ")
	plan, err := store.BuildCorpusAnalysisPlan(focus, contextBudget, maxBatches)
	if err != nil {
		return fmt.Errorf("map analyze: %w", err)
	}
	if plan.SkippedNonCurrent > 0 {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] пропущено active claims с stale/missing evidence: %d\n", plan.SkippedNonCurrent)
	}
	if len(plan.Batches) == 0 {
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: нужны минимум два active claim с current evidence из разных документов, помещающиеся в context budget (eligible=%d documents=%d).\n",
			plan.EligibleClaims, plan.EligibleDocuments)
		return nil
	}
	if plan.UncoveredClaims > 0 {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] покрытие ограничено: covered=%d eligible=%d uncovered=%d; увеличь -batches или -context-chars\n",
			plan.CoveredClaims, plan.EligibleClaims, plan.UncoveredClaims)
	}
	fmt.Fprintf(os.Stderr, "[MAP ANALYZE] semantic vectors=%d/%d guided_batches=%d fallback_batches=%d\n",
		plan.SemanticClaims, plan.EligibleClaims, plan.SemanticBatches, plan.FallbackBatches)
	configuredAnswerCfg := cfg.Answer.WithDefaults()
	answerCfg := cfg.Answer.WithMapGenerationDefaults()
	if answerCfg.MaxTokens != configuredAnswerCfg.MaxTokens {
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] output budget: %d tokens (answer.max_tokens=%d; повышен для структурированного JSON)\n",
			answerCfg.MaxTokens, configuredAnswerCfg.MaxTokens)
	}
	run, err := store.PrepareCorpusAnalysisRun(focus, contextBudget, maxBatches, plan, answerCfg, resumeID)
	if err != nil {
		return fmt.Errorf("map analyze run: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[MAP ANALYZE] run=%s status=%s batches=%d; повтор той же команды безопасно продолжит запуск\n",
		run.ID, run.Status, len(run.Batches))
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	graphs := make([]mem.KnowledgeGraph, 0, len(plan.Batches))
	insufficientBatches := 0
	for i, prompt := range plan.Batches {
		stored := run.Batches[i]
		switch stored.Status {
		case mem.CorpusAnalysisBatchCompleted:
			graphs = append(graphs, stored.Graph)
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: восстановлен проверенный результат\n",
				i+1, len(plan.Batches), prompt.BatchID)
			continue
		case mem.CorpusAnalysisBatchInsufficient:
			insufficientBatches++
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: восстановлен insufficient result (%s)\n",
				i+1, len(plan.Batches), prompt.BatchID, stored.Reason)
			continue
		}
		fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s claims=%d documents=%d; analysis через %s...\n",
			i+1, len(plan.Batches), prompt.BatchID, len(prompt.Claims), prompt.DocumentCount, answerCfg.Model)
		ctx, cancel := mem.AnswerContext(rootCtx, answerCfg)
		raw, generateErr := provider.Generate(ctx, mem.AnswerRequest{
			Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
			MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
		})
		cancel()
		if generateErr != nil {
			if saveErr := store.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, generateErr); saveErr != nil {
				return fmt.Errorf("map analyze batch %d (%s), run=%s: %v; save failure state: %w", i+1, prompt.BatchID, run.ID, generateErr, saveErr)
			}
			return fmt.Errorf("map analyze batch %d (%s), run=%s: %w", i+1, prompt.BatchID, run.ID, generateErr)
		}
		analyzed, decodeErr := mem.DecodeCorpusAnalysis(raw, prompt.Claims)
		if decodeErr != nil {
			if saveErr := store.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, decodeErr); saveErr != nil {
				return fmt.Errorf("map analyze batch %d (%s) rejected, run=%s: %v; save failure state: %w", i+1, prompt.BatchID, run.ID, decodeErr, saveErr)
			}
			return fmt.Errorf("map analyze batch %d (%s) rejected, run=%s: %w", i+1, prompt.BatchID, run.ID, decodeErr)
		}
		if analyzed.Insufficient {
			if err := store.SaveCorpusAnalysisBatchInsufficient(run.ID, prompt.BatchID, analyzed.Reason); err != nil {
				return fmt.Errorf("map analyze batch %d (%s) checkpoint, run=%s: %w", i+1, prompt.BatchID, run.ID, err)
			}
			insufficientBatches++
			fmt.Fprintf(os.Stderr, "[MAP ANALYZE] batch=%d/%d id=%s: insufficient evidence (%s)\n",
				i+1, len(plan.Batches), prompt.BatchID, analyzed.Reason)
			continue
		}
		if err := store.SaveCorpusAnalysisBatchGraph(run.ID, prompt.BatchID, analyzed.Graph); err != nil {
			return fmt.Errorf("map analyze batch %d (%s) checkpoint, run=%s: %w", i+1, prompt.BatchID, run.ID, err)
		}
		graphs = append(graphs, analyzed.Graph)
	}
	merged, err := mem.MergeCorpusAnalysisGraphs(graphs...)
	if err != nil {
		return fmt.Errorf("map analyze merge: %w", err)
	}
	if len(merged.Nodes) == 0 {
		if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
			return fmt.Errorf("map analyze complete run %s: %w", run.ID, err)
		}
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: все пакеты завершились без findings (run=%s batches=%d covered=%d/%d semantic=%d fallback=%d).\n",
			run.ID, insufficientBatches, plan.CoveredClaims, plan.EligibleClaims, plan.SemanticBatches, plan.FallbackBatches)
		return nil
	}
	if err := store.UpsertCorpusAnalysisGraph(merged); err != nil {
		return fmt.Errorf("map analyze persistence: %w", err)
	}
	if err := store.CompleteCorpusAnalysisRun(run.ID); err != nil {
		return fmt.Errorf("map analyze complete run %s: %w", run.ID, err)
	}
	fmt.Fprintf(os.Stdout, "Междокументный анализ сохранён как draft: run=%s findings=%d relations=%d batches=%d insufficient=%d covered=%d/%d documents=%d/%d semantic=%d fallback=%d\n",
		run.ID, len(merged.Nodes), len(merged.Edges), len(plan.Batches), insufficientBatches,
		plan.CoveredClaims, plan.EligibleClaims, plan.CoveredDocuments, plan.EligibleDocuments,
		plan.SemanticBatches, plan.FallbackBatches)
	return nil
}

func handleMapBuild(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapBuild")(&applogResult0)

	searchArgs, contextOverride, err := parseAskArgs(args)
	if err != nil {
		return err
	}
	if err := validateCommonFlags(searchArgs, "-tags", "-limit", "-from", "-to", "-min-score", "-vector-only", "-tag"); err != nil {
		return err
	}
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(searchArgs)
	if len(positional) == 0 {
		return errors.New("укажи фокус карты\nПример: mem map build \"архитектура импорта\" -limit 10")
	}
	focus := strings.Join(positional, " ")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	configuredAnswerCfg := cfg.Answer.WithDefaults()
	answerCfg := cfg.Answer.WithMapGenerationDefaults()

	fmt.Fprintln(os.Stderr, "[MAP] retrieval: строю embedding и ищу versioned evidence...")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := mem.AnswerContext(ctx, answerCfg)
	defer cancel()
	queryVector, err := getEmbeddingContext(ctx, cfg, focus)
	if err != nil {
		return fmt.Errorf("map retrieval: %w", err)
	}
	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: focus, QueryVector: queryVector, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
		Tags: tags, TagFilter: tagFilter, From: from, To: to, VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("map retrieval: %w", err)
	}
	if len(results) > 0 {
		reRankResults(results, focus)
		sortSearchResults(results)
	}
	if minScore > 0 {
		filtered := results[:0]
		for _, result := range results {
			if result.Score >= minScore {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]
	if len(results) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: поиск не вернул подходящих фрагментов.")
		return nil
	}

	contextBudget := answerCfg.ContextChars
	if contextOverride > 0 {
		contextBudget = contextOverride
	}
	prompt, err := mem.BuildKnowledgeExtractionPrompt(focus, results, contextBudget, cfg.Ingest.LowConfidence)
	if err != nil {
		return err
	}
	if len(prompt.Evidence) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: среди результатов нет versioned document evidence, помещающегося в context budget.")
		return nil
	}
	for _, evidence := range prompt.Evidence {
		for _, warning := range evidence.Warnings {
			fmt.Fprintf(os.Stderr, "[MAP] warning %s: %s\n", evidence.CitationID, warning)
		}
	}
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}
	if answerCfg.MaxTokens != configuredAnswerCfg.MaxTokens {
		fmt.Fprintf(os.Stderr, "[MAP] output budget: %d tokens (answer.max_tokens=%d; повышен для структурированного JSON)\n",
			answerCfg.MaxTokens, configuredAnswerCfg.MaxTokens)
	}
	fmt.Fprintf(os.Stderr, "[MAP] evidence: %d фрагм.; extraction через %s...\n", len(prompt.Evidence), answerCfg.Model)
	raw, err := provider.Generate(ctx, mem.AnswerRequest{
		Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
		MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
	})
	if err != nil {
		return fmt.Errorf("map extraction: %w", err)
	}
	extracted, err := mem.DecodeKnowledgeExtraction(raw, prompt.Evidence)
	if err != nil {
		return fmt.Errorf("map extraction rejected: %w", err)
	}
	if extracted.Insufficient {
		fmt.Fprintf(os.Stdout, "Недостаточно подтверждённых данных: %s\n", extracted.Reason)
		return nil
	}
	if err := store.UpsertCurrentKnowledgeGraph(extracted.Graph); err != nil {
		return fmt.Errorf("map persistence: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Карта обновлена: nodes=%d edges=%d evidence=%d\n",
		len(extracted.Graph.Nodes), len(extracted.Graph.Edges), len(prompt.Evidence))
	return nil
}
