package sourcefile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTextLimitsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.txt")
	for _, tc := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"unicode", []byte("Текст документа\n"), true},
		{"binary", []byte{'a', 0, 'b'}, false},
		{"invalid UTF-8", []byte{0xff}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			data, err := ReadText(context.Background(), path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && string(data) != string(tc.data) {
				t.Fatal("text changed")
			}
		})
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(MaxTextBytes + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadText(context.Background(), path); err == nil {
		t.Fatal("oversized file accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadText(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestSnapshotPinsVerifiedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	data := []byte("%PDF-1.4\noriginal source")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	snapshot, hash, err := Snapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { snapshot.Close(); os.Remove(snapshot.Name()) }()
	if hash != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
		t.Fatalf("wrong hash %s", hash)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(snapshot)
	if err != nil || string(actual) != string(data) {
		t.Fatalf("snapshot followed changed source: %q, %v", actual, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Snapshot(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation lost: %v", err)
	}
}
