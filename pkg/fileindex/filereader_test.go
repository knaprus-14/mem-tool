package fileindex

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFB2AnnotationDoesNotMaterializeTrailingBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.fb2")
	prefix := `<?xml version="1.0"?><FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info><annotation>Bounded annotation</annotation></title-info></description><body>`
	content := prefix + strings.Repeat("x", maxMetadataXMLBytes*2) + `</body></FictionBook>`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readFB2Annotation(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Bounded annotation" {
		t.Fatalf("annotation=%q", got)
	}
}

func TestReadEPUBDescriptionRejectsOversizedCompressedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.epub")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	container, err := zw.Create("META-INF/container.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := container.Write([]byte(`<?xml version="1.0"?><container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`)); err != nil {
		t.Fatal(err)
	}
	opf, err := zw.Create("content.opf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opf.Write(bytes.Repeat([]byte("x"), maxMetadataXMLBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := readEPUBDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("oversized EPUB metadata was accepted: %q", got)
	}
}

func TestCappedBufferRetainsOnlyConfiguredPrefix(t *testing.T) {
	buffer := cappedBuffer{max: 4}
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write=(%d,%v), want (8,nil)", n, err)
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("buffer=%q", got)
	}
}

func TestBoundedCommandOutputTerminatesHangingExtractor(t *testing.T) {
	if os.Getenv("MEM_FILEINDEX_HANGING_HELPER") == "1" {
		time.Sleep(time.Minute)
		return
	}
	t.Setenv("MEM_FILEINDEX_HANGING_HELPER", "1")
	started := time.Now()
	_, err := boundedCommandOutputWithTimeout(os.Args[0],
		[]string{"-test.run=TestBoundedCommandOutputTerminatesHangingExtractor"}, 64, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hanging extractor error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("hanging extractor was not terminated promptly: %s", elapsed)
	}
}
