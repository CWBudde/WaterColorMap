package tileformat

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"

	"github.com/HugoSmits86/nativewebp"
)

// Encoder turns a rendered tile into bytes.
//
// It exists so the pipeline holds one resolved encoder rather than branching on
// a format string per tile, and so adding a format later is a new
// implementation rather than another switch at every call site.
type Encoder interface {
	Encode(w io.Writer, img image.Image) error
	Format() Format
}

// EncoderOptions is the declarative form of an encoder, as it arrives from
// config or CLI flags. The zero value means PNG at default compression, which
// is what every construction site produced before this package existed.
type EncoderOptions struct {
	// Format selects the encoder. Empty means PNG.
	Format Format
	// PNGCompression is the existing knob: "default", "speed", "best" or
	// "none". Ignored for WebP.
	PNGCompression string
	// WebPEffort is nativewebp's CompressionLevel: how much analysis the
	// encoder does before committing. Zero means DefaultWebPEffort. Ignored
	// for PNG.
	WebPEffort int
}

// DefaultWebPEffort is nativewebp's own default (4 of 0-6), and measurement on
// this project's tiles says it is the right place to stop.
//
// Measured over the 689 base tiles in tiles/, re-encoding each from PNG:
//
//	effort 0  ->  1.16x smaller than PNG, 29 ms/tile
//	effort 4  ->  1.20x smaller, 52 ms/tile
//	effort 6  ->  1.21x smaller, 56 ms/tile
//
// The last two levels buy almost nothing, and against a ~576 ms render the
// 52 ms is under 10% of tile cost.
const DefaultWebPEffort = 4

// NewEncoder resolves options into an encoder, or reports why it cannot.
//
// Callers should do this once, at construction, so an unusable format fails at
// startup rather than at tile 5000.
func NewEncoder(opts EncoderOptions) (Encoder, error) {
	format, err := Parse(string(opts.Format))
	if err != nil {
		return nil, err
	}

	switch format {
	case WebP:
		effort := opts.WebPEffort
		if effort == 0 {
			effort = DefaultWebPEffort
		}
		if effort < 0 || effort > 6 {
			return nil, fmt.Errorf("webp effort %d out of range: must be 0-6", effort)
		}
		return &webpEncoder{opts: nativewebp.Options{
			CompressionLevel: nativewebp.CompressionLevel(effort),
		}}, nil
	case PNG:
		return &pngEncoder{enc: png.Encoder{
			CompressionLevel: pngCompressionLevel(opts.PNGCompression),
		}}, nil
	default:
		// Parse admits nothing else; this keeps the switch total.
		return nil, fmt.Errorf("unsupported tile image format %q", format)
	}
}

// pngCompressionLevel maps the configured string to a png level. An unknown
// value falls back to the default rather than erroring, which is the behaviour
// this replaced — the strings were never validated.
func pngCompressionLevel(s string) png.CompressionLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "speed", "fast", "best-speed":
		return png.BestSpeed
	case "best", "best-compression":
		return png.BestCompression
	case "none", "no", "nocompression", "no-compression":
		return png.NoCompression
	default:
		return png.DefaultCompression
	}
}

type pngEncoder struct {
	enc png.Encoder
}

func (e *pngEncoder) Encode(w io.Writer, img image.Image) error { return e.enc.Encode(w, img) }
func (e *pngEncoder) Format() Format                            { return PNG }

// webpEncoder encodes lossless VP8L via a pure-Go encoder.
//
// Lossless, not lossy, and that is a real trade against
// docs/data-scaling-strategy.md's headline number. That 9.24x was measured with
// lossy q80 through a cgo binding of libwebp. Lossless on the same 689 tiles is
// 1.21x, because the watercolor texture and Perlin noise fill every pixel and
// there is no flat region for a lossless codec to collapse.
//
// The pure-Go encoder was chosen anyway: no cgo means CGO_ENABLED=0 builds, the
// js/wasm target and the cross-platform release matrix all keep working with no
// build tags, and lossless removes any question of round-trip damage to the
// look the project exists to produce. A lossy encoder is a second
// implementation of this interface if the storage argument ever outweighs that.
type webpEncoder struct {
	opts nativewebp.Options
}

func (e *webpEncoder) Encode(w io.Writer, img image.Image) error {
	return nativewebp.Encode(w, img, &e.opts)
}

func (e *webpEncoder) Format() Format { return WebP }
