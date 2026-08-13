package datasource

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxResponseBytes caps a single Overpass response. Real tile queries
// run well under this even at low zoom; the cap exists to stop a hostile or
// misconfigured endpoint from driving the process out of memory.
const DefaultMaxResponseBytes int64 = 64 << 20 // 64 MiB

// ErrResponseTooLarge is returned when an upstream response exceeds the cap.
var ErrResponseTooLarge = errors.New("overpass response exceeds size limit")

// limitedTransport caps how much of each response body can be read.
//
// The unbounded io.ReadAll is inside the go-overpass client, not here, so it
// cannot be fixed at the call site. Capping at the RoundTripper means the
// limit applies no matter how the library chooses to consume the body.
type limitedTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Reject early when the server declares an oversized body, so we never
	// buffer it at all.
	if resp.ContentLength > t.limit {
		if closeErr := resp.Body.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w: %d bytes declared, limit %d (closing body: %w)",
				ErrResponseTooLarge, resp.ContentLength, t.limit, closeErr)
		}
		return nil, fmt.Errorf("%w: %d bytes declared, limit %d", ErrResponseTooLarge, resp.ContentLength, t.limit)
	}

	resp.Body = &limitedBody{rc: resp.Body, remaining: t.limit, limit: t.limit}
	return resp, nil
}

// limitedBody fails the read once more than limit bytes have been consumed.
// io.LimitReader alone would silently truncate, which is worse than an error:
// the JSON would fail to parse with a confusing message, or worse, parse into
// a partial result that renders as a plausible but wrong tile.
type limitedBody struct {
	rc        io.ReadCloser
	remaining int64
	limit     int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, b.limit)
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// withResponseLimit returns a copy of client whose responses are capped at
// limit bytes. A limit of zero or less leaves the client unchanged.
func withResponseLimit(client *http.Client, limit int64) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if limit <= 0 {
		return client
	}

	limited := *client
	limited.Transport = &limitedTransport{base: client.Transport, limit: limit}
	return &limited
}
