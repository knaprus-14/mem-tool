package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolDiscoveryExplicitPathAndEnvError(t *testing.T) {
	exe := fixtureFile(t, "tool.exe")
	got, err := defaultToolResolver("tesseract", exe)
	if err != nil || filepath.Clean(got) != filepath.Clean(exe) {
		t.Fatalf("explicit tool not found: %q %v", got, err)
	}
	t.Setenv("MEM_TESSERACT", filepath.Join(t.TempDir(), "missing.exe"))
	_, err = defaultToolResolver("tesseract", "")
	if err == nil || !strings.Contains(err.Error(), "MEM_TESSERACT") || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid env override was not actionable: %v", err)
	}
}

func TestToolDiscoveryUsesPath(t *testing.T) {
	dir := t.TempDir()
	name := "mem-fake-discovery"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	lookup := strings.TrimSuffix(name, filepath.Ext(name))
	got, err := defaultToolResolver(lookup, "")
	if err != nil || filepath.Base(got) != name {
		t.Fatalf("PATH discovery failed: %q %v", got, err)
	}
}

func TestWindowsStandardCandidatesIncludeInstalledToolFamilies(t *testing.T) {
	for name, fragment := range map[string]string{"ddjvu": "DjVuLibre", "tesseract": "Tesseract-OCR", "pdftotext": "poppler", "mutool": "MuPDF"} {
		joined := strings.Join(windowsToolCandidates(name), "|")
		if !strings.Contains(joined, fragment) || !strings.Contains(strings.ToLower(joined), name+".exe") {
			t.Fatalf("standard candidates for %s are incomplete: %s", name, joined)
		}
	}
}
