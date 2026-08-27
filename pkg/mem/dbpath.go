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

// ResolveDatabaseRoot resolves an existing local database from either its
// project directory or the .mem directory itself. The returned path is the
// absolute project directory whose direct child is .mem.
func ResolveDatabaseRoot(path string) (string, error) {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("не удалось определить текущий каталог: %w", err)
		}
		path = cwd
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("не удалось определить абсолютный путь %s: %w", path, err)
	}

	root := abs
	memPath := filepath.Join(root, MemDirName)
	if filepath.Base(abs) == MemDirName {
		root = filepath.Dir(abs)
		memPath = abs
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("каталог базы %s недоступен: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("путь базы %s не является каталогом", root)
	}
	info, err = os.Stat(memPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("локальная база не найдена: %s", memPath)
		}
		return "", fmt.Errorf("локальная база %s недоступна: %w", memPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("путь базы %s не является каталогом", memPath)
	}
	if _, err := LoadConfigIn(memPath); err != nil {
		return "", fmt.Errorf("локальная база %s не инициализирована или повреждена: %w", memPath, err)
	}
	return root, nil
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
	cfg := DefaultLocalConfig()
	configData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации config: %w", err)
	}
	cwd, _ := os.Getwd()
	meta := MemMeta{
		Name:      filepath.Base(cwd),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации meta: %w", err)
	}
	if err := createMemDirectory(MemDirName, configData, metaData); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf(".mem/ уже существует в текущей папке")
		}
		return fmt.Errorf("не удалось инициализировать %s/: %w", MemDirName, err)
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
	cfg := DefaultLocalConfig()
	configData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("сериализация config: %w", err)
	}
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
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("сериализация meta: %w", err)
	}
	parent := filepath.Dir(filepath.Clean(dir))
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("создание родительского каталога %s: %w", parent, err)
	}
	if err := createMemDirectory(dir, configData, metaData); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s/ уже существует", dir)
		}
		return fmt.Errorf("инициализация %s/: %w", dir, err)
	}
	return nil
}

func createMemDirectory(dir string, configData, metaData []byte) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	configPath := ConfigPathIn(dir)
	if err := writeMemInitializationFile(configPath, configData, 0600); err != nil {
		_ = os.Remove(configPath)
		_ = os.Remove(dir)
		return fmt.Errorf("запись config: %w", err)
	}
	metaPath := MetaPathIn(dir)
	if err := writeMemInitializationFile(metaPath, metaData, 0600); err != nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(configPath)
		_ = os.Remove(dir)
		return fmt.Errorf("запись meta: %w", err)
	}
	return nil
}

var writeMemInitializationFile = os.WriteFile
