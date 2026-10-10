package applog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func records(t *testing.T, dir string) []Fields {
	t.Helper()
	paths, e := filepath.Glob(filepath.Join(dir, "mem-tool*.log"))
	if e != nil {
		t.Fatal(e)
	}
	var out []Fields
	for _, p := range paths {
		f, e := os.Open(p)
		if e != nil {
			t.Fatal(e)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 128<<10)
		for scanner.Scan() {
			var r Fields
			if e = json.Unmarshal(scanner.Bytes(), &r); e != nil {
				t.Fatal(p, e, string(scanner.Bytes()))
			}
			out = append(out, r)
		}
		if e = scanner.Err(); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	return out
}
func activate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	l, e := New(dir, "test", "1.0.0")
	if e != nil {
		t.Fatal(e)
	}
	old := active.Swap(l)
	t.Cleanup(func() { active.Store(old) })
	return dir
}

func TestRotationAndConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	a, e := New(dir, "a", "1.0.0")
	if e != nil {
		t.Fatal(e)
	}
	b, e := New(dir, "b", "1.0.0")
	if e != nil {
		t.Fatal(e)
	}
	a.limit = 2000
	b.limit = 2000
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := a
			if i%2 == 0 {
				l = b
			}
			for j := 0; j < 30; j++ {
				if e := l.write("INFO", "событие", Fields{"writer": i, "ordinal": j}); e != nil {
					t.Error(e)
				}
			}
		}(i)
	}
	wg.Wait()
	rows := records(t, dir)
	if len(rows) != 242 {
		t.Fatal(len(rows))
	}
	archives, _ := filepath.Glob(filepath.Join(dir, "mem-tool.*.log"))
	if len(archives) < 2 {
		t.Fatal("not rotated")
	}
}
func TestLogHelperProcess(t *testing.T) {
	dir := os.Getenv("MEM_LOG_TEST_CHILD_DIR")
	if dir == "" {
		return
	}
	l, e := New(dir, "child", "1.0.0")
	if e != nil {
		t.Fatal(e)
	}
	l.limit = 2000
	for i := 0; i < 25; i++ {
		if e = l.write("INFO", "child.event", Fields{"ordinal": i}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSeparateProcessesShareLogAndRotation(t *testing.T) {
	dir := t.TempDir()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe, "-test.run=^TestLogHelperProcess$")
			cmd.Env = append(os.Environ(), "MEM_LOG_TEST_CHILD_DIR="+dir)
			if out, e := cmd.CombinedOutput(); e != nil {
				t.Errorf("child: %v %s", e, out)
			}
		}()
	}
	wg.Wait()
	if n := len(records(t, dir)); n != 78 {
		t.Fatal(n)
	}
}
func TestLifecycleFixedLaunchDirectoryAndPrivatePayloads(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	other := t.TempDir()
	code := Run("test", "1.0.0", func() int {
		if e := os.Chdir(other); e != nil {
			t.Fatal(e)
		}
		func() (err error) {
			defer Track("import")(&err)
			return fmt.Errorf("secret document and api_key=private: %w", context.DeadlineExceeded)
		}()
		log.Print("token=private full document")
		return 7
	})
	if code != 7 {
		t.Fatal(code)
	}
	rows := records(t, dir)
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret document") {
		t.Fatal("payload leaked", string(encoded))
	}
	if _, e := os.Stat(filepath.Join(other, FileName)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("log moved with cwd", e)
	}
	if rows[len(rows)-1]["event"] != "process.exit" {
		t.Fatal(rows)
	}
}
func TestHTTPStatusPrivacyAndFlush(t *testing.T) {
	dir := activate(t)
	h := HTTP("web", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("secret response"))
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/api/v1/maps/private-title?token=private", strings.NewReader("secret body")))
	if w.Code != 409 || !w.Flushed || w.Body.String() != "secret response" {
		t.Fatal(w)
	}
	rows := records(t, dir)
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret") {
		t.Fatal("request payload leaked", string(encoded))
	}
	f := rows[len(rows)-1]["data"].(map[string]any)
	if f["status"] != float64(409) || f["bytes"] != float64(15) {
		t.Fatal(f)
	}
}
func TestPanicIsRecordedAndRethrown(t *testing.T) {
	dir := activate(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		defer Track("broken")(nil)
		panic("private panic payload")
	}()
	rows := records(t, dir)
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "private panic") {
		t.Fatal("panic payload leaked")
	}
	f := rows[len(rows)-1]["data"].(map[string]any)
	if f["outcome"] != "panic" {
		t.Fatal(f)
	}
}

func TestCommandPanicIsNotLoggedAsCompleted(t *testing.T) {
	dir := activate(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		code := 0
		defer TrackCode("command")(&code)
		panic("private payload")
	}()
	rows := records(t, dir)
	f := rows[len(rows)-1]["data"].(map[string]any)
	if f["outcome"] != "failed" || rows[len(rows)-1]["level"] != "ERROR" {
		t.Fatal(rows)
	}
}
func TestOpenFailureDoesNotStopCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if e := os.Mkdir(FileName, 0700); e != nil {
		t.Fatal(e)
	}
	called := false
	if code := Run("test", "1.0.0", func() int { called = true; return 0 }); code != 0 || !called {
		t.Fatal(code, called)
	}
}

func TestRotationFailureKeepsWriting(t *testing.T) {
	dir := t.TempDir()
	l, e := New(dir, "test", "1.0.0")
	if e != nil {
		t.Fatal(e)
	}
	l.limit = 100
	l.rename = func(string, string) error { return os.ErrPermission }
	for i := 0; i < 4; i++ {
		if e = l.write("INFO", "test.event", nil); e != nil {
			t.Fatal(e)
		}
	}
	rows := records(t, dir)
	if len(rows) != 5 || rows[4]["rotation_deferred"] != true {
		t.Fatal(rows)
	}
	l.rename = os.Rename
	if e = l.write("INFO", "after.unlock", nil); e != nil {
		t.Fatal(e)
	}
	if len(records(t, dir)) != 6 {
		t.Fatal("lost deferred records")
	}
}

func TestTransportPreservesRequestAndExcludesCredentials(t *testing.T) {
	dir := activate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "private document" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("request changed")
		}
		w.WriteHeader(503)
		_, _ = w.Write([]byte("private model response"))
	}))
	defer srv.Close()
	client := &http.Client{Transport: Transport{Component: "model.http"}}
	r, _ := http.NewRequest("POST", srv.URL+"/token-secret?api_key=secret", strings.NewReader("private document"))
	r.Header.Set("Authorization", "Bearer secret")
	response, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 503 || string(body) != "private model response" {
		t.Fatal(response.StatusCode, string(body))
	}
	rows := records(t, dir)
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "private") {
		t.Fatal("transport leaked data", string(b))
	}
	f := rows[len(rows)-1]["data"].(map[string]any)
	if f["status"] != float64(503) {
		t.Fatal(f)
	}
}
