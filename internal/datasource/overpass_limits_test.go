package datasource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

const emptyOverpassJSON = `{"version":0.6,"generator":"test","elements":[]}`

// newTestDataSource points a datasource at a local fake Overpass server.
// Retries are disabled so a test failure surfaces immediately instead of being
// retried with backoff.
func newTestDataSource(t *testing.T, h http.Handler, maxBytes int64) *OverpassDataSource {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// NewWithSettings applies the library's default retry config internally,
	// so retries must be disabled explicitly rather than by passing nil.
	// Without this, every failure case below is retried with backoff.
	noRetry := overpass.RetryConfig{MaxRetries: 0}

	return NewOverpassDataSourceWithConfig(OverpassConfig{
		Endpoint:         srv.URL,
		Workers:          1,
		RetryConfig:      &noRetry,
		HTTPClient:       &http.Client{},
		MaxResponseBytes: maxBytes,
	})
}

func testTile() (types.TileCoordinate, types.BoundingBox) {
	coord := types.TileCoordinate{Zoom: 13, X: 4317, Y: 2692}
	return coord, types.TileToBounds(coord)
}

// The context threaded through FetchTileDataWithBounds used to be dropped on
// the floor: the deprecated client.Query() was called instead of
// QueryContext(), so a cancelled request could not abort an in-flight fetch
// and a hung upstream pinned a fetch worker.
func TestFetchHonoursContextCancellation(t *testing.T) {
	// Hold the request open so the only way out is the client's context.
	released := make(chan struct{})
	ds := newTestDataSource(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}), DefaultMaxResponseBytes)

	// Registered after the server, so LIFO cleanup releases the handler
	// before httptest.Close waits on it. Relying on the server-side request
	// context alone deadlocks here: it is not always cancelled promptly when
	// the client aborts.
	t.Cleanup(func() { close(released) })

	coord, bounds := testTile()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ds.FetchTileDataWithBounds(ctx, coord, bounds)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the fetch to fail when its context expires")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("fetch took %v; the context did not abort it", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// The unbounded io.ReadAll lives inside the go-overpass client, so the cap is
// enforced at the transport instead.
func TestFetchRejectsOversizedResponse(t *testing.T) {
	const limit = 4096

	t.Run("declared content length", func(t *testing.T) {
		body := strings.Repeat("x", limit*4)
		ds := newTestDataSource(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}), limit)

		coord, bounds := testTile()
		_, err := ds.FetchTileDataWithBounds(context.Background(), coord, bounds)
		if err == nil {
			t.Fatal("expected an error for an oversized response")
		}
		if !strings.Contains(err.Error(), ErrResponseTooLarge.Error()) {
			t.Fatalf("error = %v, want it to mention %v", err, ErrResponseTooLarge)
		}
	})

	// A chunked response declares no length, so the cap has to bite during
	// the read. It must fail rather than truncate: silently truncated JSON
	// either fails to parse confusingly or, worse, parses into a partial
	// result that renders as a plausible but wrong tile.
	t.Run("streamed without content length", func(t *testing.T) {
		ds := newTestDataSource(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("test server response is not flushable")
				return
			}
			chunk := strings.Repeat("x", 1024)
			for range 16 {
				if _, err := fmt.Fprint(w, chunk); err != nil {
					return
				}
				flusher.Flush()
			}
		}), limit)

		coord, bounds := testTile()
		_, err := ds.FetchTileDataWithBounds(context.Background(), coord, bounds)
		if err == nil {
			t.Fatal("expected an error for an oversized streamed response")
		}
		if !strings.Contains(err.Error(), ErrResponseTooLarge.Error()) {
			t.Fatalf("error = %v, want it to mention %v", err, ErrResponseTooLarge)
		}
	})

	t.Run("response under the limit is accepted", func(t *testing.T) {
		ds := newTestDataSource(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, emptyOverpassJSON)
		}), limit)

		coord, bounds := testTile()
		// An empty element list fails the repo's own zoom-based response
		// validation, which is fine here: what matters is that the failure
		// is not about the size cap.
		_, err := ds.FetchTileDataWithBounds(context.Background(), coord, bounds)
		if err != nil && strings.Contains(err.Error(), ErrResponseTooLarge.Error()) {
			t.Fatalf("under-limit response was rejected as too large: %v", err)
		}
	})
}

