package mem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveConfigInAtomicallyReplacesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	first := DefaultLocalConfig()
	first.Backend = "ollama"
	if err := SaveConfigIn(dir, first); err != nil {
		t.Fatalf("save first config: %v", err)
	}

	second := DefaultLocalConfig()
	second.Backend = "polza"
	second.Polza.APIKey = "secret"
	if err := SaveConfigIn(dir, second); err != nil {
		t.Fatalf("replace config: %v", err)
	}

	loaded, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatalf("load replaced config: %v", err)
	}
	if loaded.Backend != second.Backend || loaded.Polza.APIKey != second.Polza.APIKey {
		t.Fatalf("loaded config does not match replacement: %#v", loaded)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files remain: %v", matches)
	}
	if info, err := os.Stat(ConfigPathIn(dir)); err != nil {
		t.Fatal(err)
	} else if info.Size() == 0 {
		t.Fatal("saved config is empty")
	}
}

func TestSaveConfigInRejectsNilConfigWithoutDamagingExistingFile(t *testing.T) {
	dir := t.TempDir()
	want := DefaultLocalConfig()
	if err := SaveConfigIn(dir, want); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ConfigPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigIn(dir, nil); err == nil {
		t.Fatal("nil config was accepted")
	}
	after, err := os.ReadFile(ConfigPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed save changed the existing config")
	}
}
