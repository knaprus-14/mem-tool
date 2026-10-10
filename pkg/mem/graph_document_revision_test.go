package mem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentGraphReimportPinsCompletionAndPreservesSourceHistory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "document.txt")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(source, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("Pressure must remain below 5 bar.\n")
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := importPhysicalTestDocument(t, store, source, ImportOptions{})
	oldEntry := store.GetBySourceFile(source)[0]
	oldCitation, _ := CitationForEntry(oldEntry)
	calls := 0
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		return documentTestReply(documentRequestEvidence(t, r)), nil
	})
	run := func() {
		t.Helper()
		if err := store.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, physicalTestManagedID, "all", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	run()
	before, _ := store.LoadKnowledgeGraph()
	var oldSection KnowledgeNode
	for _, node := range before.Nodes {
		if node.Kind == KnowledgeNodeSection && node.Origin == KnowledgeOriginSource {
			oldSection = node
		}
	}
	if calls != 1 || oldSection.ID == "" {
		t.Fatalf("initial pass: calls=%d source=%+v", calls, oldSection)
	}
	// Editing a source node first requires its active containment relation to
	// return to draft. Use the real edit API so both edits retain an audit trail.
	for _, edge := range before.Edges {
		if edge.To != oldSection.ID || edge.Status != KnowledgeStatusActive {
			continue
		}
		item := knowledgeReviewItemByID(t, store, KnowledgeObjectEdge, edge.ID)
		if _, err := store.EditKnowledgeObject(KnowledgeEditRequest{
			ObjectType: KnowledgeObjectEdge, ID: edge.ID, Editor: "reviewer", Label: "My source relation",
			ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest, ExpectedEvidenceDigest: item.EvidenceDigest,
		}); err != nil {
			t.Fatal(err)
		}
	}
	item := knowledgeReviewItemByID(t, store, KnowledgeObjectNode, oldSection.ID)
	edited, err := store.EditKnowledgeObject(KnowledgeEditRequest{
		ObjectType: KnowledgeObjectNode, ID: oldSection.ID, Editor: "reviewer",
		Label: "My source label", Body: "My retained explanation",
		ExpectedStatus: item.Status, ExpectedContentDigest: item.ContentDigest,
		ExpectedEvidenceDigest: item.EvidenceDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes reimported and repeated run must preserve identities and edits.
	unchanged := importPhysicalTestDocument(t, store, source, ImportOptions{})
	run()
	repeated, _ := store.LoadKnowledgeGraph()
	if calls != 1 || unchanged.DocumentRevision != first.DocumentRevision || len(repeated.Nodes) != len(before.Nodes) || len(repeated.Edges) != len(before.Edges) {
		t.Fatal("unchanged reimport repeated work or changed the graph")
	}
	write("Pressure must remain below 9 bar.\n")
	second := importPhysicalTestDocument(t, store, source, ImportOptions{})
	newEntry := store.GetBySourceFile(source)[0]
	newCitation, _ := CitationForEntry(newEntry)
	if first.DocumentRevision == second.DocumentRevision || oldEntry.ChunkHash == newEntry.ChunkHash || oldCitation != newCitation {
		t.Fatal("fixture did not change the text/revision at stable coordinates")
	}
	coverage, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: physicalTestManagedID})
	if err != nil || coverage.Summary.SemanticCompletedChunks != 0 || coverage.Summary.SemanticPendingChunks != 1 {
		t.Fatalf("old completion credited before new pass: %+v %v", coverage.Summary, err)
	}
	run()
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	currentGenerated, currentSource, retainedOld := 0, 0, false
	for _, node := range graph.Nodes {
		if node.ID == oldSection.ID {
			retainedOld = node.Label == edited.Label && node.Body == edited.Body && equalEvidenceAnchors(node.Evidence, oldSection.Evidence)
		}
		for _, anchor := range node.Evidence {
			if anchor.DocumentRevision != second.DocumentRevision || store.ResolveEvidenceAnchor(anchor).State != EvidenceCurrent {
				continue
			}
			if node.Origin == KnowledgeOriginGenerated {
				currentGenerated++
			}
			if node.Origin == KnowledgeOriginSource && node.Kind == KnowledgeNodeSection {
				if node.Body != newEntry.Text || node.ID == oldSection.ID {
					t.Fatal("new source text overwrote an old edition or was omitted")
				}
				currentSource++
			}
		}
	}
	if calls != 2 || currentGenerated == 0 || currentSource != 1 || !retainedOld {
		t.Fatalf("new pass calls=%d generated=%d source=%d old edits retained=%t", calls, currentGenerated, currentSource, retainedOld)
	}
	records, err := store.ListKnowledgeEdits(10)
	if err != nil || len(records) != 2 || records[0].ID != edited.Edit.ID {
		t.Fatal("source edit audit was changed", records, err)
	}
	coverage, err = store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{Document: physicalTestManagedID})
	if err != nil || coverage.Summary.SemanticCompletedChunks != 1 || coverage.Summary.SemanticPendingChunks != 0 {
		t.Fatal(coverage.Summary, err)
	}
	run()
	if calls != 2 {
		t.Fatal("new edition was not checkpointed")
	}
}

