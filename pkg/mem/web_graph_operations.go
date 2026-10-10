package mem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Retain the decoder cause for diagnostics, but expose only a safe action to UI.
type webGraphExtractionFailure struct {
	Batch int
	Cause error
}

func (e *webGraphExtractionFailure) Error() string {
	return fmt.Sprintf("graph extraction batch %d rejected: %v", e.Batch, e.Cause)
}
func (e *webGraphExtractionFailure) Unwrap() error { return e.Cause }

func runGraphOperation(ctx context.Context, dir string, cfg *Config, op *WebOperation, progress operationProgress) error {
	s, err := OpenWebGraphIn(ctx, dir)
	if err != nil {
		return err
	}
	defer s.Close()
	if op.Kind == "graph-extract" && op.Input.AllChunks {
		answer := cfg.Answer.WithMapGenerationDefaults()
		var provider AnswerProvider
		if validateWebGraphAnswerModel(op.Kind, cfg) == nil {
			provider, _ = NewOllamaAnswerProvider(answer)
		}
		return s.RunKnowledgeDocumentGraph(ctx, cfg, provider, op.Input.Document, op.Input.Focus, op.RunID, func(stage string, current, total int) error {
			return progress(stage, current, total, "", 0)
		})
	}
	if op.Kind == "graph-duplicates" {
		graph, e := s.LoadKnowledgeGraph()
		if e != nil {
			return e
		}
		identity, e := EmbeddingIdentityForConfig(cfg)
		if e != nil {
			return e
		}
		vectors := []KnowledgeNodeVector{}
		for _, n := range graph.Nodes {
			if n.Status == KnowledgeStatusResolved {
				continue
			}
			current := true
			for _, anchor := range n.Evidence {
				if s.ResolveEvidenceAnchor(anchor).State != EvidenceCurrent {
					current = false
					break
				}
			}
			if !current {
				continue
			}
			digest, e := KnowledgeNodeContentDigest(n)
			if e != nil {
				return e
			}
			text := n.Label
			if strings.TrimSpace(n.Body) != "" {
				text += "\n\n" + n.Body
			}
			call, cancel := context.WithTimeout(ctx, 180*time.Second)
			vector, e := GetEmbeddingContext(call, cfg, text)
			cancel()
			if e != nil {
				return e
			}
			vectors = append(vectors, KnowledgeNodeVector{NodeID: n.ID, NodeDigest: digest, Embedding: vector})
			if e = progress("Сравнение смыслов узлов", len(vectors), min(1000, len(graph.Nodes)), "", 0); e != nil {
				return e
			}
			if len(vectors) >= 1000 {
				break
			}
		}
		report, e := s.DetectKnowledgeNodeDuplicates(vectors, identity.SpaceID, 0.92, 100, "")
		if e != nil {
			return e
		}
		data, e := json.Marshal(report)
		if e != nil {
			return e
		}
		if _, e = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_operation_results(id TEXT PRIMARY KEY,result TEXT NOT NULL);`); e != nil {
			return e
		}
		_, e = s.db.ExecContext(ctx, `INSERT INTO web_operation_results VALUES(?,?) ON CONFLICT(id) DO UPDATE SET result=excluded.result`, op.ID, string(data))
		return e
	}
	if err := validateWebGraphAnswerModel(op.Kind, cfg); err != nil {
		return err
	}
	answer := cfg.Answer.WithMapGenerationDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 180)
	answer.MaxTokens = min(answer.MaxTokens, 16384)
	provider, err := NewOllamaAnswerProvider(answer)
	if err != nil {
		return err
	}
	if op.Kind == "graph-extract" {
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			if op.Input.AllChunks && op.RunID != "" {
				stored, e := s.LoadKnowledgeExtractionRun(op.RunID)
				if e != nil {
					return e
				}
				if stored.Status == KnowledgeExtractionRunCompleted {
					if err = progress("Продолжаем обработку документа", op.Current, op.Total, "", 0); err != nil {
						return err
					}
					op.RunID = ""
				}
			}
			var plan KnowledgeExtractionJobPlan
			if op.RunID != "" {
				plan, err = s.RebuildKnowledgeExtractionRunPlan(op.RunID, op.Input.Focus)
			} else {
				plan, err = s.BuildKnowledgeExtractionJobPlan(op.Input.Focus, KnowledgeCoverageOptions{Document: op.Input.Document, LowConfidence: cfg.Ingest.LowConfidence}, op.Input.ContextChars, op.Input.Batches)
			}
			if err != nil {
				return err
			}
			if len(plan.Batches) == 0 {
				return progress("Новых фрагментов для графа нет: документ обработан или уже покрыт графом", plan.ScopedChunks, plan.ScopedChunks, op.RunID, 0)
			}
			run, err := s.PrepareKnowledgeExtractionRun(plan, op.Input.Focus, op.Input.ContextChars, op.Input.Batches, answer, op.RunID)
			if err != nil {
				return err
			}
			current, total := 0, len(plan.Batches)
			if op.Input.AllChunks {
				current, total = plan.ScopedChunks-plan.EligibleChunks, plan.ScopedChunks
			}
			if err = progress("Извлечение тем", current, total, run.ID, 0); err != nil {
				return err
			}
			for i, batch := range plan.Batches {
				if err = ctx.Err(); err != nil {
					return err
				}
				if run.Batches[i].Status != KnowledgeExtractionBatchCompleted && run.Batches[i].Status != KnowledgeExtractionBatchInsufficient {
					result, rejected, e := ExtractKnowledgeExtractionBatch(ctx, provider, answer, batch, i+1, len(plan.Batches))
					if e != nil {
						_ = s.SaveKnowledgeExtractionBatchFailure(run.ID, batch.BatchID, e)
						if rejected {
							return &webGraphExtractionFailure{Batch: i + 1, Cause: e}
						}
						return e
					}
					if result.Insufficient {
						e = s.SaveKnowledgeExtractionBatchInsufficient(run.ID, batch.BatchID, result.Reason)
					} else {
						e = s.SaveKnowledgeExtractionBatchGraph(run.ID, batch.BatchID, result.Graph)
					}
					if e != nil {
						return e
					}
				}
				current = i + 1
				if op.Input.AllChunks {
					current = plan.ScopedChunks - plan.EligibleChunks
					for _, completed := range plan.Batches[:i+1] {
						current += len(completed.Entries)
					}
				}
				if err = progress("Извлечение тем", current, total, run.ID, 0); err != nil {
					return err
				}
			}
			if _, err = s.FinalizeKnowledgeExtractionRun(run.ID, plan); err != nil {
				return err
			}
			if err = progress(fmt.Sprintf("Сохранено. Осталось фрагментов: %d", plan.RemainingChunks), current, total, run.ID, 0); err != nil {
				return err
			}
			if !op.Input.AllChunks || plan.RemainingChunks == 0 {
				return nil
			}
			// Finalization records processed citations atomically. Clear the durable
			// run checkpoint only after that commit, so restart cannot lose a batch.
			if err = progress("Переходим к следующим фрагментам документа", current, total, "", 0); err != nil {
				return err
			}
			op.RunID = ""
		}
	}
	plan, err := s.BuildCorpusAnalysisPlan(op.Input.Focus, op.Input.ContextChars, op.Input.Batches)
	if err != nil {
		return err
	}
	if len(plan.Batches) == 0 {
		return progress("Недостаточно подтверждённых утверждений из разных документов", 0, 0, "", 0)
	}
	run, err := s.PrepareCorpusAnalysisRun(op.Input.Focus, op.Input.ContextChars, op.Input.Batches, plan, answer, op.RunID)
	if err != nil {
		return err
	}
	if err = progress("Анализ", 0, len(plan.Batches), run.ID, 0); err != nil {
		return err
	}
	graphs := []KnowledgeGraph{}
	for i, prompt := range plan.Batches {
		if err = ctx.Err(); err != nil {
			return err
		}
		stored := run.Batches[i]
		if stored.Status == CorpusAnalysisBatchCompleted {
			graphs = append(graphs, stored.Graph)
		} else if stored.Status != CorpusAnalysisBatchInsufficient {
			requestCtx, cancel := AnswerContext(ctx, answer)
			raw, e := provider.Generate(requestCtx, AnswerRequest{Model: answer.Model, System: prompt.System, Prompt: prompt.User, MaxTokens: answer.MaxTokens, Temperature: answer.Temperature})
			cancel()
			if e != nil {
				_ = s.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, e)
				return e
			}
			result, e := DecodeCorpusAnalysis(raw, prompt.Claims)
			if e != nil {
				_ = s.SaveCorpusAnalysisBatchFailure(run.ID, prompt.BatchID, e)
				return e
			}
			if result.Insufficient {
				e = s.SaveCorpusAnalysisBatchInsufficient(run.ID, prompt.BatchID, result.Reason)
			} else {
				e = s.SaveCorpusAnalysisBatchGraph(run.ID, prompt.BatchID, result.Graph)
				graphs = append(graphs, result.Graph)
			}
			if e != nil {
				return e
			}
		}
		if err = progress("Анализ", i+1, len(plan.Batches), run.ID, 0); err != nil {
			return err
		}
	}
	merged, err := MergeCorpusAnalysisGraphs(graphs...)
	if err != nil {
		return err
	}
	if len(merged.Nodes) > 0 {
		if err = s.UpsertCorpusAnalysisGraph(merged); err != nil {
			return err
		}
	}
	return s.CompleteCorpusAnalysisRun(run.ID)
}
