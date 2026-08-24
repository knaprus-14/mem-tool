package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMindMapCLIUsesHumanLabelsAndProducesScriptableJSON(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stdout, _, err := captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"create", "Моя карта", "--description", "Проверка"})
	})
	if err != nil || !strings.Contains(stdout, "Моя карта") || strings.Contains(stdout, "mm-") {
		t.Fatalf("human create output exposes internals or failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"add-node", "Моя карта", "Моя карта", "Первая ветвь", "--summary", "Кратко"})
	})
	if err != nil || !strings.Contains(stdout, "Первая ветвь") {
		t.Fatalf("add by labels failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"show", "Моя карта", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc mem.ClassicMindMapDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Map.Title != "Моя карта" || len(doc.Nodes) != 2 || doc.Map.Revision != 2 {
		t.Fatalf("invalid JSON show: doc=%#v stdout=%q err=%v", doc, stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"history", "Моя карта"})
	})
	if err != nil || !strings.Contains(stdout, "добавление узла") || strings.Contains(stdout, "mmn-") {
		t.Fatalf("human history is not readable: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"undo", "Моя карта"})
	})
	if err != nil || !strings.Contains(stdout, "отменено") {
		t.Fatalf("human undo failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"redo", "Моя карта"})
	})
	if err != nil || !strings.Contains(stdout, "повторено") {
		t.Fatalf("human redo failed: stdout=%q err=%v", stdout, err)
	}
}

