package mem

// Document processing separates a lossless source graph from fallible semantic
// enrichment. Model failures are durable, visible work items, never assertions.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type KnowledgeDocumentIssue struct {
	CitationID string `json:"citation_id"`
	Page       int    `json:"page"`
	BlockIndex int    `json:"block_index"`
	Reason     string `json:"reason"`
}

// RunKnowledgeDocumentGraph publishes source text first, then checkpoints each
// validated window. Repeating it retries only unfinished semantic work. A nil
// provider is supported: source navigation does not require a language model.
func (s *Store) RunKnowledgeDocumentGraph(ctx context.Context, cfg *Config, provider AnswerProvider, document, focus, legacyRun string, progress func(string, int, int) error) error {
	if cfg == nil || strings.TrimSpace(document) == "" || strings.TrimSpace(focus) == "" {
		return ErrWebOperationInvalid
	}
	if progress == nil {
		progress = func(string, int, int) error { return nil }
	}
	if _, err := s.db.ExecContext(ctx, documentGraphSchema); err != nil {
		return err
	}
	entries, err := s.knowledgeCoverageEntries(KnowledgeCoverageOptions{Document: document})
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("В документе нет извлечённого текста. Проверьте результат импорта и OCR.")
	}
	focus = strings.TrimSpace(focus)
	answer := cfg.Answer.WithMapGenerationDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 60)
	answer.MaxTokens = min(answer.MaxTokens, 8192)
	configurationDigest, err := documentConfigurationDigest(answer, focus, cfg.Ingest.LowConfidence)
	if err != nil {
		return err
	}
	// Preserve validated results from an explicitly resumed old portion run.
	// Its prompt/window/config contract differs from this full-document pass,
	// so it cannot establish completion under the new input digest.
	if legacyRun != "" {
		plan, e := s.RebuildKnowledgeExtractionRunPlan(legacyRun, focus)
		if e != nil {
			return e
		}
		if !strings.EqualFold(plan.Scope.Document, document) {
			return ErrWebOperationInvalid
		}
		run, e := s.LoadKnowledgeExtractionRun(legacyRun)
		if e != nil {
			return e
		}
		for _, b := range run.Batches {
			if e = ctx.Err(); e != nil {
				return e
			}
			if b.Status != KnowledgeExtractionBatchCompleted && b.Status != KnowledgeExtractionBatchInsufficient {
				continue
			}
			if b.Status == KnowledgeExtractionBatchCompleted {
				if e = s.UpsertCurrentKnowledgeGraph(b.Graph); e != nil {
					return e
				}
			}
		}
	}
	// Unpinned legacy ledgers and other extraction modes are never sufficient
	// evidence that this exact source/configuration has been fully inspected.
	pins, err := s.prepareDocumentChunks(ctx, entries, configurationDigest)
	if err != nil {
		return err
	}
	if err = progress("Сохраняем структуру и текст документа", 0, len(entries)); err != nil {
		return err
	}
	root, err := documentSourceRoot(entries[0])
	if err != nil {
		return err
	}
	root.ID, err = s.documentSourceNodeID(ctx, root.ID, "doc-source-", KnowledgeNodeDocument, root.Evidence[0], 0)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = s.publishDocumentSource(ctx, root, e, func() error {
			return progress("Сохраняем исходный текст документа", 0, len(entries))
		}); err != nil {
			return err
		}
		if (i+1)%25 == 0 {
			if err = progress("Сохраняем структуру и текст документа", 0, len(entries)); err != nil {
				return err
			}
		}
	}
	runner := documentEnricher{store: s, provider: provider, answer: answer, focus: focus, lowConfidence: cfg.Ingest.LowConfidence, configurationDigest: configurationDigest, rootID: root.ID}
	deferred := 0
	for i, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		pin := pins[i]
		status, e := s.documentChunkStatus(ctx, pin)
		if e != nil {
			return e
		}
		if status != "completed" && status != "insufficient" {
			if err = s.saveDocumentChunk(ctx, pin, "pending", ""); err != nil {
				return err
			}
			runner.notify = func(stage string) error { return progress(stage, i, len(entries)) }
			if err = runner.notify(fmt.Sprintf("Смысловой разбор фрагмента %d/%d", i+1, len(entries))); err != nil {
				return err
			}
			// A pathological chunk cannot occupy the worker indefinitely. Windows
			// committed before this deadline are reused on the next invocation.
			chunkCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			reason := ""
			for _, text := range documentTextWindows(entry.Text, documentSemanticWindowSize, documentSemanticOverlap) {
				calls := 0
				issue, x := runner.window(chunkCtx, entry, text, &calls)
				if x != nil {
					cancel()
					return x
				}
				if issue != "" {
					reason = issue
				}
			}
			cancel()
			if err = ctx.Err(); err != nil {
				return err
			}
			status = "completed"
			if reason != "" {
				status = "deferred"
				deferred++
			}
			if err = s.saveDocumentChunk(ctx, pin, status, reason); err != nil {
				return err
			}
		}
		if err = progress("Извлечение тем", i+1, len(entries)); err != nil {
			return err
		}
		if err = progress("Сохранено. Структура, текст и проверенные результаты доступны в графе", i+1, len(entries)); err != nil {
			return err
		}
	}
	stage := "Документ сохранён в графе. Смысловой разбор завершён; утверждения остаются черновиками."
	if deferred > 0 {
		stage = fmt.Sprintf("Документ сохранён в графе. Смысловой разбор отложен для %d из %d фрагментов. Нажмите «Построить / дополнить»: повторятся только незавершённые места.", deferred, len(entries))
	}
	return progress(stage, len(entries), len(entries))
}