func TestDocumentGraphPhysicalRevisionReprocessesIdenticalText(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := 0
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		return documentTestReply(documentRequestEvidence(t, r)), nil
	})
	var previous Entry
	for i, content := range []string{"unchanged extracted text\r\n", "unchanged extracted text\n"} {
		if err = os.WriteFile(source, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		importPhysicalTestDocument(t, store, source, ImportOptions{})
		entry := store.GetBySourceFile(source)[0]
		if i > 0 && (entry.Text != previous.Text || entry.DocumentRevision == previous.DocumentRevision) {
			t.Fatal("fixture must change physical revision while preserving text")
		}
		if err = store.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, physicalTestManagedID, "all", "", nil); err != nil {
			t.Fatal(err)
		}
		previous = entry
	}
	if calls != 2 {
		t.Fatal("old window reused for new physical revision", calls)
	}
}

func TestDocumentGraphCheckpointsPinGenerationSettings(t *testing.T) {
	for _, setting := range []string{"model", "base_url", "tokens", "temperature", "focus", "ocr_threshold"} {
		t.Run(setting, func(t *testing.T) {
			store, entries := newKnowledgeExtractionJobStore(t, []string{"Evidence for a configuration-sensitive pass."})
			defer store.Close()
			cfg := DefaultLocalConfig()
			focus, calls := "all", 0
			provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
				calls++
				return documentTestReply(documentRequestEvidence(t, r)), nil
			})
			run := func() {
				t.Helper()
				if err := store.RunKnowledgeDocumentGraph(t.Context(), cfg, provider, entries[0].DocumentID, focus, "", nil); err != nil {
					t.Fatal(err)
				}
			}
			run()
			switch setting {
			case "model":
				cfg.Answer.Model = "another-model"
			case "base_url":
				cfg.Answer.BaseURL = "http://other-provider.invalid"
			case "tokens":
				cfg.Answer.MaxTokens = 6144
			case "temperature":
				cfg.Answer.Temperature = 0.5
			case "focus":
				focus = "different focus"
			case "ocr_threshold":
				cfg.Ingest.LowConfidence = 80
			}
			run()
			if calls != 2 {
				t.Fatal("old chunk/window reused under changed settings", calls)
			}
			run()
			if calls != 2 {
				t.Fatal("identical settings did not resume")
			}
		})
	}
}

