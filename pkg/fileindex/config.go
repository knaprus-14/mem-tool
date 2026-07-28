package fileindex

import (
	"fmt"
	"os"

	"github.com/knaprus-14/mem-tool/pkg/mem"
)

// defaultFileIndexConfig — дефолтный конфиг для нового .fileindex/.
// По сути дублирует mem.DefaultLocalConfig() — но мы не импортируем mem здесь,
// потому что mem импортирует этот пакет? Нет, не импортирует — но всё равно
// хотим независимости: конфиг .fileindex/ живёт своей жизнью.
//
// Реализация через прямой JSON-marshal с минимальной структурой, чтобы
// mem-index мог читать .fileindex/config.json как mem.Config без зависимости
// от типа mem.Config.
//
// На практике: создаём минимальный JSON с теми же полями, что и mem.Config.
// При LoadConfig мы парсим его через encoding/json в mem.Config — работает.
func defaultFileIndexConfig() map[string]any {
	return map[string]any{
		"Backend": "ollama",
		"Ollama": map[string]string{
			"BaseURL": "http://localhost:11434",
			"Model":   "bge-m3",
		},
		"Polza": map[string]string{
			"BaseURL": "https://polza.ai/api/v1",
			"APIKey":  "",
			"Model":   "openai/text-embedding-3-small",
		},
		"Chunking": map[string]any{
			"MaxSize":  1000,
			"Overlap":  100,
			"Strategy": "paragraph",
		},
	}
}

// LoadConfig загружает .fileindex/config.json как mem.Config.
// Chunking-поля игнорируются (не нужны для метаданных файлов).
//
// dir — путь к директории .fileindex/ (НЕ к scan-root). Обычно это
// fileindex.FileIndexDir() (cwd-relative) или FileIndexConfigPathIn(scanRoot).
func LoadConfig(dir string) (*mem.Config, error) {
	path := FileIndexConfigPathIn(dir)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("config не найден: %s: %w", path, err)
	}
	return mem.LoadConfigIn(dir)
}

// SaveConfig сохраняет mem.Config в .fileindex/config.json.
// Chunking-поля из cfg сохраняются как есть (безвредно).
func SaveConfig(dir string, cfg *mem.Config) error {
	return mem.SaveConfigIn(dir, cfg)
}