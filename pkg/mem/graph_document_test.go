package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type documentTestProvider func(context.Context, AnswerRequest) (string, error)

func (p documentTestProvider) Generate(ctx context.Context, r AnswerRequest) (string, error) {
	return p(ctx, r)
}
func documentRequestEvidence(t *testing.T, r AnswerRequest) GroundedEvidence {
	t.Helper()
	text := strings.Split(strings.Split(r.Prompt, "EVIDENCE_JSON_BEGIN\n")[1], "\nEVIDENCE_JSON_END")[0]
	var evidence []GroundedEvidence
	if err := json.Unmarshal([]byte(text), &evidence); err != nil || len(evidence) != 1 {
		t.Fatalf("evidence: %v %s", err, text)
	}
	return evidence[0]
}
func documentTestReply(e GroundedEvidence) string {
	return fmt.Sprintf(`{"nodes":[{"ref":"n","kind":"topic","label":"Topic %s","confidence":0.9,"citations":[%q]}],"edges":[]}`, strings.TrimPrefix(e.EvidenceHash, "sha256:")[:8], e.CitationID)
}

func TestDocumentGraphDefersInvalidQuoteAndRetriesOnlyUnfinished(t *testing.T) {
	s, entries := newKnowledgeExtractionJobStore(t, []string{"Pressure must remain below 5 bar.", "Other valid evidence."})
	defer s.Close()
	calls := 0
	cfg := DefaultLocalConfig()
	broken := true
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		e := documentRequestEvidence(t, r)
		if broken && strings.HasPrefix(e.Text, "Pressure") {
			return fmt.Sprintf(`{"nodes":[{"ref":"n","kind":"constraint","label":"Wrong","confidence":0.9,"citations":[%q],"requirement":{"statement":{"citation_id":%q,"text":"Pressure must remain below 9 bar."},"conditions":[],"exceptions":[],"limits":[]}}],"edges":[]}`, e.CitationID, e.CitationID), nil
		}
		return documentTestReply(e), nil
	})
	run := func() {
		t.Helper()
		if err := s.RunKnowledgeDocumentGraph(t.Context(), cfg, provider, entries[0].DocumentID, "all", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if calls != 3 {
		t.Fatal("unbounded retries", calls)
	}
	report, err := s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || report.Summary.SemanticPendingChunks != 1 || report.Summary.CoveredChunks != 2 {
		t.Fatal(report.Summary, err)
	}
	graph, _ := s.LoadKnowledgeGraph()
	for _, n := range graph.Nodes {
		if strings.Contains(n.Body, "9 bar") {
			t.Fatal("fabricated quote accepted")
		}
		for _, a := range n.Evidence {
			if s.ResolveEvidenceAnchor(a).State != EvidenceCurrent {
				t.Fatal("invalid anchor")
			}
		}
	}
	broken = false
	if _, err = s.db.Exec(`UPDATE knowledge_nodes SET label='User source label' WHERE origin='source' AND kind='document'`); err != nil {
		t.Fatal(err)
	}
	run()
	if calls != 4 {
		t.Fatal("successful fragment repeated", calls)
	}
	graph, _ = s.LoadKnowledgeGraph()
	for _, n := range graph.Nodes {
		if n.Origin == KnowledgeOriginSource && n.Kind == KnowledgeNodeDocument && n.Label != "User source label" {
			t.Fatal("source edit overwritten")
		}
	}
	report, err = s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || report.Summary.SemanticPendingChunks != 0 || report.Summary.SemanticCompletedChunks != 2 {
		t.Fatal(report.Summary, err)
	}
	run()
	if calls != 4 {
		t.Fatal("completed work repeated")
	}
}

func TestDocumentGraphHugeChunkTokenSplittingAndCheckpoint(t *testing.T) {
	text := strings.Repeat("Давление 5 бар. ", 6000) + "Конец документа."
	s, entries := newKnowledgeExtractionJobStore(t, []string{text})
	defer s.Close()
	calls := 0
	stop := errors.New("power loss after published window")
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		e := documentRequestEvidence(t, r)
		if len([]rune(e.Text)) > 3300 {
			return "", &AnswerTokenLimitError{MaxTokens: r.MaxTokens}
		}
		return documentTestReply(e), nil
	})
	cfg := DefaultLocalConfig()
	if err := s.RunKnowledgeDocumentGraph(t.Context(), cfg, provider, entries[0].DocumentID, "all", "", func(stage string, _, _ int) error {
		if stage == "Извлечение тем" {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	graph, err := s.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	sourceText := ""
	sourcePieces := map[string]bool{}
	for _, n := range graph.Nodes {
		if n.Origin == KnowledgeOriginSource && n.Kind == KnowledgeNodeSection {
			sourceText += n.Body
			sourcePieces[n.ID] = true
		}
	}
	if len([]rune(sourceText)) != len([]rune(text)) || len(sourcePieces) < 5 {
		t.Fatal("source text lost", len(sourceText), len(text))
	}
	before := calls
	if err = s.RunKnowledgeDocumentGraph(t.Context(), cfg, provider, entries[0].DocumentID, "all", "", nil); err != nil || calls != before {
		t.Fatal("checkpoint not reused", calls, before, err)
	}
	report, err := s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || report.Summary.SemanticPendingChunks != 0 {
		t.Fatal(report.Summary, err)
	}
}

func TestDocumentGraphProviderOutageDoesNotBlockDocument(t *testing.T) {
	texts := make([]string, 129)
	for i := range texts {
		texts[i] = fmt.Sprintf("Документ, фрагмент %d.", i)
	}
	s, entries := newKnowledgeExtractionJobStore(t, texts)
	defer s.Close()
	calls := 0
	provider := documentTestProvider(func(context.Context, AnswerRequest) (string, error) {
		calls++
		return "", errors.New("HTTP 503 private service detail")
	})
	var last string
	if err := s.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, entries[0].DocumentID, "all", "", func(stage string, _, _ int) error { last = stage; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(last, "129 из 129") {
		t.Fatal(calls, last)
	}
	report, err := s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || report.Summary.CoveredChunks != 129 || report.Summary.SemanticPendingChunks != 129 {
		t.Fatal(report.Summary, err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "private service detail") {
		t.Fatal("remote error leaked")
	}
}

func TestDocumentGraphTimeoutAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s, entries := newKnowledgeExtractionJobStore(t, []string{"some evidence"})
			defer s.Close()
			_, _ = s.db.Exec(documentGraphSchema)
			r := documentEnricher{store: s, focus: "all", answer: AnswerConfig{Model: "test", TimeoutSeconds: 1}, provider: documentTestProvider(func(ctx context.Context, _ AnswerRequest) (string, error) { <-ctx.Done(); return "", ctx.Err() })}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			}
			start := time.Now()
			calls := 0
			issue, err := r.window(ctx, entries[0], entries[0].Text, &calls)
			if err != nil || issue == "" || time.Since(start) > 3*time.Second {
				t.Fatal(issue, err, time.Since(start))
			}
			if cancelled && calls != 0 {
				t.Fatal("called after cancellation")
			}
		})
	}
}

