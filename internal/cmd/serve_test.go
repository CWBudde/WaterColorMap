package cmd

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Cheap regression guard: every connection phase must stay bounded. Only
// ReadHeaderTimeout was set before, which left the server open to slow-loris
// bodies and to clients that request a tile and never read the response.
func TestNewHTTPServerSetsAllTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	tests := []struct {
		name string
		got  time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout},
		{"WriteTimeout", srv.WriteTimeout},
		{"IdleTimeout", srv.IdleTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got <= 0 {
				t.Fatalf("%s is unset", tt.name)
			}
		})
	}

	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want a bound below the 1MiB default", srv.MaxHeaderBytes)
	}
	if srv.ErrorLog == nil {
		t.Fatal("ErrorLog is nil; server errors would bypass structured logging")
	}
}

func TestRunHTTPServerDrainsAndStops(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := newHTTPServer(addr, mux, logger)

	// RegisterOnShutdown callbacks run in their own goroutines, so the
	// counter has to be atomic.
	var shutdownCalls atomic.Int32
	srv.RegisterOnShutdown(func() { shutdownCalls.Add(1) })

	errCh := make(chan error, 1)
	go func() { errCh <- runHTTPServer(srv, 5*time.Second, logger) }()

	// Wait for the listener to come up.
	var resp *http.Response
	for range 100 {
		resp, err = http.Get("http://" + addr + "/healthz") //nolint:noctx // short-lived probe
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never became reachable: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing probe response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	// Shutting the server down directly exercises the same drain path a
	// signal would take, without sending a signal to the test process.
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		// A clean shutdown must not surface as an error: ErrServerClosed is
		// the expected outcome, not a failure.
		if err != nil {
			t.Fatalf("runHTTPServer returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runHTTPServer did not return after shutdown")
	}

	if got := shutdownCalls.Load(); got != 1 {
		t.Fatalf("onShutdown ran %d times, want 1", got)
	}
}
