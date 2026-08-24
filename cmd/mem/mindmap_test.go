package main

import (
	"encoding/json"
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
		return handleMindMap(store, []string{"create", "Моя карта", "--description", "Проверка"})
	})
	if err != nil || !strings.Contains(stdout, "Моя карта") || strings.Contains(stdout, "mm-") {
		t.Fatalf("human create output exposes internals or failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(store, []string{"add-node", "Моя карта", "Моя карта", "Первая ветвь", "--summary", "Кратко"})
	})
	if err != nil || !strings.Contains(stdout, "Первая ветвь") {
		t.Fatalf("add by labels failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(store, []string{"show", "Моя карта", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc mem.ClassicMindMapDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Map.Title != "Моя карта" || len(doc.Nodes) != 2 || doc.Map.Revision != 2 {
		t.Fatalf("invalid JSON show: doc=%#v stdout=%q err=%v", doc, stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(store, []string{"history", "Моя карта"})
	})
	if err != nil || !strings.Contains(stdout, "добавление узла") || strings.Contains(stdout, "mmn-") {
		t.Fatalf("human history is not readable: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(store, []string{"undo", "Моя карта"})
	})
	if err != nil || !strings.Contains(stdout, "отменено") {
		t.Fatalf("human undo failed: stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(store, []string{"redo", "Моя карта"})
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
	if err := handleMindMap(store, []string{"create", "Clear text"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(store, []string{"add-node", "Clear text", "Clear text", "Child", "--summary", "temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(store, []string{"edit-node", "Clear text", "Child", "--summary", ""}); err != nil {
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
	if err := handleMindMap(store, []string{"list", "--unknown"}); err == nil || !strings.Contains(err.Error(), "неизвестный флаг") {
		t.Fatalf("unknown flag was accepted: %v", err)
	}
}
