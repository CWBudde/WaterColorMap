package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A validator plus a revalidating default is what makes the tile cache reach
// the client: before this, every browser refetched every tile in full on every
// map pan, and the Last-Modified handling net/http already provided was dead
// because `no-store` forbade the client from keeping anything to revalidate.
func TestConditionalRequestReturns304(t *testing.T) {
	od, gen := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	first := get(t, od, "/tiles/z1_x0_y0.png")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the tile response")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
	req.Header.Set("If-None-Match", etag)
	od.serveTile(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty on a 304", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want the unchanged %q", got, etag)
	}
	if got := rec.Header().Get(cacheStatusHeader); got != cacheStatusHit {
		t.Errorf("%s = %q, want %q — a revalidated tile is still a hit", cacheStatusHeader, got, cacheStatusHit)
	}
	if got := od.Status().Cache.NotModified; got != 1 {
		t.Errorf("not_modified = %d, want 1", got)
	}
	if got := gen.renders.Load(); got != 0 {
		t.Error("a revalidation rendered a tile")
	}
}

// If-Modified-Since is counted too, which is why the status is captured from
// the response rather than guessed from the request headers.
func TestIfModifiedSinceIsCounted(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	path := writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
	req.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	od.serveTile(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if got := od.Status().Cache.NotModified; got != 1 {
		t.Errorf("not_modified = %d, want 1", got)
	}
}

// A re-rendered tile has to invalidate the client's copy, or a purge would be
// invisible to everyone who already loaded the map.
func TestETagChangesWhenTheTileIsRewritten(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	path := writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "first")

	before := get(t, od, "/tiles/z1_x0_y0.png").Header().Get("ETag")

	// A different size and a later mtime: both halves of the validator move.
	if err := os.WriteFile(path, []byte("second render, longer"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	after := get(t, od, "/tiles/z1_x0_y0.png").Header().Get("ETag")
	if after == before {
		t.Fatalf("ETag = %q unchanged after the tile was rewritten", after)
	}
}

// The default has to make the validator usable. `no-store` forbids the client
// from keeping the tile at all, so it can never send a conditional request --
// which is what made the previous default silently disable this whole path.
func TestDefaultCacheControlAllowsRevalidation(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	if got := od.cfg.CacheControl; got != "no-cache" {
		t.Fatalf("default Cache-Control = %q, want no-cache", got)
	}
	if got := get(t, od, "/tiles/z1_x0_y0.png").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// An operator who wants the old behaviour keeps it, and it must genuinely
// suppress client storage rather than merely change the header.
func TestCacheControlOverrideStillWins(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{
		GenerateMissing: true,
		CacheControl:    "no-store",
	}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	if got := get(t, od, "/tiles/z1_x0_y0.png").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want the configured no-store", got)
	}
}

// The MBTiles backend wrote raw bytes with no validator at all, so a client had
// no way to revalidate and every request cost a full body. A row has no mtime,
// so the bytes themselves are the only honest validator.
func TestMBTilesConditionalRequestReturns304(t *testing.T) {
	const z, x, y = 13, 4317, 2692

	dbPath, want := newTestMBTiles(t, z, x, y)

	h, err := NewMBTilesHandler(MBTilesConfig{MBTilesPath: dbPath, CacheControl: "no-cache"}, nil)
	if err != nil {
		t.Fatalf("NewMBTilesHandler: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	const url = "/tiles/z13_x4317_y2692.png"

	first := httptest.NewRecorder()
	h.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, url, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}
	if got := first.Body.Bytes(); string(got) != string(want) {
		t.Fatal("the stored tile bytes were not served verbatim")
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the MBTiles response")
	}
	// A row has no modification time; claiming one would be a guess.
	if got := first.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none — a row has no mtime", got)
	}

	second := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-None-Match", etag)
	h.Handler().ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 (body %q)", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Errorf("body = %q, want empty on a 304", second.Body.String())
	}

	// The same row must validate the same way twice, or every request would
	// re-download a tile that never changed.
	third := httptest.NewRecorder()
	h.Handler().ServeHTTP(third, httptest.NewRequest(http.MethodGet, url, nil))
	if got := third.Header().Get("ETag"); got != etag {
		t.Errorf("ETag = %q on a second read, want the stable %q", got, etag)
	}
}

func TestRowETagDistinguishesContent(t *testing.T) {
	if rowETag([]byte("one")) == rowETag([]byte("two")) {
		t.Error("two different tiles share an ETag")
	}
	// Two separate slices with the same content, so the hash is what is being
	// compared rather than the identity of one expression.
	first, second := []byte("same"), []byte("same")
	if rowETag(first) != rowETag(second) {
		t.Error("the same bytes produced two ETags")
	}
}

// An error response must never be cached or validated: a cached 404 pins a tile
// broken long after the render that would have fixed it.
func TestErrorResponsesCarryNoValidator(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: false}, 0)

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q, want none on an error", got)
	}
}
