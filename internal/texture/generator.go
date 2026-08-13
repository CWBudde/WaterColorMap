package texture

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// TextureParams defines a seamless watercolor texture.
type TextureParams struct {
	Size      int
	BaseColor color.RGBA
	Variation float64
	Brushness float64
	Seed      int64
}

// TextureWriteResult reports which textures were written or skipped.
type TextureWriteResult struct {
	Written []string
	Skipped []string
}

var defaultTextureOrder = []geojson.LayerType{
	geojson.LayerLand,
	geojson.LayerWater,
	geojson.LayerParks,
	geojson.LayerUrban,
	geojson.LayerRoads,
	geojson.LayerHighways,
	geojson.LayerPaper,
}

var defaultTextureColors = map[geojson.LayerType]color.RGBA{
	geojson.LayerLand:     {R: 218, G: 198, B: 174, A: 255},
	geojson.LayerWater:    {R: 105, G: 160, B: 210, A: 255},
	geojson.LayerParks:    {R: 122, G: 170, B: 120, A: 255},
	geojson.LayerUrban:    {R: 200, G: 190, B: 210, A: 255},
	geojson.LayerRoads:    {R: 190, G: 186, B: 178, A: 255},
	geojson.LayerHighways: {R: 232, G: 202, B: 132, A: 255},
	geojson.LayerPaper:    {R: 244, G: 240, B: 232, A: 255},
}

var defaultTextureVariations = map[geojson.LayerType]float64{
	geojson.LayerLand:     0.85,
	geojson.LayerWater:    0.9,
	geojson.LayerParks:    0.8,
	geojson.LayerUrban:    0.7,
	geojson.LayerRoads:    0.6,
	geojson.LayerHighways: 0.75,
	geojson.LayerPaper:    0.5,
}

// WriteDefaultTextures generates the default texture set into dir.
// variationScale is a 0..1 multiplier applied to the layer defaults.
func WriteDefaultTextures(dir string, size int, seed int64, variationScale float64, brushness float64, overwrite bool) (TextureWriteResult, error) {
	result := TextureWriteResult{}
	if size <= 0 {
		return result, fmt.Errorf("size must be positive")
	}
	if variationScale < 0 || variationScale > 1 {
		return result, fmt.Errorf("variation scale must be within [0,1]")
	}
	if brushness < 0 || brushness > 1 {
		return result, fmt.Errorf("brushness must be within [0,1]")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, fmt.Errorf("failed to create texture dir: %w", err)
	}

	for i, layer := range defaultTextureOrder {
		filename, ok := DefaultLayerTextures[layer]
		if !ok {
			return result, fmt.Errorf("missing default texture filename for layer %s", layer)
		}
		path := filepath.Join(dir, filename)
		if !overwrite {
			if _, err := os.Stat(path); err == nil {
				result.Skipped = append(result.Skipped, path)
				continue
			}
		}

		params, err := defaultTextureParams(layer, size, seed+int64(i)*1000, variationScale, brushness)
		if err != nil {
			return result, err
		}

		img, err := generateLayerTexture(layer, params)
		if err != nil {
			return result, err
		}

		if err := writePNG(path, img); err != nil {
			return result, err
		}
		result.Written = append(result.Written, path)
	}

	return result, nil
}

// defaultTextureParams builds the texture parameters for a default layer texture.
func defaultTextureParams(
	layer geojson.LayerType,
	size int,
	seed int64,
	variationScale float64,
	brushness float64,
) (TextureParams, error) {
	baseColor, ok := defaultTextureColors[layer]
	if !ok {
		return TextureParams{}, fmt.Errorf("missing base color for layer %s", layer)
	}
	layerVariation, ok := defaultTextureVariations[layer]
	if !ok {
		return TextureParams{}, fmt.Errorf("missing variation for layer %s", layer)
	}

	return TextureParams{
		Size:      size,
		BaseColor: baseColor,
		Variation: clamp01(layerVariation * variationScale),
		Brushness: brushness,
		Seed:      seed,
	}, nil
}