func documentSourceRoot(e Entry) (KnowledgeNode, error) {
	a, err := evidenceAnchorFromGrounded(groundedEvidenceForEntry(e, truncateRunes(strings.TrimSpace(e.Text), MaxKnowledgeExcerptRunes), 0))
	return KnowledgeNode{ID: "doc-source-" + documentGraphID(e.DocumentID+"\x00"+e.DocumentRevision), Kind: KnowledgeNodeDocument, Label: truncateRunes(firstNonEmpty(e.Title, e.SourcePath), MaxKnowledgeLabelRunes), Origin: KnowledgeOriginSource, Status: KnowledgeStatusActive, Evidence: []EvidenceAnchor{a}}, err
}
func documentGraphID(text string) string {
	return strings.TrimPrefix(ChunkContentHash(text), "sha256:")
}
func (s *Store) publishDocumentSource(ctx context.Context, root KnowledgeNode, e Entry, progress func() error) error {
	for i, text := range documentTextWindows(e.Text, MaxKnowledgeExcerptRunes, 0) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 && i%25 == 0 {
			if err := progress(); err != nil {
				return err
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		a, err := evidenceAnchorFromGrounded(groundedEvidenceForEntry(e, text, 0))
		if err != nil {
			return err
		}
		id, err := s.documentSourceSectionID(ctx, a, i)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("Фрагмент %d.%d", e.BlockIndex+1, e.BlockChunkIndex+1)
		if e.Page > 0 {
			label = fmt.Sprintf("Стр. %d · %s", e.Page, label)
		}
		if i > 0 {
			label += fmt.Sprintf(" · часть %d", i+1)
		}
		n := KnowledgeNode{ID: id, Kind: KnowledgeNodeSection, Label: label, Body: text, Origin: KnowledgeOriginSource, Status: KnowledgeStatusActive, Evidence: []EvidenceAnchor{a}}
		edge := KnowledgeEdge{ID: "source-contains-" + documentGraphID(root.ID+id), From: root.ID, To: id, Kind: KnowledgeRelationContains, Origin: KnowledgeOriginSource, Status: KnowledgeStatusActive, Evidence: []EvidenceAnchor{a}}
		fragment := KnowledgeGraph{}
		// Repeating extraction must preserve edits to source-node labels/bodies.
		for _, node := range []KnowledgeNode{root, n} {
			var count int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes WHERE id=?`, node.ID).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				fragment.Nodes = append(fragment.Nodes, node)
			}
		}
		var count int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_edges WHERE id=?`, edge.ID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			fragment.Edges = append(fragment.Edges, edge)
		}
		if len(fragment.Nodes) == 0 && len(fragment.Edges) == 0 {
			continue
		}
		if err = s.UpsertCurrentKnowledgeGraph(fragment); err != nil {
			return err
		}
	}
	return nil
}

// Rune windows preserve every character, with optional context overlap. Even a
// single huge paragraph has finite windows; source evidence keeps its full hash.
func documentTextWindows(text string, size, overlap int) []string {
	r := []rune(text)
	out := []string{}
	for start := 0; start < len(r); {
		end := min(start+size, len(r))
		out = append(out, string(r[start:end]))
		if end == len(r) {
			break
		}
		start = end - overlap
	}
	return out
}

type documentEnricher struct {
	store               *Store
	provider            AnswerProvider
	answer              AnswerConfig
	focus               string
	lowConfidence       float64
	notify              func(string) error
	serviceFailures     int
	configurationDigest string
	rootID              string
}

func (r *documentEnricher) window(ctx context.Context, e Entry, text string, calls *int) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	if ctx.Err() != nil {
		return "Достигнут предел времени или повторов для фрагмента; сохранены текст и готовые части.", nil
	}
	configurationDigest := r.configurationDigest
	if configurationDigest == "" {
		var err error
		configurationDigest, err = documentConfigurationDigest(r.answer, r.focus, r.lowConfidence)
		if err != nil {
			return "", err
		}
	}
	pin, err := documentInputPin(e, configurationDigest)
	if err != nil {
		return "", err
	}
	hash := ChunkContentHash(text)
	var completed int
	err = r.store.db.QueryRowContext(ctx, `SELECT 1 FROM knowledge_document_windows_v2 WHERE citation_id=? AND input_digest=? AND window_hash=?`, pin.CitationID, pin.InputDigest, hash).Scan(&completed)
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if r.provider == nil {
		return "Модель ответов не настроена; сохранены структура и исходный текст.", nil
	}
	if r.serviceFailures >= 2 {
		return "Сервис модели не отвечает. Сохранён исходный текст; повторите смысловой разбор позже.", nil
	}
	if ctx.Err() != nil || *calls >= 7 {
		return "Достигнут предел времени или повторов для фрагмента; сохранены текст и готовые части.", nil
	}
	evidence := []GroundedEvidence{groundedEvidenceForEntry(e, text, r.lowConfidence)}
	data, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	focus, _ := json.Marshal(r.focus)
	prompt := "Focus (user input): " + string(focus) + "\nEVIDENCE_JSON_BEGIN\n" + string(data) + "\nEVIDENCE_JSON_END\n"
	system := knowledgeExtractionSystemPrompt + knowledgeExtractionShapeGuidance
	var result KnowledgeExtractionResult
	invalid := false
	for attempt := 0; attempt < 2 && *calls < 7; attempt++ {
		*calls++
		var raw string
		raw, err = r.generate(ctx, AnswerRequest{Model: r.answer.Model, System: system, Prompt: prompt, MaxTokens: r.answer.MaxTokens, Temperature: r.answer.Temperature})
		invalid = false
		if err != nil {
			break
		}
		r.serviceFailures = 0
		result, err = DecodeKnowledgeExtraction(raw, evidence)
		invalid = err != nil
		if err == nil {
			break
		}
		system = correctedKnowledgeExtractionPrompt(knowledgeExtractionSystemPrompt, err, 1) + knowledgeExtractionShapeGuidance
		prompt = "Focus (user input): " + string(focus) + "\nEVIDENCE_JSON_BEGIN\n" + string(data) + "\nEVIDENCE_JSON_END\n" + knowledgeExtractionRejectedCandidate(raw, err)
	}
	if err != nil {
		var progressErr *documentProgressError
		if errors.As(err, &progressErr) {
			return "", progressErr.err
		}
		_, tokens := AnswerTokenLimit(err)
		deadline := errors.Is(err, context.DeadlineExceeded)
		contextLimit := false
		for _, marker := range []string{"context length", "context window", "maximum context", "input too long", "too many tokens"} {
			contextLimit = contextLimit || strings.Contains(strings.ToLower(err.Error()), marker)
		}
		if (tokens || deadline || contextLimit) && len([]rune(text)) > 1200 && ctx.Err() == nil && *calls < 7 {
			parts := documentTextWindows(text, (len([]rune(text))+1)/2+128, 128)
			reason := ""
			for _, part := range parts {
				issue, x := r.window(ctx, e, part, calls)
				if x != nil {
					return "", x
				}
				if issue != "" {
					reason = issue
				}
			}
			if reason != "" {
				return reason, nil
			}
		} else {
			if contextLimit {
				return "Модель не вместила текст запроса; сохранены исходный текст и готовые части.", nil
			}
			if tokens {
				return "Модель исчерпала лимит ответа; сохранён исходный текст.", nil
			}
			if deadline || ctx.Err() != nil {
				r.serviceFailures++
				return "Модель не завершила разбор вовремя; сохранены текст и готовые части.", nil
			}
			// Decoder errors are local to this fragment; transport failures trip a
			// circuit breaker, avoiding the same wait for every remaining chunk.
			if invalid {
				return "Ответ модели не прошёл проверку структуры или точности цитат; сохранён исходный текст.", nil
			}
			r.serviceFailures++
			return "Ошибка сервиса модели; сохранён исходный текст. Повторите разбор позже.", nil
		}
	} else if !result.Insufficient {
		// Host-created provenance links connect extracted topics to their source
		// document. They make no causal, temporal or normative claim.
		rootID := r.rootID
		for _, node := range result.Graph.Nodes {
			result.Graph.Edges = append(result.Graph.Edges, KnowledgeEdge{ID: "document-derived-" + documentGraphID(node.ID+rootID), From: node.ID, To: rootID, Kind: KnowledgeRelationDerivedFrom, Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: node.Evidence})
		}
		if err = r.store.UpsertCurrentKnowledgeGraph(result.Graph); err != nil {
			return "", err
		}
	}
	// Graph publication precedes its checkpoint. A crash between the two only
	// repeats an idempotent upsert, never records unpublished work as complete.
	_, err = r.store.db.ExecContext(ctx, `INSERT INTO knowledge_document_windows_v2 VALUES(?,?,?) ON CONFLICT DO NOTHING`, pin.CitationID, pin.InputDigest, hash)
	return "", err
}

