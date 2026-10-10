package webapp

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type chunkPreviewInput struct {
	Text     string `json:"text"`
	Size     int    `json:"size"`
	Overlap  int    `json:"overlap"`
	Strategy string `json:"strategy"`
}

func (a *App) settingsToolRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id, action string) {
	base, err := a.manager.Resolve(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if r.Method != "POST" {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	switch action {
	case "preview":
		var in chunkPreviewInput
		if !decodeLimit(w, r, &in, 256<<10) {
			return
		}
		if !utf8.ValidString(in.Text) || strings.TrimSpace(in.Text) == "" || utf8.RuneCountInString(in.Text) > 10000 || in.Size < 100 || in.Size > mem.MaxEmbeddingChars || in.Overlap < 0 || in.Overlap > 1000 || in.Overlap >= in.Size || (in.Strategy != "paragraph" && in.Strategy != "sentence" && in.Strategy != "fixed") {
			writeError(w, 400, "Введите до 10 000 символов; размер 100–2000, перекрытие 0–1000 и меньше размера.")
			return
		}
		chunks := mem.ChunkDocument(in.Text, in.Size, in.Overlap, in.Strategy)
		total := len(chunks)
		if len(chunks) > 100 {
			chunks = chunks[:100]
		}
		writeJSON(w, 200, struct {
			Chunks    []mem.Chunk `json:"chunks"`
			Total     int         `json:"total"`
			Truncated bool        `json:"truncated"`
		}{chunks, total, total > len(chunks)})
	case "check-model":
		var in struct {
			Revision string `json:"revision"`
			Role     string `json:"role"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Role != "embedding" && in.Role != "answer" {
			writeError(w, 400, "Выберите модель поиска или ответов.")
			return
		}
		cfg, e := mem.LoadConfigIn(filepath.Join(base.Root, mem.MemDirName))
		if e != nil {
			writeError(w, 500, "Не удалось прочитать настройки")
			return
		}
		if in.Revision == "" || in.Revision != cfg.Revision() {
			writeError(w, 409, "Настройки изменились. Перечитайте их перед проверкой.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		dimensions := 0
		if in.Role == "embedding" {
			var vector []float32
			vector, e = mem.GetEmbeddingContext(ctx, cfg, "Проверка подключения mem-tool")
			dimensions = len(vector)
		} else {
			answer := cfg.Answer.WithDefaults()
			answer.MaxTokens = 64
			answer.TimeoutSeconds = 20
			var provider *mem.OllamaAnswerProvider
			provider, e = mem.NewOllamaAnswerProvider(answer)
			if e == nil {
				var text string
				text, e = provider.Generate(ctx, mem.AnswerRequest{Model: answer.Model, Prompt: "Ответь одним словом: ОК", MaxTokens: 64})
				if strings.TrimSpace(text) == "" && e == nil {
					writeError(w, 502, "Модель вернула пустой ответ.")
					return
				}
			}
		}
		if e != nil {
			writeError(w, 502, "Модель не прошла проверку за 20 секунд. Проверьте имя, доступность подключения и его настройки через CLI или у администратора.")
			return
		}
		writeJSON(w, 200, struct {
			OK         bool `json:"ok"`
			Dimensions int  `json:"dimensions,omitempty"`
		}{true, dimensions})
	default:
		writeError(w, 404, "Действие не найдено")
	}
}