// generateLayerTexture renders the texture for a layer using its matching generator.
func generateLayerTexture(layer geojson.LayerType, params TextureParams) (*image.RGBA, error) {
	if layer == geojson.LayerPaper {
		return GeneratePaperTexture(params)
	}
	return GenerateSeamlessTexture(params)
}

func writePNG(path string, img image.Image) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create texture %s: %w", path, err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close texture %s: %w", path, cerr)
		}
	}()

	if err := png.Encode(file, img); err != nil {
		return fmt.Errorf("failed to encode texture %s: %w", path, err)
	}
	return nil
}

// GenerateSeamlessTexture creates a seamless watercolor texture.
func GenerateSeamlessTexture(p TextureParams) (*image.RGBA, error) {
	if p.Size <= 0 {
		return nil, fmt.Errorf("size must be positive")
	}
	p.Variation = clamp01(p.Variation)
	p.Brushness = clamp01(p.Brushness)

	rng := rand.New(rand.NewSource(p.Seed))
	sx := newSimplex(p.Seed)

	imgF := newFloatImg(p.Size, p.Size)
	baseR, baseG, baseB := rgbaToFloat(p.BaseColor)

	warpStrength := 0.04 + 0.10*p.Variation

	// 1) Base paper tint + low-frequency noise, domain-warped for organic variation.
	for y := 0; y < p.Size; y++ {
		v := float64(y) / float64(p.Size)
		for x := 0; x < p.Size; x++ {
			u := float64(x) / float64(p.Size)

			du := sx.fbm(u+0.31, v+0.17, 3, 2.0, 0.5, 2.2) * warpStrength
			dv := sx.fbm(u+0.73, v+0.51, 3, 2.0, 0.5, 2.2) * warpStrength
			uu := wrap01(u + du)
			vv := wrap01(v + dv)

			n := sx.fbm(uu, vv, 5, 2.0, 0.5, 1.2) // [-1,1]
			n = (n + 1) * 0.5                     // [0,1]
			amt := 0.06 + 0.22*p.Variation*n

			i := imgF.idx(x, y)
			imgF.R[i] = lerp(0.98, baseR, amt)
			imgF.G[i] = lerp(0.98, baseG, amt)
			imgF.B[i] = lerp(0.98, baseB, amt)
		}
	}

	// 2) Wet washes (main watercolor strokes).
	washCount := int(220 + 620*p.Variation)
	for i := 0; i < washCount; i++ {
		cx := rng.Float64()
		cy := rng.Float64()

		rad := lerp(0.04, 0.18, rng.Float64()) * lerp(0.7, 1.25, p.Variation)

		offU := rng.Float64()
		offV := rng.Float64()
		nr := sx.fbm(offU, offV, 4, 2.1, 0.55, 2.3)
		ng := sx.fbm(offU+0.33, offV+0.17, 4, 2.1, 0.55, 2.3)
		nb := sx.fbm(offU+0.71, offV+0.49, 4, 2.1, 0.55, 2.3)

		jitter := 0.18 + 0.55*p.Variation
		col := [3]float64{
			clamp01(baseR + jitter*0.35*nr),
			clamp01(baseG + jitter*0.35*ng),
			clamp01(baseB + jitter*0.35*nb),
		}

		alpha := lerp(0.035, 0.14, rng.Float64()) * lerp(0.85, 1.45, p.Variation)
		imgF.addWash(cx, cy, rad, col, alpha)
	}

	// 3) Wet-in-wet diffusion.
	blurIters := 4 + int(6*p.Variation)
	blurRadius := 2 + int(6*p.Variation)
	imgF.blurWrapped(blurIters, blurRadius)

	// 4) Directional brush strokes.
	applyBrushStrokes(imgF, sx, p.Seed, p.Variation, p.Brushness)

	// 5) Pigment granulation + paper grain.
	for y := 0; y < p.Size; y++ {
		v := float64(y) / float64(p.Size)
		for x := 0; x < p.Size; x++ {
			u := float64(x) / float64(p.Size)
			i := imgF.idx(x, y)

			du := sx.fbm(u+0.19, v+0.47, 3, 2.0, 0.5, 3.5) * warpStrength
			dv := sx.fbm(u+0.67, v+0.11, 3, 2.0, 0.5, 3.5) * warpStrength
			uu := wrap01(u + du)
			vv := wrap01(v + dv)

			grain := sx.fbm(uu, vv, 6, 2.3, 0.55, 8.0)
			grain = (grain + 1) * 0.5

			gran := sx.fbm(uu+0.12, vv+0.39, 5, 2.0, 0.5, 3.5)
			gran = (gran + 1) * 0.5

			paperAmt := 0.03 + 0.07*p.Variation
			granAmt := 0.04 + 0.10*p.Variation

			imgF.R[i] = clamp01(imgF.R[i] + paperAmt*(grain-0.5) - granAmt*(0.5-gran))
			imgF.G[i] = clamp01(imgF.G[i] + paperAmt*(grain-0.5) - granAmt*(0.5-gran))
			imgF.B[i] = clamp01(imgF.B[i] + paperAmt*(grain-0.5) - granAmt*(0.5-gran))
		}
	}

	out := image.NewRGBA(image.Rect(0, 0, p.Size, p.Size))
	for y := 0; y < p.Size; y++ {
		for x := 0; x < p.Size; x++ {
			i := imgF.idx(x, y)
			out.SetRGBA(x, y, floatToRGBA(imgF.R[i], imgF.G[i], imgF.B[i]))
		}
	}
	return out, nil
}

