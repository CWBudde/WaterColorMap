package tilejson_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tilejson"
)

func TestNew_Defaults(t *testing.T) {
	doc := tilejson.New(tilejson.Options{Tiles: []string{"z{z}_x{x}_y{y}.png"}})

	if doc.TileJSON != "3.0.0" {
		t.Errorf("tilejson version = %q, want 3.0.0", doc.TileJSON)
	}
	if doc.MinZoom != tilejson.DefaultMinZoom || doc.MaxZoom != tilejson.DefaultMaxZoom {
		t.Errorf("zoom range = %d..%d, want %d..%d",
			doc.MinZoom, doc.MaxZoom, tilejson.DefaultMinZoom, tilejson.DefaultMaxZoom)
	}
	if !reflect.DeepEqual(doc.Bounds, tilejson.WorldBounds[:]) {
		t.Errorf("bounds = %v, want world bounds %v", doc.Bounds, tilejson.WorldBounds)
	}
	if doc.Center != nil {
		t.Errorf("center = %v, want omitted when unknown", doc.Center)
	}
	if doc.Attribution != tilejson.DefaultAttribution {
		t.Errorf("attribution = %q, want %q", doc.Attribution, tilejson.DefaultAttribution)
	}
	if doc.Format != "png" || doc.Scheme != "xyz" {
		t.Errorf("format/scheme = %q/%q, want png/xyz", doc.Format, doc.Scheme)
	}
}

// Zoom 0 is a valid zoom and must survive as 0, not be replaced by the default.
func TestNew_ZeroMinZoomIsKept(t *testing.T) {
	doc := tilejson.New(tilejson.Options{
		Tiles:   []string{"t.png"},
		MinZoom: mbtiles.Zoom(0),
		MaxZoom: mbtiles.Zoom(0),
	})

	if doc.MinZoom != 0 || doc.MaxZoom != 0 {
		t.Errorf("zoom range = %d..%d, want 0..0", doc.MinZoom, doc.MaxZoom)
	}
}

func TestNew_DoesNotAliasCallerSlice(t *testing.T) {
	tiles := []string{"a.png"}
	doc := tilejson.New(tilejson.Options{Tiles: tiles})
	tiles[0] = "mutated.png"

	if doc.Tiles[0] != "a.png" {
		t.Errorf("doc.Tiles[0] = %q, want a.png (caller slice must be copied)", doc.Tiles[0])
	}
}

func TestFromMBTilesMetadata(t *testing.T) {
	meta := mbtiles.Metadata{
		Name:        "Hanover",
		Format:      "png",
		MinZoom:     mbtiles.Zoom(10),
		MaxZoom:     mbtiles.Zoom(15),
		Bounds:      [4]float64{9.6, 52.3, 9.9, 52.45},
		Center:      [3]float64{9.75, 52.375, 12},
		Attribution: "© OpenStreetMap contributors",
		Description: "Hanover coverage",
		Version:     "1.0",
	}

	doc := tilejson.FromMBTilesMetadata(meta, "/tiles/z{z}_x{x}_y{y}.png")

	if doc.MinZoom != 10 || doc.MaxZoom != 15 {
		t.Errorf("zoom range = %d..%d, want 10..15", doc.MinZoom, doc.MaxZoom)
	}
	if want := []float64{9.6, 52.3, 9.9, 52.45}; !reflect.DeepEqual(doc.Bounds, want) {
		t.Errorf("bounds = %v, want %v", doc.Bounds, want)
	}
	if want := []float64{9.75, 52.375, 12}; !reflect.DeepEqual(doc.Center, want) {
		t.Errorf("center = %v, want %v", doc.Center, want)
	}
	if doc.Name != "Hanover" || doc.Description != "Hanover coverage" {
		t.Errorf("name/description = %q/%q", doc.Name, doc.Description)
	}
	if doc.Attribution != "© OpenStreetMap contributors" {
		t.Errorf("attribution = %q, want the metadata value", doc.Attribution)
	}
	if len(doc.Tiles) != 1 || doc.Tiles[0] != "/tiles/z{z}_x{x}_y{y}.png" {
		t.Errorf("tiles = %v", doc.Tiles)
	}
}

