package mem

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeLearningExportProducesAnkiMarkdownAndCSV(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil || !route.Ready {
		t.Fatalf("learning route is not ready: route=%#v err=%v", route, err)
	}
	session, err := store.startKnowledgeLearningSessionAt(KnowledgeLearningSessionStartRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedRouteDigest: route.Digest,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.gradeKnowledgeLearningItemAt(KnowledgeLearningGradeRequest{
		SessionID: session.ID, NodeID: session.Items[0].NodeID, Grade: KnowledgeLearningGradeGood,
	}, now); err != nil {
		t.Fatal(err)
	}
	base := KnowledgeLearningExportRequest{
		Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedRouteDigest: route.Digest, Title: "Закон Ома",
	}

	t.Run("anki", func(t *testing.T) {
		request := base
		request.Format = KnowledgeLearningExportAnki
		exported, err := store.exportKnowledgeLearningAt(request, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		text := string(exported.Content)
		for _, expected := range []string{"#separator:Tab", "#html:true", "#deck:Закон Ома", "#columns:Front\tBack\tTags", `"<span style=""display:none"">mem-tool:session-card</span>Формула"`, "Как найти ток?", "I = U / R", "стр. 4", "mem_tool::card", "следующее повторение 2026-08-25T10:00:00Z"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("Anki export is missing %q:\n%s", expected, text)
			}
		}
		if exported.Filename != "mem-learning-anki.txt" || exported.ContentType != "text/plain; charset=utf-8" {
			t.Fatalf("unexpected Anki metadata: %#v", exported)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		request := base
		request.Format = KnowledgeLearningExportMarkdown
		exported, err := store.exportKnowledgeLearningAt(request, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		text := string(exported.Content)
		for _, expected := range []string{"# Закон Ома", "## Этап 1", "интервал 1 дн", "Попыток: 1", "Manifest:", "Route:"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("Markdown export is missing %q:\n%s", expected, text)
			}
		}
	})

	t.Run("csv", func(t *testing.T) {
		request := base
		request.Format = KnowledgeLearningExportCSV
		exported, err := store.exportKnowledgeLearningAt(request, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(exported.Content, []byte{0xef, 0xbb, 0xbf}) {
			t.Fatal("learning CSV has no UTF-8 BOM")
		}
		text := string(exported.Content)
		for _, expected := range []string{"Следующее повторение", "session-card", "2026-08-25T10:00:00Z", ";86400;", "Разделить напряжение"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("CSV export is missing %q:\n%s", expected, text)
			}
		}
	})
}

func TestKnowledgeLearningExportFailsClosed(t *testing.T) {
	store, selection, manifest := knowledgeLearningSessionFixture(t)
	defer store.Close()
	route, err := store.BuildKnowledgeLearningRoute(KnowledgeLearningRouteRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest})
	if err != nil {
		t.Fatal(err)
	}
	request := KnowledgeLearningExportRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, ExpectedRouteDigest: "sha256:wrong", Format: KnowledgeLearningExportMarkdown}
	if _, err := store.ExportKnowledgeLearning(request); !errors.Is(err, ErrKnowledgeSelectionChanged) {
		t.Fatalf("changed route was exported: %v", err)
	}
	request.ExpectedRouteDigest = route.Digest
	request.Title = "unsafe\n#deck:other"
	if _, err := store.ExportKnowledgeLearning(request); err == nil || !strings.Contains(err.Error(), "one line") {
		t.Fatalf("multiline title was accepted: %v", err)
	}
	request.Title = "safe"
	request.Format = "apkg"
	if _, err := store.ExportKnowledgeLearning(request); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported format was accepted: %v", err)
	}
}
