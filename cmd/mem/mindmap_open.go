package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

type mindMapOpenOptions struct {
	Port      int
	NoBrowser bool
}

var launchClassicMindMapBrowser = openBrowserURL

func handleClassicMindMapOpen(cfg *Config, store *Store, args []string) error {
	options, err := parseMindMapOpenOptions(args)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(options.Port)))
	if err != nil {
		return fmt.Errorf("mindmap open: listen on loopback: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return fmt.Errorf("mindmap open: refused non-loopback listener %q", listener.Addr())
	}
	sessionToken, err := newKnowledgeMapSessionToken()
	if err != nil {
		return fmt.Errorf("mindmap open: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var assistant *mem.ClassicMindMapAIWorkspace
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "[MINDMAP OPEN] AI-помощник отключён: конфигурация недоступна.")
	} else {
		answerCfg := cfg.Answer.WithMapGenerationDefaults()
		provider, providerErr := newAnswerProvider(answerCfg)
		if providerErr != nil {
			// The manual editor must remain available even when the optional
			// answer model is not configured. AI endpoints return 503 until the
			// user fixes config and restarts this local workspace.
			fmt.Fprintf(os.Stderr, "[MINDMAP OPEN] AI-помощник отключён: %v\n", providerErr)
		} else {
			assistant = mem.NewClassicMindMapAIWorkspace(ctx, &mem.ClassicMindMapAIService{
				Store: store, Provider: provider, Config: answerCfg,
			})
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", address.Port)
	server := &http.Server{
		Handler:           mem.NewClassicMindMapWorkspaceHandlerWithAI(store, sessionToken, assistant),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	fmt.Fprintln(os.Stdout, "Библиотека карт мыслей:", url)
	fmt.Fprintln(os.Stdout, "Редактор работает только с активной локальной базой; Ctrl+C — остановить сервер.")
	if !options.NoBrowser {
		if err := launchClassicMindMapBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "[MINDMAP OPEN] Не удалось открыть браузер автоматически: %v\n", err)
			fmt.Fprintln(os.Stderr, "[MINDMAP OPEN] Откройте адрес вручную:", url)
		}
	}
	return serveClassicMindMap(ctx, server, listener)
}

func parseMindMapOpenOptions(args []string) (mindMapOpenOptions, error) {
	var options mindMapOpenOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-browser":
			options.NoBrowser = true
		case "--port":
			if i+1 >= len(args) {
				return options, errors.New("использование: mem mindmap open [--port N] [--no-browser]")
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil || port < 0 || port > 65535 {
				return options, errors.New("mindmap open: --port должен быть от 0 до 65535; 0 выбирает свободный порт")
			}
			options.Port = port
		default:
			return options, fmt.Errorf("неизвестный аргумент mindmap open: %s", args[i])
		}
	}
	return options, nil
}

func serveClassicMindMap(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("mindmap open: serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("mindmap open: shutdown: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("mindmap open: serve: %w", err)
		}
		fmt.Fprintln(os.Stdout, "Редактор карт мыслей остановлен.")
		return nil
	}
}
