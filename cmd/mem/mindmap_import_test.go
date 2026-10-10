package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func TestMindMapImportCLIOptions(t *testing.T) {
	base := []string{"--file", "source.opml", "--format", "opml"}
	digest := "sha256:" + strings.Repeat("a", 64)
	request := strings.Repeat("b", 32)
	for _, format := range []string{"opml", "freemind", "xmind", "markdown"} {
		options, err := parseClassicMindMapImportCLIOptions([]string{"--json", "--file", "space in path", "--format", format})
		if err != nil || options.apply || !options.jsonOutput || options.format != format || options.filePath != "space in path" {
			t.Fatalf("preview options: %+v %v", options, err)
		}
	}
	apply := append(append([]string{}, base...), "--apply", "--digest", digest, "--request-id", request)
	options, err := parseClassicMindMapImportCLIOptions(apply)
	if err != nil || !options.apply || options.digest != digest || options.requestID != request {
		t.Fatalf("apply options: %+v %v", options, err)
	}
	cases := [][]string{nil, {"--file"}, {"--file", "x"}, {"--format", "opml"}, {"--file", "--format", "opml"}, {"--file", "x", "--format", "json"}, {"--file", "x", "--format", "OPML"}}
	for _, extra := range [][]string{{"--unknown"}, {"unexpected"}, {"--file", "other"}, {"--format", "opml"}, {"--json", "--json"}, {"--apply"}, {"--apply", "--apply"}, {"--digest", digest}, {"--request-id", request}, {"--apply", "--digest", digest}, {"--apply", "--request-id", request}, {"--apply", "--digest", "sha256:bad", "--request-id", request}, {"--apply", "--digest", digest, "--request-id", "short"}, {"--apply", "--digest", strings.ToUpper(digest), "--request-id", request}, {"--apply", "--digest", digest, "--request-id", strings.Repeat("B", 32)}} {
		cases = append(cases, append(append([]string{}, base...), extra...))
	}
	for _, args := range cases {
		if _, err := parseClassicMindMapImportCLIOptions(args); err == nil {
			t.Errorf("accepted invalid arguments: %q", args)
		}
	}
}

func TestMindMapImportCLIFileBoundsAndExactBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.xmind")
	data := bytes.Repeat([]byte{0, 255, 128, '\r', '\n', 42, 13, 77}, mem.MaxOutlineImportBytes/8)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readClassicMindMapImportFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("8 MiB exact read: bytes=%d err=%v", len(got), err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readClassicMindMapImportFile(path); err == nil || !strings.Contains(err.Error(), "8 МиБ") {
		t.Fatalf("oversized file accepted: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, empty, filepath.Join(dir, "missing"), os.DevNull} {
		if _, err := readClassicMindMapImportFile(path); err == nil {
			t.Errorf("accepted non-file/empty input: %s", path)
		}
	}
}

func TestMindMapImportCLIPreviewAllFormatsWithoutDatabaseOrInputWrites(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, err := zw.Create("content.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`[{"id":"sheet","title":"Карта","rootTopic":{"id":"root","title":"Корень","children":{"attached":[{"id":"child","title":"Ветвь"}]}}}]`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		format, filename string
		data             []byte
	}{
		{"opml", "source.opml", []byte("\xef\xbb\xbf<opml version=\"2.0\"><head><title>Карта</title></head><body><outline text=\"Корень\"><outline text=\"Ветвь\"/></outline></body></opml>\r\n")},
		{"freemind", "source.mm", []byte(`<map version="1.0.1"><node TEXT="Корень"><node TEXT="Ветвь"/></node></map>`)},
		{"markdown", "source.md", []byte("# Корень\r\n- Ветвь 😀\r\n")},
		{"xmind", "source.xmind", archive.Bytes()},
	} {
		t.Run(input.format, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, input.filename)
			if err := os.WriteFile(path, input.data, 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			args := []string{"import", "--file", path, "--format", input.format, "--json"}
			stdout, _, err := captureCLIStreams(func() error { return handleMindMap(nil, nil, args) })
			if err != nil {
				t.Fatal(err)
			}
			var preview mem.OutlineImportPreview
			if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
				t.Fatalf("preview JSON: %v %q", err, stdout)
			}
			if preview.Format != input.format || preview.NodeCount != 2 || preview.SourceBytes != len(input.data) || preview.SourceSHA256 != fmt.Sprintf("sha256:%x", sha256.Sum256(input.data)) || len(preview.Warnings) == 0 {
				t.Fatalf("unexpected preview: %+v", preview)
			}
			stdout, _, err = captureCLIStreams(func() error { return handleMindMap(nil, nil, args[:len(args)-1]) })
			if err != nil || !strings.Contains(stdout, "Карта не создана") || !strings.Contains(stdout, preview.Digest) || !strings.Contains(stdout, "--request-id") {
				t.Fatalf("human preview: %v %s", err, stdout)
			}
			if strings.Contains(stdout, dir) {
				t.Fatalf("preview leaked full local path: %s", stdout)
			}
			after, _ := os.Stat(path)
			unchanged, _ := os.ReadFile(path)
			files, _ := os.ReadDir(dir)
			if !bytes.Equal(unchanged, input.data) || !before.ModTime().Equal(after.ModTime()) || len(files) != 1 {
				t.Fatal("preview changed input or created a side file/database")
			}
		})
	}
}