type documentProgressError struct{ err error }

func (e *documentProgressError) Error() string { return e.err.Error() }
func (e *documentProgressError) Unwrap() error { return e.err }

// Keep progress on the worker goroutine; no concurrent mutation of WebOperation.
func (r *documentEnricher) generate(ctx context.Context, request AnswerRequest) (string, error) {
	call, cancel := AnswerContext(ctx, r.answer)
	defer cancel()
	type response struct {
		raw string
		err error
	}
	done := make(chan response, 1)
	go func() { raw, err := r.provider.Generate(call, request); done <- response{raw, err} }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case reply := <-done:
			return reply.raw, reply.err
		case <-call.Done():
			return "", call.Err()
		case <-ticker.C:
			if r.notify != nil {
				if err := r.notify(fmt.Sprintf("Модель обрабатывает фрагмент · %d с (предел запроса %d с)", int(time.Since(start).Seconds()), r.answer.TimeoutSeconds)); err != nil {
					return "", &documentProgressError{err}
				}
			}
		}
	}
}

func (s *Store) documentExtractionIssues(selected map[string]knowledgeCoverageChunk) ([]KnowledgeDocumentIssue, int, error) {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='knowledge_document_chunks_v2'`).Scan(&exists); err != nil {
		return nil, 0, err
	}
	issues := []KnowledgeDocumentIssue{}
	completed := 0
	seen := map[string]bool{}
	if exists != 0 {
		rows, err := s.db.Query(`SELECT citation_id,source_digest,status,reason FROM knowledge_document_chunks_v2 ORDER BY citation_id`)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, sourceDigest, status, reason string
			if err = rows.Scan(&id, &sourceDigest, &status, &reason); err != nil {
				return nil, 0, err
			}
			chunk, ok := selected[id]
			if !ok {
				continue
			}
			currentDigest, err := documentSourceDigest(chunk.entry)
			if err != nil {
				return nil, 0, err
			}
			if currentDigest != sourceDigest {
				continue
			}
			seen[id] = true
			if status == "completed" || status == "insufficient" {
				completed++
				continue
			}
			if reason == "" {
				reason = "Смысловой разбор ещё не завершён."
			}
			issues = append(issues, KnowledgeDocumentIssue{CitationID: id, Page: chunk.entry.Page, BlockIndex: chunk.entry.BlockIndex, Reason: reason})
		}
		if err = rows.Err(); err != nil {
			return nil, 0, err
		}
	}
	for id, chunk := range selected {
		if !seen[id] {
			issues = append(issues, KnowledgeDocumentIssue{CitationID: id, Page: chunk.entry.Page, BlockIndex: chunk.entry.BlockIndex, Reason: "Смысловой разбор ещё не запускался в полном режиме."})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Page != issues[j].Page {
			return issues[i].Page < issues[j].Page
		}
		if issues[i].BlockIndex != issues[j].BlockIndex {
			return issues[i].BlockIndex < issues[j].BlockIndex
		}
		return issues[i].CitationID < issues[j].CitationID
	})
	return issues, completed, nil
}
