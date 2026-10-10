package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

func handleSearch(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleSearch")(&applogResult0)

	if err := validateCommonFlags(args, "-tags", "-limit", "-from", "-to", "-min-score", "-vector-only", "-tag"); err != nil {
		return err
	}
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(args)
	if len(positional) == 0 {
		return fmt.Errorf("укажи поисковый запрос\nПример: mem search \"IP сервера\" -tags \"инфраструктура\" -from 2026-06-01")
	}

	query := strings.Join(positional, " ")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Printf(">> Поиск через %s... ", cfg.Backend)

	queryVec, err := getEmbedding(cfg, query)
	if err != nil {
		return fmt.Errorf("ошибка эмбеддинга: %w", err)
	}
	fmt.Printf("вектор %d измерений\n", len(queryVec))

	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: query, QueryVector: queryVec, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
		Tags: tags, TagFilter: tagFilter, From: from, To: to,
		VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("ошибка поиска: %w", err)
	}

	if len(tags) > 0 {
		fmt.Printf("[TAG] Фильтр по тегам: %s\n", strings.Join(tags, ", "))
	}

	if tagFilter != "" {
		fmt.Printf("[TAG-FILTER] Категория: %s\n", tagFilter)
	}

	if from != "" || to != "" {
		dateRange := ""
		if from != "" {
			dateRange = "от " + from
		}
		if to != "" {
			if dateRange != "" {
				dateRange += " "
			}
			dateRange += "до " + to
		}
		fmt.Printf("[DATE] Фильтр по дате: %s\n", dateRange)
	}

	if !vectorOnly && len(results) > 0 {
		lexicalCount := 0
		for _, result := range results {
			if result.LexicalHit {
				lexicalCount++
			}
		}
		fmt.Printf("[HYBRID] Vector %.0f%% + lexical %.0f%% (%s; lexical candidates %d/%d)\n",
			mem.VectorFusionWeight*100, mem.LexicalFusionWeight*100, store.LexicalMode(), lexicalCount, len(results))
	} else if vectorOnly && len(results) > 0 {
		fmt.Printf("[VECTOR] Только векторный поиск\n")
	}

	// Повторное ранжирование: свежесть + совпадение тегов + важность
	if len(results) > 0 {
		rerankCount := reRankResults(results, query)
		if rerankCount > 0 {
			sort.Slice(results, func(i, j int) bool {
				return results[i].Score > results[j].Score
			})
			fmt.Printf("[RERANK] Повторное ранжирование: буст у %d/%d записей\n",
				rerankCount, len(results))
		}
	}
	sortSearchResults(results)

	// Фильтрация по порогу релевантности (после всех бустов)
	if minScore > 0 {
		var filtered []Entry
		for _, r := range results {
			if r.Score >= minScore {
				filtered = append(filtered, r)
			}
		}
		results = filtered
		fmt.Printf("[SCORE] Порог релевантности: >= %.0f%%\n", minScore*100)
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]

	if len(results) == 0 {
		fmt.Println(ui.Warn("Ничего не найдено"))
		return nil
	}

	fmt.Println()
	for i, r := range results {
		pct := int(r.Score * 100)
		var markKind string
		switch {
		case pct > 90:
			markKind = "good"
		case pct > 70:
			markKind = "mid"
		default:
			markKind = "low"
		}
		// Форматируем дату
		dateStr := r.Created
		if t, err := time.Parse(time.RFC3339, r.Created); err == nil {
			dateStr = t.Format("2006-01-02")
		}

		title := r.Title
		if title == "" {
			title = "(без заголовка)"
		}

		impMark := ""
		if r.Important {
			impMark = " " + ui.Mark("warn")
		}
		// Заголовок результата: mark ID [score] title (date) [!]
		header := fmt.Sprintf("%s %s %s %s (%s)%s",
			ui.Mark(markKind),
			ui.ID(fmt.Sprintf("#%d", r.ID)),
			ui.Score(pct),
			title,
			ui.Date(dateStr),
			impMark,
		)
		fmt.Println(header)
		fmt.Printf("   %s\n", r.Text)
		fmt.Printf("   %s vector=%.3f lexical=%.3f fusion=%.3f final=%.3f\n",
			ui.Key("[SCORE]"), r.VectorScore, r.LexicalScore, r.FusionScore, r.Score)

		citationID, citationLabel := r.CitationID, r.CitationLabel
		if citationID == "" || citationLabel == "" {
			citationID, citationLabel = mem.CitationForEntry(r)
		}
		fmt.Printf("   %s %s %s\n", ui.Key("[CITE]"), ui.ID(citationID), ui.Tag(citationLabel))

		if len(r.Tags) > 0 {
			fmt.Printf("   %s %s\n", ui.Key("[TAG]"), ui.Tag(strings.Join(r.Tags, ", ")))
		}
		if i < len(results)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return nil
}