func TestMindMapImportCLIApplyPreviewPinRetryAndPayloadConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := mem.NewStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sqliteutil.OpenExisting(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables := func() []string {
		rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}
	before := tables()
	path := filepath.Join(dir, "source.md")
	original := []byte("\xef\xbb\xbf# Импорт\r\n- Ветвь 😀\r\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"import", "--file", path, "--format", "markdown", "--json"}
	preview := func() mem.OutlineImportPreview {
		stdout, _, err := captureCLIStreams(func() error { return handleMindMap(nil, store, base) })
		if err != nil {
			t.Fatal(err)
		}
		var p mem.OutlineImportPreview
		if err := json.Unmarshal([]byte(stdout), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := preview()
	if !reflect.DeepEqual(before, tables()) {
		t.Fatal("preview created map/receipt/archive tables")
	}
	request := strings.Repeat("d", 32)
	applyArgs := func(digest string) []string {
		return append(append([]string{}, base...), "--apply", "--digest", digest, "--request-id", request)
	}
	if err := handleMindMap(nil, nil, applyArgs(p.Digest)); err == nil {
		t.Fatal("apply accepted missing store")
	}
	if err := os.WriteFile(path, []byte("# Изменённый файл\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handleMindMap(nil, store, applyArgs(p.Digest)); !errors.Is(err, mem.ErrClassicMindMapImportPreview) {
		t.Fatalf("stale preview accepted: %v", err)
	}
	if !reflect.DeepEqual(before, tables()) {
		t.Fatal("failed pin created tables")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var first mem.WebMapCreated
	for attempt := 0; attempt < 2; attempt++ {
		stdout, _, err := captureCLIStreams(func() error { return handleMindMap(nil, store, applyArgs(p.Digest)) })
		if err != nil {
			t.Fatal(err)
		}
		var result mem.WebMapCreated
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatal(err)
		}
		if result.ID == "" || result.Replayed != (attempt == 1) {
			t.Fatalf("receipt result: %+v", result)
		}
		if attempt == 0 {
			first = result
		} else if result.ID != first.ID {
			t.Fatal("retry created another map")
		}
	}
	archived, err := mem.ReadWebMapImportSourceIn(context.Background(), filepath.Dir(store.Path()), first.ID)
	if err != nil || !bytes.Equal(archived.Data, original) || archived.Filename != filepath.Base(path) {
		t.Fatalf("archive exact bytes: %+v %v", archived, err)
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("apply/retry changed input")
	}
	if err := os.WriteFile(path, []byte("# Другая карта\n- Новая ветвь\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := preview()
	if err := handleMindMap(nil, store, applyArgs(other.Digest)); !errors.Is(err, mem.ErrWebMapRequest) {
		t.Fatalf("same nonce accepted changed payload: %v", err)
	}
	var maps, sources, receipts int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM mind_maps),(SELECT COUNT(*) FROM web_map_import_sources),(SELECT COUNT(*) FROM web_map_creates)`).Scan(&maps, &sources, &receipts); err != nil {
		t.Fatal(err)
	}
	if maps != 1 || sources != 1 || receipts != 1 {
		t.Fatalf("not one atomic import: maps=%d sources=%d receipts=%d", maps, sources, receipts)
	}
}
