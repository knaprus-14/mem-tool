package mem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ParseGlobalFlag извлекает флаги --global и --dir=<path> / --dir <path>
// из произвольного набора аргументов.
//
// --global    — переключает cwd на родительскую директорию глобальной базы знаний
//               (по умолчанию ~/global-mem/, путь берётся из env MEM_GLOBAL_DIR).
//               После этого все команды работают с глобальной базой, как если бы
//               пользователь запустил mem в её родительской папке.
//
// --dir <path> — переключает cwd на path/.mem/ (если path уже содержит .mem, берётся path).
//               Полезно для работы с произвольной базой без cd.
//
// Возвращает (useGlobal bool, customDir string, remainingArgs []string).
// Если useGlobal==true, customDir игнорируется.
// Применяется через ApplyDirSwitch в самом начале run() ДО остальной логики.
//
// Экспортировано в v1.16.0 — раньше было приватным в cmd/mem/main.go,
// теперь переиспользуется cmd/mem-index/main.go.
func ParseGlobalFlag(args []string) (bool, string, []string) {
	useGlobal := false
	customDir := ""
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--global":
			useGlobal = true
		case a == "--dir":
			if i+1 < len(args) {
				customDir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--dir="):
			customDir = strings.TrimPrefix(a, "--dir=")
		default:
			out = append(out, a)
		}
	}
	return useGlobal, customDir, out
}

// ApplyDirSwitch применяет --global/--dir к текущему процессу через os.Chdir.
// Если указан --global: cwd → родительская директория GlobalMemDir() (обычно ~/global-mem).
// Если указан --dir <path>: cwd → path, либо path/.mem если path — родитель.
// Возвращает ошибку, если перейти не удалось.
//
// После этого все последующие вызовы MemDir(), MemExists(), NewStore(memDir())
// будут работать с целевой базой без дополнительных изменений.
func ApplyDirSwitch(useGlobal bool, customDir string) error {
	if useGlobal {
		gdir := GlobalMemDir()
		// gdir обычно заканчивается на .mem — нам нужен его родитель (~/global-mem)
		parent := filepath.Dir(gdir)
		if err := os.Chdir(parent); err != nil {
			return fmt.Errorf("не удалось перейти в глобальную базу %s: %w", parent, err)
		}
		return nil
	}
	if customDir != "" {
		// customDir может быть как "C:/foo/.mem", так и "C:/foo" — нормализуем.
		target := customDir
		if filepath.Base(target) != MemDirName {
			target = filepath.Join(target, MemDirName)
		}
		// Переходим в родительскую директорию (или саму target, если .mem)
		cwd := filepath.Dir(target)
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("не удалось перейти в %s: %w", cwd, err)
		}
		return nil
	}
	return nil
}

// ParseColorFlag извлекает флаги --color, --no-color, --color=always/never/auto
// из произвольного набора аргументов и возвращает режим цветов и оставшиеся аргументы.
func ParseColorFlag(args []string) (string, []string) {
	mode := "auto"
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--color", "--color=always", "--color=yes":
			mode = "always"
		case "--no-color", "--color=never", "--color=no":
			mode = "never"
		default:
			out = append(out, a)
		}
	}
	return mode, out
}

// PrintVersion печатает версию программы в формате "<name> v<version>".
// Используется обоими бинарями: mem и mem-index.
func PrintVersion(name, ver string) {
	fmt.Printf("%s v%s\n", name, ver)
	fmt.Println("(c) 2026 Кнап Руслан Юрьевич")
	fmt.Println("Векторная база знаний для работы с Claude")
}