func handleAsk(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleAsk")(&applogResult0)

	searchArgs, contextOverride, err := parseAskArgs(args)
	if err != nil {
		return err
	}
	if err := validateCommonFlags(searchArgs, "-tags", "-limit", "-from", "-to", "-min-score", "-vector-only", "-tag"); err != nil {
		return err
	}
	positional, _, tags, limit, from, to, minScore, vectorOnly, _, tagFilter := parseFlags(searchArgs)
	if len(positional) == 0 {
		return fmt.Errorf("укажи вопрос\nПример: mem ask \"где описан порядок запуска?\" -limit 5")
	}
	question := strings.Join(positional, " ")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	answerCfg := cfg.Answer.WithDefaults()
	provider, err := newAnswerProvider(answerCfg)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "[ASK] retrieval: строю embedding и ищу evidence...")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := mem.AnswerContext(ctx, answerCfg)
	defer cancel()
	queryVector, err := getEmbeddingContext(ctx, cfg, question)
	if err != nil {
		return fmt.Errorf("ask retrieval: %w", err)
	}
	results, err := store.SearchWithOptions(mem.SearchOptions{
		Query: question, QueryVector: queryVector, Backend: embeddingIdentity.Backend, EmbeddingSpace: embeddingIdentity.SpaceID,
		Tags: tags, TagFilter: tagFilter, From: from, To: to, VectorOnly: vectorOnly,
	})
	if err != nil {
		return fmt.Errorf("ask retrieval: %w", err)
	}
	if len(results) > 0 {
		reRankResults(results, question)
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
	prompt, err := mem.BuildGroundedPromptWithOptions(question, results, contextBudget, cfg.Ingest.LowConfidence)
	if err != nil {
		return err
	}
	if len(prompt.Evidence) == 0 {
		fmt.Fprintln(os.Stdout, "Недостаточно подтверждённых данных: evidence не помещается в заданный context budget.")
		return nil
	}
	for _, evidence := range prompt.Evidence {
		for _, warning := range evidence.Warnings {
			fmt.Fprintf(os.Stderr, "[ASK] warning %s: %s\n", evidence.CitationID, warning)
		}
	}
	fmt.Fprintf(os.Stderr, "[ASK] evidence: %d фрагм.; generation через %s...\n", len(prompt.Evidence), answerCfg.Model)
	answerRequest := mem.AnswerRequest{
		Model: answerCfg.Model, System: prompt.System, Prompt: prompt.User,
		MaxTokens: answerCfg.MaxTokens, Temperature: answerCfg.Temperature,
	}
	schemaMode := mem.UsesGroundedAnswerSchema(answerCfg.Model)
	if schemaMode {
		answerRequest, err = mem.GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if err != nil {
			return fmt.Errorf("ask structured response: %w", err)
		}
		fmt.Fprintln(os.Stderr, "[ASK] compatibility: модель использует citation-bounded JSON Schema")
	}
	rawAnswer, err := provider.Generate(ctx, answerRequest)
	if err != nil {
		return fmt.Errorf("ask generation: %w", err)
	}
	validated := mem.ValidateGroundedAnswer(rawAnswer, prompt.Evidence)
	if schemaMode {
		validated = mem.ValidateGroundedSchemaAnswer(rawAnswer, prompt.Evidence)
	} else if mem.ShouldRetryGroundedAnswerWithSchema(rawAnswer, validated) {
		fmt.Fprintln(os.Stderr, "[ASK] compatibility: повторяю generation со строгой схемой citations...")
		retryRequest, schemaErr := mem.GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if schemaErr != nil {
			return fmt.Errorf("ask compatibility schema: %w", schemaErr)
		}
		rawAnswer, err = provider.Generate(ctx, retryRequest)
		if err != nil {
			return fmt.Errorf("ask compatibility generation: %w", err)
		}
		validated = mem.ValidateGroundedSchemaAnswer(rawAnswer, prompt.Evidence)
	}
	if validated.Rejected {
		if len(validated.UnknownIDs) > 0 {
			return fmt.Errorf("grounded answer rejected: %s (%s)", validated.Reason, strings.Join(validated.UnknownIDs, ", "))
		}
		return fmt.Errorf("grounded answer rejected: %s", validated.Reason)
	}
	fmt.Fprintln(os.Stdout, validated.Answer)
	if len(validated.Used) > 0 {
		printGroundedSources(os.Stdout, validated.Used)
	}
	return nil
}

