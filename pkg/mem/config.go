package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/filelock"
)

type OllamaConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

// AnswerConfig is deliberately separate from OllamaConfig: embedding models
// such as bge-m3 are not chat models and must never be selected implicitly.
type AnswerConfig struct {
	BaseURL        string  `json:"base_url"`
	Model          string  `json:"model"`
	TimeoutSeconds int     `json:"timeout_seconds,omitempty"`
	MaxTokens      int     `json:"max_tokens,omitempty"`
	ContextChars   int     `json:"context_chars,omitempty"`
	Temperature    float64 `json:"temperature,omitempty"`
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

// IngestConfig contains project-local paths and OCR settings. Empty tool paths
// fall back to MEM_* environment variables, PATH, and standard Windows installs.
type IngestConfig struct {
	PDFToText     string  `json:"pdftotext,omitempty"`
	MuTool        string  `json:"mutool,omitempty"`
	PDFInfo       string  `json:"pdfinfo,omitempty"`
	PDFToPPM      string  `json:"pdftoppm,omitempty"`
	Python        string  `json:"python,omitempty"`
	DjVuText      string  `json:"djvutxt,omitempty"`
	DjVuUsed      string  `json:"djvused,omitempty"`
	DjVuRender    string  `json:"ddjvu,omitempty"`
	Tesseract     string  `json:"tesseract,omitempty"`
	TessdataDir   string  `json:"tessdata_dir,omitempty"`
	OCRLanguages  string  `json:"ocr_languages,omitempty"`
	OCRDPI        int     `json:"ocr_dpi,omitempty"`
	LowConfidence float64 `json:"ocr_low_confidence,omitempty"`
}

type Config struct {
	revision   string
	loadedPath string
	raw        map[string]json.RawMessage
	Backend    string       `json:"backend"` // "ollama" or "polza"
	Ollama     OllamaConfig `json:"ollama"`
	Answer     AnswerConfig `json:"answer,omitempty"`
	Polza      PolzaConfig  `json:"polza"`
	Chunking   ChunkConfig  `json:"chunking"`
	Ingest     IngestConfig `json:"ingest,omitempty"`
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
func LoadConfig() (applogResult0 *Config, applogResult1 error) {
	defer applog.Track("pkg.mem.LoadConfig")(&applogResult1)

	return loadConfigFile(ConfigPath())
}

// SaveConfig сохраняет конфиг в config.json
func SaveConfig(cfg *Config) (applogResult0 error) {
	defer applog.Track("pkg.mem.SaveConfig")(&applogResult0)

	path := ConfigPath()
	return saveConfigFile(path, cfg)
}

// === Per-directory варианты (для бота / multi-user) ===

// LoadConfigIn читает config.json из указанной директории .mem/.
func LoadConfigIn(dir string) (applogResult0 *Config, applogResult1 error) {
	defer applog.Track("pkg.mem.LoadConfigIn")(&applogResult1)

	return loadConfigFile(ConfigPathIn(dir))
}

func loadConfigFile(path string) (*Config, error) {
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
	if err := json.Unmarshal(data, &cfg.raw); err != nil || cfg.raw == nil {
		return nil, fmt.Errorf("конфигурация должна быть JSON-объектом")
	}
	cfg.revision = fmt.Sprintf("%x", sha256.Sum256(data))
	cfg.loadedPath, _ = filepath.Abs(path)
	return cfg, nil
}

var ErrConfigConflict = errors.New("настройки изменились; перечитайте их перед сохранением")

// Revision identifies the exact file loaded, including fields hidden from the UI.
func (c *Config) Revision() string { return c.revision }

// SaveConfigIn сохраняет конфиг в config.json внутри указанной директории.
func SaveConfigIn(dir string, cfg *Config) (applogResult0 error) {
	defer applog.Track("pkg.mem.SaveConfigIn")(&applogResult0)

	path := ConfigPathIn(dir)
	return saveConfigFile(path, cfg)
}

// saveConfigFile replaces the configuration atomically. Writing directly to
// config.json can truncate the only copy when a process, filesystem, or disk
// fails halfway through the write. The temporary file lives in the same
// directory so the final replace cannot cross filesystems.
func saveConfigFile(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("сохранение конфига: nil config")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := filelock.Try(filepath.Join(dir, "config.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if cfg.revision != "" && cfg.loadedPath == absPath {
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(current)) != cfg.revision {
			return ErrConfigConflict
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Preserve extension fields, including nested provider/OCR settings, on edits.
	if cfg.raw != nil {
		var updated map[string]json.RawMessage
		if err := json.Unmarshal(data, &updated); err != nil {
			return err
		}
		data, err = json.MarshalIndent(mergeConfigFields(configExtensions(cfg.raw, reflect.TypeOf(*cfg)), updated), "", "  ")
		if err != nil {
			return err
		}
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("создание временного конфига: %w", err)
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		_ = tmp.Close()
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("права временного конфига: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("запись временного конфига: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("синхронизация временного конфига: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие временного конфига: %w", err)
	}
	if err := replaceAtomicFile(tmpPath, path); err != nil {
		return fmt.Errorf("атомарная замена конфига: %w", err)
	}
	keepTemp = false
	cfg.revision = fmt.Sprintf("%x", sha256.Sum256(data))
	cfg.loadedPath = absPath
	return nil
}

func configExtensions(original map[string]json.RawMessage, typ reflect.Type) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(original))
	for key, value := range original {
		result[key] = value
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		key := strings.Split(field.Tag.Get("json"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		delete(result, key)
		if field.Type.Kind() == reflect.Struct {
			var nested map[string]json.RawMessage
			if json.Unmarshal(original[key], &nested) == nil {
				extra := configExtensions(nested, field.Type)
				if len(extra) > 0 {
					result[key], _ = json.Marshal(extra)
				}
			}
		}
	}
	return result
}

func mergeConfigFields(original, updated map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(original)+len(updated))
	for key, value := range original {
		merged[key] = value
	}
	for key, value := range updated {
		var oldObject, newObject map[string]json.RawMessage
		if json.Unmarshal(original[key], &oldObject) == nil && oldObject != nil && json.Unmarshal(value, &newObject) == nil && newObject != nil {
			value, _ = json.Marshal(mergeConfigFields(oldObject, newObject))
		}
		merged[key] = value
	}
	return merged
}
