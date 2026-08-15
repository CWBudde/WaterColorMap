package tileformat

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"

	"golang.org/x/image/webp"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Format
		wantErr bool
	}{
		{"empty defaults to png", "", PNG, false},
		{"png", "png", PNG, false},
		{"webp", "webp", WebP, false},
		{"case insensitive", "WebP", WebP, false},
		{"surrounding space", "  png  ", PNG, false},
		// Valid in an MBTiles metadata table, but not something this project
		// produces — better rejected at startup than discovered at tile 5000.
		{"jpg is rejected", "jpg", "", true},
		{"pbf is rejected", "pbf", "", true},
		{"nonsense is rejected", "wepb", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) expected an error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseExt(t *testing.T) {
	tests := []struct {
		input  string
		want   Format
		wantOK bool
	}{
		{".png", PNG, true},
		{"png", PNG, true},
		{".webp", WebP, true},
		{".WEBP", WebP, true},
		// No default here: a request without an extension is not a request
		// for a PNG.
		{"", "", false},
		{".jpg", "", false},
	}

	for _, tt := range tests {
		t.Run("ext "+tt.input, func(t *testing.T) {
			got, ok := ParseExt(tt.input)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ParseExt(%q) = (%v, %v), want (%v, %v)", tt.input, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestFormatExtAndContentType(t *testing.T) {
	tests := []struct {
		format      Format
		ext         string
		dotExt      string
		contentType string
	}{
		{PNG, "png", ".png", "image/png"},
		{WebP, "webp", ".webp", "image/webp"},
		// The zero value has to behave as PNG: it is what an unset config
		// field reaches this package as.
		{Format(""), "png", ".png", "image/png"},
	}

	for _, tt := range tests {
		t.Run("format "+string(tt.format), func(t *testing.T) {
			if got := tt.format.Ext(); got != tt.ext {
				t.Errorf("Ext() = %q, want %q", got, tt.ext)
			}
			if got := tt.format.DotExt(); got != tt.dotExt {
				t.Errorf("DotExt() = %q, want %q", got, tt.dotExt)
			}
			if got := tt.format.ContentType(); got != tt.contentType {
				t.Errorf("ContentType() = %q, want %q", got, tt.contentType)
			}
		})
	}
}

// noiseTile builds a 256x256 stand-in for a rendered watercolor tile.
//
// Getting this fixture right matters more than it looks. A first version used
// independent uniform random values per pixel, and on that WebP came out 1.07x
// *larger* than PNG — the opposite of the 1.21x measured over the project's 689
// real tiles. Per-pixel uniform noise is maximum entropy: nothing to model, so
// VP8L's transform machinery is pure overhead. A real tile is not that. Its
// Perlin noise is smooth and its paper texture is spatially correlated, which is
// exactly the structure a lossless codec exploits.
//
// So the fixture is a smooth low-frequency field (a coarse random grid,
// bilinearly upsampled) plus a little fine grain — correlated like the real
// thing, and reproducing the real direction of the result.
func noiseTile(seed int64) *image.NRGBA {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test fixture

	// Coarse control grid, one value per 16px cell, with a margin for
	// interpolation.
	const cells = 17
	var grid [cells][cells][3]float64
	for gy := range grid {
		for gx := range grid[gy] {
			grid[gy][gx] = [3]float64{
				200 + rng.Float64()*55,
				180 + rng.Float64()*55,
				150 + rng.Float64()*55,
			}
		}
	}

	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }

	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		gy, fy := y/16, float64(y%16)/16
		for x := 0; x < 256; x++ {
			gx, fx := x/16, float64(x%16)/16

			var ch [3]uint8
			for c := 0; c < 3; c++ {
				top := lerp(grid[gy][gx][c], grid[gy][gx+1][c], fx)
				bot := lerp(grid[gy+1][gx][c], grid[gy+1][gx+1][c], fx)
				// A few levels of grain on top of the smooth field, the way
				// the texture pass leaves it.
				v := lerp(top, bot, fy) + float64(rng.Intn(5)-2)
				switch {
				case v < 0:
					v = 0
				case v > 255:
					v = 255
				}
				ch[c] = uint8(v)
			}
			img.SetNRGBA(x, y, color.NRGBA{R: ch[0], G: ch[1], B: ch[2], A: 255})
		}
	}
	return img
}

func encodeTo(t *testing.T, opts EncoderOptions, img image.Image) []byte {
	t.Helper()

	enc, err := NewEncoder(opts)
	if err != nil {
		t.Fatalf("NewEncoder(%+v): %v", opts, err)
	}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf.Bytes()
}

// TestZeroOptionsIsPNG is the compatibility guarantee the whole change rests
// on: every existing construction site passes an unset format and must keep
// producing PNG.
func TestZeroOptionsIsPNG(t *testing.T) {
	enc, err := NewEncoder(EncoderOptions{})
	if err != nil {
		t.Fatalf("NewEncoder(zero): %v", err)
	}
	if enc.Format() != PNG {
		t.Errorf("zero options gave format %v, want png", enc.Format())
	}

	data := encodeTo(t, EncoderOptions{}, noiseTile(1))
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("zero options did not produce a decodable PNG: %v", err)
	}
}

func TestNewEncoderRejectsUnknownFormat(t *testing.T) {
	if _, err := NewEncoder(EncoderOptions{Format: Format("jpeg")}); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

// TestNewEncoderKeepsExplicitEffortZero guards the flag/encoder contract: 0 is
// nativewebp's fastest level, so it has to reach the encoder rather than being
// read as "unset" and silently promoted to DefaultWebPEffort.
//
// The effort lives in unexported encoder state, so this asserts on output
// instead — and specifically against DefaultWebPEffort, because that is the
// comparison the old behaviour could not pass: promoting 0 to 4 made the two
// encoders identical. Effort 0 does less analysis, so its output is larger.
func TestNewEncoderKeepsExplicitEffortZero(t *testing.T) {
	img := noiseTile(6)

	encode := func(effort int) []byte {
		enc, err := NewEncoder(EncoderOptions{Format: WebP, WebPEffort: effort})
		if err != nil {
			t.Fatalf("NewEncoder(effort %d): %v", effort, err)
		}
		var buf bytes.Buffer
		if err := enc.Encode(&buf, img); err != nil {
			t.Fatalf("Encode(effort %d): %v", effort, err)
		}
		return buf.Bytes()
	}

	zero, def := encode(0), encode(DefaultWebPEffort)
	if bytes.Equal(zero, def) {
		t.Fatalf("effort 0 and effort %d produced identical output (%d bytes); "+
			"effort 0 was substituted rather than passed through", DefaultWebPEffort, len(zero))
	}
	if len(zero) <= len(def) {
		t.Errorf("effort 0 produced %d bytes and effort %d produced %d; "+
			"expected the faster level to be larger", len(zero), DefaultWebPEffort, len(def))
	}
}

func TestNewEncoderRejectsOutOfRangeEffort(t *testing.T) {
	for _, effort := range []int{-1, 7, 99} {
		if _, err := NewEncoder(EncoderOptions{Format: WebP, WebPEffort: effort}); err == nil {
			t.Errorf("expected an error for webp effort %d", effort)
		}
	}
}

// TestPNGCompressionOrdering checks the compression strings are actually wired
// through, by asserting on output size rather than by reaching into the
// encoder's unexported state.
func TestPNGCompressionOrdering(t *testing.T) {
	img := noiseTile(2)

	none := len(encodeTo(t, EncoderOptions{PNGCompression: "none"}, img))
	def := len(encodeTo(t, EncoderOptions{PNGCompression: "default"}, img))
	best := len(encodeTo(t, EncoderOptions{PNGCompression: "best"}, img))

	if none <= def || def < best {
		t.Errorf("expected none > default >= best, got none=%d default=%d best=%d", none, def, best)
	}

	// An unknown value must fall back to the default rather than erroring:
	// these strings were never validated before this package existed.
	unknown := len(encodeTo(t, EncoderOptions{PNGCompression: "wat"}, img))
	if unknown != def {
		t.Errorf("unknown compression gave %d bytes, want the default's %d", unknown, def)
	}
}

func TestWebPProducesRIFFContainer(t *testing.T) {
	data := encodeTo(t, EncoderOptions{Format: WebP}, noiseTile(3))

	if len(data) < 12 {
		t.Fatalf("webp output is %d bytes, far too short", len(data))
	}
	if got := string(data[0:4]); got != "RIFF" {
		t.Errorf("magic = %q, want RIFF", got)
	}
	if got := string(data[8:12]); got != "WEBP" {
		t.Errorf("form type = %q, want WEBP", got)
	}
}

// TestWebPRoundTripIsPixelExact is the payoff of choosing a lossless encoder:
// this is an equality assertion, not a tolerance. If it ever has to become a
// tolerance, the encoder silently stopped being lossless.
func TestWebPRoundTripIsPixelExact(t *testing.T) {
	src := noiseTile(4)
	data := encodeTo(t, EncoderOptions{Format: WebP}, src)

	got, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode webp: %v", err)
	}
	if got.Bounds() != src.Bounds() {
		t.Fatalf("bounds = %v, want %v", got.Bounds(), src.Bounds())
	}

	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			wantR, wantG, wantB, wantA := src.At(x, y).RGBA()
			gotR, gotG, gotB, gotA := got.At(x, y).RGBA()
			if gotR != wantR || gotG != wantG || gotB != wantB || gotA != wantA {
				t.Fatalf("pixel (%d,%d) = (%d,%d,%d,%d), want (%d,%d,%d,%d) — encoder is not lossless",
					x, y, gotR, gotG, gotB, gotA, wantR, wantG, wantB, wantA)
			}
		}
	}
}

