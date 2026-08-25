package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

const classicMindMapExportUsage = "использование: mem mindmap export <карта> --format html|svg|png|json|opml|markdown|mermaid|obsidian --output <путь> [--force]"

type classicMindMapExportCLIOptions struct {
	mapRef     string
	format     mem.ClassicMindMapExportFormat
	outputPath string
	force      bool
}

func handleClassicMindMapExport(store *Store, args []string) error {
	if store == nil {
		return errors.New("активная база карт недоступна")
	}
	options, err := parseClassicMindMapExportCLIOptions(args)
	if err != nil {
		return err
	}
	if err := rejectClassicMindMapExportDatabaseTarget(options.outputPath, store.Path()); err != nil {
		return err
	}
	doc, err := store.LoadClassicMindMap(options.mapRef)
	if err != nil {
		return err
	}
	artifact, err := store.ExportClassicMindMap(mem.ClassicMindMapExportRequest{
		MapRef: options.mapRef, Format: options.format,
		ExpectedRevision: doc.Map.Revision, ExpectedDigest: doc.Digest, ExpectedStateDigest: doc.StateDigest,
	})
	if err != nil {
		return err
	}
	outputPath, err := writeClassicMindMapExportArtifact(options.outputPath, artifact.Data, options.force)
	if err != nil {
		return err
	}
	fmt.Printf("%s Карта %s экспортирована.\n", ui.Mark("ok"), ui.Value(doc.Map.Title))
	fmt.Printf("Формат: %s · файл: %s · размер: %d байт\n", artifact.Format, outputPath, len(artifact.Data))
	fmt.Printf("Ревизия: %d · узлов: %d · источников: %d · digest: %s · state: %s\n",
		artifact.Revision, artifact.NodeCount, artifact.SourceCount, artifact.Digest, artifact.StateDigest)
	return nil
}

func rejectClassicMindMapExportDatabaseTarget(outputPath, databasePath string) error {
	if strings.TrimSpace(databasePath) == "" {
		return nil
	}
	outputAbsolute, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("mindmap export: абсолютный путь результата: %w", err)
	}
	databaseAbsolute, err := filepath.Abs(databasePath)
	if err != nil {
		return fmt.Errorf("mindmap export: абсолютный путь активной базы: %w", err)
	}
	outputAbsolute = filepath.Clean(outputAbsolute)
	databaseAbsolute = filepath.Clean(databaseAbsolute)
	protected := []string{
		databaseAbsolute,
		databaseAbsolute + "-wal",
		databaseAbsolute + "-shm",
		databaseAbsolute + "-journal",
	}
	for _, candidate := range protected {
		if classicMindMapExportPathsEqual(outputAbsolute, candidate) {
			return errors.New("mindmap export: путь результата совпадает с активной базой или её служебным SQLite-файлом; выбери другой файл")
		}
	}
	outputInfo, outputErr := os.Stat(outputAbsolute)
	if outputErr != nil {
		if os.IsNotExist(outputErr) {
			return nil
		}
		return fmt.Errorf("mindmap export: проверить файл результата: %w", outputErr)
	}
	for _, candidate := range protected {
		candidateInfo, candidateErr := os.Stat(candidate)
		if candidateErr != nil {
			if os.IsNotExist(candidateErr) {
				continue
			}
			return fmt.Errorf("mindmap export: проверить защищённый SQLite-файл: %w", candidateErr)
		}
		if os.SameFile(outputInfo, candidateInfo) {
			return errors.New("mindmap export: файл результата ссылается на активную базу или её служебный SQLite-файл; выбери другой файл")
		}
	}
	return nil
}

func classicMindMapExportPathsEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func parseClassicMindMapExportCLIOptions(args []string) (classicMindMapExportCLIOptions, error) {
	var options classicMindMapExportCLIOptions
	formatSet, outputSet := false, false
	value := func(index *int, flag string) (string, error) {
		if *index+1 >= len(args) {
			return "", fmt.Errorf("%s ожидает значение\n%s", flag, classicMindMapExportUsage)
		}
		*index = *index + 1
		return args[*index], nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			if options.force {
				return options, errors.New("--force указан повторно")
			}
			options.force = true
		case "--format":
			if formatSet {
				return options, errors.New("--format указан повторно")
			}
			formatSet = true
			format, err := value(&i, "--format")
			if err != nil {
				return options, err
			}
			options.format = mem.ClassicMindMapExportFormat(strings.ToLower(strings.TrimSpace(format)))
		case "--output":
			if outputSet {
				return options, errors.New("--output указан повторно")
			}
			outputSet = true
			output, err := value(&i, "--output")
			if err != nil {
				return options, err
			}
			options.outputPath = strings.TrimSpace(output)
		default:
			if strings.HasPrefix(args[i], "-") {
				return options, fmt.Errorf("неизвестный флаг mindmap export %q\n%s", args[i], classicMindMapExportUsage)
			}
			if options.mapRef != "" {
				return options, errors.New(classicMindMapExportUsage)
			}
			options.mapRef = strings.TrimSpace(args[i])
		}
	}
	if options.mapRef == "" || !formatSet || !outputSet || options.outputPath == "" {
		return options, errors.New(classicMindMapExportUsage)
	}
	switch options.format {
	case mem.ClassicMindMapExportHTML, mem.ClassicMindMapExportSVG, mem.ClassicMindMapExportPNG,
		mem.ClassicMindMapExportJSON, mem.ClassicMindMapExportOPML, mem.ClassicMindMapExportMarkdown,
		mem.ClassicMindMapExportMermaid, mem.ClassicMindMapExportObsidian:
		return options, nil
	default:
		return options, fmt.Errorf("неподдерживаемый формат mindmap export %q; доступны html, svg, png, json, opml, markdown, mermaid, obsidian", options.format)
	}
}

func writeClassicMindMapExportArtifact(path string, data []byte, force bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("mindmap export: путь результата: %w", err)
	}
	directory := filepath.Dir(absolute)
	if info, statErr := os.Stat(directory); statErr != nil {
		return "", fmt.Errorf("mindmap export: каталог результата недоступен: %w", statErr)
	} else if !info.IsDir() {
		return "", fmt.Errorf("mindmap export: родитель результата не является каталогом: %s", directory)
	}
	if !force {
		file, openErr := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			if os.IsExist(openErr) {
				return "", fmt.Errorf("mindmap export: файл %s уже существует; используй --force для перезаписи", absolute)
			}
			return "", fmt.Errorf("mindmap export: создать файл: %w", openErr)
		}
		if err := writeAndSyncClassicMindMapExport(file, data); err != nil {
			_ = os.Remove(absolute)
			return "", err
		}
		return absolute, nil
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(absolute)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("mindmap export: создать временный файл: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeAndSyncClassicMindMapExport(temporary, data); err != nil {
		return "", err
	}
	if err := replaceClassicMindMapExportFile(temporaryPath, absolute); err != nil {
		return "", fmt.Errorf("mindmap export: атомарно заменить %s: %w", absolute, err)
	}
	keepTemporary = true
	return absolute, nil
}

func writeAndSyncClassicMindMapExport(file *os.File, data []byte) error {
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = fmt.Errorf("короткая запись: %d из %d байт", written, len(data))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("mindmap export: записать результат: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("mindmap export: закрыть результат: %w", closeErr)
	}
	return nil
}