// GeneratePaperTexture creates a cold-pressed paper texture.
func GeneratePaperTexture(p TextureParams) (*image.RGBA, error) {
	if p.Size <= 0 {
		return nil, fmt.Errorf("size must be positive")
	}
	p.Variation = clamp01(p.Variation)
	p.Brushness = clamp01(p.Brushness)

	sx := newSimplex(p.Seed + 4242)
	imgF := newFloatImg(p.Size, p.Size)
	baseR, baseG, baseB := rgbaToFloat(p.BaseColor)

	baseTint := 0.85 + 0.12*p.Variation
	for y := 0; y < p.Size; y++ {
		for x := 0; x < p.Size; x++ {
			i := imgF.idx(x, y)
			imgF.R[i] = lerp(1.0, baseR, baseTint)
			imgF.G[i] = lerp(1.0, baseG, baseTint)
			imgF.B[i] = lerp(1.0, baseB, baseTint)
		}
	}

	applyPaperGrain(imgF, sx, p.Variation)

	out := image.NewRGBA(image.Rect(0, 0, p.Size, p.Size))
	for y := 0; y < p.Size; y++ {
		for x := 0; x < p.Size; x++ {
			i := imgF.idx(x, y)
			out.SetRGBA(x, y, floatToRGBA(imgF.R[i], imgF.G[i], imgF.B[i]))
		}
	}
	return out, nil
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func rgbaToFloat(c color.RGBA) (r, g, b float64) {
	return float64(c.R) / 255.0, float64(c.G) / 255.0, float64(c.B) / 255.0
}

func floatToRGBA(r, g, b float64) color.RGBA {
	r = clamp01(r)
	g = clamp01(g)
	b = clamp01(b)
	return color.RGBA{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255), A: 255}
}

func wrap01(x float64) float64 {
	x = math.Mod(x, 1.0)
	if x < 0 {
		x += 1
	}
	return x
}

func wrapIndex(x, max int) int {
	x %= max
	if x < 0 {
		x += max
	}
	return x
}

func applyBrushStrokes(img *floatImg, sx *simplex, seed int64, variation float64, brushness float64) {
	rng := rand.New(rand.NewSource(seed + 911))
	angle := rng.Float64() * math.Pi
	cosA := math.Cos(angle)
	sinA := math.Sin(angle)

	strokeStrength := (0.025 + 0.10*variation) * (0.6 + 1.4*brushness)
	stretchAlong := 0.45
	stretchPerp := 7.5

	for y := 0; y < img.h; y++ {
		v := float64(y) / float64(img.h)
		for x := 0; x < img.w; x++ {
			u := float64(x) / float64(img.w)

			uRot := (u-0.5)*cosA - (v-0.5)*sinA + 0.5
			vRot := (u-0.5)*sinA + (v-0.5)*cosA + 0.5

			uAlong := wrap01(uRot * stretchAlong)
			vPerp := wrap01(vRot * stretchPerp)
			streak := sx.fbm(uAlong, vPerp, 4, 2.0, 0.5, 4.0)
			streak = (streak + 1) * 0.5
			streak = math.Pow(streak, 2.2)

			bristle := sx.fbm(wrap01(uRot), wrap01(vRot), 3, 2.6, 0.55, 24.0)
			bristle = (bristle + 1) * 0.5

			brush := 0.65*streak + 0.35*bristle
			delta := strokeStrength * (brush - 0.5)

			i := img.idx(x, y)
			img.R[i] = clamp01(img.R[i] + delta)
			img.G[i] = clamp01(img.G[i] + delta)
			img.B[i] = clamp01(img.B[i] + delta)
		}
	}
}

