package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMindMapWorkbenchCLIListsTemplatesCreatesComparesAndBuildsStudyPack(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stdout, _, err := captureCLIStreams(func() error { return handleMindMap(nil, store, []string{"templates"}) })
	if err != nil || !strings.Contains(stdout, "study") || !strings.Contains(stdout, "Изучение темы") {
		t.Fatalf("templates output=%q err=%v", stdout, err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"template-create", "study", "Учебная карта", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var created mem.ClassicMindMapDocument
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || len(created.Nodes) != 6 {
		t.Fatalf("created=%#v output=%q err=%v", created, stdout, err)
	}
	copyDoc, err := store.DuplicateClassicMindMap(created.Map.ID, "Учебная карта 2")
	if err != nil {
		t.Fatal(err)
	}
	var child mem.ClassicMindMapNode
	for _, node := range copyDoc.Nodes {
		if node.ParentID == copyDoc.Map.RootNodeID {
			child = node
			break
		}
	}
	summary := "Материал для карточки"
	copyDoc, _, err = store.EditClassicMindMapNode(copyDoc.Map.ID, child.ID, mem.ClassicMindMapNodePatch{Summary: &summary}, copyDoc.Map.Revision, "test", "study")
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"compare", created.Map.ID, copyDoc.Map.ID})
	})
	if err != nil || strings.Contains(stdout, "изменено: 0") || !strings.Contains(stdout, "изменено:") {
		t.Fatalf("compare output=%q err=%v", stdout, err)
	}
	output := filepath.Join(t.TempDir(), "study.md")
	stdout, _, err = captureCLIStreams(func() error {
		return handleMindMap(nil, store, []string{"study", copyDoc.Map.ID, child.ID, "--output", output})
	})
	if err != nil || !strings.Contains(stdout, "1 карточек") {
		t.Fatalf("study output=%q err=%v", stdout, err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil || !strings.Contains(string(data), "Материал для карточки") {
		t.Fatalf("study file=%q err=%v", data, readErr)
	}
}

func TestMindMapWorkbenchCLIParsingIsStrict(t *testing.T) {
	if _, _, err := parseMindMapWorkbenchArgs([]string{"map", "--unknown"}); err == nil {
		t.Fatal("unknown workbench flag was accepted")
	}
	if _, _, err := parseMindMapWorkbenchArgs([]string{"map", "--json", "--output", "x.md"}); err == nil {
		t.Fatal("conflicting workbench outputs were accepted")
	}
}
