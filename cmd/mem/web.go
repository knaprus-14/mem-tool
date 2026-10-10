package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/knaprus-14/mem-tool/internal/webapp"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type webOptions struct {
	Port             int
	NoBrowser        bool
	StateDir, Config string
}

var launchWebBrowser = openBrowserURL

func parseWebOptions(command string, args []string) (webOptions, error) {
	var o webOptions
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		key := args[i]
		if seen[key] {
			return o, fmt.Errorf("%s: повторный флаг %s", command, key)
		}
		seen[key] = true
		if command == "gui" && key == "--no-browser" {
			o.NoBrowser = true
			continue
		}
		if (command == "gui" && (key == "--port" || key == "--state-dir")) || (command == "serve" && key == "--config") {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "--") {
				return o, fmt.Errorf("%s: флаг %s требует значение", command, key)
			}
			i++
			switch key {
			case "--port":
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 0 || n > 65535 {
					return o, fmt.Errorf("gui: порт должен быть от 0 до 65535")
				}
				o.Port = n
			case "--state-dir":
				o.StateDir = args[i]
			case "--config":
				o.Config = args[i]
			}
			continue
		}
		return o, fmt.Errorf("%s: неизвестный аргумент %s", command, key)
	}
	if command == "serve" && o.Config == "" {
		return o, fmt.Errorf("использование: mem serve --config <server.json>")
	}
	return o, nil
}

func handleWebCommand(command string, args []string, useGlobal bool, customDir string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleWebCommand")(&applogResult0)

	o, err := parseWebOptions(command, args)
	if err != nil {
		return err
	}
	var cfg webapp.Config
	initialRoot := ""
	if command == "serve" {
		if useGlobal || customDir != "" {
			return fmt.Errorf("serve: каталоги задаются в server.json; --dir и --global относятся к локальным командам")
		}
		cfg, err = webapp.LoadConfig(o.Config)
		if err != nil {
			return err
		}
	} else {
		initialRoot = customDir
		if useGlobal {
			initialRoot = filepath.Dir(mem.GlobalMemDir())
		}
		if initialRoot == "" {
			initialRoot, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		initialRoot, err = filepath.Abs(initialRoot)
		if err != nil {
			return err
		}
		if filepath.Base(initialRoot) == mem.MemDirName {
			initialRoot = filepath.Dir(initialRoot)
		}
		info, e := os.Stat(initialRoot)
		if e != nil {
			return e
		}
		if !info.IsDir() {
			return fmt.Errorf("gui: выберите папку базы")
		}
		if o.StateDir == "" {
			base, e := os.UserConfigDir()
			if e != nil {
				return e
			}
			o.StateDir = filepath.Join(base, "mem-tool", "web")
		}
		cfg = webapp.Config{Local: true, Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)), DataDir: o.StateDir}
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("веб-сервер: %w", err)
	}
	defer listener.Close()
	if cfg.Local {
		cfg.PublicOrigin = "http://" + listener.Addr().String()
	}
	manager, err := workspace.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("каталог веб-баз: %w", err)
	}
	defer manager.Close()
	initialID := ""
	if cfg.Local {
		if _, e := os.Stat(filepath.Join(initialRoot, mem.MemDirName)); e == nil {
			w, e := manager.Register(context.Background(), workspace.Actor{ID: "local"}, initialRoot)
			if e != nil {
				return e
			}
			initialID = w.ID
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	app, err := webapp.New(cfg, manager)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Start(ctx)
	defer app.Close()
	url := cfg.PublicOrigin + "/"
	if initialID != "" {
		url += "#workspace=" + initialID
	}
	fmt.Fprintln(os.Stdout, "mem-tool web:", url)
	if cfg.Local {
		fmt.Fprintln(os.Stdout, "Локальная библиотека. Ctrl+C — остановить сервер.")
	} else {
		fmt.Fprintln(os.Stdout, "Серверный режим: вход по разрешённой учётной записи. Доступ через настроенный HTTPS-прокси.")
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	if cfg.Local && !o.NoBrowser {
		if e := launchWebBrowser(url); e != nil {
			fmt.Fprintln(os.Stderr, "Не удалось открыть браузер; откройте адрес выше вручную.")
		}
	}
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			<-serveErrors
			return err
		}
		err := <-serveErrors
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