func applyPaperGrain(img *floatImg, sx *simplex, variation float64) {
	grainStrength := 0.03 + 0.06*variation
	ridgeStrength := 0.02 + 0.05*variation

	for y := 0; y < img.h; y++ {
		v := float64(y) / float64(img.h)
		for x := 0; x < img.w; x++ {
			u := float64(x) / float64(img.w)

			coarse := sx.fbm(u, v, 3, 2.0, 0.5, 3.0)
			coarse = (coarse + 1) * 0.5

			fine := sx.fbm(u+0.13, v+0.41, 4, 2.2, 0.55, 18.0)
			fine = (fine + 1) * 0.5

			ridge := 1.0 - math.Abs(2.0*coarse-1.0)
			ridge = math.Pow(ridge, 2.4)

			noise := grainStrength*(fine-0.5) + ridgeStrength*(ridge-0.5)

			i := img.idx(x, y)
			img.R[i] = clamp01(img.R[i] + noise)
			img.G[i] = clamp01(img.G[i] + noise)
			img.B[i] = clamp01(img.B[i] + noise)
		}
	}
}

type floatImg struct {
	R []float64
	G []float64
	B []float64
	w int
	h int
}

func newFloatImg(w, h int) *floatImg {
	n := w * h
	return &floatImg{
		w: w,
		h: h,
		R: make([]float64, n),
		G: make([]float64, n),
		B: make([]float64, n),
	}
}

func (f *floatImg) idx(x, y int) int { return y*f.w + x }

func (f *floatImg) addWash(cx, cy float64, radius float64, col [3]float64, alpha float64) {
	rpx := radius * float64(f.w)
	minX := int(math.Floor((cx-radius)*float64(f.w))) - 1
	maxX := int(math.Ceil((cx+radius)*float64(f.w))) + 1
	minY := int(math.Floor((cy-radius)*float64(f.h))) - 1
	maxY := int(math.Ceil((cy+radius)*float64(f.h))) + 1

	sigma := rpx * 0.55
	if sigma < 1 {
		sigma = 1
	}
	inv2sig2 := 1.0 / (2 * sigma * sigma)

	for yy := minY; yy <= maxY; yy++ {
		y := wrapIndex(yy, f.h)
		vy := (float64(yy)/float64(f.h) - cy)
		vy -= math.Round(vy)
		dy := vy * float64(f.h)

		for xx := minX; xx <= maxX; xx++ {
			x := wrapIndex(xx, f.w)
			vx := (float64(xx)/float64(f.w) - cx)
			vx -= math.Round(vx)
			dx := vx * float64(f.w)

			d2 := dx*dx + dy*dy
			wgt := math.Exp(-d2 * inv2sig2)
			a := alpha * wgt
			i := f.idx(x, y)
			f.R[i] = f.R[i]*(1-a) + col[0]*a
			f.G[i] = f.G[i]*(1-a) + col[1]*a
			f.B[i] = f.B[i]*(1-a) + col[2]*a
		}
	}
}

