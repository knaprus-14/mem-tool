package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// handleDb — диспетчер команд управления базами данных.
//   mem db                       → список баз (кратко)
//   mem db create <name>         → создать (интерактивно или с флагами)
//   mem db list                  → список баз (подробно)
//   mem db use <name>            → переключить активную
//   mem db rename <old> <new>    → переименовать
//   mem db delete <name>         → удалить (с подтверждением)
//   mem db info                  → инфо о текущей базе
//   mem db config <name> [...]   → настройки базы
func handleDb(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Без аргументов — краткий список
	if len(args) == 0 {
		return showDBList(cfg, false)
	}

	switch args[0] {
	case "create":
		return createDB(cfg, args[1:])
	case "list", "ls":
		return showDBList(cfg, true)
	case "use":
		return useDB(cfg, args[1:])
	case "rename", "mv":
		return renameDB(cfg, args[1:])
	case "delete", "rm":
		return deleteDB(cfg, args[1:])
	case "info":
		return infoDB(cfg)
	case "config":
		return dbConfigCmd(cfg, args[1:])
	default:
		return fmt.Errorf("неизвестная подкоманда db: %s", args[0])
	}
}

// showDBList выводит список всех баз с маркером текущей.
// verbose=true — с путями и размером; false — только имена.
func showDBList(cfg *Config, verbose bool) error {
	current := activeDBName(cfg)

	dir, err := databasesDir(cfg)
	if err != nil {
		return err
	}

	// Собираем записи: всегда default, плюс все .jsonl из dir
	type dbEntry struct {
		name      string
		path      string
		size      int64
		isCurrent bool
	}

	var entries []dbEntry

	// 1. default — старая база
	if st, err := os.Stat(filepath.Join(cfg.StorePath, "store.jsonl")); err == nil {
		entries = append(entries, dbEntry{
			name:      "default",
			path:      filepath.Join(cfg.StorePath, "store.jsonl"),
			size:      st.Size(),
			isCurrent: current == "default",
		})
	}

	// 2. файлы в databases_dir
	if files, err := os.ReadDir(dir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			name := strings.TrimSuffix(f.Name(), ".jsonl")
			if name == "default" {
				continue // уже добавлен выше
			}
			fullPath := filepath.Join(dir, f.Name())
			st, _ := os.Stat(fullPath)
			size := int64(0)
			if st != nil {
				size = st.Size()
			}
			entries = append(entries, dbEntry{
				name:      name,
				path:      fullPath,
				size:      size,
				isCurrent: name == current,
			})
		}
	}

	// Сортируем по имени (default идёт первым по особому правилу)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name == "default" {
			return true
		}
		if entries[j].name == "default" {
			return false
		}
		return entries[i].name < entries[j].name
	})

	if len(entries) == 0 {
		fmt.Println("[-] Нет баз. Создай первую: mem db create работа")
		return nil
	}

	fmt.Println("[DB] Базы данных:")
	fmt.Println(strings.Repeat("--", 50))
	for _, e := range entries {
		mark := "  "
		if e.isCurrent {
			mark = "* "
		}
		if verbose {
			fmt.Printf("%s%-20s %s (%d байт)\n", mark, e.name, e.path, e.size)
		} else {
			fmt.Printf("%s%s\n", mark, e.name)
		}
	}
	fmt.Println(strings.Repeat("--", 50))
	fmt.Printf("Активная: %s\n", current)
	return nil
}

// createDB создаёт новую базу (создаёт пустой файл и сохраняет настройки).
func createDB(cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("использование: mem db create <имя> [--backend ...] [--model ...] [--chunk-strategy ...] [--chunk-size N]")
	}

	name := strings.TrimSpace(args[0])
	if !isValidDBName(name) {
		return fmt.Errorf("некорректное имя базы: %q (запрещены пустые имена и \"default\")", args[0])
	}

	// Проверяем, что такой базы ещё нет
	if _, hasCfg := cfg.Databases[name]; hasCfg {
		return fmt.Errorf("база %q уже существует в конфиге. Удали: mem db config %s (или переименуй)", name, name)
	}
	filePath, err := dbFilePath(cfg, name)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filePath); statErr == nil {
		return fmt.Errorf("файл базы уже существует: %s", filePath)
	}

	// Парсим флаги и/или интерактивно спрашиваем
	dbCfg, err := promptDBConfig(cfg, name, args[1:])
	if err != nil {
		return err
	}

	// Создаём пустой файл базы
	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return err
	}
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("не удалось создать файл базы: %w", err)
	}
	f.Close()

	// Сохраняем per-db настройки
	if cfg.Databases == nil {
		cfg.Databases = map[string]DatabaseConfig{}
	}
	cfg.Databases[name] = dbCfg

	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("[OK] База %q создана: %s\n", name, filePath)
	fmt.Printf("  Бэкенд: %s", dbCfg.Backend)
	if dbCfg.Backend != "" {
		if dbCfg.Ollama.Model != "" {
			fmt.Printf(" (ollama: %s)", dbCfg.Ollama.Model)
		} else if dbCfg.Polza.Model != "" {
			fmt.Printf(" (polza: %s)", dbCfg.Polza.Model)
		}
	}
	fmt.Println()
	fmt.Printf("Использовать: mem db use %s\n", name)
	return nil
}