// The wire format is what clients consume, so the tags matter more than the
// Go field names: bounds and center must be JSON number arrays, the zooms
// plain numbers, and the spec version must be present.
func TestJSONShape(t *testing.T) {
	doc := tilejson.FromMBTilesMetadata(mbtiles.Metadata{
		MinZoom: mbtiles.Zoom(3),
		MaxZoom: mbtiles.Zoom(7),
		Bounds:  [4]float64{-1, -2, 3, 4},
		Center:  [3]float64{1, 2, 5},
	}, "a.png")

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"tilejson", "tiles", "bounds", "center", "minzoom", "maxzoom", "format", "attribution", "description", "name"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in %s", key, data)
		}
	}
	if raw["tilejson"] != "3.0.0" {
		t.Errorf("tilejson = %v, want 3.0.0", raw["tilejson"])
	}
	if got, ok := raw["bounds"].([]any); !ok || len(got) != 4 {
		t.Errorf("bounds = %v, want a 4-element array", raw["bounds"])
	}
	if got, ok := raw["center"].([]any); !ok || len(got) != 3 {
		t.Errorf("center = %v, want a 3-element array", raw["center"])
	}
	if raw["minzoom"] != float64(3) || raw["maxzoom"] != float64(7) {
		t.Errorf("zooms = %v/%v, want 3/7", raw["minzoom"], raw["maxzoom"])
	}
}

