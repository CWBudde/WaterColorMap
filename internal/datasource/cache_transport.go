package datasource

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// maxCachedQueryBytes bounds how much of a request body is inspected to build
// a cache key. Tile queries are a few kilobytes; anything larger is not one of
// ours and is simply passed through uncached.
const maxCachedQueryBytes = 1 << 20

// cachingTransport answers Overpass POSTs from an on-disk cache.
//
// It lives at the RoundTripper layer for the same reason limitedTransport
// does: the bytes it needs are inside the go-overpass client, not at the call
// site. Sitting under the client also means the cache is invisible to
// everything above it — retry, the response-size cap, the worker semaphore and
// MultiOverpassDataSource's per-server routing all keep working untouched, and
// a hit feeds the decoder exactly the bytes a miss would have.
//
// Only successful, JSON-shaped 200s are stored. Errors (429, 504, an HTML
// error page, a truncated body) are passed through unstored so the retry logic
// keeps seeing them.
type cachingTransport struct {
	base  http.RoundTripper
	cache *ResponseCache
}

func (t *cachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	endpoint, query, ok := cacheableRequest(req)
	if !ok {
		return base.RoundTrip(req)
	}

	if body, hit := t.cache.Get(endpoint, query); hit {
		return cachedResponse(req, body), nil
	}

	resp, err := base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}

	// The body has to be buffered here to be stored, which costs nothing in
	// practice: go-overpass io.ReadAll's it immediately anyway. Reading through
	// the inner limitedTransport keeps the response-size cap in force on the
	// miss path — an oversized response fails here exactly as it did before,
	// and nothing is written.
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	if json.Valid(body) {
		t.cache.Put(endpoint, query, body)
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

// cacheableRequest recovers the endpoint and the Overpass query from a
// request without consuming its body.
//
// go-overpass posts a form built over a *strings.Reader, so
// http.NewRequestWithContext populates GetBody and the body can be replayed
// here. A request without GetBody is passed through uncached rather than being
// read destructively.
func cacheableRequest(req *http.Request) (endpoint, query string, ok bool) {
	if req == nil || req.Method != http.MethodPost || req.GetBody == nil || req.URL == nil {
		return "", "", false
	}

	rc, err := req.GetBody()
	if err != nil || rc == nil {
		return "", "", false
	}
	defer rc.Close() //nolint:errcheck // replayed copy of an in-memory body

	raw, err := io.ReadAll(io.LimitReader(rc, maxCachedQueryBytes+1))
	if err != nil || len(raw) > maxCachedQueryBytes {
		return "", "", false
	}

	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return "", "", false
	}
	query = values.Get("data")
	if query == "" {
		return "", "", false
	}

	return req.URL.String(), query, true
}

// cachedResponse synthesizes the 200 that the upstream would have returned.
func cachedResponse(req *http.Request, body []byte) *http.Response {
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// withResponseCache returns a copy of client whose Overpass POSTs are served
// from cache when possible. A nil cache leaves the client unchanged.
//
// Apply it *outside* withResponseLimit so the size cap still bounds the miss
// path; a cached body is already bounded by maxCacheEntryBytes on read.
func withResponseCache(client *http.Client, cache *ResponseCache) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if cache == nil {
		return client
	}

	cached := *client
	cached.Transport = &cachingTransport{base: client.Transport, cache: cache}
	return &cached
}
