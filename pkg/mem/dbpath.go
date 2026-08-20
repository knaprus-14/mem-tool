package mem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MemMeta — метаданные локальной базы
type MemMeta struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// MemDirName — имя скрытой директории с локальной базой
const MemDirName = ".mem"

// EnvGlobalDir — имя env-переменной для пути к глобальной базе знаний.
// Если не задана, используется DefaultGlobalDir() (~/global-mem/.mem на каждой ОС).
const EnvGlobalDir = "MEM_GLOBAL_DIR"

// memDir возвращает путь к директории .mem/ относительно cwd
func MemDir() string {
	return MemDirName
}

// DefaultGlobalDir возвращает ОС-зависимый путь по умолчанию к глобальной базе:
// Windows: %USERPROFILE%\global-mem\.mem
// Unix:    $HOME/global-mem/.mem
// Можно переопределить через env MEM_GLOBAL_DIR.
func DefaultGlobalDir() string {
	if env := os.Getenv(EnvGlobalDir); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// fallback: относительный путь рядом с cwd
		return filepath.Join("global-mem", MemDirName)
	}
	return filepath.Join(home, "global-mem", MemDirName)
}

// GlobalMemDir возвращает путь к глобальной базе знаний. Используется для
// команд с флагом --global или --dir. Путь берётся из env MEM_GLOBAL_DIR,
// иначе — из DefaultGlobalDir().
func GlobalMemDir() string {
	return DefaultGlobalDir()
}

// memConfigPath возвращает путь к .mem/config.json
func MemConfigPath() string {
	return filepath.Join(MemDirName, "config.json")
}

// memStorePath возвращает путь к .mem/store.db (SQLite-база)
func MemStorePath() string {
	return filepath.Join(MemDirName, "store.db")
}

// memMetaPath возвращает путь к .mem/meta.json
func MemMetaPath() string {
	return filepath.Join(MemDirName, "meta.json")
}

// memExists проверяет, существует ли .mem/ в текущей директории
func MemExists() bool {
	info, err := os.Stat(MemDirName)
	return err == nil && info.IsDir()
}

// defaultLocalConfig возвращает дефолтный конфиг для новой локальной базы
func DefaultLocalConfig() *Config {
	return &Config{
		Backend: "ollama",
		Ollama: OllamaConfig{
			BaseURL: "http://localhost:11434",
			Model:   "bge-m3",
		},
		Answer: AnswerConfig{
			BaseURL:        "http://localhost:11434",
			Model:          "",
			TimeoutSeconds: DefaultAnswerTimeoutSeconds,
			MaxTokens:      DefaultAnswerMaxTokens,
			ContextChars:   DefaultAnswerContextChars,
			Temperature:    DefaultAnswerTemperature,
		},
		Polza: PolzaConfig{
			BaseURL: "https://polza.ai/api/v1",
			APIKey:  "",
			Model:   "openai/text-embedding-3-small",
		},
		Chunking: ChunkConfig{
			MaxSize:  1000,
			Overlap:  100,
			Strategy: "paragraph",
		},
		Ingest: IngestConfig{
			OCRLanguages:  "rus+eng",
			OCRDPI:        300,
			LowConfidence: 65,
		},
	}
}

// initMem создаёт .mem/ с дефолтным config.json и meta.json.
// SQLite-база store.db создаётся автоматически при первом openStore.
// Если .mem/ уже существует — возвращает ошибку.
func InitMem() error {
	if MemExists() {
		return fmt.Errorf(".mem/ уже существует в текущей папке")
	}

	// Создаём директорию
	if err := os.MkdirAll(MemDirName, 0700); err != nil {
		return fmt.Errorf("не удалось создать %s/: %w", MemDirName, err)
	}

	// Пишем дефолтный config.json
	cfg := DefaultLocalConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации config: %w", err)
	}
	if err := os.WriteFile(MemConfigPath(), data, 0600); err != nil {
		// Откатываем создание директории
		os.RemoveAll(MemDirName)
		return fmt.Errorf("не удалось записать %s: %w", MemConfigPath(), err)
	}

	// Пишем meta.json с именем = basename(cwd) и датой создания
	cwd, _ := os.Getwd()
	name := filepath.Base(cwd)
	meta := MemMeta{
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		os.RemoveAll(MemDirName)
		return fmt.Errorf("ошибка сериализации meta: %w", err)
	}
	if err := os.WriteFile(MemMetaPath(), data, 0600); err != nil {
		os.RemoveAll(MemDirName)
		return fmt.Errorf("не удалось записать %s: %w", MemMetaPath(), err)
	}

	return nil
}

// ensureMem автоматически создаёт .mem/, если её нет и команда это позволяет.
// Возвращает true, если база есть (только что создана или уже была).
func EnsureMem(allowAutocreate bool) (bool, error) {
	if MemExists() {
		return true, nil
	}
	if !allowAutocreate {
		return false, nil
	}
	if err := InitMem(); err != nil {
		return false, err
	}
	return true, nil
}

// === Per-directory варианты (для бота / multi-user) ===
// Эти функции принимают явный путь к директории .mem/, а не используют cwd.
// Используются, когда нужно работать с произвольной базой (например, per-user).

// MemExistsIn проверяет, существует ли директория dir
func MemExistsIn(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// ConfigPathIn возвращает путь к config.json внутри dir
func ConfigPathIn(dir string) string {
	return filepath.Join(dir, "config.json")
}

// StorePathIn возвращает путь к store.db внутри dir
func StorePathIn(dir string) string {
	return filepath.Join(dir, "store.db")
}

// MetaPathIn возвращает путь к meta.json внутри dir
func MetaPathIn(dir string) string {
	return filepath.Join(dir, "meta.json")
}

// InitMemIn создаёт .mem/ в указанной директории (с config.json и meta.json).
// Если уже существует — возвращает ошибку.
// name используется в meta.json как имя базы.
func InitMemIn(dir string, name string) error {
	if MemExistsIn(dir) {
		return fmt.Errorf("%s/ уже существует", dir)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("не удалось создать %s/: %w", dir, err)
	}

	// config.json
	cfg := DefaultLocalConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("сериализация config: %w", err)
	}
	if err := os.WriteFile(ConfigPathIn(dir), data, 0600); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("запись config: %w", err)
	}

	// meta.json
	if name == "" {
		name = filepath.Base(filepath.Dir(dir))
		if name == "." || name == "/" {
			name = "mem"
		}
	}
	meta := MemMeta{
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("сериализация meta: %w", err)
	}
	if err := os.WriteFile(MetaPathIn(dir), data, 0600); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("запись meta: %w", err)
	}

	return nil
}
