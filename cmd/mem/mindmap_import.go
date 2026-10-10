package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/applog"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

const classicMindMapImportUsage = "использование: mem mindmap import --file <путь> --format opml|freemind|xmind|markdown [--json] [--apply --digest sha256:... --request-id <32 hex>]\nБез --apply выполняется только предпросмотр. Повтор создания требует того же файла, формата, digest и request-id."

type classicMindMapImportCLIOptions struct {
	filePath, format, digest, requestID string
	apply, jsonOutput                   bool
}

func parseClassicMindMapImportCLIOptions(args []string) (classicMindMapImportCLIOptions, error) {
	var out classicMindMapImportCLIOptions
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return out, fmt.Errorf("%s указан повторно", flag)
		}
		seen[flag] = true
		switch flag {
		case "--apply":
			out.apply = true
		case "--json":
			out.jsonOutput = true
		case "--file", "--format", "--digest", "--request-id":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") || args[i+1] == "" {
				return out, fmt.Errorf("%s ожидает значение\n%s", flag, classicMindMapImportUsage)
			}
			i++
			switch flag {
			case "--file":
				out.filePath = args[i]
			case "--format":
				out.format = args[i]
			case "--digest":
				out.digest = args[i]
			case "--request-id":
				out.requestID = args[i]
			}
		default:
			return out, fmt.Errorf("неизвестный аргумент mindmap import %q\n%s", flag, classicMindMapImportUsage)
		}
	}
	if strings.TrimSpace(out.filePath) == "" {
		return out, errors.New(classicMindMapImportUsage)
	}
	switch out.format {
	case "opml", "freemind", "xmind", "markdown":
	default:
		return out, errors.New("нужен --format opml|freemind|xmind|markdown")
	}
	if !out.apply {
		if seen["--digest"] || seen["--request-id"] {
			return out, errors.New("--digest и --request-id используются только вместе с --apply")
		}
		return out, nil
	}
	if len(out.digest) != 71 || !strings.HasPrefix(out.digest, "sha256:") || strings.Trim(out.digest[7:], "0123456789abcdef") != "" {
		return out, errors.New("--apply требует --digest sha256: с 64 строчными hex-символами из предпросмотра")
	}
	if len(out.requestID) != 32 || strings.Trim(out.requestID, "0123456789abcdef") != "" {
		return out, errors.New("--apply требует --request-id: 32 строчных hex-символа; для повтора сохраните тот же идентификатор")
	}
	return out, nil
}

func readClassicMindMapImportFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("mindmap import: проверить файл: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("mindmap import: нужен обычный файл")
	}
	if info.Size() > mem.MaxOutlineImportBytes {
		return nil, errors.New("mindmap import: файл превышает 8 МиБ")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mindmap import: открыть файл: %w", err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("mindmap import: проверить открытый файл: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("mindmap import: нужен обычный файл")
	}
	data, err := io.ReadAll(io.LimitReader(file, mem.MaxOutlineImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mindmap import: прочитать файл: %w", err)
	}
	if len(data) == 0 || len(data) > mem.MaxOutlineImportBytes {
		return nil, errors.New("mindmap import: нужен непустой файл до 8 МиБ")
	}
	return data, nil
}

func handleClassicMindMapImport(store *Store, args []string) (resultErr error) {
	defer applog.Track("cmd.mem.handleClassicMindMapImport")(&resultErr)
	options, err := parseClassicMindMapImportCLIOptions(args)
	if err != nil {
		return err
	}
	if options.apply && (store == nil || store.Path() == "") {
		return errors.New("активная база карт недоступна")
	}
	data, err := readClassicMindMapImportFile(options.filePath)
	if err != nil {
		return err
	}
	input := mem.OutlineImportInput{Format: options.format, Filename: filepath.Base(options.filePath), ContentBase64: base64.StdEncoding.EncodeToString(data)}
	if !options.apply {
		preview, err := mem.PreviewMindMapOutline(input)
		if err != nil {
			return err
		}
		if options.jsonOutput {
			return printMindMapJSON(preview)
		}
		fmt.Printf("Предпросмотр: %s · %s · узлов: %d · источников: %d\n", preview.Title, preview.Format, preview.NodeCount, preview.SourceCount)
		fmt.Printf("Исходный файл: %s · %d байт · %s\n", input.Filename, preview.SourceBytes, preview.SourceSHA256)
		for _, warning := range preview.Warnings {
			fmt.Println("- " + warning)
		}
		for _, n := range preview.Nodes {
			fmt.Printf("%s- %s [узел %s; родитель %s; глубина %d]\n", strings.Repeat("  ", min(n.Depth, 12)), n.Label, n.Ref, n.ParentRef, n.Depth)
		}
		fmt.Printf("Digest: %s\nКарта не создана. Для создания повторите команду с --apply --digest %s --request-id <32 hex>. Сохраните выбранный request-id для повторной отправки.\n", preview.Digest, preview.Digest)
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	created, err := mem.ImportWebMapOutlineIn(ctx, filepath.Dir(store.Path()), "cli", mem.OutlineImportApplyInput{OutlineImportInput: input, Digest: options.digest, RequestID: options.requestID}, nil)
	if err != nil {
		return err
	}
	if options.jsonOutput {
		return printMindMapJSON(created)
	}
	if created.Replayed {
		fmt.Printf("Повтор подтверждён: карта %s уже создана этой отправкой.\n", created.ID)
	} else {
		fmt.Printf("Создана новая карта: %s. Исходный файл сохранён без изменений.\n", created.ID)
	}
	return nil
}
