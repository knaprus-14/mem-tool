package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolvedDB — эффективные настройки активной базы, готовые к использованию.
// Dir — директория для newStore; Path — полный путь к jsonl (для отображения).
type ResolvedDB struct {
	Name     string
	Dir      string          // директория для newStore
	Path     string          // полный путь к jsonl
	Backend  string          // "ollama" или "polza"
	Embed    EmbedSettings   // готовые настройки для getEmbedding
	Chunking ChunkConfig     // настройки чанкинга
}

// activeDBName возвращает имя активной базы из конфига (или "default", если пусто).
func activeDBName(cfg *Config) string {
	if cfg.CurrentDB == "" {
		return "default"
	}
	return cfg.CurrentDB
}

// resolveDB резолвит базу по имени: путь + эффективные настройки.
//   - name == "" → берётся cfg.CurrentDB (или "default").
//   - name == "default" → старая база в cfg.StorePath.
//   - любое другое → databases_dir/<name>.jsonl.
//
// Если name явно задано (через --db), база должна существовать (файл или настройка).
func resolveDB(cfg *Config, name string) (*ResolvedDB, error) {
	if name == "" {
		name = activeDBName(cfg)
	}

	// "default" — всегда legacy, путь из StorePath
	if name == "default" {
		backend, ollama, polza, chunking := mergeDBConfig(cfg, "default")
		return &ResolvedDB{
			Name:     "default",
			Dir:      cfg.StorePath,
			Path:     filepath.Join(cfg.StorePath, "store.jsonl"),
			Backend:  backend,
			Embed:    EmbedSettings{Backend: backend, Ollama: ollama, Polza: polza},
			Chunking: chunking,
		}, nil
	}

	// Именованная база: проверяем, что она существует (есть файл или хотя бы настройка)
	dir, err := databasesDir(cfg)
	if err != nil {
		return nil, fmt.Errorf("не удалось подготовить каталог баз: %w", err)
	}
	filePath := safeFilePath(dir, name)

	if _, statErr := os.Stat(filePath); statErr != nil {
		if os.IsNotExist(statErr) {
			if _, hasCfg := cfg.Databases[name]; !hasCfg {
				return nil, fmt.Errorf("база %q не найдена. Создай: mem db create %s", name, name)
			}
		} else {
			return nil, fmt.Errorf("ошибка доступа к базе %q: %w", name, statErr)
		}
	}

	backend, ollama, polza, chunking := mergeDBConfig(cfg, name)
	return &ResolvedDB{
		Name:     name,
		Dir:      dir,
		Path:     filePath,
		Backend:  backend,
		Embed:    EmbedSettings{Backend: backend, Ollama: ollama, Polza: polza},
		Chunking: chunking,
	}, nil
}

// safeFilePath возвращает полный путь к jsonl для именованной базы.
func safeFilePath(dir, name string) string {
	return filepath.Join(dir, safeFileName(name)+".jsonl")
}

// parseGlobalDBFlag извлекает --db <name> из начала args (если есть).
// Возвращает очищенный args (без --db) и имя базы (или "" если не указан).
// --db должен идти первым аргументом, перед именем команды.
func parseGlobalDBFlag(args []string) (cleanArgs []string, dbName string) {
	if len(args) >= 2 && args[0] == "--db" {
		return args[2:], args[1]
	}
	return args, ""
}