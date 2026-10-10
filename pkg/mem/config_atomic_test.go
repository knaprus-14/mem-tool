package mem

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/filelock"
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

func TestConfigEditsDetectConflictsPreserveExtensionsAndClearFields(t *testing.T) {
	dir := t.TempDir()
	path := ConfigPathIn(dir)
	data := []byte(`{"backend":"ollama","future":{"enabled":true},"polza":{"api_key":"keep-secret","future_provider":7},"answer":{"max_tokens":900,"future_answer":"keep"},"ingest":{"python":"private-tool","ocr_languages":"rus"}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	one, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := LoadConfigIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	one.Answer.MaxTokens = 0 // omitted fields must clear, not resurrect old values
	one.Ingest.OCRLanguages = ""
	one.Chunking.MaxSize = 800
	if err = SaveConfigIn(dir, one); err != nil {
		t.Fatal(err)
	}
	if one.Revision() == two.Revision() {
		t.Fatal("revision unchanged")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(written, &raw); err != nil {
		t.Fatal(err)
	}
	var answer, polza, ingest map[string]json.RawMessage
	json.Unmarshal(raw["answer"], &answer)
	json.Unmarshal(raw["polza"], &polza)
	json.Unmarshal(raw["ingest"], &ingest)
	if string(raw["future"]) == "" || string(answer["future_answer"]) != `"keep"` || string(polza["api_key"]) != `"keep-secret"` || string(polza["future_provider"]) != "7" || string(ingest["python"]) != `"private-tool"` {
		t.Fatalf("extensions or hidden fields lost: %s", written)
	}
	if _, ok := answer["max_tokens"]; ok {
		t.Fatal("old token budget resurrected")
	}
	if _, ok := ingest["ocr_languages"]; ok {
		t.Fatal("old OCR languages resurrected")
	}
	two.Chunking.MaxSize = 700
	if err = SaveConfigIn(dir, two); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("stale save: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(written) {
		t.Fatal("stale save modified file")
	}
	lock, err := filelock.Try(filepath.Join(dir, "config.lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = SaveConfigIn(dir, one)
	lock.Close()
	if !errors.Is(err, filelock.ErrBusy) {
		t.Fatalf("concurrent save: %v", err)
	}
	if err = SaveConfigIn(dir, one); err != nil {
		t.Fatal(err)
	}
	// A caller may intentionally copy a config to a new directory.
	if err = SaveConfigIn(t.TempDir(), one); err != nil {
		t.Fatal(err)
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