// A body of exactly limit bytes is within the cap. io.ReadAll still issues one
// more read to find EOF, so the wrapper must not mistake that read for an overrun.
func TestLimitedBodyBoundary(t *testing.T) {
	const limit = 32

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "one byte under the limit", size: limit - 1},
		{name: "exactly at the limit", size: limit},
		{name: "one byte over the limit", size: limit + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := strings.Repeat("x", tt.size)
			// iotest.OneByteReader mimics a chunked body: the reader never
			// signals EOF together with the final bytes.
			body := &limitedBody{
				rc:        io.NopCloser(iotest.OneByteReader(strings.NewReader(payload))),
				remaining: limit,
				limit:     limit,
			}

			got, err := io.ReadAll(body)
			switch {
			case tt.wantErr && !errors.Is(err, ErrResponseTooLarge):
				t.Fatalf("error = %v, want %v", err, ErrResponseTooLarge)
			case !tt.wantErr && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case !tt.wantErr && string(got) != payload:
				t.Fatalf("read %d bytes, want %d", len(got), len(payload))
			}
		})
	}
}

// The multi-server path builds its OverpassConfig as a literal, so an omitted
// cap must still be defaulted rather than leaving those endpoints unbounded.
func TestMultiServerAppliesDefaultResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Declaring more than the default cap is enough: the transport
		// rejects the response before any body is buffered.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.FormatInt(DefaultMaxResponseBytes+1, 10))
		fmt.Fprint(w, emptyOverpassJSON)
	}))
	t.Cleanup(srv.Close)

	noRetry := overpass.RetryConfig{MaxRetries: 0}
	ds := NewMultiOverpassDataSource(ServerConfig{
		Endpoint:    srv.URL,
		Workers:     1,
		RetryConfig: &noRetry,
		HTTPClient:  &http.Client{},
		Name:        "test",
		// MaxResponseBytes deliberately left unset.
	})

	coord, bounds := testTile()
	_, err := ds.FetchTileDataWithBounds(context.Background(), coord, bounds)
	if err == nil {
		t.Fatal("expected an error for an oversized response")
	}
	if !strings.Contains(err.Error(), ErrResponseTooLarge.Error()) {
		t.Fatalf("error = %v, want it to mention %v", err, ErrResponseTooLarge)
	}
}

func TestWithResponseLimit(t *testing.T) {
	t.Run("zero limit leaves the client untouched", func(t *testing.T) {
		client := &http.Client{}
		if got := withResponseLimit(client, 0); got != client {
			t.Fatal("expected the same client back when the limit is disabled")
		}
	})

	t.Run("preserves client settings", func(t *testing.T) {
		client := &http.Client{Timeout: 42 * time.Second}
		limited := withResponseLimit(client, 1024)

		if limited == client {
			t.Fatal("expected a copy, not the original client")
		}
		if limited.Timeout != 42*time.Second {
			t.Fatalf("Timeout = %v, want 42s", limited.Timeout)
		}
		if _, ok := limited.Transport.(*limitedTransport); !ok {
			t.Fatalf("Transport = %T, want *limitedTransport", limited.Transport)
		}
	})
}

func TestDefaultConfigHasTimeoutAndLimit(t *testing.T) {
	cfg := DefaultOverpassConfig()

	if cfg.HTTPClient.Timeout <= 0 {
		t.Error("default HTTP client has no timeout; a hung upstream would pin a fetch worker forever")
	}
	if cfg.MaxResponseBytes <= 0 {
		t.Error("default config does not cap response size")
	}
}
