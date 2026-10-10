package ingest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPhysicalRevisionPinsBytesAndOriginalLocator(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(path, []byte("same text\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := Extract(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Extract(ctx, path)
	if err != nil || again.Revision != first.Revision {
		t.Fatalf("unchanged physical edition: %s != %s: %v", again.Revision, first.Revision, err)
	}
	if err := os.WriteFile(path, []byte("same text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := Extract(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Blocks[0].Text != changed.Blocks[0].Text || first.SourceFileHash == changed.SourceFileHash || first.Revision == changed.Revision {
		t.Fatalf("equal extracted text must not merge physical editions: first=%+v changed=%+v", first, changed)
	}
	otherPath := filepath.Join(filepath.Dir(path), "retained-upload.txt")
	if err := os.WriteFile(otherPath, []byte("same text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	moved, err := Extract(ctx, otherPath)
	if err != nil || moved.SourceFileHash != changed.SourceFileHash || moved.Revision == changed.Revision {
		t.Fatalf("retained source locators must have distinct editions: %+v %v", moved, err)
	}
	if err := ValidateDocument(changed); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		caseVariant := changed
		caseVariant.SourcePath = strings.ToUpper(changed.SourcePath)
		if ContentRevision(caseVariant) != changed.Revision {
			t.Fatal("Windows path case changed a physical edition")
		}
	}
}

func TestPhysicalRevisionDoesNotInventLegacyFingerprint(t *testing.T) {
	doc, err := ParseMarkdown(filepath.Join(t.TempDir(), "legacy.md"), "unchanged text")
	if err != nil {
		t.Fatal(err)
	}
	legacyRevision := doc.Revision
	originalPath := doc.SourcePath
	doc.SourcePath = filepath.Join(t.TempDir(), "elsewhere.md")
	if ContentRevision(doc) != legacyRevision || PhysicalSourceRevision(legacyRevision, doc.SourcePath, "") != legacyRevision {
		t.Fatal("synthetic text-only documents acquired physical provenance")
	}
	doc.SourcePath = originalPath
	doc.SourceFileHash = "not-a-fingerprint"
	doc.Revision = ContentRevision(doc)
	if err := ValidateDocument(doc); err == nil || !strings.Contains(err.Error(), "physical source fingerprint") {
		t.Fatal("invalid physical fingerprint accepted")
	}
}

func TestPhysicalRevisionLocatorIsIndependentOfHostOS(t *testing.T) {
	for _, pair := range [][2]string{
		{`C:\Books\Original.PDF`, `c:/books/original.pdf`},
		{`\\SERVER\Share\Original.PDF`, `//server/share/original.pdf`},
	} {
		if PhysicalSourceRevision("content", pair[0], "fingerprint") != PhysicalSourceRevision("content", pair[1], "fingerprint") {
			t.Fatalf("Windows locator depends on host spelling: %q vs %q", pair[0], pair[1])
		}
	}
	if PhysicalSourceRevision("content", "/books/Original.pdf", "fingerprint") == PhysicalSourceRevision("content", "/books/original.pdf", "fingerprint") {
		t.Fatal("POSIX locator lost case sensitivity")
	}
}
