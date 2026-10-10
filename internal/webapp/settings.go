package webapp

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

// ModelChoice contains names only; endpoints and credentials remain administrator-owned.
type ModelChoice struct {
	Role    string `json:"role"`
	Backend string `json:"backend"`
	Model   string `json:"model"`
}

type settingsView struct {
	workspace.Settings
	Local  bool          `json:"local"`
	Models []ModelChoice `json:"models"`
}

type settingsUpdate struct {
	workspace.Settings
	AcceptEmbeddingChange bool `json:"accept_embedding_change"`
}

func validModelName(model string) bool {
	return model != "" && utf8.ValidString(model) && utf8.RuneCountInString(model) <= 200 && strings.IndexFunc(model, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) == -1
}

func (a *App) modelAllowed(role, backend, model string) bool {
	if a.cfg.Local {
		return true
	}
	for _, choice := range a.cfg.Models {
		if choice.Role == role && choice.Backend == backend && choice.Model == model {
			return true
		}
	}
	return false
}

func (a *App) settingsRoute(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id string) {
	item, err := a.manager.Resolve(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		writeError(w, 405, "Метод не поддерживается")
		return
	}
	dir := filepath.Join(item.Root, mem.MemDirName)
	cfg, err := mem.LoadConfigIn(dir)
	if err != nil {
		writeError(w, 500, "Не удалось прочитать настройки базы")
		return
	}
	current := workspace.SettingsFromConfig(cfg)
	if r.Method == "POST" {
		var body settingsUpdate
		if !decode(w, r, &body) {
			return
		}
		if body.Revision == "" || body.Revision != current.Revision {
			writeError(w, 409, "Настройки изменились. Обновите форму и повторите изменения.")
			return
		}
		body.EmbeddingModel = strings.TrimSpace(body.EmbeddingModel)
		body.AnswerModel = strings.TrimSpace(body.AnswerModel)
		if (body.Backend != "ollama" && body.Backend != "polza") || !validModelName(body.EmbeddingModel) || (body.AnswerModel != "" && !validModelName(body.AnswerModel)) {
			writeError(w, 400, "Проверьте подключение и названия моделей: до 200 символов без пробелов.")
			return
		}
		embeddingChanged := body.Backend != current.Backend || body.EmbeddingModel != current.EmbeddingModel
		if (embeddingChanged && !a.modelAllowed("embedding", body.Backend, body.EmbeddingModel)) || (body.AnswerModel != "" && body.AnswerModel != current.AnswerModel && !a.modelAllowed("answer", "ollama", body.AnswerModel)) {
			writeError(w, 400, "Выберите модель из списка, разрешённого администратором.")
			return
		}
		if embeddingChanged && !body.AcceptEmbeddingChange {
			writeError(w, 400, "Подтвердите смену модели: старые векторы автоматически не пересчитываются.")
			return
		}
		if embeddingChanged && body.Backend == "polza" && strings.TrimSpace(cfg.Polza.APIKey) == "" {
			writeError(w, 400, "Подключение Polza ещё не настроено. Ключ задаётся через CLI или администратором.")
			return
		}
		if body.ChunkSize < 100 || body.ChunkSize > mem.MaxEmbeddingChars || body.ChunkOverlap < 0 || body.ChunkOverlap > 1000 || body.ChunkOverlap >= body.ChunkSize || (body.ChunkStrategy != "paragraph" && body.ChunkStrategy != "sentence" && body.ChunkStrategy != "fixed") {
			writeError(w, 400, "Размер фрагмента: 100–2000, перекрытие: 0–1000 и меньше размера. Выберите способ разбиения.")
			return
		}
		// Preserve larger pre-existing CLI budgets; the web query applies its own runtime caps.
		if body.AnswerTimeout < 1 || body.AnswerTimeout > mem.MaxAnswerTimeoutSeconds || body.AnswerMaxTokens < 1 || body.AnswerMaxTokens > mem.MaxAnswerTokens || body.AnswerContextChars < 1 || body.AnswerContextChars > mem.MaxAnswerContextChars {
			writeError(w, 400, "Бюджет ответа: 1–3600 секунд, 1–100000 токенов, 1–1000000 символов контекста.")
			return
		}
		cfg.Backend = body.Backend
		if body.Backend == "polza" {
			cfg.Polza.Model = body.EmbeddingModel
		} else {
			cfg.Ollama.Model = body.EmbeddingModel
		}
		cfg.Answer.Model = body.AnswerModel
		cfg.Answer.TimeoutSeconds, cfg.Answer.MaxTokens, cfg.Answer.ContextChars = body.AnswerTimeout, body.AnswerMaxTokens, body.AnswerContextChars
		cfg.Chunking = mem.ChunkConfig{MaxSize: body.ChunkSize, Overlap: body.ChunkOverlap, Strategy: body.ChunkStrategy}
		if err = r.Context().Err(); err != nil {
			writeError(w, 408, "Запрос отменён")
			return
		}
		if err = mem.SaveConfigIn(dir, cfg); err != nil {
			if errors.Is(err, mem.ErrConfigConflict) || errors.Is(err, filelock.ErrBusy) {
				writeError(w, 409, "Настройки изменены или сохраняются другим клиентом. Обновите форму.")
			} else {
				writeError(w, 500, "Не удалось сохранить настройки базы")
			}
			return
		}
		current = workspace.SettingsFromConfig(cfg)
	}
	choices := append([]ModelChoice{}, a.cfg.Models...)
	writeJSON(w, 200, settingsView{Settings: current, Local: a.cfg.Local, Models: choices})
}
