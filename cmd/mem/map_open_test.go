package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseMapOpenOptions(t *testing.T) {
	options, err := parseMapOpenOptions([]string{"--port", "8123", "--title", "Карта проекта", "--no-browser"})
	if err != nil || options.Port != 8123 || options.Title != "Карта проекта" || !options.NoBrowser {
		t.Fatalf("unexpected map open options: %#v err=%v", options, err)
	}
	for _, args := range [][]string{
		{"--port"}, {"--port", "-1"}, {"--port", "65536"}, {"--title", "  "}, {"--unknown"},
	} {
		if _, err := parseMapOpenOptions(args); err == nil {
			t.Fatalf("invalid map open options were accepted: %v", args)
		}
	}
}

func TestNewKnowledgeMapSessionToken(t *testing.T) {
	first, err := newKnowledgeMapSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newKnowledgeMapSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 40 || first == second || strings.ContainsAny(first, "+/=") {
		t.Fatalf("session token is not a unique base64url capability: first=%q second=%q", first, second)
	}
}

func TestServeKnowledgeMapStopsWithContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() { done <- serveKnowledgeMap(ctx, server, listener) }()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		cancel()
		t.Fatalf("map server did not accept a loopback request: %v", err)
	}
	response.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("map server shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("map server did not stop after context cancellation")
	}
}