// promptDBConfig собирает per-db настройки: из флагов (если заданы) или интерактивно.
func promptDBConfig(cfg *Config, name string, flags []string) (DatabaseConfig, error) {
	// Парсим поддерживаемые флаги (parseFlags не покрывает --backend/--model/--chunk-strategy/--chunk-size)
	var dbCfg DatabaseConfig

	backend := ""
	ollamaModel := ""
	polzaModel := ""
	chunkStrategy := ""
	chunkSize := 0

	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--backend":
			if i+1 < len(flags) {
				i++
				backend = flags[i]
			}
		case "--model":
			if i+1 < len(flags) {
				i++
				// В зависимости от текущего бэкенда (уже заданного или глобального)
				targetBackend := backend
				if targetBackend == "" {
					targetBackend = cfg.Backend
				}
				if targetBackend == "polza" {
					polzaModel = flags[i]
				} else {
					ollamaModel = flags[i]
				}
			}
		case "--chunk-strategy":
			if i+1 < len(flags) {
				i++
				chunkStrategy = flags[i]
			}
		case "--chunk-size":
			if i+1 < len(flags) {
				i++
				if n, err := strconv.Atoi(flags[i]); err == nil {
					chunkSize = n
				}
			}
		}
	}

	reader := bufio.NewReader(os.Stdin)

	// === Backend ===
	if backend == "" {
		defBackend := cfg.Backend
		fmt.Fprintf(os.Stderr, "? Бэкенд эмбеддингов [ollama/polza] (по умолч. %s): ", defBackend)
		line, _ := reader.ReadString('\n')
		backend = strings.TrimSpace(line)
		if backend == "" {
			backend = defBackend
		}
	}
	if backend != "ollama" && backend != "polza" {
		return dbCfg, fmt.Errorf("бэкенд должен быть 'ollama' или 'polza', получено: %q", backend)
	}
	dbCfg.Backend = backend

	// === Model ===
	if backend == "ollama" && ollamaModel == "" {
		defModel := cfg.Ollama.Model
		fmt.Fprintf(os.Stderr, "? Модель Ollama (по умолч. %s): ", defModel)
		line, _ := reader.ReadString('\n')
		ollamaModel = strings.TrimSpace(line)
		if ollamaModel == "" {
			ollamaModel = defModel
		}
		dbCfg.Ollama = OllamaConfig{BaseURL: cfg.Ollama.BaseURL, Model: ollamaModel}
	}
	if backend == "polza" && polzaModel == "" {
		defModel := cfg.Polza.Model
		fmt.Fprintf(os.Stderr, "? Модель Polza (по умолч. %s): ", defModel)
		line, _ := reader.ReadString('\n')
		polzaModel = strings.TrimSpace(line)
		if polzaModel == "" {
			polzaModel = defModel
		}
		dbCfg.Polza = PolzaConfig{
			BaseURL: cfg.Polza.BaseURL,
			APIKey:  cfg.Polza.APIKey,
			Model:   polzaModel,
		}
	}

	// === Chunking ===
	if chunkStrategy == "" {
		defStrategy := cfg.Chunking.Strategy
		fmt.Fprintf(os.Stderr, "? Стратегия чанкинга [paragraph/sentence/fixed] (по умолч. %s): ", defStrategy)
		line, _ := reader.ReadString('\n')
		chunkStrategy = strings.TrimSpace(line)
		if chunkStrategy == "" {
			chunkStrategy = defStrategy
		}
	}
	if chunkStrategy != "paragraph" && chunkStrategy != "sentence" && chunkStrategy != "fixed" {
		return dbCfg, fmt.Errorf("стратегия должна быть: paragraph, sentence или fixed")
	}

	if chunkSize == 0 {
		defSize := cfg.Chunking.MaxSize
		fmt.Fprintf(os.Stderr, "? Размер чанка (по умолч. %d): ", defSize)
		line, _ := reader.ReadString('\n')
		sizeStr := strings.TrimSpace(line)
		if sizeStr == "" {
			chunkSize = defSize
		} else {
			n, err := strconv.Atoi(sizeStr)
			if err != nil || n < 100 || n > 10000 {
				return dbCfg, fmt.Errorf("размер чанка должен быть от 100 до 10000")
			}
			chunkSize = n
		}
	} else if chunkSize < 100 || chunkSize > 10000 {
		return dbCfg, fmt.Errorf("размер чанка должен быть от 100 до 10000")
	}

	dbCfg.Chunking = ChunkConfig{
		MaxSize:  chunkSize,
		Overlap:  cfg.Chunking.Overlap,
		Strategy: chunkStrategy,
	}

	return dbCfg, nil
}

