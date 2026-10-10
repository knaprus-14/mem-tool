package mem

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableDatabaseKeepsHistoryAndRelocatesSource(t *testing.T) {
	ctx := context.Background()
	dir, _, id, node, source, original := mapDocumentFixture(t)
	cfg, e := LoadConfigIn(dir)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Polza.APIKey = "private-export-token"
	cfg.Answer.BaseURL = "https://private.example/"
	cfg.Ingest.Python = "private-executable"
	if e = SaveConfigIn(dir, cfg); e != nil {
		t.Fatal(e)
	}
	archive, manifest, cleanup, e := PreparePortableDatabaseIn(ctx, dir, true)
	defer cleanup()
	if e != nil {
		t.Fatal(e)
	}
	if manifest.Version != 1 {
		t.Fatal(manifest)
	}
	dest := filepath.Join(t.TempDir(), ".mem")
	if _, e = RestorePortableDatabaseIn(ctx, archive, dest, "new-owner"); e != nil {
		t.Fatal(e)
	}
	config, e := os.ReadFile(ConfigPathIn(dest))
	if e != nil || strings.Contains(string(config), "private-") {
		t.Fatal(string(config), e)
	}
	if e = os.Remove(original); e != nil {
		t.Fatal(e)
	}
	legacy, e := OpenWebGraphIn(ctx, dest)
	if e != nil {
		t.Fatal(e)
	}
	resolved, e := legacy.ResolveClassicMindMapFileSource(id, source)
	legacy.Close()
	if e != nil || !strings.HasPrefix(resolved.Path, dest) {
		t.Fatal(resolved, e)
	}
	view, e := ReadWebMapIn(ctx, dest, id)
	if e != nil || view.Map.Revision != 2 {
		t.Fatal(view, e)
	}
	file, closeFile, e := OpenWebMapDocumentPDFIn(ctx, dest, id, node, source, 2, false)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(file)
	closeFile()
	if e != nil || !strings.HasPrefix(string(data), "%PDF-") {
		t.Fatal(string(data), e)
	}
	// Transfer the already-relocated base once more.
	second, _, clean, e := PreparePortableDatabaseIn(ctx, dest, false)
	defer clean()
	if e != nil {
		t.Fatal(e)
	}
	next := filepath.Join(t.TempDir(), ".mem")
	if _, e = RestorePortableDatabaseIn(ctx, second, next, "third-owner"); e != nil {
		t.Fatal(e)
	}
}
func TestPortableRejectsTraversalAndExistingDestination(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.zip")
	f, e := os.Create(archive)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("../escape")
	w.Write([]byte("bad"))
	z.Close()
	f.Close()
	parent := t.TempDir()
	dest := filepath.Join(parent, ".mem")
	if _, e = RestorePortableDatabaseIn(context.Background(), archive, dest, "alice"); e == nil {
		t.Fatal("traversal accepted")
	}
	if _, e = os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	os.Mkdir(dest, 0700)
	os.WriteFile(filepath.Join(dest, "keep"), []byte("keep"), 0600)
	if _, e = RestorePortableDatabaseIn(context.Background(), archive, dest, "alice"); e == nil {
		t.Fatal("existing target accepted")
	}
	if _, e = os.Stat(filepath.Join(dest, "keep")); e != nil {
		t.Fatal(e)
	}
}
