package main

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

// memDirName — имя скрытой директории с локальной базой
const memDirName = ".mem"

// memDir возвращает путь к директории .mem/ относительно cwd
func memDir() string {
	return memDirName
}

// memConfigPath возвращает путь к .mem/config.json
func memConfigPath() string {
	return filepath.Join(memDirName, "config.json")
}

// memStorePath возвращает путь к .mem/store.jsonl
func memStorePath() string {
	return filepath.Join(memDirName, "store.jsonl")
}

// memMetaPath возвращает путь к .mem/meta.json
func memMetaPath() string {
	return filepath.Join(memDirName, "meta.json")
}

// memExists проверяет, существует ли .mem/ в текущей директории
func memExists() bool {
	info, err := os.Stat(memDirName)
	return err == nil && info.IsDir()
}

// defaultLocalConfig возвращает дефолтный конфиг для новой локальной базы
func defaultLocalConfig() *Config {
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
		Chunking: ChunkConfig{
			MaxSize:  1000,
			Overlap:  100,
			Strategy: "paragraph",
		},
	}
}

// initMem создаёт .mem/ с пустой базой (config.json, store.jsonl, meta.json)
// Если .mem/ уже существует — возвращает ошибку
func initMem() error {
	if memExists() {
		return fmt.Errorf(".mem/ уже существует в текущей папке")
	}

	// Создаём директорию
	if err := os.MkdirAll(memDirName, 0700); err != nil {
		return fmt.Errorf("не удалось создать %s/: %w", memDirName, err)
	}

	// Пишем дефолтный config.json
	cfg := defaultLocalConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации config: %w", err)
	}
	if err := os.WriteFile(memConfigPath(), data, 0600); err != nil {
		// Откатываем создание директории
		os.RemoveAll(memDirName)
		return fmt.Errorf("не удалось записать %s: %w", memConfigPath(), err)
	}

	// Создаём пустой store.jsonl
	f, err := os.Create(memStorePath())
	if err != nil {
		os.RemoveAll(memDirName)
		return fmt.Errorf("не удалось создать %s: %w", memStorePath(), err)
	}
	f.Close()

	// Пишем meta.json с именем = basename(cwd) и датой создания
	cwd, _ := os.Getwd()
	name := filepath.Base(cwd)
	meta := MemMeta{
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		os.RemoveAll(memDirName)
		return fmt.Errorf("ошибка сериализации meta: %w", err)
	}
	if err := os.WriteFile(memMetaPath(), data, 0600); err != nil {
		os.RemoveAll(memDirName)
		return fmt.Errorf("не удалось записать %s: %w", memMetaPath(), err)
	}

	return nil
}

// ensureMem автоматически создаёт .mem/, если её нет и команда это позволяет.
// Возвращает true, если база есть (только что создана или уже была).
func ensureMem(allowAutocreate bool) (bool, error) {
	if memExists() {
		return true, nil
	}
	if !allowAutocreate {
		return false, nil
	}
	if err := initMem(); err != nil {
		return false, err
	}
	return true, nil
}