func TestDocumentGraphLegacyUnpinnedCompletionIsNotTrusted(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{"Current evidence after an unknown legacy pass."})
	defer store.Close()
	entry := entries[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "chunk-source-" + documentGraphID(anchor.CitationID+":0")
	root, err := documentSourceRoot(entry)
	if err != nil {
		t.Fatal(err)
	}
	legacySection := KnowledgeNode{ID: legacyID, Kind: KnowledgeNodeSection, Origin: KnowledgeOriginSource,
		Status: KnowledgeStatusActive, Label: "Legacy user label", Body: "Legacy user explanation", Evidence: []EvidenceAnchor{anchor}}
	if err = store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{root, legacySection}}); err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`CREATE TABLE knowledge_document_chunks(citation_id TEXT PRIMARY KEY,status TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '');
CREATE TABLE knowledge_document_windows(citation_id TEXT NOT NULL,window_hash TEXT NOT NULL,status TEXT NOT NULL,PRIMARY KEY(citation_id,window_hash));
INSERT INTO knowledge_document_chunks VALUES(?,'completed','legacy record');
INSERT INTO knowledge_document_windows VALUES(?,?,'completed')`, anchor.CitationID, anchor.CitationID, ChunkContentHash(entry.Text))
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := store.BuildKnowledgeCoverageReport(KnowledgeCoverageOptions{})
	if err != nil || coverage.Summary.SemanticCompletedChunks != 0 || coverage.Summary.SemanticPendingChunks != 1 {
		t.Fatal("legacy completion was trusted", coverage.Summary, err)
	}
	calls := 0
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		return documentTestReply(documentRequestEvidence(t, r)), nil
	})
	if err = store.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, entry.DocumentID, "all", "", nil); err != nil {
		t.Fatal(err)
	}
	graph, _ := store.LoadKnowledgeGraph()
	sourceSections := 0
	for _, n := range graph.Nodes {
		if n.Origin == KnowledgeOriginSource && n.Kind == KnowledgeNodeSection {
			sourceSections++
			if n.ID != legacyID || n.Label != legacySection.Label || n.Body != legacySection.Body {
				t.Fatal("unchanged legacy source or edits were replaced")
			}
		}
	}
	var reason string
	err = store.db.QueryRow(`SELECT reason FROM knowledge_document_chunks WHERE citation_id=?`, anchor.CitationID).Scan(&reason)
	if err != nil || reason != "legacy record" || calls != 1 || sourceSections != 1 {
		t.Fatalf("legacy compatibility: calls=%d sources=%d reason=%q err=%v", calls, sourceSections, reason, err)
	}
}

func TestDocumentGraphChangedInputDuringPassDoesNotOverwriteCheckpoint(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{"Evidence."})
	defer store.Close()
	if _, err := store.db.Exec(documentGraphSchema); err != nil {
		t.Fatal(err)
	}
	oldPins, err := store.prepareDocumentChunks(t.Context(), entries, "old configuration")
	if err != nil {
		t.Fatal(err)
	}
	newPins, err := store.prepareDocumentChunks(t.Context(), entries, "new configuration")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.saveDocumentChunk(t.Context(), oldPins[0], "completed", ""); err == nil || !strings.Contains(err.Error(), "input changed") {
		t.Fatal("old concurrent pass completed a newer request", err)
	}
	status, err := store.documentChunkStatus(t.Context(), newPins[0])
	if err != nil || status != "pending" {
		t.Fatal("new pass was modified", status, err)
	}
}

func TestDocumentGraphRechunkPinsRootAndSections(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{"First requirement. Second requirement."})
	defer store.Close()
	calls := 0
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		return documentTestReply(documentRequestEvidence(t, r)), nil
	})
	run := func() {
		t.Helper()
		if err := store.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, entries[0].DocumentID, "all", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	run()
	chunks := knowledgeExtractionJobChunks([]string{"First requirement.", "Second requirement."}, "job revision")
	if chunks[0].Provenance.DocumentRevision != entries[0].DocumentRevision {
		t.Fatal("rechunk fixture changed document revision")
	}
	if err := store.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	run()
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		t.Fatal(err)
	}
	currentRoots, currentSections := 0, 0
	for _, node := range graph.Nodes {
		if node.Origin != KnowledgeOriginSource {
			continue
		}
		for _, anchor := range node.Evidence {
			if store.ResolveEvidenceAnchor(anchor).State != EvidenceCurrent {
				continue
			}
			if node.Kind == KnowledgeNodeDocument {
				currentRoots++
			} else if node.Kind == KnowledgeNodeSection {
				currentSections++
			}
		}
	}
	if calls != 3 || currentRoots != 1 || currentSections != 2 {
		t.Fatalf("rechunk calls=%d current roots=%d sections=%d", calls, currentRoots, currentSections)
	}
}

func TestDocumentGraphReturningToEarlierConfigurationStartsFreshPass(t *testing.T) {
	store, entries := newKnowledgeExtractionJobStore(t, []string{"One source."})
	defer store.Close()
	calls := 0
	provider := documentTestProvider(func(_ context.Context, r AnswerRequest) (string, error) {
		calls++
		return documentTestReply(documentRequestEvidence(t, r)), nil
	})
	for _, focus := range []string{"A", "B", "A", "A"} {
		if err := store.RunKnowledgeDocumentGraph(t.Context(), DefaultLocalConfig(), provider, entries[0].DocumentID, focus, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatal("intervening output cannot be treated as an old cached configuration", calls)
	}
}
