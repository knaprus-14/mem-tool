package ingest

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPDFPreviewRendersOnlyRequestedPhysicalPage(t *testing.T) {
	e := newEngine(Options{TempRoot: t.TempDir()})
	e.resolve = func(name, explicit string) (string, error) {
		if name != "pdftoppm" {
			t.Fatalf("unexpected tool %s", name)
		}
		return "renderer", nil
	}
	var outputPath string
	e.run = func(ctx context.Context, tool string, args ...string) (commandOutput, error) {
		want := []string{"-f", "15", "-l", "15", "-scale-to", "2000", "-png", "-singlefile", "private snapshot"}
		if tool != "renderer" || len(args) != 10 || !reflect.DeepEqual(args[:9], want) {
			t.Fatalf("unexpected command %s %q", tool, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("renderer has no deadline")
		}
		outputPath = args[9] + ".png"
		f, err := os.Create(outputPath)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 6)))
		f.Close()
		return commandOutput{}, err
	}
	data, err := e.renderPDFPage(context.Background(), "private snapshot", 15)
	if err != nil || len(data) < 10 {
		t.Fatalf("preview: %d bytes, %v", len(data), err)
	}
	if _, err := os.Stat(filepath.Dir(outputPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary preview remains: %v", err)
	}
}

func TestPDFPreviewRejectsInvalidOutputAndCancellation(t *testing.T) {
	e := newEngine(Options{TempRoot: t.TempDir()})
	e.resolve = func(string, string) (string, error) { return "renderer", nil }
	e.run = func(_ context.Context, _ string, args ...string) (commandOutput, error) {
		return commandOutput{}, os.WriteFile(args[len(args)-1]+".png", []byte("not a PNG"), 0600)
	}
	if _, err := e.renderPDFPage(context.Background(), "snapshot", 1); err == nil {
		t.Fatal("invalid image accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.run = func(context.Context, string, ...string) (commandOutput, error) {
		t.Fatal("renderer invoked after cancellation")
		return commandOutput{}, nil
	}
	if _, err := e.renderPDFPage(ctx, "snapshot", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := e.renderPDFPage(context.Background(), "snapshot", 0); err == nil {
		t.Fatal("invalid page accepted")
	}
}
