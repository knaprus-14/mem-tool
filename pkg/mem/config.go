package mem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type OllamaConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

type PolzaConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type ChunkConfig struct {
	MaxSize  int    `json:"chunk_max_size"` // макс символов в чанке
	Overlap  int    `json:"chunk_overlap"`  // перекрытие между чанками
	Strategy string `json:"chunk_strategy"` // "paragraph", "sentence", "fixed"
}

type Config struct {
	Backend  string       `json:"backend"` // "ollama" or "polza"
	Ollama   OllamaConfig `json:"ollama"`
	Polza    PolzaConfig  `json:"polza"`
	Chunking ChunkConfig  `json:"chunking"`
}

// DefaultConfig возвращает дефолтный конфиг (алиас для DefaultLocalConfig)
func DefaultConfig() *Config {
	return DefaultLocalConfig()
}

// ConfigPath возвращает путь к локальному .mem/config.json
func ConfigPath() string {
	return MemConfigPath()
}

// LoadConfig читает config.json с диска. Если файла нет — возвращает ошибку.
func LoadConfig() (*Config, error) {
	path := ConfigPath()
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("конфиг не найден: %s", path)
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("ошибка чтения конфига: %w", err)
	}
	return cfg, nil
}

// SaveConfig сохраняет конфиг в config.json
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// === Per-directory варианты (для бота / multi-user) ===

// LoadConfigIn читает config.json из указанной директории .mem/.
func LoadConfigIn(dir string) (*Config, error) {
	path := ConfigPathIn(dir)
	cfg := DefaultLocalConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("конфиг не найден: %s", path)
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("ошибка чтения конфига: %w", err)
	}
	return cfg, nil
}

// SaveConfigIn сохраняет конфиг в config.json внутри указанной директории.
func SaveConfigIn(dir string, cfg *Config) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := ConfigPathIn(dir)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
