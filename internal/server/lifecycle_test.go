package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
)

func newLifecycleTiles(t *testing.T) *OnDemandTiles {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return &OnDemandTiles{
		cfg:         OnDemandTilesConfig{TilesDir: t.TempDir()},
		retryQueue:  make(chan retryJob, 8),
		retryCtx:    ctx,
		retryCancel: cancel,
		stopCh:      make(chan struct{}),
	}
}

func TestStopIsIdempotentAndPrompt(t *testing.T) {
	od := newLifecycleTiles(t)
	od.wg.Add(1)
	go func() {
		defer od.wg.Done()
		<-od.retryCtx.Done()
	}()

	done := make(chan struct{})
	go func() {
		od.Stop()
		od.Stop() // must not panic or block
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return promptly")
	}
}

// A worker parked in a retry backoff must observe cancellation rather than
// sleeping out its delay, otherwise shutdown stalls for up to 30s.
func TestStopInterruptsRetryBackoff(t *testing.T) {
	od := newLifecycleTiles(t)
	od.sem = make(chan struct{}, 1)

	od.wg.Add(1)
	go func() {
		defer od.wg.Done()
		od.retryWorker()
	}()

	// z12 -> 5s base delay; Stop must not wait for it.
	od.retryQueue <- retryJob{coords: mustCoords(t, 12, 100, 100)}

	start := time.Now()
	od.Stop()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Stop took %v; the retry backoff was not interrupted", elapsed)
	}
}

// The status stream must outlive the server-wide WriteTimeout. This is the
// test that would catch a future middleware wrapping ResponseWriter without
// Unwrap(), which silently turns SetWriteDeadline into a no-op.
func TestStatusStreamOutlivesWriteTimeout(t *testing.T) {
	od := newLifecycleTiles(t)

	srv := httptest.NewUnstartedServer(od.StatusStreamHandler())
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer resp.Body.Close()

	events := 0
	deadline := time.Now().Add(2 * time.Second)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && time.Now().Before(deadline) {
		if strings.HasPrefix(scanner.Text(), "data:") {
			events++
		}
		// Well past the 200ms WriteTimeout by this point.
		if events >= 4 {
			break
		}
	}

	if events < 4 {
		t.Fatalf("received %d events, want at least 4 — the stream died at the server WriteTimeout", events)
	}
}

// BeginShutdown releases the stream so http.Server.Shutdown does not wait out
// the entire drain timeout on an idle demo tab.
func TestBeginShutdownReleasesStatusStream(t *testing.T) {
	od := newLifecycleTiles(t)

	srv := httptest.NewServer(od.StatusStreamHandler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer resp.Body.Close()

	// Wait for the stream to actually be established.
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading first byte: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_, _ = bufio.NewReader(resp.Body).ReadString(0) // read until EOF
	}()

	od.BeginShutdown()
	od.BeginShutdown() // idempotent

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("status stream did not end after BeginShutdown")
	}
}

func mustCoords(t *testing.T, z, x, y uint32) tile.Coords {
	t.Helper()
	c := tile.NewCoords(z, x, y)
	if err := c.Validate(); err != nil {
		t.Fatalf("invalid test coords: %v", err)
	}
	return c
}
