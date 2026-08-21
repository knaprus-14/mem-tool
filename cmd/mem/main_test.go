package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestHandleAddFileStoresExactlyEmbeddedText(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := strings.Repeat("Русский текст. ", 45)
	path := filepath.Join(root, "single.txt")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	var embeddedText string
	getEmbedding = func(_ *Config, text string) ([]float32, error) { embeddedText = text; return []float32{1, 2, 3}, nil }
	if err := handleAddFile(testCLIConfig(1500, "paragraph"), store, []string{path}); err != nil {
		t.Fatal(err)
	}
	source, _ := mem.CanonicalSourcePath(path)
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 {
		t.Fatalf("got %d stored entries, want 1", len(entries))
	}
	want := strings.TrimSpace(input)
	if entries[0].Text != want || embeddedText != want || entries[0].Text != embeddedText {
		t.Fatalf("stored and embedded text differ: stored=%d runes embedded=%d runes want=%d runes", len([]rune(entries[0].Text)), len([]rune(embeddedText)), len([]rune(want)))
	}
}

func TestHandleAddFileRoutesPDFToProvenanceImport(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	originalImportDocument := importDocument
	defer func() { importDocument = originalImportDocument }()
	called := false
	importDocument = func(_ context.Context, _ *mem.Config, gotStore *mem.Store, path string, options mem.ImportOptions) (mem.ImportResult, error) {
		called = true
		if gotStore != store || path != "manual.PDF" || len(options.Tags) != 1 || options.Tags[0] != "radio" {
			t.Fatalf("unexpected routed import: store=%p path=%q options=%#v", gotStore, path, options)
		}
		return mem.ImportResult{SourcePath: path, DocumentID: "doc", DocumentRevision: "sha256:test", Chunks: 2}, nil
	}
	stdout, _, err := captureCLIStreams(func() error {
		return handleAddFile(testCLIConfig(1000, "paragraph"), store, []string{"manual.PDF", "-tags", "radio"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(stdout, "переключаю на mem import") {
		t.Fatalf("PDF was not visibly routed to provenance import: called=%v output=%q", called, stdout)
	}
}

func TestHandleAddFileRejectsBinaryInsteadOfEmbeddingBytes(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "payload.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	calls := 0
	getEmbedding = func(*Config, string) ([]float32, error) {
		calls++
		return []float32{1}, nil
	}
	err = handleAddFile(testCLIConfig(1000, "paragraph"), store, []string{path})
	if err == nil || !strings.Contains(err.Error(), "не является UTF-8 текстом") {
		t.Fatalf("binary file error = %v", err)
	}
	if calls != 0 || store.Stats()["total_entries"] != 0 {
		t.Fatalf("binary content reached embedding/storage: calls=%d stats=%#v", calls, store.Stats())
	}
}

func TestShouldReportImportProgressBoundsLargeOutput(t *testing.T) {
	reports := 0
	for i := 1; i <= 12327; i++ {
		if shouldReportImportProgress(i, 12327) {
			reports++
		}
	}
	if reports < 90 || reports > 120 {
		t.Fatalf("large import reported %d progress lines, want roughly 100", reports)
	}
	if !shouldReportImportProgress(1, 12327) || !shouldReportImportProgress(12327, 12327) {
		t.Fatal("large import omitted first or final progress")
	}
}

type fakeAnswerProvider struct {
	answer  string
	calls   int
	request mem.AnswerRequest
}

type corpusBatchAnswerProvider struct {
	calls     int
	failAt    int
	maxTokens []int
}

func (p *corpusBatchAnswerProvider) Generate(_ context.Context, request mem.AnswerRequest) (string, error) {
	p.calls++
	p.maxTokens = append(p.maxTokens, request.MaxTokens)
	if p.failAt > 0 && p.calls == p.failAt {
		return "", errors.New("planned batch failure")
	}
	const begin = "CLAIMS_JSON_BEGIN\n"
	const end = "\nCLAIMS_JSON_END"
	start := strings.Index(request.Prompt, begin)
	finish := strings.Index(request.Prompt, end)
	if start < 0 || finish < 0 || finish <= start+len(begin) {
		return "", errors.New("batch prompt has no claims JSON")
	}
	var claims []struct {
		Ref      string                 `json:"ref"`
		Evidence []mem.GroundedEvidence `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(request.Prompt[start+len(begin):finish]), &claims); err != nil {
		return "", err
	}
	if len(claims) < 2 || len(claims[0].Evidence) == 0 || len(claims[1].Evidence) == 0 {
		return "", errors.New("batch prompt has insufficient claims")
	}
	response := map[string]any{"findings": []map[string]any{{
		"kind": "gap", "label": fmt.Sprintf("Batch gap %d", p.calls), "confidence": 0.8,
		"claim_refs": []string{claims[0].Ref, claims[1].Ref},
		"citations":  []string{claims[0].Evidence[0].CitationID, claims[1].Evidence[0].CitationID},
	}}}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func (p *fakeAnswerProvider) Generate(_ context.Context, request mem.AnswerRequest) (string, error) {
	p.calls++
	p.request = request
	return p.answer, nil
}

func TestHandleAskPrintsHumanReadableSourcesAndKeepsStatusOnStderr(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Русский факт о запуске"
	revision := mem.ChunkContentHash("document revision fixture")
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(testCLIConfig(1500, "paragraph"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.AddDocumentChunkWithEmbeddingIdentity(text, "Book", nil, embeddingIdentity, []float32{1, 0},
		"section", 0, 1, false, mem.Provenance{
			DocumentID: "doc-ask", DocumentRevision: revision, ChunkHash: mem.ChunkContentHash(text),
			SourcePath: "C:/docs/book.pdf", MediaType: "application/pdf", Page: 3, BlockIndex: 0, BlockMarker: "<!-- page: 3 -->",
			BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
	if err != nil {
		t.Fatal(err)
	}
	citationID, _ := mem.CitationForEntry(*entry)
	fake := &fakeAnswerProvider{answer: `{"claims":[{"text":"Ответ подтверждён","citations":["` + citationID + `"]}]}`}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	cfg.Answer.ContextChars = 5000
	originalEmbedding := getEmbeddingContext
	originalProvider := newAnswerProvider
	defer func() { getEmbeddingContext = originalEmbedding; newAnswerProvider = originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleAsk(cfg, store, []string{"где", "запуск"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Ответ подтверждён [1, стр. 3]") || !strings.Contains(stdout, "[1] book.pdf") || !strings.Contains(stdout, "Файл: C:/docs/book.pdf") || !strings.Contains(stdout, "Место: страница 3 · блок 1 · фрагмент 1/1") || strings.Contains(stdout, "<!-- page:") || strings.Contains(stdout, citationID) || strings.Contains(stdout, revision) || strings.Contains(stdout, "[ASK]") {
		t.Fatalf("stdout does not contain human-readable citations: %q", stdout)
	}
	if strings.Contains(stdout, "evidence=sha256:") || !strings.Contains(stderr, "[ASK] retrieval") || !strings.Contains(stderr, "[ASK] evidence") || fake.calls != 1 {
		t.Fatalf("status/provider/version contract failed: stdout=%q stderr=%q calls=%d", stdout, stderr, fake.calls)
	}
}

func TestHandleConfigRejectsRemoteAnswerURLBeforeSave(t *testing.T) {
	originalLoadConfig, originalSaveConfig := loadConfig, saveConfig
	defer func() { loadConfig, saveConfig = originalLoadConfig, originalSaveConfig }()
	cfg := mem.DefaultLocalConfig()
	saved := false
	loadConfig = func() (*Config, error) { return cfg, nil }
	saveConfig = func(*Config) error {
		saved = true
		return nil
	}
	if err := handleConfig([]string{"set-answer-base-url", "https://example.com"}); err == nil {
		t.Fatal("remote answer URL was accepted")
	}
	if saved {
		t.Fatal("remote answer URL was persisted")
	}
	if err := handleConfig([]string{"set-answer-base-url", "http://127.0.0.1:11434/"}); err != nil {
		t.Fatalf("loopback answer URL was rejected: %v", err)
	}
	if !saved || cfg.Answer.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("loopback URL was not normalized/persisted: saved=%v cfg=%#v", saved, cfg.Answer)
	}
}

func TestHandleAskReturnsInsufficientEvidenceWithoutGeneration(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fake := &fakeAnswerProvider{answer: "should not be called"}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalEmbedding := getEmbeddingContext
	originalProvider := newAnswerProvider
	defer func() { getEmbeddingContext = originalEmbedding; newAnswerProvider = originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, _, err := captureCLIStreams(func() error {
		return handleAsk(cfg, store, []string{"вопрос"})
	})
	if err != nil || !strings.Contains(stdout, "Недостаточно") || fake.calls != 0 {
		t.Fatalf("insufficient evidence was not honest: stdout=%q err=%v calls=%d", stdout, err, fake.calls)
	}
}

func TestHandleMapBuildPersistsStrictAnchoredGraphAndExportsJSON(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Импорт сохраняет provenance каждого блока документа."
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(testCLIConfig(1500, "paragraph"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.AddDocumentChunkWithEmbeddingIdentity(text, "Architecture", nil, embeddingIdentity, []float32{1, 0},
		"import", 0, 1, false, mem.Provenance{
			DocumentID: "doc-map", DocumentRevision: mem.ChunkContentHash("map revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: "C:/docs/architecture.md",
			MediaType: "text/markdown", Page: 2, BlockIndex: 0, BlockChunkIndex: 0,
			BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
	if err != nil {
		t.Fatal(err)
	}
	citation, _ := mem.CitationForEntry(*entry)
	fake := &fakeAnswerProvider{answer: `{"nodes":[{"ref":"n1","kind":"claim","label":"Import keeps provenance","confidence":0.93,"citations":["` + citation + `"]}]}`}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	cfg.Answer.ContextChars = 5000
	originalEmbedding, originalProvider := getEmbeddingContext, newAnswerProvider
	defer func() { getEmbeddingContext, newAnswerProvider = originalEmbedding, originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }

	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"build", "архитектура", "импорта"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "nodes=1 edges=0 evidence=1") || !strings.Contains(stderr, "[MAP] retrieval") ||
		!strings.Contains(stderr, "[MAP] output budget: 4096 tokens") || !strings.Contains(stderr, "[MAP] evidence") {
		t.Fatalf("map streams are not scriptable: stdout=%q stderr=%q", stdout, stderr)
	}
	if fake.calls != 1 || fake.request.MaxTokens != mem.DefaultMapGenerationTokens ||
		!strings.Contains(fake.request.System, "typed knowledge graph") || !strings.Contains(fake.request.Prompt, citation) {
		t.Fatalf("map provider contract failed: calls=%d request=%#v", fake.calls, fake.request)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 || graph.Nodes[0].Status != mem.KnowledgeStatusDraft || graph.Nodes[0].Evidence[0].CitationID != citation || !strings.HasPrefix(graph.Nodes[0].ID, "kn-") {
		t.Fatalf("map build did not persist a host-anchored graph: %#v", graph)
	}

	exported, exportStatus, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"export"})
	})
	if err != nil || exportStatus != "" {
		t.Fatalf("map export failed: err=%v stderr=%q", err, exportStatus)
	}
	var decoded mem.KnowledgeGraph
	if err := json.Unmarshal([]byte(exported), &decoded); err != nil || len(decoded.Nodes) != 1 || decoded.Nodes[0].ID != graph.Nodes[0].ID {
		t.Fatalf("map export is not valid graph JSON: err=%v output=%q", err, exported)
	}
}

func TestHandleMapRejectsUnknownCitationWithoutPartialWrite(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Versioned evidence"
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(testCLIConfig(1500, "paragraph"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddDocumentChunkWithEmbeddingIdentity(text, "Doc", nil, embeddingIdentity, []float32{1, 0}, "section", 0, 1, false, mem.Provenance{
		DocumentID: "doc-reject", DocumentRevision: mem.ChunkContentHash("reject revision"), ChunkHash: mem.ChunkContentHash(text),
		SourcePath: "C:/docs/reject.md", MediaType: "text/markdown", Page: 1, BlockIndex: 0,
		BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnswerProvider{answer: `{"nodes":[{"ref":"n1","kind":"claim","label":"x","confidence":0.5,"citations":["cite-invented"]}]}`}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	cfg.Answer.ContextChars = 5000
	originalEmbedding, originalProvider := getEmbeddingContext, newAnswerProvider
	defer func() { getEmbeddingContext, newAnswerProvider = originalEmbedding, originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }

	_, _, err = captureCLIStreams(func() error { return handleMap(cfg, store, []string{"build", "test"}) })
	if err == nil || !strings.Contains(err.Error(), "unknown citation") {
		t.Fatalf("unknown citation was not rejected: %v", err)
	}
	graph, loadErr := store.LoadKnowledgeGraph()
	if loadErr != nil || len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("rejected extraction partially changed graph: graph=%#v err=%v", graph, loadErr)
	}
}

func TestHandleMapSkipsGenerationForUnversionedSearchResults(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(testCLIConfig(1500, "paragraph"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddWithEmbeddingIdentity("manual note", "", nil, embeddingIdentity, []float32{1, 0}, false); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnswerProvider{answer: "should not be called"}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalEmbedding, originalProvider := getEmbeddingContext, newAnswerProvider
	defer func() { getEmbeddingContext, newAnswerProvider = originalEmbedding, originalProvider }()
	getEmbeddingContext = func(context.Context, *Config, string) ([]float32, error) { return []float32{1, 0}, nil }
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, _, err := captureCLIStreams(func() error { return handleMap(cfg, store, []string{"build", "note"}) })
	if err != nil || !strings.Contains(stdout, "Недостаточно") || fake.calls != 0 {
		t.Fatalf("unversioned evidence reached generation: stdout=%q err=%v calls=%d", stdout, err, fake.calls)
	}
}

func TestHandleMapStatusAndApproveExposeReviewWorkflow(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Evidence awaiting human review"
	entry, err := store.AddDocumentChunk(text, "Review", nil, "test", []float32{1, 0}, "review", 0, 1, false, mem.Provenance{
		DocumentID: "doc-review", DocumentRevision: mem.ChunkContentHash("review revision"), ChunkHash: mem.ChunkContentHash(text),
		SourcePath: "C:/docs/review.md", MediaType: "text/markdown", Page: 1, BlockIndex: 0,
		BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := mem.EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "review-cli", Kind: mem.KnowledgeNodeClaim, Label: "Review CLI",
		Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}

	statusJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"status", "--json"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("JSON status failed: err=%v stderr=%q", err, stderr)
	}
	var report mem.KnowledgeReviewReport
	if err := json.Unmarshal([]byte(statusJSON), &report); err != nil || report.Summary.Ready != 1 || len(report.Items) != 1 || report.Items[0].EvidenceState != mem.EvidenceCurrent {
		t.Fatalf("status JSON lost review state: report=%#v err=%v output=%q", report, err, statusJSON)
	}
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"approve", "node", "review-cli", "--reviewer", "Руслан", "--comment", "Проверено по источнику"})
	})
	if err != nil || stderr != "" || !strings.Contains(stdout, "draft->active") {
		t.Fatalf("CLI approval failed: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || graph.Nodes[0].Status != mem.KnowledgeStatusActive {
		t.Fatalf("CLI approval was not persisted: graph=%#v err=%v", graph, err)
	}
	reviews, err := store.ListKnowledgeReviews(10)
	if err != nil || len(reviews) != 1 || reviews[0].Reviewer != "Руслан" || reviews[0].Comment != "Проверено по источнику" {
		t.Fatalf("CLI approval audit is incomplete: reviews=%#v err=%v", reviews, err)
	}
	human, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"status"})
	})
	if err != nil || !strings.Contains(human, "active=1") || !strings.Contains(human, "evidence=current") {
		t.Fatalf("human status is incomplete: output=%q err=%v", human, err)
	}
	if !cmdRequiresDB["map"] {
		t.Fatal("map command is not routed through the project database")
	}
}

func TestHandleMapCoverageCLIReportsScopedPhysicalPages(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const text = "Coverage CLI evidence"
	const source = "C:/docs/coverage-cli.pdf"
	if _, err := store.AddDocumentChunk(text, "Coverage CLI", []string{"manual"}, "test", []float32{1, 0},
		"page 7", 0, 1, false, mem.Provenance{
			DocumentID: "doc-coverage-cli", DocumentRevision: mem.ChunkContentHash("coverage cli revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
			Page: 7, BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1,
			ExtractionMethod: "text", OCRConfidence: -1,
		}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"coverage", "--document", source, "--pages", "7", "--tag", "manual", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var report mem.KnowledgeCoverageReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode coverage JSON: %v\n%s", err, stdout)
	}
	if report.Summary.Documents != 1 || report.Summary.ChunksWithText != 1 || report.Scope.PageFrom != 7 || report.Scope.PageTo != 7 {
		t.Fatalf("unexpected coverage JSON: %#v", report)
	}

	human, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"coverage", "--pages", "7-7"})
	})
	if err != nil || !strings.Contains(human, "Покрытие карты знаний") || !strings.Contains(human, "покрыто 0 из 1") ||
		!strings.Contains(human, "Непокрытые страницы: 7") || strings.Contains(human, report.SnapshotDigest) {
		t.Fatalf("unexpected human coverage output=%q err=%v", human, err)
	}
	if _, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"coverage", "--pages", "9-2"})
	}); err == nil {
		t.Fatal("invalid coverage range was accepted")
	}
}

func TestHandleMapExtractRunsControlledBatchesAndUpdatesProcessingCoverage(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const source = "C:/docs/extract-cli.pdf"
	texts := []string{"First extraction evidence", "Second extraction evidence"}
	for i, text := range texts {
		if _, err := store.AddDocumentChunk(text, "Extract CLI", []string{"manual"}, "test", []float32{1, 0},
			fmt.Sprintf("page %d", i+1), i, len(texts), false, mem.Provenance{
				DocumentID: "doc-extract-cli", DocumentRevision: mem.ChunkContentHash("extract cli revision"),
				ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
				Page: i + 1, BlockIndex: i, BlockChunkIndex: 0, BlockTotalChunks: 1,
				ExtractionMethod: "text", OCRConfidence: -1,
			}); err != nil {
			t.Fatal(err)
		}
	}
	focus := "полный разбор"
	plan, err := store.BuildKnowledgeExtractionJobPlan(focus, mem.KnowledgeCoverageOptions{Document: source}, mem.MaxAnswerContextChars, 1)
	if err != nil || len(plan.Batches) != 1 || len(plan.Batches[0].Prompt.Evidence) != 2 {
		t.Fatalf("unexpected CLI extraction fixture plan=%#v err=%v", plan, err)
	}
	citation := plan.Batches[0].Prompt.Evidence[0].CitationID
	fake := &fakeAnswerProvider{answer: `{"nodes":[{"ref":"n1","kind":"claim","label":"Extracted CLI claim","confidence":0.9,"citations":["` + citation + `"]}]}`}
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.BaseURL = "http://localhost:11434"
	cfg.Answer.Model = "test-chat"

	dryOut, _, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{
			"extract", focus, "--document", source, "-context-chars", strconv.Itoa(mem.MaxAnswerContextChars), "-batches", "1", "--dry-run",
		})
	})
	if err != nil || !strings.Contains(dryOut, "План извлечения") || fake.calls != 0 {
		t.Fatalf("dry extraction plan changed state: stdout=%q calls=%d err=%v", dryOut, fake.calls, err)
	}

	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{
			"extract", focus, "--document", source, "-context-chars", strconv.Itoa(mem.MaxAnswerContextChars), "-batches", "1",
		})
	})
	if err != nil || fake.calls != 1 || !strings.Contains(stdout, "processed=2") || !strings.Contains(stderr, "checkpoints") {
		t.Fatalf("extraction CLI failed: stdout=%q stderr=%q calls=%d err=%v", stdout, stderr, fake.calls, err)
	}
	report, err := store.BuildKnowledgeCoverageReport(mem.KnowledgeCoverageOptions{Document: source})
	if err != nil || report.Summary.ProcessedChunks != 2 || report.Summary.UnprocessedChunks != 0 || report.Summary.CoveredChunks != 1 {
		t.Fatalf("processing coverage was not updated: report=%#v err=%v", report.Summary, err)
	}
	runsJSON, _, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"extract-runs", "--json"})
	})
	if err != nil || !strings.Contains(runsJSON, `"status": "completed"`) {
		t.Fatalf("extraction run history is unavailable: output=%q err=%v", runsJSON, err)
	}
	secondOut, _, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{
			"extract", focus, "--document", source, "-context-chars", strconv.Itoa(mem.MaxAnswerContextChars), "-batches", "1",
		})
	})
	if err != nil || !strings.Contains(secondOut, "Задание не требуется") || fake.calls != 1 {
		t.Fatalf("processed chunks were generated again: output=%q calls=%d err=%v", secondOut, fake.calls, err)
	}
}

func TestHandleMapApproveBatchAndReviews(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Pinned batch evidence"
	entry, err := store.AddDocumentChunk(text, "Batch", nil, "test", []float32{1, 0}, "batch", 0, 1, false, mem.Provenance{
		DocumentID: "doc-batch", DocumentRevision: mem.ChunkContentHash("batch revision"), ChunkHash: mem.ChunkContentHash(text),
		SourcePath: "C:/docs/batch.md", MediaType: "text/markdown", Page: 1, BlockIndex: 0,
		BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := mem.EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "batch-cli", Kind: mem.KnowledgeNodeClaim, Label: "Batch CLI",
		Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReviewKnowledgeGraph()
	if err != nil || len(report.Items) != 1 {
		t.Fatalf("review report failed: report=%#v err=%v", report, err)
	}
	manifest := mem.KnowledgeApprovalManifest{
		Reviewer: "Руслан", Comment: "Пакетная проверка",
		Objects: []mem.KnowledgeApprovalTarget{{
			ObjectType: mem.KnowledgeObjectNode, ID: "batch-cli",
			ExpectedEvidenceDigest: report.Items[0].EvidenceDigest,
		}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "approval.json")
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"approve-batch", manifestPath})
	})
	if err != nil || stderr != "" || !strings.Contains(stdout, "objects=1") {
		t.Fatalf("batch CLI failed: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	historyJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"reviews", "--json", "-limit", "10"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("reviews CLI failed: output=%q stderr=%q err=%v", historyJSON, stderr, err)
	}
	var records []mem.KnowledgeReviewRecord
	if err := json.Unmarshal([]byte(historyJSON), &records); err != nil || len(records) != 1 || records[0].Reviewer != "Руслан" {
		t.Fatalf("reviews JSON is incomplete: records=%#v err=%v output=%q", records, err, historyJSON)
	}
	current, err := store.ReviewKnowledgeGraph()
	if err != nil || len(current.Items) != 1 {
		t.Fatalf("review report before edit failed: report=%#v err=%v", current, err)
	}
	if _, err := store.EditKnowledgeObject(mem.KnowledgeEditRequest{
		ObjectType: mem.KnowledgeObjectNode, ID: "batch-cli", Editor: "Руслан",
		Comment: "Уточнено вручную", Label: "Batch CLI уточнённый",
		ExpectedStatus: current.Items[0].Status, ExpectedContentDigest: current.Items[0].ContentDigest,
		ExpectedEvidenceDigest: current.Items[0].EvidenceDigest,
	}); err != nil {
		t.Fatal(err)
	}
	editsJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"edits", "--json", "-limit", "10"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("edits CLI failed: output=%q stderr=%q err=%v", editsJSON, stderr, err)
	}
	var edits []mem.KnowledgeEditRecord
	if err := json.Unmarshal([]byte(editsJSON), &edits); err != nil || len(edits) != 1 || edits[0].Editor != "Руслан" || edits[0].NewLabel != "Batch CLI уточнённый" {
		t.Fatalf("edits JSON is incomplete: edits=%#v err=%v output=%q", edits, err, editsJSON)
	}
}

func TestHandleMapAnalysisRunHistoryShowAndPrune(t *testing.T) {
	store := cliCorpusStoreWithClaims(t, 5)
	defer store.Close()
	budget := cliCorpusTwoClaimBudget(t, store, "pressure")
	plan, err := store.BuildCorpusAnalysisPlan("pressure", budget, 3)
	if err != nil {
		t.Fatal(err)
	}
	answer := mem.AnswerConfig{BaseURL: "http://127.0.0.1:11434", Model: "history-test", MaxTokens: 1000, Temperature: 0.1}
	completed, err := store.PrepareCorpusAnalysisRun("completed history", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range completed.Batches {
		if err := store.SaveCorpusAnalysisBatchInsufficient(completed.ID, batch.BatchID, "no finding"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CompleteCorpusAnalysisRun(completed.ID); err != nil {
		t.Fatal(err)
	}
	running, err := store.PrepareCorpusAnalysisRun("running history", budget, 3, plan, answer, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCorpusAnalysisBatchFailure(running.ID, running.Batches[0].BatchID, errors.New("retry later")); err != nil {
		t.Fatal(err)
	}

	historyJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"runs", "--json", "-limit", "10"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("run history failed: output=%q stderr=%q err=%v", historyJSON, stderr, err)
	}
	var history []mem.CorpusAnalysisRunSummary
	if err := json.Unmarshal([]byte(historyJSON), &history); err != nil || len(history) != 2 {
		t.Fatalf("run history JSON is incomplete: history=%#v err=%v output=%q", history, err, historyJSON)
	}
	var sawCompleted, sawRunning bool
	for _, run := range history {
		switch run.ID {
		case completed.ID:
			sawCompleted = run.Status == mem.CorpusAnalysisRunCompleted && run.InsufficientBatches == 3
		case running.ID:
			sawRunning = run.Status == mem.CorpusAnalysisRunRunning && run.FailedBatches == 1
		}
	}
	if !sawCompleted || !sawRunning {
		t.Fatalf("run history lost statuses: %#v", history)
	}

	detailJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"run", running.ID, "--json"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("run detail failed: output=%q stderr=%q err=%v", detailJSON, stderr, err)
	}
	var detail mem.CorpusAnalysisRun
	if err := json.Unmarshal([]byte(detailJSON), &detail); err != nil || detail.ID != running.ID || detail.Batches[0].Reason != "retry later" {
		t.Fatalf("run detail JSON is incomplete: run=%#v err=%v output=%q", detail, err, detailJSON)
	}

	previewJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"prune-runs", "-older-than", "1ns", "-keep", "0", "--json"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("prune preview failed: output=%q stderr=%q err=%v", previewJSON, stderr, err)
	}
	var preview mem.CorpusAnalysisRunPruneResult
	if err := json.Unmarshal([]byte(previewJSON), &preview); err != nil || !preview.DryRun || len(preview.Runs) != 1 || preview.Runs[0].ID != completed.ID {
		t.Fatalf("prune preview is unsafe or incomplete: result=%#v err=%v output=%q", preview, err, previewJSON)
	}
	if _, err := store.LoadCorpusAnalysisRun(completed.ID); err != nil {
		t.Fatalf("preview deleted completed run: %v", err)
	}

	deleted, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"prune-runs", "-older-than", "1ns", "-keep", "0", "--yes"})
	})
	if err != nil || !strings.Contains(deleted, "runs=1 batches=3") {
		t.Fatalf("confirmed prune failed: output=%q err=%v", deleted, err)
	}
	if _, err := store.LoadCorpusAnalysisRun(completed.ID); err == nil {
		t.Fatal("confirmed prune kept completed run")
	}
	if resumable, err := store.LoadCorpusAnalysisRun(running.ID); err != nil || resumable.Status != mem.CorpusAnalysisRunRunning {
		t.Fatalf("confirmed prune removed running run: run=%#v err=%v", resumable, err)
	}
}

func TestHandleMapDuplicateDetectionMergeAndHistory(t *testing.T) {
	store, anchor := cliGraphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{
		{ID: "cli-duplicate-target", Kind: mem.KnowledgeNodeClaim, Label: "Canonical pressure", Body: "Maximum pressure is 1.0 MPa.", Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginGenerated, Confidence: 0.9, Evidence: []mem.EvidenceAnchor{anchor}},
		{ID: "cli-duplicate-source", Kind: mem.KnowledgeNodeClaim, Label: "Duplicate pressure", Body: "Working pressure shall not exceed 1.0 MPa.", Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginGenerated, Confidence: 0.88, Evidence: []mem.EvidenceAnchor{anchor}},
	}}); err != nil {
		t.Fatal(err)
	}
	originalEmbedding := getEmbeddingContext
	defer func() { getEmbeddingContext = originalEmbedding }()
	getEmbeddingContext = func(_ context.Context, _ *Config, text string) ([]float32, error) {
		if strings.Contains(text, "Canonical") {
			return []float32{1, 0}, nil
		}
		return []float32{0.99, 0.01}, nil
	}
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"duplicates", "--json", "-kind", "claim", "-threshold", "0.95"})
	})
	if err != nil || !strings.Contains(stderr, "embedding node=2/2") {
		t.Fatalf("duplicate detection failed: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var report mem.KnowledgeDuplicateReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || len(report.Candidates) != 1 {
		t.Fatalf("duplicate report is incomplete: report=%#v err=%v output=%q", report, err, stdout)
	}
	candidate := report.Candidates[0]
	source, target := candidate.Left, candidate.Right
	if source.ID != candidate.SuggestedSource {
		source, target = candidate.Right, candidate.Left
	}
	manifest := mem.KnowledgeNodeMergeRequest{
		SourceID: source.ID, TargetID: target.ID, Reviewer: "Руслан", Comment: "Один норматив",
		ExpectedSourceNodeDigest: source.NodeDigest, ExpectedTargetNodeDigest: target.NodeDigest,
		ExpectedSourceEvidenceDigest: source.EvidenceDigest, ExpectedTargetEvidenceDigest: target.EvidenceDigest,
		Similarity: candidate.Similarity, EmbeddingSpace: candidate.EmbeddingSpace,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "merge.json")
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	merged, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"merge-node", manifestPath})
	})
	if err != nil || stderr != "" || !strings.Contains(merged, "source=cli-duplicate-source target=cli-duplicate-target") {
		t.Fatalf("duplicate merge failed: stdout=%q stderr=%q err=%v", merged, stderr, err)
	}
	historyJSON, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"merges", "--json"})
	})
	if err != nil || stderr != "" {
		t.Fatalf("merge history failed: output=%q stderr=%q err=%v", historyJSON, stderr, err)
	}
	var history []mem.KnowledgeNodeMergeRecord
	if err := json.Unmarshal([]byte(historyJSON), &history); err != nil || len(history) != 1 || !history[0].Current || history[0].Reviewer != "Руслан" {
		t.Fatalf("merge history is incomplete: history=%#v err=%v output=%q", history, err, historyJSON)
	}
}

func TestHandleMapExportHTMLIsSelfContainedAndRequiresForce(t *testing.T) {
	store, anchor := cliGraphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "html-map-node", Kind: mem.KnowledgeNodeContradiction, Label: "Conflicting limits",
		Status: mem.KnowledgeStatusDraft, Origin: mem.KnowledgeOriginGenerated,
		Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "knowledge-map.html")
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"export-html", outputPath, "--title", "Project map"})
	})
	if err != nil || stderr != "" || !strings.Contains(stdout, "nodes=1 edges=0") {
		t.Fatalf("HTML export failed: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`<title>Project map</title>`)) || !bytes.Contains(content, []byte(`data-mem-map="v1"`)) || !bytes.Contains(content, []byte(`html-map-node`)) {
		t.Fatalf("HTML export is incomplete: %q", content[:min(len(content), 500)])
	}
	if _, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"export-html", outputPath})
	}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("existing HTML was overwritten without --force: %v", err)
	}
	if _, _, err := captureCLIStreams(func() error {
		return handleMap(testCLIConfig(1500, "paragraph"), store, []string{"export-html", outputPath, "--force"})
	}); err != nil {
		t.Fatalf("forced HTML export failed: %v", err)
	}
}

func TestParseAnalysisRunRetention(t *testing.T) {
	duration, err := parseAnalysisRunRetention("30d")
	if err != nil || duration != 30*24*time.Hour {
		t.Fatalf("30d was not parsed: duration=%s err=%v", duration, err)
	}
	if _, err := parseAnalysisRunRetention("0d"); err == nil {
		t.Fatal("zero-day retention was accepted")
	}
	if _, err := parseAnalysisRunRetention("later"); err == nil {
		t.Fatal("invalid retention was accepted")
	}
}

func TestHandleMapAnalyzePersistsDraftCrossDocumentFinding(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	texts := []string{"Limit is 1.0 MPa.", "Limit is 1.2 MPa."}
	anchors := make([]mem.EvidenceAnchor, 0, 2)
	embeddingIdentity := mem.EmbeddingIdentity{Backend: "test", Model: "test-model", SpaceID: mem.ChunkContentHash("test-space")}
	for i, text := range texts {
		source := fmt.Sprintf("C:/docs/analyze-%d.md", i)
		entry, err := store.AddDocumentChunkWithEmbeddingIdentity(text, "Analyze", nil, embeddingIdentity, []float32{1, 0}, source, 0, 1, false, mem.Provenance{
			DocumentID: fmt.Sprintf("analyze-doc-%d", i), DocumentRevision: mem.ChunkContentHash(fmt.Sprintf("revision-%d", i)),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown", Page: 1,
			BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		anchor, err := mem.EvidenceAnchorForEntry(*entry, entry.Text)
		if err != nil {
			t.Fatal(err)
		}
		anchors = append(anchors, anchor)
	}
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{
		{ID: "analyze-claim-a", Kind: mem.KnowledgeNodeClaim, Label: "First limit", Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchors[0]}},
		{ID: "analyze-claim-b", Kind: mem.KnowledgeNodeClaim, Label: "Second limit", Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchors[1]}},
	}}); err != nil {
		t.Fatal(err)
	}
	prompt, err := store.BuildCorpusAnalysisPrompt("limit", 100000)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnswerProvider{answer: `{"findings":[{"kind":"contradiction","label":"Different limits",` +
		`"confidence":0.9,"claim_refs":["c1","c2"],"citations":["` + prompt.Claims[0].Evidence[0].CitationID + `","` + prompt.Claims[1].Evidence[0].CitationID + `"]}]}`}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"analyze", "limit", "-context-chars", "100000"})
	})
	if err != nil || !strings.Contains(stdout, "сохранён как draft") || !strings.Contains(stderr, "documents=2") || fake.calls != 1 {
		t.Fatalf("map analyze failed: stdout=%q stderr=%q calls=%d err=%v", stdout, stderr, fake.calls, err)
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 3 || len(graph.Edges) != 3 {
		t.Fatalf("map analyze graph is incomplete: graph=%#v err=%v", graph, err)
	}
	for _, node := range graph.Nodes {
		if node.Kind == mem.KnowledgeNodeContradiction && node.Status != mem.KnowledgeStatusDraft {
			t.Fatalf("analyzed finding was auto-approved: %#v", node)
		}
	}
}

func TestHandleMapAnalyzeSkipsModelWithoutTwoCurrentDocuments(t *testing.T) {
	store, anchor := cliGraphStoreAndAnchor(t)
	defer store.Close()
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: []mem.KnowledgeNode{{
		ID: "single-corpus-claim", Kind: mem.KnowledgeNodeClaim, Label: "Only claim",
		Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchor},
	}}}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnswerProvider{answer: "should not be called"}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }
	stdout, _, err := captureCLIStreams(func() error { return handleMap(cfg, store, []string{"analyze", "claim"}) })
	if err != nil || !strings.Contains(stdout, "Недостаточно") || fake.calls != 0 {
		t.Fatalf("insufficient corpus reached model: stdout=%q calls=%d err=%v", stdout, fake.calls, err)
	}
}

func TestHandleMapAnalyzeProcessesBatchesAndPersistsOnce(t *testing.T) {
	store := cliCorpusStoreWithClaims(t, 5)
	defer store.Close()
	budget := cliCorpusTwoClaimBudget(t, store, "pressure")
	provider := &corpusBatchAnswerProvider{}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return provider, nil }

	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"analyze", "pressure", "-context-chars", strconv.Itoa(budget), "-batches", "3"})
	})
	if err != nil || provider.calls != 3 || !strings.Contains(stdout, "batches=3") || !strings.Contains(stdout, "covered=5/5") ||
		!strings.Contains(stdout, "semantic=3 fallback=0") || !strings.Contains(stderr, "batch=3/3") ||
		!strings.Contains(stderr, "semantic vectors=5/5 guided_batches=3 fallback_batches=0") ||
		!strings.Contains(stderr, "[MAP ANALYZE] output budget: 4096 tokens") {
		t.Fatalf("batched analysis failed: stdout=%q stderr=%q calls=%d err=%v", stdout, stderr, provider.calls, err)
	}
	for i, maxTokens := range provider.maxTokens {
		if maxTokens != mem.DefaultMapGenerationTokens {
			t.Fatalf("batch %d max tokens = %d, want %d", i+1, maxTokens, mem.DefaultMapGenerationTokens)
		}
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil || len(graph.Nodes) != 8 || len(graph.Edges) != 6 {
		t.Fatalf("batched graph is incomplete: graph=%#v err=%v", graph, err)
	}
}

func TestHandleMapAnalyzeBatchFailureLeavesGraphUntouched(t *testing.T) {
	store := cliCorpusStoreWithClaims(t, 5)
	defer store.Close()
	budget := cliCorpusTwoClaimBudget(t, store, "pressure")
	provider := &corpusBatchAnswerProvider{failAt: 2}
	cfg := testCLIConfig(1500, "paragraph")
	cfg.Answer.Model = "fake-chat"
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return provider, nil }

	_, _, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"analyze", "pressure", "-context-chars", strconv.Itoa(budget), "-batches", "3"})
	})
	if err == nil || !strings.Contains(err.Error(), "planned batch failure") || provider.calls != 2 {
		t.Fatalf("batch failure was not propagated: calls=%d err=%v", provider.calls, err)
	}
	graph, loadErr := store.LoadKnowledgeGraph()
	if loadErr != nil || len(graph.Nodes) != 5 || len(graph.Edges) != 0 {
		t.Fatalf("failed batch analysis partially persisted: graph=%#v err=%v", graph, loadErr)
	}

	provider.failAt = 0
	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMap(cfg, store, []string{"analyze", "pressure", "-context-chars", strconv.Itoa(budget), "-batches", "3"})
	})
	if err != nil || provider.calls != 4 || !strings.Contains(stderr, "восстановлен проверенный результат") ||
		!strings.Contains(stdout, "сохранён как draft") {
		t.Fatalf("failed run did not resume: stdout=%q stderr=%q calls=%d err=%v", stdout, stderr, provider.calls, err)
	}
	graph, loadErr = store.LoadKnowledgeGraph()
	if loadErr != nil || len(graph.Nodes) != 8 || len(graph.Edges) != 6 {
		t.Fatalf("resumed analysis graph is incomplete: graph=%#v err=%v", graph, loadErr)
	}
}

func cliGraphStoreAndAnchor(t *testing.T) (*mem.Store, mem.EvidenceAnchor) {
	t.Helper()
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	text := "Single active claim evidence"
	source := "C:/docs/single-corpus.md"
	entry, err := store.AddDocumentChunk(text, "Single", nil, "test", []float32{1, 0}, source, 0, 1, false, mem.Provenance{
		DocumentID: "single-corpus-doc", DocumentRevision: mem.ChunkContentHash("single revision"),
		ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown", Page: 1,
		BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	anchor, err := mem.EvidenceAnchorForEntry(*entry, entry.Text)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, anchor
}

func cliCorpusStoreWithClaims(t *testing.T, count int) *mem.Store {
	t.Helper()
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]mem.KnowledgeNode, 0, count)
	embeddingIdentity := mem.EmbeddingIdentity{Backend: "test", Model: "test-model", SpaceID: mem.ChunkContentHash("test-space")}
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("Pressure requirement %d.", i+1)
		source := fmt.Sprintf("C:/docs/batch-plan-%d.md", i+1)
		entry, err := store.AddDocumentChunkWithEmbeddingIdentity(text, "Batch plan", nil, embeddingIdentity, []float32{1, 0}, source, 0, 1, false, mem.Provenance{
			DocumentID: fmt.Sprintf("batch-plan-doc-%d", i+1), DocumentRevision: mem.ChunkContentHash(fmt.Sprintf("batch-plan-revision-%d", i+1)),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "text/markdown", Page: 1,
			BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1, ExtractionMethod: "text", OCRConfidence: -1,
		})
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		anchor, err := mem.EvidenceAnchorForEntry(*entry, entry.Text)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		nodes = append(nodes, mem.KnowledgeNode{
			ID: fmt.Sprintf("batch-plan-claim-%d", i+1), Kind: mem.KnowledgeNodeClaim,
			Label: fmt.Sprintf("Pressure claim %d", i+1), Body: text,
			Status: mem.KnowledgeStatusActive, Origin: mem.KnowledgeOriginGenerated, Evidence: []mem.EvidenceAnchor{anchor},
		})
	}
	if err := store.UpsertKnowledgeGraph(mem.KnowledgeGraph{Nodes: nodes}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func cliCorpusTwoClaimBudget(t *testing.T, store *mem.Store, focus string) int {
	t.Helper()
	prompt, err := store.BuildCorpusAnalysisPrompt(focus, mem.MaxAnswerContextChars)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt.Claims) < 2 {
		t.Fatalf("fixture produced only %d claims", len(prompt.Claims))
	}
	claims, err := json.MarshalIndent(prompt.Claims[:2], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	focusJSON, err := json.Marshal(focus)
	if err != nil {
		t.Fatal(err)
	}
	user := "Focus (user input): " + string(focusJSON) + "\n\nCLAIMS_JSON_BEGIN\n" + string(claims) + "\nCLAIMS_JSON_END\n"
	return len([]rune(prompt.System)) + len([]rune(user))
}

func captureCLIStreams(fn func() error) (string, string, error) {
	oldOut, oldErr := os.Stdout, os.Stderr
	outReader, outWriter, _ := os.Pipe()
	errReader, errWriter, _ := os.Pipe()
	os.Stdout, os.Stderr = outWriter, errWriter
	err := fn()
	_ = outWriter.Close()
	_ = errWriter.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(outReader)
	errOut, _ := io.ReadAll(errReader)
	_ = outReader.Close()
	_ = errReader.Close()
	return string(out), string(errOut), err
}

func TestHandleAddFileDoesNotReportPartialEmbeddingAsSuccess(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "multi.txt")
	if err := os.WriteFile(path, []byte("11111 22222 33333"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalGetEmbedding := getEmbedding
	defer func() { getEmbedding = originalGetEmbedding }()
	calls := 0
	getEmbedding = func(_ *Config, text string) ([]float32, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("test embedding failure")
		}
		return []float32{float32(len(text)), 1}, nil
	}
	if err := handleAddFile(testCLIConfig(6, "fixed"), store, []string{path}); err == nil {
		t.Fatal("partial embedding failure reported success")
	}
	if got := store.Stats()["total_entries"]; got != 0 {
		t.Fatalf("partial document was stored: total_entries=%v", got)
	}
}

func testCLIConfig(maxSize int, strategy string) *Config {
	cfg := mem.DefaultLocalConfig()
	cfg.Chunking = ChunkConfig{MaxSize: maxSize, Strategy: strategy}
	return cfg
}

func TestHandleConfigRejectsChunkSizeAboveEmbeddingLimit(t *testing.T) {
	originalLoadConfig := loadConfig
	originalSaveConfig := saveConfig
	defer func() {
		loadConfig = originalLoadConfig
		saveConfig = originalSaveConfig
	}()

	cfg := mem.DefaultLocalConfig()
	loadConfig = func() (*Config, error) { return cfg, nil }
	saves := 0
	saveConfig = func(*Config) error {
		saves++
		return nil
	}

	if err := handleConfig([]string{"set-chunk-size", "2001"}); err == nil {
		t.Fatal("chunk size above embedding limit was accepted")
	}
	if saves != 0 {
		t.Fatalf("invalid config was saved %d time(s)", saves)
	}
	if err := handleConfig([]string{"set-chunk-size", "2000"}); err != nil {
		t.Fatalf("chunk size at embedding limit was rejected: %v", err)
	}
	if cfg.Chunking.MaxSize != mem.MaxEmbeddingChars || saves != 1 {
		t.Fatalf("valid config not saved: size=%d saves=%d", cfg.Chunking.MaxSize, saves)
	}
}
