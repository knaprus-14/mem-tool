package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type mapOpenOptions struct {
	Port      int
	Title     string
	NoBrowser bool
}

var launchKnowledgeMapBrowser = openBrowserURL

func handleMapOpen(store *Store, args []string) error {
	options, err := parseMapOpenOptions(args)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(options.Port)))
	if err != nil {
		return fmt.Errorf("map open: listen on loopback: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return fmt.Errorf("map open: refused non-loopback listener %q", listener.Addr())
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", address.Port)
	sessionToken, err := newKnowledgeMapSessionToken()
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           mem.NewKnowledgeMapWorkspaceHandler(store, options.Title, sessionToken, mem.DefaultKnowledgeMapView),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(os.Stdout, "Живая карта: %s\n", url)
	fmt.Fprintln(os.Stdout, "Источник данных: активная локальная база; расположение узлов и масштаб сохраняются автоматически; Ctrl+C — остановить сервер.")
	if !options.NoBrowser {
		if err := launchKnowledgeMapBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "[MAP OPEN] Не удалось открыть браузер автоматически: %v\n", err)
			fmt.Fprintln(os.Stderr, "[MAP OPEN] Откройте адрес вручную:", url)
		}
	}
	return serveKnowledgeMap(ctx, server, listener)
}

func newKnowledgeMapSessionToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("map open: create session capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func parseMapOpenOptions(args []string) (mapOpenOptions, error) {
	options := mapOpenOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-browser":
			options.NoBrowser = true
		case "--port":
			if i+1 >= len(args) {
				return mapOpenOptions{}, errors.New("использование: mem map open [--port N] [--title <текст>] [--no-browser]")
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil || port < 0 || port > 65535 {
				return mapOpenOptions{}, errors.New("map open: --port должен быть от 0 до 65535; 0 выбирает свободный порт")
			}
			options.Port = port
		case "--title":
			if i+1 >= len(args) {
				return mapOpenOptions{}, errors.New("использование: mem map open [--port N] [--title <текст>] [--no-browser]")
			}
			i++
			options.Title = strings.TrimSpace(args[i])
			if options.Title == "" {
				return mapOpenOptions{}, errors.New("map open: --title не должен быть пустым")
			}
		default:
			return mapOpenOptions{}, fmt.Errorf("неизвестный аргумент map open: %s", args[i])
		}
	}
	return options, nil
}

func serveKnowledgeMap(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("map open: serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("map open: shutdown: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("map open: serve: %w", err)
		}
		fmt.Fprintln(os.Stdout, "Живая карта остановлена.")
		return nil
	}
}

func openBrowserURL(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	case "linux":
		command = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("автоматическое открытие браузера не поддерживается для %s", runtime.GOOS)
	}
	return command.Start()
}
