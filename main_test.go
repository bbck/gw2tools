package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestServeStopsCleanly(t *testing.T) {
	// A fake API that is slow to answer, so shutdown lands mid-refresh.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(`[]`))
	}))
	defer api.Close()
	t.Setenv("GW2_API_KEY", "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, slog.New(slog.DiscardHandler), []string{
			"-db", filepath.Join(t.TempDir(), "serve.db"),
			"-listen", "127.0.0.1:0",
			"-api-url", api.URL,
		})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServeReturnsListenError(t *testing.T) {
	l := httptest.NewServer(http.NotFoundHandler()) // occupies a port
	defer l.Close()
	err := serve(context.Background(), slog.New(slog.DiscardHandler), []string{
		"-db", filepath.Join(t.TempDir(), "serve.db"),
		"-listen", l.Listener.Addr().String(),
	})
	if err == nil {
		t.Fatal("expected a listen error")
	}
}