func TestFolderTileTemplate(t *testing.T) {
	tests := []struct {
		structure string
		format    string
		want      string
	}{
		{"flat", "png", "z{z}_x{x}_y{y}.png"},
		{"nested", "png", "{z}/{x}/{y}.png"},
		{"", "png", "z{z}_x{x}_y{y}.png"},
		{"unknown", "png", "z{z}_x{x}_y{y}.png"},
		{"flat", "webp", "z{z}_x{x}_y{y}.webp"},
		{"nested", "webp", "{z}/{x}/{y}.webp"},
		// An unset or unrecognised format keeps the PNG template, which is
		// what every caller produced before formats were selectable.
		{"flat", "", "z{z}_x{x}_y{y}.png"},
		{"nested", "jpeg", "{z}/{x}/{y}.png"},
	}

	for _, tt := range tests {
		if got := tilejson.FolderTileTemplate(tt.structure, tt.format); got != tt.want {
			t.Errorf("FolderTileTemplate(%q, %q) = %q, want %q", tt.structure, tt.format, got, tt.want)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tiles")

	doc := tilejson.New(tilejson.Options{Tiles: []string{tilejson.FolderTileTemplate("nested", "png")}})
	path, err := tilejson.WriteFile(dir, doc)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if want := filepath.Join(dir, "tilejson.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var round tilejson.TileJSON
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(round, doc) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", round, doc)
	}
}

func TestHandler_ResolvesRelativeTileURLs(t *testing.T) {
	doc := tilejson.New(tilejson.Options{Tiles: []string{"/tiles/z{z}_x{x}_y{y}.png"}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://tiles.example:8080/tiles/tilejson.json", nil)
	tilejson.Handler(doc, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != tilejson.ContentType {
		t.Errorf("content type = %q, want %q", ct, tilejson.ContentType)
	}

	var got tilejson.TileJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := "http://tiles.example:8080/tiles/z{z}_x{x}_y{y}.png"
	if got.Tiles[0] != want {
		t.Errorf("tiles[0] = %q, want %q", got.Tiles[0], want)
	}
	// The served document must not have mutated the source.
	if doc.Tiles[0] != "/tiles/z{z}_x{x}_y{y}.png" {
		t.Errorf("handler mutated the source document: %q", doc.Tiles[0])
	}
}

func TestHandler_KeepsAbsoluteTileURLs(t *testing.T) {
	doc := tilejson.New(tilejson.Options{Tiles: []string{"https://cdn.example/{z}/{x}/{y}.png"}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/tiles/tilejson.json", nil)
	tilejson.Handler(doc, nil, nil).ServeHTTP(rec, req)

	var got tilejson.TileJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tiles[0] != "https://cdn.example/{z}/{x}/{y}.png" {
		t.Errorf("tiles[0] = %q, want the absolute URL untouched", got.Tiles[0])
	}
}

func TestHandler_RejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/tiles/tilejson.json", nil)
	tilejson.Handler(tilejson.New(tilejson.Options{Tiles: []string{"a.png"}}), nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// The /tiles/tilejson.json pattern must beat the /tiles/ subtree, otherwise the
// tile handler would answer with a 404 for a malformed tile path.
func TestHandler_WinsOverTilesSubtree(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/tiles/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "tile not found", http.StatusNotFound)
	}))
	mux.Handle("/tiles/tilejson.json", tilejson.Handler(
		tilejson.New(tilejson.Options{Tiles: []string{"/tiles/z{z}_x{x}_y{y}.png"}}), nil, nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/tiles/tilejson.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the tile subtree shadowed the route)", rec.Code)
	}
}

func TestFromMBTilesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.mbtiles")

	w, err := mbtiles.New(path, mbtiles.Metadata{
		Name:    "FileMeta",
		Format:  "png",
		MinZoom: mbtiles.Zoom(4),
		MaxZoom: mbtiles.Zoom(9),
		Bounds:  [4]float64{9.6, 52.3, 9.9, 52.45},
		Center:  [3]float64{9.75, 52.375, 6},
	})
	if err != nil {
		t.Fatalf("create mbtiles: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close mbtiles: %v", err)
	}

	doc, err := tilejson.FromMBTilesFile(path, "/tiles/z{z}_x{x}_y{y}.png")
	if err != nil {
		t.Fatalf("FromMBTilesFile: %v", err)
	}

	if doc.Name != "FileMeta" || doc.MinZoom != 4 || doc.MaxZoom != 9 {
		t.Errorf("doc = %+v, want name FileMeta and zooms 4..9", doc)
	}
	if want := []float64{9.6, 52.3, 9.9, 52.45}; !reflect.DeepEqual(doc.Bounds, want) {
		t.Errorf("bounds = %v, want %v", doc.Bounds, want)
	}
}

func TestFromMBTilesFile_MissingFile(t *testing.T) {
	if _, err := tilejson.FromMBTilesFile(filepath.Join(t.TempDir(), "nope.mbtiles")); err == nil {
		t.Error("expected an error for a missing MBTiles file")
	}
}

// Behind a TLS-terminating proxy the backend request is plain HTTP, so without
// honouring the forwarded scheme the document advertises http:// tile URLs to a
// page loaded over https and the browser blocks them as mixed content.
func TestHandler_ForwardedProto(t *testing.T) {
	trustAll := func(*http.Request) bool { return true }

	tests := []struct {
		name    string
		trust   tilejson.TrustForwarded
		headers map[string]string
		want    string
	}{
		{"no headers", trustAll, nil, "http://maps.example/tiles/z1_x2_y3.png"},
		{"x-forwarded-proto, trusted", trustAll,
			map[string]string{"X-Forwarded-Proto": "https"}, "https://maps.example/tiles/z1_x2_y3.png"},
		{"x-forwarded-proto, untrusted peer", nil,
			map[string]string{"X-Forwarded-Proto": "https"}, "http://maps.example/tiles/z1_x2_y3.png"},
		{"forwarded header wins", trustAll,
			map[string]string{"Forwarded": `for=1.2.3.4;proto=https`}, "https://maps.example/tiles/z1_x2_y3.png"},
		{"leftmost hop of a chain", trustAll,
			map[string]string{"X-Forwarded-Proto": "https, http"}, "https://maps.example/tiles/z1_x2_y3.png"},
		{"garbage scheme ignored", trustAll,
			map[string]string{"X-Forwarded-Proto": "javascript:"}, "http://maps.example/tiles/z1_x2_y3.png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := tilejson.New(tilejson.Options{Tiles: []string{"/tiles/z{z}_x{x}_y{y}.png"}})
			req := httptest.NewRequest(http.MethodGet, "http://maps.example/tiles/tilejson.json", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			tilejson.Handler(doc, nil, tt.trust).ServeHTTP(rec, req)

			var got tilejson.TileJSON
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := strings.NewReplacer("z1_x2_y3", "z{z}_x{x}_y{y}").Replace(tt.want)
			if len(got.Tiles) != 1 || got.Tiles[0] != want {
				t.Errorf("tiles = %v, want [%s]", got.Tiles, want)
			}
		})
	}
}

// nil already means "not supplied", so an explicit zero value must survive:
// [0, 0, 0] is null island at zoom 0 and [0,0,0,0] is a degenerate but stated
// bounds. Both were being silently replaced before.
func TestNew_PreservesExplicitZeroBoundsAndCenter(t *testing.T) {
	zeroBounds := [4]float64{}
	zeroCenter := [3]float64{}

	doc := tilejson.New(tilejson.Options{
		Tiles:  []string{"z{z}_x{x}_y{y}.png"},
		Bounds: &zeroBounds,
		Center: &zeroCenter,
	})

	if !reflect.DeepEqual(doc.Bounds, []float64{0, 0, 0, 0}) {
		t.Errorf("bounds = %v, want [0 0 0 0]", doc.Bounds)
	}
	if !reflect.DeepEqual(doc.Center, []float64{0, 0, 0}) {
		t.Errorf("center = %v, want [0 0 0]", doc.Center)
	}
}
