package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var toolEnv = map[string]string{
	"pdftotext": "MEM_PDFTOTEXT", "mutool": "MEM_MUTOOL", "pdfinfo": "MEM_PDFINFO",
	"pdftoppm": "MEM_PDFTOPPM", "djvutxt": "MEM_DJVUTXT", "djvused": "MEM_DJVUSED",
	"ddjvu": "MEM_DDJVU", "tesseract": "MEM_TESSERACT",
}

func defaultToolResolver(name, explicit string) (string, error) {
	var checked []string
	if explicit != "" {
		checked = append(checked, explicit)
		if isExecutableFile(explicit) {
			return filepath.Clean(explicit), nil
		}
		return "", fmt.Errorf("configured %s executable does not exist or is a directory: %s", name, explicit)
	}
	if envName := toolEnv[name]; envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			checked = append(checked, value)
			if isExecutableFile(value) {
				return filepath.Clean(value), nil
			}
			return "", fmt.Errorf("%s points to an invalid %s executable: %s", envName, name, value)
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	checked = append(checked, "PATH")
	if runtime.GOOS == "windows" {
		for _, candidate := range windowsToolCandidates(name) {
			checked = append(checked, candidate)
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("%s executable not found (checked %s); configure %s, add a project config path, or install it in a standard location", name, strings.Join(checked, ", "), toolEnv[name])
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func windowsToolCandidates(name string) []string {
	exe := name + ".exe"
	var dirs []string
	switch name {
	case "djvutxt", "djvused", "ddjvu":
		dirs = []string{`C:\Program Files (x86)\DjVuLibre`, `C:\Program Files\DjVuLibre`}
	case "tesseract":
		dirs = []string{`C:\Program Files\Tesseract-OCR`, `C:\Program Files (x86)\Tesseract-OCR`}
	case "pdftotext", "pdfinfo", "pdftoppm":
		dirs = []string{`C:\Program Files\poppler\Library\bin`, `C:\Program Files\poppler\bin`, `C:\Program Files (x86)\poppler\Library\bin`, `C:\Program Files (x86)\poppler\bin`}
	case "mutool":
		dirs = []string{`C:\Program Files\MuPDF`, `C:\Program Files (x86)\MuPDF`}
	}
	result := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		result = append(result, filepath.Join(dir, exe))
	}
	return result
}

func (e *engine) optionalTool(name, explicit string) (string, bool, error) {
	path, err := e.resolve(name, explicit)
	if err == nil {
		return path, true, nil
	}
	// An invalid explicit/env override must not silently fall through.
	if explicit != "" || strings.TrimSpace(os.Getenv(toolEnv[name])) != "" {
		return "", false, err
	}
	return "", false, nil
}
