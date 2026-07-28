// Package fileindex — семантический каталог файлов.
//
// mem-index — отдельный бинарь (см. cmd/mem-index/main.go), который хранит
// метаданные файлов (имя, путь относительно scan-root, размер, mtime, hash,
// аннотацию) и embedding строки (name + parent_dirs + annotation) в собственной
// SQLite-базе под .fileindex/. Поиск — косинусом по embedding, с гибридным
// boost по имени файла.
//
// Конвенция путей — как в pkg/mem: cwd-relative по умолчанию, плюс варианты
// с суффиксом In(dir) для явной директории. Scan-root — filepath.Dir(.fileindex/).
package fileindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileIndexDirName — имя скрытой директории с базой файлового каталога.
// Параллель mem.MemDirName = ".mem".
const FileIndexDirName = ".fileindex"

// MemMeta — метаданные базы. Те же поля, что в pkg/mem, чтобы код
// не зависел от типа (мы только пишем и читаем JSON).
type MemMeta struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// FileIndexDir возвращает путь к директории .fileindex/ относительно cwd.
func FileIndexDir() string { return FileIndexDirName }

// FileIndexConfigPath возвращает путь к .fileindex/config.json.
func FileIndexConfigPath() string {
	return filepath.Join(FileIndexDirName, "config.json")
}

// FileIndexStorePath возвращает путь к .fileindex/store.db (SQLite-база).
func FileIndexStorePath() string {
	return filepath.Join(FileIndexDirName, "store.db")
}

// FileIndexMetaPath возвращает путь к .fileindex/meta.json.
func FileIndexMetaPath() string {
	return filepath.Join(FileIndexDirName, "meta.json")
}

// FileIndexExists проверяет, существует ли .fileindex/ в текущей директории.
func FileIndexExists() bool {
	info, err := os.Stat(FileIndexDirName)
	return err == nil && info.IsDir()
}

// === Per-directory варианты ===

// FileIndexExistsIn проверяет, существует ли директория dir.
func FileIndexExistsIn(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// FileIndexConfigPathIn возвращает путь к config.json внутри dir.
func FileIndexConfigPathIn(dir string) string {
	return filepath.Join(dir, "config.json")
}

// FileIndexStorePathIn возвращает путь к store.db внутри dir.
func FileIndexStorePathIn(dir string) string {
	return filepath.Join(dir, "store.db")
}

// FileIndexMetaPathIn возвращает путь к meta.json внутри dir.
func FileIndexMetaPathIn(dir string) string {
	return filepath.Join(dir, "meta.json")
}

// InitFileIndex создаёт .fileindex/ в текущей директории с дефолтным
// config.json и meta.json. Если .fileindex/ уже существует — ошибка.
//
// Конфиг берётся из mem.DefaultLocalConfig() — те же дефолты, что для mem:
// Ollama http://localhost:11434 (bge-m3) или Polza. Chunking-поля в config
// присутствуют (безвредны), но игнорируются mem-index.
func InitFileIndex() error {
	if FileIndexExists() {
		return fmt.Errorf(".fileindex/ уже существует в текущей папке")
	}
	if err := os.MkdirAll(FileIndexDirName, 0700); err != nil {
		return fmt.Errorf("не удалось создать %s/: %w", FileIndexDirName, err)
	}

	cfg := defaultFileIndexConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.RemoveAll(FileIndexDirName)
		return fmt.Errorf("ошибка сериализации config: %w", err)
	}
	if err := os.WriteFile(FileIndexConfigPath(), data, 0600); err != nil {
		os.RemoveAll(FileIndexDirName)
		return fmt.Errorf("не удалось записать %s: %w", FileIndexConfigPath(), err)
	}

	cwd, _ := os.Getwd()
	name := filepath.Base(cwd)
	meta := MemMeta{
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		os.RemoveAll(FileIndexDirName)
		return fmt.Errorf("ошибка сериализации meta: %w", err)
	}
	if err := os.WriteFile(FileIndexMetaPath(), data, 0600); err != nil {
		os.RemoveAll(FileIndexDirName)
		return fmt.Errorf("не удалось записать %s: %w", FileIndexMetaPath(), err)
	}

	return nil
}

// InitFileIndexIn создаёт .fileindex/ в указанной директории (с config.json
// и meta.json). Если уже существует — ошибка. name используется в meta.json.
func InitFileIndexIn(dir, name string) error {
	if FileIndexExistsIn(dir) {
		return fmt.Errorf("%s/ уже существует", dir)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("не удалось создать %s/: %w", dir, err)
	}

	cfg := defaultFileIndexConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("сериализация config: %w", err)
	}
	if err := os.WriteFile(FileIndexConfigPathIn(dir), data, 0600); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("запись config: %w", err)
	}

	if name == "" {
		name = filepath.Base(filepath.Dir(dir))
		if name == "." || name == "/" {
			name = "fileindex"
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
	if err := os.WriteFile(FileIndexMetaPathIn(dir), data, 0600); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("запись meta: %w", err)
	}

	return nil
}