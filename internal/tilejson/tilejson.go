// Package tilejson builds and serves TileJSON 3.0.0 documents describing a
// generated or served tile set.
//
// Both delivery paths — the folder/MBTiles output written by `generate` and the
// `/tiles/tilejson.json` endpoint of `serve` — go through New, so the two can
// not drift apart. FromMBTilesMetadata bridges the MBTiles metadata table,
// which is already TileJSON-shaped, into the same constructor.
//
// Field names and types follow the TileJSON 3.0.0 specification: bounds is
// [west, south, east, north] and center is [longitude, latitude, zoom], both as
// floating point numbers.
package tilejson

import (
	"fmt"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
)

// SpecVersion is the value of the mandatory "tilejson" field.
const SpecVersion = "3.0.0"

// Defaults applied by New when the corresponding option is unset.
const (
	// DefaultName and DefaultDescription mirror the MBTiles metadata written
	// by `generate --format=mbtiles`.
	DefaultName        = "WaterColorMap"
	DefaultDescription = "Watercolor-styled map tiles"

	// DefaultAttribution is the attribution that must accompany every
	// delivery of these tiles: the data is OpenStreetMap's, the styling is
	// watercolor-inspired in the spirit of Stamen's watercolor map.
	DefaultAttribution = "© OpenStreetMap contributors · Watercolor-inspired rendering"

	// DefaultFormat is the only tile format this project produces.
	DefaultFormat = "png"

	// DefaultMinZoom and DefaultMaxZoom are used when the zoom range is not
	// known. DefaultMaxZoom matches the Leaflet demo's maxZoom rather than
	// tile.MaxZoom: on-demand generation accepts higher zooms, but
	// advertising them would invite clients to request tiles that take
	// minutes each to render.
	DefaultMinZoom = 0
	DefaultMaxZoom = 18
)

// WorldBounds is the TileJSON default extent: the whole world, clipped to the
// latitudes representable in Web Mercator.
var WorldBounds = [4]float64{-180, -85.051129, 180, 85.051129}

// TileJSON is a TileJSON 3.0.0 document.
//
// Fields are ordered by descending size to satisfy govet's fieldalignment
// check; the JSON tags, not the declaration order, define the wire format.
type TileJSON struct {
	TileJSON    string `json:"tilejson"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	Scheme      string `json:"scheme,omitempty"`
	Format      string `json:"format,omitempty"`
	// Tiles holds the tile URL templates. It is mandatory and must contain
	// at least one entry.
	Tiles   []string  `json:"tiles"`
	Bounds  []float64 `json:"bounds,omitempty"`
	Center  []float64 `json:"center,omitempty"`
	MinZoom int       `json:"minzoom"`
	MaxZoom int       `json:"maxzoom"`
}

// Options describes a tile set. Every field is optional except Tiles; New
// substitutes the package defaults for whatever is left unset.
//
// MinZoom and MaxZoom are pointers so that zoom 0 — a valid zoom — can be told
// apart from "not set", matching mbtiles.Metadata. Use mbtiles.Zoom to build
// one from a literal.
type Options struct {
	MinZoom *int
	MaxZoom *int
	// Bounds is [west, south, east, north]; the zero value means "unknown"
	// and yields WorldBounds.
	Bounds *[4]float64
	// Center is [longitude, latitude, zoom]; nil omits the field.
	Center      *[3]float64
	Name        string
	Description string
	Version     string
	Attribution string
	Format      string
	// Tiles are the tile URL templates, e.g. "z{z}_x{x}_y{y}.png" or
	// "/tiles/z{z}_x{x}_y{y}.png".
	Tiles []string
}

// New builds a TileJSON document, filling in the package defaults.
func New(opts Options) TileJSON {
	doc := TileJSON{
		TileJSON:    SpecVersion,
		Tiles:       append([]string(nil), opts.Tiles...),
		Scheme:      "xyz",
		Name:        orDefault(opts.Name, DefaultName),
		Description: orDefault(opts.Description, DefaultDescription),
		Attribution: orDefault(opts.Attribution, DefaultAttribution),
		Format:      orDefault(opts.Format, DefaultFormat),
		Version:     opts.Version,
		MinZoom:     DefaultMinZoom,
		MaxZoom:     DefaultMaxZoom,
	}

	if opts.MinZoom != nil {
		doc.MinZoom = *opts.MinZoom
	}
	if opts.MaxZoom != nil {
		doc.MaxZoom = *opts.MaxZoom
	}

	// The pointer already distinguishes "not supplied" from "supplied"; an extra
	// zero-value check would drop legitimate input. [0, 0, 0] is null island at
	// zoom 0, a perfectly valid centre, and it was being silently discarded.
	bounds := WorldBounds
	if opts.Bounds != nil {
		bounds = *opts.Bounds
	}
	doc.Bounds = bounds[:]

	if opts.Center != nil {
		center := *opts.Center
		doc.Center = center[:]
	}

	return doc
}

// FromMBTilesMetadata converts MBTiles metadata — which is already
// TileJSON-shaped — into a TileJSON document with the given tile URL templates.
func FromMBTilesMetadata(meta mbtiles.Metadata, tiles ...string) TileJSON {
	bounds := meta.Bounds
	center := meta.Center

	return New(Options{
		Tiles:       tiles,
		MinZoom:     meta.MinZoom,
		MaxZoom:     meta.MaxZoom,
		Bounds:      &bounds,
		Center:      &center,
		Name:        meta.Name,
		Description: meta.Description,
		Version:     meta.Version,
		Attribution: meta.Attribution,
		Format:      meta.Format,
	})
}

// FromMBTilesFile reads the metadata table of an MBTiles file and converts it
// into a TileJSON document. The database is closed again before returning.
func FromMBTilesFile(path string, tiles ...string) (TileJSON, error) {
	reader, err := mbtiles.OpenReader(path)
	if err != nil {
		return TileJSON{}, fmt.Errorf("open mbtiles: %w", err)
	}
	defer reader.Close() // nolint:errcheck // Read-only handle; nothing to flush.

	meta, err := reader.Metadata()
	if err != nil {
		return TileJSON{}, fmt.Errorf("read mbtiles metadata: %w", err)
	}

	return FromMBTilesMetadata(meta, tiles...), nil
}

// FolderTileTemplate returns the tile URL template matching the layout written
// by `generate --format=folder --folder-structure=<structure>`. It is relative
// to the directory holding tilejson.json.
func FolderTileTemplate(structure string) string {
	if structure == "nested" {
		return "{z}/{x}/{y}.png"
	}
	return "z{z}_x{x}_y{y}.png"
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
