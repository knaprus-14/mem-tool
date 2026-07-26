package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// DatabaseConfig — настройки, специфичные для одной базы данных.
// Любое пустое/нулевое поле наследуется из глобального Config.
type DatabaseConfig struct {
	Backend  string `json:"backend,omitempty"`  // "ollama" или "polza"; пусто = глобальный
	Ollama   OllamaConfig `json:"ollama,omitempty"`
	Polza    PolzaConfig  `json:"polza,omitempty"`
	Chunking ChunkConfig  `json:"chunking,omitempty"`
}

type Config struct {
	Backend   string       `json:"backend"` // "ollama" or "polza"
	Ollama    OllamaConfig `json:"ollama"`
	Polza     PolzaConfig  `json:"polza"`
	StorePath string       `json:"store_path"`
	Chunking  ChunkConfig  `json:"chunking"`

	// === Множественные базы (v1.10) ===
	DatabasesDir string                     `json:"databases_dir"` // каталог для новых баз
	CurrentDB    string                     `json:"current_db"`     // имя активной базы ("default" = старая)
	Databases    map[string]DatabaseConfig  `json:"databases"`     // per-db настройки по имени
}

func defaultConfig() *Config {
	home, _ := os.UserHomeDir()
	storePath := filepath.Join(home, ".mem")

	return &Config{
		Backend: "ollama",
		Ollama: OllamaConfig{
			BaseURL: "http://localhost:11434",
			Model:   "bge-m3",
		},
		Polza: PolzaConfig{
			BaseURL: "https://polza.ai/api/v1",
			APIKey:  "",
			Model:   "openai/text-embedding-3-small",
		},
		StorePath: storePath,
		Chunking: ChunkConfig{
			MaxSize:  1000,
			Overlap:  100,
			Strategy: "paragraph",
		},
		DatabasesDir: filepath.Join(storePath, "databases"),
		CurrentDB:    "default",
		Databases:    map[string]DatabaseConfig{},
	}
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mem", "config.json"), nil
}

// databasesDir возвращает каталог, где хранятся все именованные базы.
// Создаёт каталог, если его ещё нет.
func databasesDir(cfg *Config) (string, error) {
	dir := cfg.DatabasesDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".mem", "databases")
		cfg.DatabasesDir = dir
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// dbFilePath возвращает путь к JSONL-файлу для именованной базы.
// "default" → старая база cfg.StorePath/store.jsonl (обратная совместимость).
// Любое другое имя → databasesDir/<safeName>.jsonl.
func dbFilePath(cfg *Config, name string) (string, error) {
	if name == "" || name == "default" {
		return filepath.Join(cfg.StorePath, "store.jsonl"), nil
	}
	dir, err := databasesDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safeFileName(name)+".jsonl"), nil
}

// safeFileName нормализует имя базы для использования в имени файла:
// - заменяет пробелы и спецсимволы на '_'
// - оставляет латиницу, цифры, _, -, кириллицу
func safeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-',
			r >= 0x0400 && r <= 0x04FF: // кириллица
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "db"
	}
	return out
}

// isValidDBName проверяет, что имя базы допустимо (не пустое, не "default",
// не содержит только спецсимволов).
func isValidDBName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "default" {
		return false
	}
	return true
}

// mergeDBConfig возвращает эффективные настройки для именованной базы:
// per-db поля перекрывают глобальные, остальное наследуется.
func mergeDBConfig(cfg *Config, name string) (string, OllamaConfig, PolzaConfig, ChunkConfig) {
	backend := cfg.Backend
	ollama := cfg.Ollama
	polza := cfg.Polza
	chunk := cfg.Chunking

	if dbCfg, ok := cfg.Databases[name]; ok {
		if dbCfg.Backend != "" {
			backend = dbCfg.Backend
		}
		if dbCfg.Ollama.Model != "" || dbCfg.Ollama.BaseURL != "" {
			if dbCfg.Ollama.Model != "" {
				ollama.Model = dbCfg.Ollama.Model
			}
			if dbCfg.Ollama.BaseURL != "" {
				ollama.BaseURL = dbCfg.Ollama.BaseURL
			}
		}
		if dbCfg.Polza.APIKey != "" || dbCfg.Polza.Model != "" || dbCfg.Polza.BaseURL != "" {
			if dbCfg.Polza.APIKey != "" {
				polza.APIKey = dbCfg.Polza.APIKey
			}
			if dbCfg.Polza.Model != "" {
				polza.Model = dbCfg.Polza.Model
			}
			if dbCfg.Polza.BaseURL != "" {
				polza.BaseURL = dbCfg.Polza.BaseURL
			}
		}
		if dbCfg.Chunking.Strategy != "" {
			chunk.Strategy = dbCfg.Chunking.Strategy
			chunk.MaxSize = dbCfg.Chunking.MaxSize
			chunk.Overlap = dbCfg.Chunking.Overlap
		}
	}
	return backend, ollama, polza, chunk
}

func loadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("ошибка чтения конфига: %w", err)
	}
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func handleConfig(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	cmd := args[0]
	switch cmd {
	case "set-backend":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-backend <ollama|polza>")
		}
		backend := args[1]
		if backend != "ollama" && backend != "polza" {
			return fmt.Errorf("бэкенд должен быть 'ollama' или 'polza'")
		}
		cfg.Backend = backend
		return saveConfig(cfg)

	case "set-polza-key":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-polza-key <api_key>")
		}
		cfg.Polza.APIKey = args[1]
		return saveConfig(cfg)

	case "set-polza-model":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-polza-model <model_name>")
		}
		cfg.Polza.Model = args[1]
		return saveConfig(cfg)

	case "set-ollama-model":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-ollama-model <model_name>")
		}
		cfg.Ollama.Model = args[1]
		return saveConfig(cfg)

	case "set-chunk-size":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-size <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 100 || n > 10000 {
			return fmt.Errorf("размер чанка должен быть от 100 до 10000")
		}
		cfg.Chunking.MaxSize = n
		return saveConfig(cfg)

	case "set-chunk-overlap":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-overlap <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 1000 {
			return fmt.Errorf("перекрытие должно быть от 0 до 1000")
		}
		cfg.Chunking.Overlap = n
		return saveConfig(cfg)

	case "set-chunk-strategy":
		if len(args) < 2 {
			return fmt.Errorf("использование: mem config set-chunk-strategy <paragraph|sentence|fixed>")
		}
		strategy := args[1]
		if strategy != "paragraph" && strategy != "sentence" && strategy != "fixed" {
			return fmt.Errorf("стратегия должна быть: paragraph, sentence или fixed")
		}
		cfg.Chunking.Strategy = strategy
		return saveConfig(cfg)

	default:
		return fmt.Errorf("неизвестная команда: %s", cmd)
	}
}
