package mem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

const physicalTestManagedID = "web:0123456789abcdef0123456789abcdef"

func importPhysicalTestDocument(t *testing.T, store *Store, path string, options ImportOptions) ImportResult {
	t.Helper()
	doc, err := ingest.Extract(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if options.retained == nil {
		doc, err = ingest.WithManagedIdentity(doc, physicalTestManagedID)
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := importExtractedDocumentWithEmbedder(testConfig(1000, "paragraph"), store, doc, options, fakeEmbedding)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPhysicalFingerprintRejectsEditionMutationAtomically(t *testing.T) {
	for _, change := range []string{"bytes", "locator"} {
		t.Run(change, func(t *testing.T) {
			store, anchor, source := knowledgeMapPDFSourceFixture(t)
			defer store.Close()
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			originalHash, err := sourcefile.Hash(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			target, hash := source, originalHash
			if change == "bytes" {
				if err := os.WriteFile(source, []byte("%PDF-1.4\nchanged visual formula; same extracted text\n"), 0600); err != nil {
					t.Fatal(err)
				}
				hash, err = sourcefile.Hash(context.Background(), source)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				target = filepath.Join(filepath.Dir(source), "retained.pdf")
			}
			chunks := validStructuredChunks()
			for i := range chunks {
				chunks[i].Provenance.SourceFileHash = hash
				chunks[i].Provenance.SourcePath = target
				chunks[i].Provenance.Page = 12
			}
			if err := store.ReplaceDocumentChunks(target, chunks); !errors.Is(err, ErrSourceFileChanged) {
				t.Fatalf("same edition mutation must fail: %v", err)
			}
			var keptPath, keptHash string
			if err := store.db.QueryRow(`SELECT source_path,file_hash FROM document_source_files WHERE document_id=? AND document_revision=?`, anchor.DocumentID, anchor.DocumentRevision).Scan(&keptPath, &keptHash); err != nil {
				t.Fatal(err)
			}
			if keptPath != source || keptHash != originalHash || len(store.GetBySourceFile(source)) != len(chunks) {
				t.Fatal("failed edition mutation changed stored provenance or document")
			}
			if change == "locator" && len(store.GetBySourceFile(target)) != 0 {
				t.Fatal("failed replacement committed new document chunks")
			}
			if err := os.WriteFile(source, before, 0600); err != nil {
				t.Fatal(err)
			}
			assertPhysicalTestSource(t, store, anchor, source, before)
		})
	}
}

func assertPhysicalTestSource(t *testing.T, store *Store, anchor EvidenceAnchor, physicalPath string, want []byte) {
	t.Helper()
	file, err := os.Open(physicalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	snapshot, err := store.verifiedSourceFile(context.Background(), anchor, file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSourceSnapshot(snapshot)
	got, err := io.ReadAll(snapshot)
	if err != nil || string(got) != string(want) {
		t.Fatalf("wrong verified bytes: %q, err=%v", got, err)
	}
}

func TestPhysicalImportChangedBytesWithSameTextStalesOldEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original.txt")
	if err := os.WriteFile(path, []byte("same extracted text\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := importPhysicalTestDocument(t, store, path, ImportOptions{})
	entry := store.GetBySourceFile(path)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: "physical-source", Kind: KnowledgeNodeClaim, Label: "Original evidence", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same extracted text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := importPhysicalTestDocument(t, store, path, ImportOptions{})
	fresh := store.GetBySourceFile(path)[0]
	if fresh.Text != entry.Text || first.DocumentRevision == second.DocumentRevision {
		t.Fatalf("physical-only change failed: old=%+v new=%+v", first, second)
	}
	if _, err := store.resolveKnowledgeMapSource(anchor.CitationID); !errors.Is(err, ErrKnowledgeMapSourceNotCurrent) {
		t.Fatalf("old evidence still current after raw bytes changed: %v", err)
	}
	var editions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM document_source_files WHERE document_id=?`, physicalTestManagedID).Scan(&editions); err != nil || editions != 2 {
		t.Fatalf("editions=%d err=%v", editions, err)
	}
}

func TestPhysicalSameTextUploadRestoreKeepsOriginalFingerprint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldPath, newPath := filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")
	content := []byte("same imported content")
	for _, path := range []string{oldPath, newPath} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := importPhysicalTestDocument(t, store, oldPath, ImportOptions{})
	entry := store.GetBySourceFile(oldPath)[0]
	anchor, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadDocumentUpdateStateIn(ctx, dir, physicalTestManagedID)
	if err != nil {
		t.Fatal(err)
	}
	second := importPhysicalTestDocument(t, store, newPath, ImportOptions{Replacement: &state.DocumentReplacement})
	if first.DocumentRevision == second.DocumentRevision {
		t.Fatal("retained paths shared one physical revision")
	}
	history, err := store.ListDocumentHistorySnapshots(physicalTestManagedID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	plan, err := store.BuildDocumentRestorePlan(physicalTestManagedID, history[0].SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDocumentRestore(physicalTestManagedID, history[0].SnapshotID, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	assertPhysicalTestSource(t, store, anchor, oldPath, content)
}

func TestLegacyIndexPreservesManagedIdentityAndRejectsOldOriginal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "db")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldPath, newPath := filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")
	for _, path := range []string{oldPath, newPath} {
		if err := os.WriteFile(path, []byte("managed original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	importPhysicalTestDocument(t, store, oldPath, ImportOptions{})
	if _, err := indexFileWithEmbedder(testConfig(1000, "paragraph"), store, oldPath, fakeEmbedding); err != nil {
		t.Fatal(err)
	}
	if got := store.GetBySourceFile(oldPath)[0].DocumentID; got != physicalTestManagedID {
		t.Fatalf("managed ID lost during index: %s", got)
	}
	state, err := ReadDocumentUpdateStateIn(ctx, dir, physicalTestManagedID)
	if err != nil {
		t.Fatal(err)
	}
	importPhysicalTestDocument(t, store, newPath, ImportOptions{Replacement: &state.DocumentReplacement})
	if _, err := indexFileWithEmbedder(testConfig(1000, "paragraph"), store, oldPath, fakeEmbedding); !errors.Is(err, ErrDocumentUpdateStateChanged) {
		t.Fatalf("retained old original must not create another live document: %v", err)
	}
	if got := store.GetBySourceFile(newPath); len(got) != 1 || got[0].DocumentID != physicalTestManagedID || len(store.GetBySourceFile(oldPath)) != 0 {
		t.Fatalf("failed index changed managed state: %+v", got)
	}
}

func TestManagedIdentityCannotChangeThroughDirectReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "original.txt")
	if err := os.WriteFile(path, []byte("managed original"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	importPhysicalTestDocument(t, store, path, ImportOptions{})
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
	}
	if err := store.ReplaceDocumentChunks(path, chunks); !errors.Is(err, ErrDocumentUpdateStateChanged) {
		t.Fatalf("direct replacement changed web identity: %v", err)
	}
	if got := store.GetBySourceFile(path); len(got) != 1 || got[0].DocumentID != physicalTestManagedID {
		t.Fatalf("failed replacement mutated current entries: %+v", got)
	}
}

func TestPhysicalPortableRechunkRetainsLogicalSourceAndMapping(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "physical-edition"
		if legacy {
			name = "legacy-explicit-upgrade"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			dir := filepath.Join(root, ".mem")
			original := filepath.Join(root, "original.txt")
			content := []byte("portable original with unchanged extracted text")
			if err := os.WriteFile(original, content, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveConfigIn(dir, testConfig(1000, "paragraph")); err != nil {
				store.Close()
				t.Fatal(err)
			}
			importPhysicalTestDocument(t, store, original, ImportOptions{})
			if legacy {
				// Recreate a pre-upgrade database with an existing known fingerprint
				// attached to its old text-only revision. Missing fingerprints are
				// deliberately not manufactured by the production upgrade path.
				doc, err := ingest.Extract(ctx, original)
				if err != nil {
					t.Fatal(err)
				}
				doc.SourceFileHash = ""
				oldRevision := ingest.ContentRevision(doc)
				if _, err := store.db.Exec(`UPDATE entries SET document_revision=?; UPDATE document_source_files SET document_revision=?`, oldRevision, oldRevision); err != nil {
					t.Fatal(err)
				}
			}
			entry := store.GetBySourceFile(original)[0]
			oldAnchor, err := EvidenceAnchorForEntry(entry, entry.Text)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: "portable-evidence", Kind: KnowledgeNodeClaim, Label: "Retained original", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{oldAnchor}}}}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			archive, manifest, cleanup, err := PreparePortableDatabaseIn(ctx, dir, true)
			defer cleanup()
			if err != nil || manifest.MissingSources != 0 {
				t.Fatalf("portable prepare: %+v %v", manifest, err)
			}
			dest := filepath.Join(t.TempDir(), ".mem")
			if _, err := RestorePortableDatabaseIn(ctx, archive, dest, "portable-owner"); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
			moved, err := NewStore(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer moved.Close()
			physicalPath, mapped, err := sourceLocation(ctx, moved.db, dest, oldAnchor)
			if err != nil || !mapped || physicalPath == original {
				t.Fatalf("relocated source unavailable: %s %v %v", physicalPath, mapped, err)
			}
			state, err := ReadDocumentUpdateStateIn(ctx, dest, physicalTestManagedID)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := sourcefile.Hash(ctx, physicalPath)
			if err != nil {
				t.Fatal(err)
			}
			result := importPhysicalTestDocument(t, moved, physicalPath, ImportOptions{
				retained: &retainedDocumentIdentity{ID: physicalTestManagedID, Path: original, Hash: hash}, Replacement: &state.DocumentReplacement,
			})
			if result.SourcePath != original || result.DocumentID != physicalTestManagedID {
				t.Fatalf("rechunk replaced logical identity with archive path: %+v", result)
			}
			if (result.DocumentRevision == entry.DocumentRevision) == legacy {
				t.Fatalf("unexpected revision transition legacy=%v: old=%s new=%s", legacy, entry.DocumentRevision, result.DocumentRevision)
			}
			fresh := moved.GetBySourceFile(original)[0]
			anchor, err := EvidenceAnchorForEntry(fresh, fresh.Text)
			if err != nil {
				t.Fatal(err)
			}
			currentPath, mapped, err := sourceLocation(ctx, moved.db, dest, anchor)
			if err != nil || !mapped || currentPath != physicalPath {
				t.Fatalf("rechunk lost source_locations mapping: %s %v %v", currentPath, mapped, err)
			}
			assertPhysicalTestSource(t, moved, oldAnchor, physicalPath, content)
			assertPhysicalTestSource(t, moved, anchor, physicalPath, content)
			_, err = moved.resolveKnowledgeMapSource(oldAnchor.CitationID)
			if (!legacy && err != nil) || (legacy && !errors.Is(err, ErrKnowledgeMapSourceNotCurrent)) {
				t.Fatalf("legacy=%v old evidence state after relocation/rechunk: %v", legacy, err)
			}
		})
	}
}