func printGroundedSources(w io.Writer, evidence []mem.GroundedEvidence) {
	fmt.Fprintln(w, "\nИсточники:")
	for i, item := range evidence {
		number := i + 1
		sourcePath := strings.TrimSpace(item.SourcePath)
		name := filepath.Base(sourcePath)
		if sourcePath == "" || name == "." {
			name = strings.TrimSpace(item.CitationLabel)
		}
		if strings.HasPrefix(item.CitationID, "entry-") {
			name = "Запись базы #" + strings.TrimPrefix(item.CitationID, "entry-")
		}
		fmt.Fprintf(w, "[%d] %s\n", number, name)
		if sourcePath != "" {
			fmt.Fprintf(w, "    Файл: %s\n", sourcePath)
		}

		location := make([]string, 0, 4)
		if item.Page > 0 {
			location = append(location, fmt.Sprintf("страница %d", item.Page))
		}
		if item.DocumentID != "" || item.Page > 0 || item.BlockMarker != "" || item.BlockTotalChunks > 0 {
			location = append(location, fmt.Sprintf("блок %d", item.BlockIndex+1))
		}
		if item.BlockChunk != "" {
			location = append(location, "фрагмент "+item.BlockChunk)
		} else if item.Chunk != "" {
			location = append(location, "фрагмент "+item.Chunk)
		}
		marker := humanSectionMarker(item.BlockMarker)
		if marker != "" {
			location = append(location, "раздел "+marker)
		}
		if len(location) > 0 {
			fmt.Fprintln(w, "    Место: "+strings.Join(location, " · "))
		} else {
			fmt.Fprintln(w, "    Место: номер страницы отсутствует")
		}
	}
}

func humanSectionMarker(marker string) string {
	marker = strings.TrimSpace(marker)
	normalized := strings.ToLower(marker)
	if strings.HasPrefix(normalized, "page:") || strings.HasPrefix(normalized, "page ") || strings.HasPrefix(normalized, "<!-- page:") {
		return ""
	}
	return marker
}

func parseAskArgs(args []string) ([]string, int, error) {
	contextBudget := 0
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			out = append(out, args[i:]...)
			break
		}
		if args[i] != "-context-chars" {
			out = append(out, args[i])
			continue
		}
		if i+1 >= len(args) {
			return nil, 0, errors.New("использование: -context-chars <положительное число>")
		}
		i++
		n, parseErr := strconv.Atoi(args[i])
		if parseErr != nil || n <= 0 || n > mem.MaxAnswerContextChars {
			return nil, 0, fmt.Errorf("-context-chars должен быть от 1 до %d", mem.MaxAnswerContextChars)
		}
		contextBudget = n
	}
	return out, contextBudget, nil
}