func TestMindMapCLIRejectsUnknownFlagsAndCanClearText(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := handleMindMap(nil, store, []string{"create", "Clear text"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(nil, store, []string{"add-node", "Clear text", "Clear text", "Child", "--summary", "temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(nil, store, []string{"edit-node", "Clear text", "Child", "--summary", ""}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.LoadClassicMindMap("Clear text")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range doc.Nodes {
		if node.Label == "Child" && node.Summary != "" {
			t.Fatalf("summary was not cleared: %#v", node)
		}
	}
	if err := handleMindMap(nil, store, []string{"list", "--unknown"}); err == nil || !strings.Contains(err.Error(), "неизвестный флаг") {
		t.Fatalf("unknown flag was accepted: %v", err)
	}
}

func TestMindMapCLIManagesNonEvidenceSources(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := handleMindMap(nil, store, []string{"create", "Source CLI"}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"source-add", "Source CLI", "Source CLI", "--url", "https://example.org/spec", "--title", "Спецификация"})
	})
	if err != nil || !strings.Contains(stdout, "Спецификация") {
		t.Fatalf("URL source add failed: stdout=%q err=%v", stdout, err)
	}
	doc, err := store.LoadClassicMindMap("Source CLI")
	if err != nil || len(doc.Nodes[0].Sources) != 1 {
		t.Fatalf("URL source missing: doc=%#v err=%v", doc, err)
	}
	sourceID := doc.Nodes[0].Sources[0].ID
	if err := handleMindMap(nil, store, []string{"source-move", "Source CLI", "Source CLI", "--source", sourceID, "--position", "0"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(nil, store, []string{"source-remove", "Source CLI", "Source CLI", "--source", sourceID}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadClassicMindMap("Source CLI")
	if err != nil || len(loaded.Nodes[0].Sources) != 0 {
		t.Fatalf("URL source was not removed: doc=%#v err=%v", loaded, err)
	}
	if err := handleMindMap(nil, store, []string{"source-add", "Source CLI", "Source CLI", "--url", "https://example.org", "--file", "missing"}); err == nil || !strings.Contains(err.Error(), "ровно один") {
		t.Fatalf("ambiguous source selector was accepted: %v", err)
	}
}

func TestMindMapAICLIPreviewDoesNotMutateAndApplyPublishes(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := testCLIConfig(1000, "paragraph")
	cfg.Answer.Model = "fake-chat"
	fake := &fakeAnswerProvider{answer: `{"nodes":[` +
		`{"ref":"root","parent_ref":"","label":"Новая AI-карта","summary":"Кратко","body_markdown":"","kind":"topic","citations":[]},` +
		`{"ref":"child","parent_ref":"root","label":"Первая ветвь","summary":"Содержание","body_markdown":"","kind":"subtopic","citations":[]}]}`}
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }

	stdout, stderr, err := captureCLIStreams(func() error {
		return handleMindMap(cfg, store, []string{"ai-new", "Составь структуру", "--without-sources", "--title", "Новая AI-карта"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Карта не изменена") || !strings.Contains(stderr, "[MINDMAP AI]") {
		t.Fatalf("preview output is incomplete: stdout=%q stderr=%q", stdout, stderr)
	}
	items, err := store.ListClassicMindMaps(true)
	if err != nil || len(items) != 0 {
		t.Fatalf("preview mutated map library: items=%#v err=%v", items, err)
	}
	fields := strings.Fields(stdout)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "mmg-") {
		t.Fatalf("preview ID missing from output: %q", stdout)
	}
	previewID := fields[1]
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(cfg, store, []string{"ai-apply", previewID})
	})
	if err != nil || !strings.Contains(stdout, "опубликован атомарно") ||
		!strings.Contains(stdout, "Открыть созданную карту") || strings.Contains(stdout, "mindmap undo") {
		t.Fatalf("apply failed: stdout=%q err=%v", stdout, err)
	}
	items, err = store.ListClassicMindMaps(true)
	if err != nil || len(items) != 1 || items[0].Title != "Новая AI-карта" || items[0].Revision != 1 {
		t.Fatalf("published map mismatch: items=%#v err=%v", items, err)
	}
}

func TestMindMapAIExistingCommandsCreatePersistedPreviewsWithFakeProvider(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const source = `C:\docs\manual.pdf`
	text := "Подтверждённый фрагмент документа"
	if err := store.ReplaceDocumentChunks(source, []mem.DocumentChunk{{
		Text: text, Backend: "test", Embedding: []float32{1, 0}, ChunkIndex: 0, TotalChunks: 1,
		Provenance: mem.Provenance{
			DocumentID: "doc-cli-ai", DocumentRevision: mem.ChunkContentHash("cli ai revision"),
			ChunkHash: mem.ChunkContentHash(text), SourcePath: source, MediaType: "application/pdf",
			Page: 7, BlockIndex: 0, BlockChunkIndex: 0, BlockTotalChunks: 1,
			BlockMarker: "<!-- page: 7 -->", ExtractionMethod: "text", OCRConfidence: -1,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	entries := store.GetBySourceFile(source)
	if len(entries) != 1 {
		t.Fatalf("versioned evidence entries=%d, want 1", len(entries))
	}
	if err := handleMindMap(nil, store, []string{"create", "AI CLI карта"}); err != nil {
		t.Fatal(err)
	}

	cfg := testCLIConfig(1000, "paragraph")
	cfg.Answer.Model = "fake-chat"
	fake := &fakeAnswerProvider{}
	originalProvider := newAnswerProvider
	defer func() { newAnswerProvider = originalProvider }()
	newAnswerProvider = func(mem.AnswerConfig) (mem.AnswerProvider, error) { return fake, nil }

	tests := []struct {
		command string
		answer  string
	}{
		{"ai-expand", `{"nodes":[{"ref":"n1","parent_ref":"","label":"Новая ветвь","summary":"Факт","body_markdown":"","kind":"subtopic","citations":["E1"]}]}`},
		{"ai-fill", `{"fill":{"summary":"Дополнение","body_markdown":"Подробность","citations":["E1"]}}`},
		{"ai-sources", `{"sources":[{"evidence_ref":"E1","reason":"точное подтверждение"}]}`},
	}
	var firstPreviewID string
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			fake.answer = test.answer
			stdout, stderr, err := captureCLIStreams(func() error {
				return handleMindMap(cfg, store, []string{
					test.command, "AI CLI карта", "AI CLI карта", "Проверь предложение",
					"--entry", strconv.FormatInt(entries[0].ID, 10), "--expect", "1",
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(stdout)
			if len(fields) < 2 || !strings.HasPrefix(fields[1], "mmg-") || !strings.Contains(stdout, "Карта не изменена") ||
				!strings.Contains(stdout, "manual.pdf") || !strings.Contains(stdout, "стр. 7") ||
				!strings.Contains(stdout, text) || !strings.Contains(stderr, "[MINDMAP AI]") {
				t.Fatalf("preview output is incomplete: stdout=%q stderr=%q", stdout, stderr)
			}
			if firstPreviewID == "" {
				firstPreviewID = fields[1]
			}
		})
	}
	items, err := store.ListClassicMindMaps(true)
	if err != nil || len(items) != 1 || items[0].Revision != 1 {
		t.Fatalf("preview commands mutated map: items=%#v err=%v", items, err)
	}
	stdout, _, err := captureCLIStreams(func() error {
		return handleMindMap(cfg, store, []string{"ai-show", firstPreviewID, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var preview mem.ClassicMindMapAIPreview
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil || preview.RunID != firstPreviewID || len(preview.Proposals) == 0 {
		t.Fatalf("ai-show returned invalid preview: preview=%#v stdout=%q err=%v", preview, stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(cfg, store, []string{"ai-show", firstPreviewID})
	})
	if err != nil || !strings.Contains(stdout, "Новая ветвь") || !strings.Contains(stdout, "manual.pdf") ||
		!strings.Contains(stdout, "стр. 7") || !strings.Contains(stdout, text) {
		t.Fatalf("human ai-show is not reviewable: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(cfg, store, []string{"ai-apply", firstPreviewID, "--expect", "1"})
	})
	if err != nil || !strings.Contains(stdout, "Для отмены: mem mindmap undo") || strings.Contains(stdout, "Открыть созданную карту") {
		t.Fatalf("existing-map apply hint is incorrect: stdout=%q err=%v", stdout, err)
	}
}

func TestMindMapAIHelpDocumentsCompleteEvidenceLimit(t *testing.T) {
	for _, fragment := range []string{
		"--page-from N", "--page-to N", "--query <текст>", "повторяемый --entry N",
		"--node-sources", "--without-sources", "--limit N (1..10000)", "Без --limit безопасный автоматический порог",
		"512 current chunks",
		"не гарантирует полный корпус",
	} {
		if !strings.Contains(mindMapUsage, fragment) {
			t.Fatalf("mindmap help misses %q", fragment)
		}
	}
}

func TestParseMindMapAIOptionsSupportsRepeatedEntriesAndSelection(t *testing.T) {
	options, err := parseMindMapAIOptions([]string{
		"Карта", "Узел", "Запрос", "--document", `D:\Books\manual.pdf`, "--page-from", "2", "--page-to", "5",
		"--query", "требования", "--entry", "42", "--entry", "43", "--limit", "10", "--node-sources", "--expect", "7", "--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(options.positional) != 3 || options.document != `D:\Books\manual.pdf` || options.pageFrom != 2 || options.pageTo != 5 ||
		options.query != "требования" || len(options.entryIDs) != 2 || options.entryIDs[0] != 42 || options.entryIDs[1] != 43 ||
		options.limit != 10 || !options.nodeSources || options.expect != 7 || !options.jsonOutput {
		t.Fatalf("AI options parsed incorrectly: %#v", options)
	}
	selected, err := parseMindMapAISelection("p1, p2")
	if err != nil || len(selected) != 2 || selected[0] != "p1" || selected[1] != "p2" {
		t.Fatalf("selection parsed incorrectly: %#v err=%v", selected, err)
	}
	if _, err := parseMindMapAISelection("p1,p1"); err == nil {
		t.Fatal("duplicate proposal selection was accepted")
	}
}