func (f *floatImg) blurWrapped(iterations int, radius int) {
	if radius <= 0 || iterations <= 0 {
		return
	}
	tmpR := make([]float64, f.w*f.h)
	tmpG := make([]float64, f.w*f.h)
	tmpB := make([]float64, f.w*f.h)

	for it := 0; it < iterations; it++ {
		for y := 0; y < f.h; y++ {
			for x := 0; x < f.w; x++ {
				sumR, sumG, sumB := 0.0, 0.0, 0.0
				count := 0.0
				for k := -radius; k <= radius; k++ {
					xx := wrapIndex(x+k, f.w)
					i := f.idx(xx, y)
					sumR += f.R[i]
					sumG += f.G[i]
					sumB += f.B[i]
					count++
				}
				j := y*f.w + x
				tmpR[j] = sumR / count
				tmpG[j] = sumG / count
				tmpB[j] = sumB / count
			}
		}
		copy(f.R, tmpR)
		copy(f.G, tmpG)
		copy(f.B, tmpB)

		for y := 0; y < f.h; y++ {
			for x := 0; x < f.w; x++ {
				sumR, sumG, sumB := 0.0, 0.0, 0.0
				count := 0.0
				for k := -radius; k <= radius; k++ {
					yy := wrapIndex(y+k, f.h)
					i := yy*f.w + x
					sumR += f.R[i]
					sumG += f.G[i]
					sumB += f.B[i]
					count++
				}
				j := y*f.w + x
				tmpR[j] = sumR / count
				tmpG[j] = sumG / count
				tmpB[j] = sumB / count
			}
		}
		copy(f.R, tmpR)
		copy(f.G, tmpG)
		copy(f.B, tmpB)
	}
}

var grad4 = [32][4]float64{
	{0, 1, 1, 1}, {0, 1, 1, -1}, {0, 1, -1, 1}, {0, 1, -1, -1},
	{0, -1, 1, 1}, {0, -1, 1, -1}, {0, -1, -1, 1}, {0, -1, -1, -1},
	{1, 0, 1, 1}, {1, 0, 1, -1}, {1, 0, -1, 1}, {1, 0, -1, -1},
	{-1, 0, 1, 1}, {-1, 0, 1, -1}, {-1, 0, -1, 1}, {-1, 0, -1, -1},
	{1, 1, 0, 1}, {1, 1, 0, -1}, {1, -1, 0, 1}, {1, -1, 0, -1},
	{-1, 1, 0, 1}, {-1, 1, 0, -1}, {-1, -1, 0, 1}, {-1, -1, 0, -1},
	{1, 1, 1, 0}, {1, 1, -1, 0}, {1, -1, 1, 0}, {1, -1, -1, 0},
	{-1, 1, 1, 0}, {-1, 1, -1, 0}, {-1, -1, 1, 0}, {-1, -1, -1, 0},
}

type simplex struct {
	perm [512]uint8
}

