package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// RenderClassicMindMapWorkspace returns a self-contained local application.
// It intentionally uses no remote assets so the editor remains private and
// continues to work without Internet access.
func RenderClassicMindMapWorkspace(sessionToken string) (applogResult0 []byte, applogResult1 error) {
	defer applog.Track("pkg.mem.RenderClassicMindMapWorkspace")(&applogResult1)

	bootstrap, err := json.Marshal(map[string]any{
		"session_token": strings.TrimSpace(sessionToken),
		"node_kinds": []map[string]string{
			{"value": "topic", "label": "Тема"}, {"value": "subtopic", "label": "Подтема"},
			{"value": "fact", "label": "Факт"}, {"value": "note", "label": "Заметка"},
			{"value": "quote", "label": "Цитата"}, {"value": "question", "label": "Вопрос"},
			{"value": "task", "label": "Задача"}, {"value": "decision", "label": "Решение"},
			{"value": "link", "label": "Ссылка"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map workspace: %w", err)
	}
	page := strings.Replace(classicMindMapWorkspaceHTML, "__MEM_BOOTSTRAP__", string(bootstrap), 1)
	return []byte(page), nil
}

//go:embed web/classicMindMapWorkspaceHTML.html
var classicMindMapWorkspaceHTML string