// useDB переключает активную базу (сохраняет в конфиг).
func useDB(cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("использование: mem db use <имя>")
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return fmt.Errorf("имя не может быть пустым")
	}
	if name != "default" {
		if _, hasCfg := cfg.Databases[name]; !hasCfg {
			filePath, _ := dbFilePath(cfg, name)
			if _, err := os.Stat(filePath); err != nil {
				return fmt.Errorf("база %q не найдена. Создай: mem db create %s", name, name)
			}
		}
	}
	cfg.CurrentDB = name
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("[OK] Активная база: %s\n", name)
	return nil
}

// renameDB переименовывает файл базы и обновляет ключ в cfg.Databases.
func renameDB(cfg *Config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("использование: mem db rename <старое> <новое>")
	}
	oldName := strings.TrimSpace(args[0])
	newName := strings.TrimSpace(args[1])

	if !isValidDBName(newName) {
		return fmt.Errorf("некорректное новое имя: %q", newName)
	}
	if oldName == "default" {
		return fmt.Errorf("нельзя переименовать базу \"default\" (она зарезервирована)")
	}

	oldPath, err := dbFilePath(cfg, oldName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("база %q не найдена", oldName)
	}

	newPath, err := dbFilePath(cfg, newName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("база с именем %q уже существует", newName)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("не удалось переименовать файл: %w", err)
	}

	// Обновляем ключи в конфиге
	if dbCfg, ok := cfg.Databases[oldName]; ok {
		delete(cfg.Databases, oldName)
		cfg.Databases[newName] = dbCfg
	}
	if cfg.CurrentDB == oldName {
		cfg.CurrentDB = newName
	}

	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("[OK] База переименована: %s → %s\n", oldName, newName)
	return nil
}

// deleteDB удаляет базу (файл + per-db настройки). Требует подтверждения.
func deleteDB(cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("использование: mem db delete <имя>")
	}
	name := strings.TrimSpace(args[0])
	if name == "default" {
		return fmt.Errorf("нельзя удалить базу \"default\" (это твоя основная база)")
	}

	filePath, err := dbFilePath(cfg, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("база %q не найдена", name)
	}

	// Интерактивное подтверждение
	fmt.Fprintf(os.Stderr, "? Удалить базу %q со всеми записями? [y/N]: ", name)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" && answer != "д" && answer != "да" {
		fmt.Println("Отменено.")
		return nil
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("не удалось удалить файл: %w", err)
	}

	delete(cfg.Databases, name)
	if cfg.CurrentDB == name {
		cfg.CurrentDB = "default"
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("[OK] База %q удалена. Активная: %s\n", name, cfg.CurrentDB)
	return nil
}

