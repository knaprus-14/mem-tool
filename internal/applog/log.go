// Package applog writes bounded, structured technical events without application
// payloads. Logging is opt-in for library consumers and enabled by every command.
package applog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/knaprus-14/mem-tool/internal/filelock"
)

const FileName = "mem-tool.log"
const rotationBytes int64 = 10 << 20

func IsLogFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == FileName || name == FileName+".lock" || (strings.HasPrefix(name, "mem-tool.") && strings.HasSuffix(name, ".log"))
}

type Fields map[string]any
type Logger struct {
	mu                            sync.Mutex
	path, component, version, run string
	seq                           uint64
	limit                         int64
	warned                        bool
	rename                        func(string, string) error
}

var active atomic.Pointer[Logger]
var spanSequence atomic.Uint64

func New(dir, component, version string) (*Logger, error) {
	abs, err := filepath.Abs(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	id := make([]byte, 12)
	if _, err = rand.Read(id); err != nil {
		return nil, err
	}
	l := &Logger{path: abs, component: component, version: version, run: hex.EncodeToString(id), limit: rotationBytes, rename: os.Rename}
	if err = l.write("INFO", "logging.ready", nil); err != nil {
		return nil, err
	}
	return l, nil
}

// Run pins the log to the launch directory, before --dir or the TUI changes cwd.
// Logging failure is visible on stderr but never replaces a successful operation.
func Run(component, version string, run func() int) (code int) {
	dir, err := os.Getwd()
	var l *Logger
	if err == nil {
		l, err = New(dir, component, version)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Не удалось открыть %s: %v\n", FileName, err)
		return run()
	}
	previous := active.Swap(l)
	oldWriter, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(io.MultiWriter(oldWriter, legacyWriter{}))
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
		active.Store(previous)
	}()
	started := time.Now()
	Event("INFO", "process.start", nil)
	defer func() {
		if p := recover(); p != nil {
			Event("ERROR", "process.panic", Fields{"panic_type": fmt.Sprintf("%T", p), "stack": stackFrames()})
			Event("ERROR", "process.exit", Fields{"exit_code": 2, "duration_ms": time.Since(started).Milliseconds()})
			panic(p)
		}
		level := "INFO"
		if code != 0 {
			level = "ERROR"
		}
		Event(level, "process.exit", Fields{"exit_code": code, "duration_ms": time.Since(started).Milliseconds()})
	}()
	return run()
}

func Event(level, event string, fields Fields) {
	if l := active.Load(); l != nil {
		if err := l.write(level, event, fields); err != nil {
			l.mu.Lock()
			defer l.mu.Unlock()
			if !l.warned {
				fmt.Fprintf(os.Stderr, "Ошибка записи %s: %v\n", FileName, err)
				l.warned = true
			}
		}
	}
}

func (l *Logger) write(level, event string, fields Fields) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	record := Fields{"time": time.Now().UTC().Format(time.RFC3339Nano), "level": level, "event": event, "component": l.component, "version": l.version, "pid": os.Getpid(), "run_id": l.run, "seq": l.seq}
	if _, file, line, ok := runtime.Caller(2); ok {
		record["site"] = filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	if fields != nil {
		record["data"] = fields
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(line) > 32<<10 {
		return errors.New("log record exceeds 32 KiB")
	}
	line = append(line, '\n')
	until := time.Now().Add(2 * time.Second)
	var lock *os.File
	for {
		lock, err = filelock.Try(l.path + ".lock")
		if !errors.Is(err, filelock.ErrBusy) || time.Now().After(until) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if info, e := os.Stat(l.path); e == nil && info.Size()+int64(len(line)) > l.limit {
		archive := filepath.Join(filepath.Dir(l.path), "mem-tool."+time.Now().UTC().Format("20060102T150405.000000000")+"."+strconv.Itoa(os.Getpid())+"."+strconv.FormatUint(l.seq, 10)+".log")
		if err = l.rename(l.path, archive); err != nil {
			// Windows viewers may deny renaming. Keep appending and retry later.
			record["rotation_deferred"] = true
			line, _ = json.Marshal(record)
			line = append(line, '\n')
		}
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Track records paired events. The returned pointer is read at function exit,
// after its named error result has been assigned by any return path.
func Track(operation string) func(*error) {
	if active.Load() == nil {
		return func(*error) {}
	}
	id := spanSequence.Add(1)
	started := time.Now()
	Event("DEBUG", operation+".start", Fields{"operation_id": id})
	return func(result *error) {
		panicked := recover()
		f := Fields{"operation_id": id, "duration_ms": time.Since(started).Milliseconds(), "outcome": "completed"}
		level := "INFO"
		if result != nil && *result != nil {
			level = "ERROR"
			f["outcome"] = "failed"
			for k, v := range ErrorFields(*result) {
				f[k] = v
			}
		}
		if panicked != nil {
			level = "ERROR"
			f["outcome"], f["panic_type"], f["stack"] = "panic", fmt.Sprintf("%T", panicked), stackFrames()
		}
		Event(level, operation+".finish", f)
		if panicked != nil {
			panic(panicked)
		}
	}
}

// ErrorFields intentionally excludes Error(): provider/SQL/parser errors can
// contain full documents, credentials or generated text. IDs correlate repeats.
func ErrorFields(err error) Fields {
	if err == nil {
		return nil
	}
	kind := "other"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = "timeout"
	case errors.Is(err, context.Canceled):
		kind = "cancelled"
	case errors.Is(err, os.ErrPermission):
		kind = "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		kind = "not_found"
	case errors.Is(err, io.EOF):
		kind = "unexpected_end"
	}
	f := Fields{"error_kind": kind, "error_type": fmt.Sprintf("%T", err), "error_id": Ref(err.Error())}
	root := err
	for i := 0; i < 8; i++ {
		next := errors.Unwrap(root)
		if next == nil {
			break
		}
		root = next
	}
	f["cause_type"] = fmt.Sprintf("%T", root)
	var exited interface{ ExitCode() int }
	if errors.As(err, &exited) {
		f["exit_code"] = exited.ExitCode()
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		f["error_kind"] = "timeout"
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		f["error_code"] = coded.Code()
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		f["error_kind"] = "invalid_json"
		f["offset"] = syntax.Offset
	}
	return f
}
func Error(operation string, err error) {
	if err != nil {
		Event("ERROR", operation, ErrorFields(err))
	}
}
func TrackCode(operation string) func(*int) {
	finish := Track(operation)
	return func(code *int) {
		panicked := recover()
		var err error
		if code != nil && *code != 0 {
			err = fmt.Errorf("exit code %d", *code)
		}
		if panicked != nil {
			err = errors.New("command panicked")
			Event("ERROR", operation+".panic", Fields{"panic_type": fmt.Sprintf("%T", panicked), "stack": stackFrames()})
		}
		finish(&err)
		if panicked != nil {
			panic(panicked)
		}
	}
}
func Ref(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

type legacyWriter struct{}

func (legacyWriter) Write(p []byte) (int, error) {
	// Existing console diagnostics remain untouched. Their raw text is not copied
	// because third-party errors may contain user data or a Telegram token.
	Event("INFO", "legacy.log", Fields{"message_id": Ref(string(p)), "bytes": len(p)})
	return len(p), nil
}
func stackFrames() []string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	out := []string{}
	for {
		frame, more := frames.Next()
		out = append(out, frame.Function+":"+strconv.Itoa(frame.Line))
		if !more {
			break
		}
	}
	return out
}