func newSimplex(seed int64) *simplex {
	s := &simplex{}
	r := rand.New(rand.NewSource(seed))
	p := make([]uint8, 256)
	for i := 0; i < 256; i++ {
		p[i] = uint8(i)
	}
	for i := 255; i > 0; i-- {
		j := r.Intn(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	for i := 0; i < 512; i++ {
		s.perm[i] = p[i&255]
	}
	return s
}

func fastFloor(x float64) int {
	if x >= 0 {
		return int(x)
	}
	return int(x) - 1
}

func dot4(g [4]float64, x, y, z, w float64) float64 {
	return g[0]*x + g[1]*y + g[2]*z + g[3]*w
}

// simplexRanks ranks the four simplex coordinates against each other, yielding
// the traversal order of the simplex corners.
func simplexRanks(x0, y0, z0, w0 float64) [4]int {
	var ranks [4]int
	coords := [4]float64{x0, y0, z0, w0}
	for a := 0; a < 4; a++ {
		for b := a + 1; b < 4; b++ {
			if coords[a] > coords[b] {
				ranks[a]++
			} else {
				ranks[b]++
			}
		}
	}
	return ranks
}

// simplexOffsets converts corner ranks into the unit offsets for the corner at
// the given rank threshold.
func simplexOffsets(ranks [4]int, threshold int) (i, j, k, l int) {
	var offsets [4]int
	for n, r := range ranks {
		if r >= threshold {
			offsets[n] = 1
		}
	}
	return offsets[0], offsets[1], offsets[2], offsets[3]
}

// simplexContribution computes a single simplex corner's contribution to the
// noise value.
func simplexContribution(g [4]float64, x, y, z, w float64) float64 {
	t := 0.6 - x*x - y*y - z*z - w*w
	if t <= 0 {
		return 0
	}
	t *= t
	return t * t * dot4(g, x, y, z, w)
}

func (s *simplex) noise4D(x, y, z, w float64) float64 {
	const F4 = 0.30901699437494745
	const G4 = 0.1381966011250105

	t := (x + y + z + w) * F4
	i := fastFloor(x + t)
	j := fastFloor(y + t)
	k := fastFloor(z + t)
	l := fastFloor(w + t)

	t0 := float64(i+j+k+l) * G4
	X0 := float64(i) - t0
	Y0 := float64(j) - t0
	Z0 := float64(k) - t0
	W0 := float64(l) - t0

	x0 := x - X0
	y0 := y - Y0
	z0 := z - Z0
	w0 := w - W0

	ranks := simplexRanks(x0, y0, z0, w0)
	i1, j1, k1, l1 := simplexOffsets(ranks, 3)
	i2, j2, k2, l2 := simplexOffsets(ranks, 2)
	i3, j3, k3, l3 := simplexOffsets(ranks, 1)

	x1 := x0 - float64(i1) + G4
	y1 := y0 - float64(j1) + G4
	z1 := z0 - float64(k1) + G4
	w1 := w0 - float64(l1) + G4

	x2 := x0 - float64(i2) + 2.0*G4
	y2 := y0 - float64(j2) + 2.0*G4
	z2 := z0 - float64(k2) + 2.0*G4
	w2 := w0 - float64(l2) + 2.0*G4

	x3 := x0 - float64(i3) + 3.0*G4
	y3 := y0 - float64(j3) + 3.0*G4
	z3 := z0 - float64(k3) + 3.0*G4
	w3 := w0 - float64(l3) + 3.0*G4

	x4 := x0 - 1.0 + 4.0*G4
	y4 := y0 - 1.0 + 4.0*G4
	z4 := z0 - 1.0 + 4.0*G4
	w4 := w0 - 1.0 + 4.0*G4

	ii := i & 255
	jj := j & 255
	kk := k & 255
	ll := l & 255

	gi0 := s.perm[ii+int(s.perm[jj+int(s.perm[kk+int(s.perm[ll])])])] % 32
	gi1 := s.perm[ii+i1+int(s.perm[jj+j1+int(s.perm[kk+k1+int(s.perm[ll+l1])])])] % 32
	gi2 := s.perm[ii+i2+int(s.perm[jj+j2+int(s.perm[kk+k2+int(s.perm[ll+l2])])])] % 32
	gi3 := s.perm[ii+i3+int(s.perm[jj+j3+int(s.perm[kk+k3+int(s.perm[ll+l3])])])] % 32
	gi4 := s.perm[ii+1+int(s.perm[jj+1+int(s.perm[kk+1+int(s.perm[ll+1])])])] % 32

	n0 := simplexContribution(grad4[gi0], x0, y0, z0, w0)
	n1 := simplexContribution(grad4[gi1], x1, y1, z1, w1)
	n2 := simplexContribution(grad4[gi2], x2, y2, z2, w2)
	n3 := simplexContribution(grad4[gi3], x3, y3, z3, w3)
	n4 := simplexContribution(grad4[gi4], x4, y4, z4, w4)

	return 27.0 * (n0 + n1 + n2 + n3 + n4)
}

func (s *simplex) seamless2D(u, v, freq float64) float64 {
	theta := 2 * math.Pi * u
	phi := 2 * math.Pi * v

	x := math.Cos(theta) * freq
	y := math.Sin(theta) * freq
	z := math.Cos(phi) * freq
	w := math.Sin(phi) * freq

	return s.noise4D(x, y, z, w)
}

func (s *simplex) fbm(u, v float64, octaves int, lacunarity, gain, baseFreq float64) float64 {
	amp := 0.5
	freq := baseFreq
	sum := 0.0
	norm := 0.0
	for i := 0; i < octaves; i++ {
		n := s.seamless2D(u, v, freq)
		sum += amp * n
		norm += amp
		amp *= gain
		freq *= lacunarity
	}
	return sum / norm
}
