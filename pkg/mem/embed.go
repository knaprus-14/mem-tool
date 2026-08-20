package mem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"
)

// maxEmbedChars — максимальное количество символов (рун) для одного эмбеддинга.
// bge-m3 через Ollama имеет лимит ~2000 токенов для embedding endpoint.
// Ставим 2000 как безопасный предел.
const maxEmbedChars = 2000

// === Ollama ===

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func embedOllama(cfg *Config, text string) ([]float32, error) {
	return embedOllamaContext(context.Background(), cfg, text)
}

func embedOllamaContext(ctx context.Context, cfg *Config, text string) ([]float32, error) {
	if err := validateEmbeddingText(text); err != nil {
		return nil, err
	}

	url := cfg.Ollama.BaseURL + "/api/embeddings"
	req := ollamaEmbedRequest{
		Model:  cfg.Ollama.Model,
		Prompt: text,
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: ошибка запроса: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: не удалось connected: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: статус %d: %s", resp.StatusCode, string(respBody))
	}

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: ошибка ответа: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama: пустой эмбеддинг")
	}

	return result.Embedding, nil
}

// === Polza AI (OpenAI-совместимый) ===

type polzaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type polzaEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func embedPolza(cfg *Config, text string) ([]float32, error) {
	return embedPolzaContext(context.Background(), cfg, text)
}

func embedPolzaContext(ctx context.Context, cfg *Config, text string) ([]float32, error) {
	if cfg.Polza.APIKey == "" {
		return nil, fmt.Errorf("polza: не указан API ключ. Настрой: mem config set-polza-key <key>")
	}

	if err := validateEmbeddingText(text); err != nil {
		return nil, err
	}

	url := cfg.Polza.BaseURL + "/embeddings"
	req := polzaEmbedRequest{
		Model: cfg.Polza.Model,
		Input: text,
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("polza: ошибка запроса: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Polza.APIKey)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("polza: не удалось connected: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("polza: статус %d: %s", resp.StatusCode, string(respBody))
	}

	var result polzaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("polza: ошибка ответа: %w", err)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("polza: пустой эмбеддинг")
	}

	return result.Data[0].Embedding, nil
}

func validateEmbeddingText(text string) error {
	length := utf8.RuneCountInString(text)
	if length > maxEmbedChars {
		return fmt.Errorf("текст для embedding слишком длинный: %d символов, максимум %d; уменьшите chunk_max_size", length, maxEmbedChars)
	}
	return nil
}

// === Общий интерфейс ===

func GetEmbedding(cfg *Config, text string) ([]float32, error) {
	return GetEmbeddingContext(context.Background(), cfg, text)
}

// GetEmbeddingContext builds an embedding while allowing callers such as
// document import to cancel an in-flight Ollama or Polza HTTP request.
func GetEmbeddingContext(ctx context.Context, cfg *Config, text string) ([]float32, error) {
	switch cfg.Backend {
	case "ollama":
		return embedOllamaContext(ctx, cfg, text)
	case "polza":
		return embedPolzaContext(ctx, cfg, text)
	default:
		return nil, fmt.Errorf("неизвестный бэкенд: %s (используй ollama или polza)", cfg.Backend)
	}
}
