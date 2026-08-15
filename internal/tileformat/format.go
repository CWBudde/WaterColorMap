// Package tileformat owns the identity and encoding of a rendered tile image:
// which format it is, what extension and MIME type go with it, and how to turn
// an image.Image into bytes in that format.
//
// It is a leaf package by design — it imports nothing from internal/ — so the
// pipeline, the tile server, the MBTiles writer, the CLI and the WASM build can
// all agree on a format without agreeing on anything else.
package tileformat

import (
	"fmt"
	"strings"
)

// Format is a tile image format.
//
// The zero value is deliberately not a valid format: every options struct in
// the project reaches this package with an unset field at some point, and PNG
// has to be what that means. Parse and NewEncoder both map "" to PNG rather
// than erroring, so an omitted config key keeps today's behaviour.
type Format string

const (
	// PNG is the default and what every tile before this existed as.
	PNG Format = "png"
	// WebP is lossless VP8L. See NewEncoder for why lossless and not lossy.
	WebP Format = "webp"
)

// All lists the supported formats, in the order a help string should show them.
var All = []Format{PNG, WebP}

// Parse turns a configured format name into a Format. It is case-insensitive
// and treats the empty string as PNG.
//
// Formats that are valid in an MBTiles metadata table but that this project
// does not produce — jpg, pbf — are rejected rather than silently accepted, so
// a typo in a config file fails at startup instead of at the first tile.
func Parse(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(PNG):
		return PNG, nil
	case string(WebP):
		return WebP, nil
	default:
		return "", fmt.Errorf("unsupported tile image format %q: must be one of %s", s, join(All))
	}
}

// ParseExt maps a file extension, with or without its leading dot, to a Format.
// Unlike Parse it does not default: an empty or unknown extension reports false,
// because a request for "z13_x1_y2" without an extension is not a request for a
// PNG.
func ParseExt(ext string) (Format, bool) {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case string(PNG):
		return PNG, true
	case string(WebP):
		return WebP, true
	default:
		return "", false
	}
}

// Ext returns the file extension without a leading dot, matching the argument
// tile.Coords.Path and FileName expect.
func (f Format) Ext() string {
	if f == "" {
		return string(PNG)
	}
	return string(f)
}

// DotExt returns the file extension with its leading dot.
func (f Format) DotExt() string { return "." + f.Ext() }

// ContentType returns the MIME type to serve this format as.
//
// Worth setting explicitly rather than letting http.ServeFile sniff it: Go's
// builtin table does map .webp, but the mime package also loads
// /etc/mime.types on Linux, which would make the served header depend on the
// host the server happens to run on.
func (f Format) ContentType() string {
	switch f {
	case WebP:
		return "image/webp"
	case PNG:
		return "image/png"
	default:
		return "image/png"
	}
}

func (f Format) String() string { return f.Ext() }

func join(formats []Format) string {
	names := make([]string, len(formats))
	for i, f := range formats {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}