// TestWebPBeatsPNGOnATile guards the reason the format exists at all. The
// margin is deliberately loose: the measured figure over the project's 689 real
// tiles is 1.21x, and this asserts only that WebP is not *larger*, which is the
// claim that would invalidate the feature.
func TestWebPIsNotLargerThanPNG(t *testing.T) {
	img := noiseTile(5)

	pngSize := len(encodeTo(t, EncoderOptions{}, img))
	webpSize := len(encodeTo(t, EncoderOptions{Format: WebP}, img))

	if webpSize >= pngSize {
		t.Errorf("webp %d bytes >= png %d bytes; the storage argument for webp does not hold",
			webpSize, pngSize)
	}
	t.Logf("png %d B, webp %d B, ratio %.3f", pngSize, webpSize, float64(webpSize)/float64(pngSize))
}

func TestEncoderReportsItsFormat(t *testing.T) {
	for _, f := range All {
		enc, err := NewEncoder(EncoderOptions{Format: f})
		if err != nil {
			t.Fatalf("NewEncoder(%v): %v", f, err)
		}
		if enc.Format() != f {
			t.Errorf("Format() = %v, want %v", enc.Format(), f)
		}
	}
}

func BenchmarkEncodeTile(b *testing.B) {
	img := noiseTile(6)

	benches := []struct {
		name string
		opts EncoderOptions
	}{
		{"png", EncoderOptions{}},
		{"webp_effort4", EncoderOptions{Format: WebP, WebPEffort: DefaultWebPEffort}},
		{"webp_effort0", EncoderOptions{Format: WebP, WebPEffort: 0}},
	}

	for _, bb := range benches {
		b.Run(bb.name, func(b *testing.B) {
			enc, err := NewEncoder(bb.opts)
			if err != nil {
				b.Fatalf("NewEncoder: %v", err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var buf bytes.Buffer
				if err := enc.Encode(&buf, img); err != nil {
					b.Fatalf("Encode: %v", err)
				}
			}
		})
	}
}