func TestDocumentGraphHeartbeatAndProgressFailure(t *testing.T) {
	s, _ := newKnowledgeExtractionJobStore(t, []string{"some evidence"})
	defer s.Close()
	stopped := errors.New("progress database unavailable")
	updates := 0
	r := documentEnricher{store: s, answer: AnswerConfig{TimeoutSeconds: 5}, notify: func(stage string) error { updates++; return stopped }, provider: documentTestProvider(func(ctx context.Context, _ AnswerRequest) (string, error) { <-ctx.Done(); return "", ctx.Err() })}
	_, err := r.generate(t.Context(), AnswerRequest{})
	if !errors.Is(err, stopped) || updates != 1 {
		t.Fatal(err, updates)
	}
}

// Opt-in replay on a SQLite backup only. It deliberately simulates persistent
// model failure; completion proves resilience and provenance, not model quality.
func TestDocumentGraphRealCorpusOffline(t *testing.T) {
	root := os.Getenv("MEM_DOCUMENT_REPRO")
	if root == "" {
		t.Skip("requires a disposable corpus backup")
	}
	s, err := NewStore(filepath.Join(root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg, err := LoadConfigIn(filepath.Join(root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListKnowledgeExtractionRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range runs {
		run, err := s.LoadKnowledgeExtractionRun(summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Scope.Document == "" {
			continue
		}
		calls := 0
		provider := documentTestProvider(func(context.Context, AnswerRequest) (string, error) {
			calls++
			return "", &AnswerTokenLimitError{MaxTokens: 8192}
		})
		if err = s.RunKnowledgeDocumentGraph(t.Context(), cfg, provider, run.Scope.Document, run.Focus, run.ID, nil); err != nil {
			t.Fatal(err)
		}
		report, err := s.BuildKnowledgeCoverageReport(run.Scope)
		if err != nil {
			t.Fatal(err)
		}
		graph, err := s.LoadKnowledgeGraph()
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range graph.Nodes {
			for _, a := range n.Evidence {
				if s.ResolveEvidenceAnchor(a).State != EvidenceCurrent {
					t.Fatal("stale published evidence")
				}
			}
		}
		data, _ := json.MarshalIndent(map[string]any{"report": report, "simulated_model_calls": calls, "nodes": len(graph.Nodes), "edges": len(graph.Edges)}, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "document-result.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if report.Summary.ChunksWithText != 129 || report.Summary.CoveredChunks != 129 {
			t.Fatal(report.Summary)
		}
		t.Logf("all chunks=%d covered=%d semantic completed=%d pending=%d nodes=%d edges=%d", report.Summary.ChunksWithText, report.Summary.CoveredChunks, report.Summary.SemanticCompletedChunks, report.Summary.SemanticPendingChunks, len(graph.Nodes), len(graph.Edges))
		return
	}
	t.Fatal("no previous document extraction run")
}

// A bounded live sample on the same disposable copy, never the user's store.
func TestDocumentGraphRealModelSample(t *testing.T) {
	root := os.Getenv("MEM_DOCUMENT_LIVE_SAMPLE")
	if root == "" {
		t.Skip("requires explicit disposable corpus path")
	}
	s, err := NewStore(filepath.Join(root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg, err := LoadConfigIn(filepath.Join(root, ".mem"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := NewOllamaAnswerProvider(cfg.Answer.WithMapGenerationDefaults())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.knowledgeCoverageEntries(KnowledgeCoverageOptions{})
	if err != nil || len(entries) != 129 {
		t.Fatal(len(entries), err)
	}
	var mu sync.Mutex
	calls := 0
	records := []map[string]any{}
	provider := documentTestProvider(func(ctx context.Context, r AnswerRequest) (string, error) {
		mu.Lock()
		if calls >= 3 {
			mu.Unlock()
			return "", errors.New("bounded live sample complete")
		}
		calls++
		mu.Unlock()
		start := time.Now()
		raw, e := actual.Generate(ctx, r)
		record := map[string]any{"evidence": documentRequestEvidence(t, r), "raw": raw, "elapsed_seconds": time.Since(start).Seconds()}
		if e != nil {
			record["error"] = e.Error()
		}
		mu.Lock()
		records = append(records, record)
		mu.Unlock()
		return raw, e
	})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Second)
	defer cancel()
	err = s.RunKnowledgeDocumentGraph(ctx, cfg, provider, entries[0].DocumentID, "Полный разбор документа: темы, определения, требования, условия, исключения и связи", "", func(stage string, current, total int) error {
		if strings.HasPrefix(stage, "Смысловой") {
			t.Logf("%d/%d %s", current, total, stage)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: entries[0].DocumentID})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	data, _ := json.MarshalIndent(map[string]any{"report": report, "live_requests": records, "model": cfg.Answer.Model, "limit": 3}, "", "  ")
	count := calls
	mu.Unlock()
	if err = os.WriteFile(filepath.Join(root, "live-sample-result.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if count == 0 || count > 3 || report.Summary.CoveredChunks != 129 {
		t.Fatal(count, report.Summary)
	}
	t.Logf("live calls=%d semantic completed=%d pending=%d covered=%d", count, report.Summary.SemanticCompletedChunks, report.Summary.SemanticPendingChunks, report.Summary.CoveredChunks)
}