// infoDB показывает путь и настройки текущей базы.
func infoDB(cfg *Config) error {
	name := activeDBName(cfg)
	resolved, err := resolveDB(cfg, name)
	if err != nil {
		return err
	}
	path := resolved.Path

	backend, ollama, polza, chunking := mergeDBConfig(cfg, name)

	fmt.Println("[DB] Текущая база")
	fmt.Println(strings.Repeat("--", 50))
	fmt.Printf("  Имя:         %s\n", name)
	fmt.Printf("  Путь:        %s\n", path)
	if st, err := os.Stat(path); err == nil {
		fmt.Printf("  Размер:      %d байт\n", st.Size())
	}
	fmt.Printf("  Бэкенд:      %s\n", backend)
	if backend == "ollama" {
		fmt.Printf("  Модель:      %s\n", ollama.Model)
		fmt.Printf("  URL:         %s\n", ollama.BaseURL)
	} else if backend == "polza" {
		fmt.Printf("  Модель:      %s\n", polza.Model)
		keyHint := "(задан)"
		if polza.APIKey == "" {
			keyHint = "(НЕ ЗАДАН — настрой: mem config set-polza-key)"
		}
		fmt.Printf("  API ключ:    %s\n", keyHint)
	}
	fmt.Printf("  Чанкинг:     %s (max %d, overlap %d)\n",
		chunking.Strategy, chunking.MaxSize, chunking.Overlap)

	// Per-db override (если есть)
	if dbCfg, ok := cfg.Databases[name]; ok && dbCfg.Backend != "" {
		overrides := []string{}
		if dbCfg.Backend != "" {
			overrides = append(overrides, "backend")
		}
		if dbCfg.Ollama.Model != "" || dbCfg.Ollama.BaseURL != "" {
			overrides = append(overrides, "ollama")
		}
		if dbCfg.Polza.Model != "" || dbCfg.Polza.APIKey != "" || dbCfg.Polza.BaseURL != "" {
			overrides = append(overrides, "polza")
		}
		if dbCfg.Chunking.Strategy != "" {
			overrides = append(overrides, "chunking")
		}
		if len(overrides) > 0 {
			fmt.Printf("  Override:    %s\n", strings.Join(overrides, ", "))
		}
	}
	return nil
}

// dbConfigCmd — показать/изменить per-db настройки.
//   mem db config <name>                       → показать
//   mem db config <name> set-backend X         → изменить
//   mem db config <name> set-model X
//   mem db config <name> set-chunk-strategy X
//   mem db config <name> set-chunk-size N
func dbConfigCmd(cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("использование: mem db config <имя> [set-... значение]")
	}
	name := strings.TrimSpace(args[0])

	// Без подкоманд — показать
	if len(args) == 1 {
		return showDBConfig(cfg, name)
	}

	// Инициализируем map если нужно
	if cfg.Databases == nil {
		cfg.Databases = map[string]DatabaseConfig{}
	}
	dbCfg, ok := cfg.Databases[name]
	if !ok {
		dbCfg = DatabaseConfig{}
	}

	cmd := args[1]
	switch cmd {
	case "set-backend":
		if len(args) < 3 {
			return fmt.Errorf("использование: mem db config %s set-backend <ollama|polza>", name)
		}
		backend := args[2]
		if backend != "ollama" && backend != "polza" {
			return fmt.Errorf("бэкенд должен быть 'ollama' или 'polza'")
		}
		dbCfg.Backend = backend
	case "set-model":
		if len(args) < 3 {
			return fmt.Errorf("использование: mem db config %s set-model <имя_модели>", name)
		}
		targetBackend := dbCfg.Backend
		if targetBackend == "" {
			targetBackend = cfg.Backend
		}
		if targetBackend == "ollama" {
			dbCfg.Ollama.Model = args[2]
		} else {
			dbCfg.Polza.Model = args[2]
		}
	case "set-chunk-strategy":
		if len(args) < 3 {
			return fmt.Errorf("использование: mem db config %s set-chunk-strategy <paragraph|sentence|fixed>", name)
		}
		strategy := args[2]
		if strategy != "paragraph" && strategy != "sentence" && strategy != "fixed" {
			return fmt.Errorf("стратегия должна быть: paragraph, sentence или fixed")
		}
		dbCfg.Chunking.Strategy = strategy
	case "set-chunk-size":
		if len(args) < 3 {
			return fmt.Errorf("использование: mem db config %s set-chunk-size <100-10000>", name)
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 100 || n > 10000 {
			return fmt.Errorf("размер чанка должен быть от 100 до 10000")
		}
		dbCfg.Chunking.MaxSize = n
	case "clear":
		// Сбросить все per-db настройки (наследовать глобальные)
		delete(cfg.Databases, name)
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("[OK] Per-db настройки для %q сброшены (наследует глобальные)\n", name)
		return nil
	default:
		return fmt.Errorf("неизвестная подкоманда: %s", cmd)
	}

	cfg.Databases[name] = dbCfg
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("[OK] Настройка %s для базы %q обновлена\n", cmd, name)
	return nil
}

// showDBConfig печатает per-db настройки (и какие наследуются).
func showDBConfig(cfg *Config, name string) error {
	dbCfg, ok := cfg.Databases[name]

	fmt.Printf("[CFG] Настройки базы %q\n", name)
	fmt.Println(strings.Repeat("--", 50))

	if !ok {
		fmt.Println("  (нет per-db настроек — всё наследуется из глобального конфига)")
	} else {
		data, _ := json.MarshalIndent(dbCfg, "  ", "  ")
		fmt.Println("  " + string(data))
	}
	return nil
}