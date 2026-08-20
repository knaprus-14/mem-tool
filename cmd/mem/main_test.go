package main

import (
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

type fakeAnswerProvider struct {
	answer  string
	calls   int
	request mem.AnswerRequest
}

type corpusBatchAnswerProvider struct {
	calls  int
	failAt int
}

func (p *corpusBatchAnswerProvider) Generate(_ context.Context, request mem.AnswerRequest) (string, error) {
	p.calls++
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

func TestHandleAskKeepsStatusOnStderrAndVersionedAnswerOnStdout(t *testing.T) {
	root := t.TempDir()
	store, err := mem.NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	text := "Русский факт о запуске"
	revision := mem.ChunkContentHash("document revision fixture")
	entry, err := store.AddDocumentChunk(text, "Book", nil, "test", []float32{1, 0},
		"section", 0, 1, false, mem.Provenance{
			DocumentID: "doc-ask", DocumentRevision: revision, ChunkHash: mem.ChunkContentHash(text),
			SourcePath: "C:/docs/book.pdf", MediaType: "application/pdf", Page: 3, BlockIndex: 0,
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
	if !strings.Contains(stdout, "Ответ подтверждён") || !strings.Contains(stdout, citationID) || !strings.Contains(stdout, revision) || strings.Contains(stdout, "[ASK]") {
		t.Fatalf("stdout is not a versioned, scriptable answer stream: %q", stdout)
	}
	if !strings.Contains(stdout, "evidence=sha256:") || !strings.Contains(stderr, "[ASK] retrieval") || !strings.Contains(stderr, "[ASK] evidence") || fake.calls != 1 {
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
	entry, err := store.AddDocumentChunk(text, "Architecture", nil, "test", []float32{1, 0},
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
	if !strings.Contains(stdout, "nodes=1 edges=0 evidence=1") || !strings.Contains(stderr, "[MAP] retrieval") || !strings.Contains(stderr, "[MAP] evidence") {
		t.Fatalf("map streams are not scriptable: stdout=%q stderr=%q", stdout, stderr)
	}
	if fake.calls != 1 || !strings.Contains(fake.request.System, "typed knowledge graph") || !strings.Contains(fake.request.Prompt, citation) {
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
	if _, err := store.AddDocumentChunk(text, "Doc", nil, "test", []float32{1, 0}, "section", 0, 1, false, mem.Provenance{
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
	if _, err := store.Add("manual note", "", nil, "test", []float32{1, 0}, false); err != nil {
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
}

func TestHandleMapAnalyzePersistsDraftCrossDocumentFinding(t *testing.T) {
	store, err := mem.NewStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	texts := []string{"Limit is 1.0 MPa.", "Limit is 1.2 MPa."}
	anchors := make([]mem.EvidenceAnchor, 0, 2)
	for i, text := range texts {
		source := fmt.Sprintf("C:/docs/analyze-%d.md", i)
		entry, err := store.AddDocumentChunk(text, "Analyze", nil, "test", []float32{1, 0}, source, 0, 1, false, mem.Provenance{
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
		!strings.Contains(stderr, "batch=3/3") {
		t.Fatalf("batched analysis failed: stdout=%q stderr=%q calls=%d err=%v", stdout, stderr, provider.calls, err)
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
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("Pressure requirement %d.", i+1)
		source := fmt.Sprintf("C:/docs/batch-plan-%d.md", i+1)
		entry, err := store.AddDocumentChunk(text, "Batch plan", nil, "test", []float32{1, 0}, source, 0, 1, false, mem.Provenance{
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
	return &Config{Backend: "test", Chunking: ChunkConfig{MaxSize: maxSize, Strategy: strategy}}
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
