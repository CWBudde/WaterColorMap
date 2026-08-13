//go:build js && wasm
// +build js,wasm

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"syscall/js"
	"time"

	"github.com/cwbudde/watercolormap/internal/composite"
	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
	"github.com/cwbudde/watercolormap/internal/raster"
	"github.com/cwbudde/watercolormap/internal/texture"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/cwbudde/watercolormap/internal/watercolor"
)

const defaultConcurrency = 4

const defaultSeed int64 = 1

// GenerateTileRequest represents a tile generation request from JS
type GenerateTileRequest struct {
	Zoom  int  `json:"zoom"`
	X     int  `json:"x"`
	Y     int  `json:"y"`
	HiDPI bool `json:"hidpi"`
}

type GenerateTileResponse struct {
	Key      string `json:"key"`
	Filename string `json:"filename"`
}

type OverpassQueryResponse struct {
	Query    string  `json:"query"`
	TileSize int     `json:"tileSize"`
	PadPx    int     `json:"padPx"`
	Metatile int     `json:"metatileSize"`
	MinLon   float64 `json:"minLon"`
	MinLat   float64 `json:"minLat"`
	MaxLon   float64 `json:"maxLon"`
	MaxLat   float64 `json:"maxLat"`
}

var embeddedTextures map[geojson.LayerType]image.Image

func ensureTexturesLoaded() error {
	if embeddedTextures != nil {
		return nil
	}
	tex, err := texture.LoadEmbeddedDefaultTextures()
	if err != nil {
		return err
	}
	embeddedTextures = tex
	return nil
}

// getConcurrency returns the recommended number of concurrent operations.
// Uses navigator.hardwareConcurrency if available, otherwise defaults to 4.
func getConcurrency(_ js.Value, _ []js.Value) interface{} {
	navigator := js.Global().Get("navigator")
	if navigator.IsUndefined() || navigator.IsNull() {
		return defaultConcurrency
	}

	hwConcurrency := navigator.Get("hardwareConcurrency")
	if hwConcurrency.IsUndefined() || hwConcurrency.IsNull() {
		return defaultConcurrency
	}

	cores := hwConcurrency.Int()
	if cores < 1 {
		return defaultConcurrency
	}
	return cores
}

// generateTile is called from JavaScript to generate a tile
// In the browser, we delegate to a backend server or use a simplified renderer
func generateTile(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return map[string]any{"error": "missing arguments"}
	}

	reqStr := args[0].String()
	var req GenerateTileRequest
	if err := json.Unmarshal([]byte(reqStr), &req); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to parse request: %v", err)}
	}

	// We cannot render Mapnik in WASM, but we *can* provide a canonical filename builder
	// so the browser code can reliably hit a backend `watercolormap serve` instance.
	suffix := ""
	if req.HiDPI {
		suffix = "@2x"
	}

	key := fmt.Sprintf("z%d_x%d_y%d%s", req.Zoom, req.X, req.Y, suffix)
	// syscall/js.ValueOf cannot convert arbitrary Go values.
	// Use map[string]any (JS-convertible) rather than map[string]string.
	return map[string]any{
		"key":      key,
		"filename": key + ".png",
	}
}

func tileSizeForRequest(req GenerateTileRequest) int {
	if req.HiDPI {
		return 512
	}
	return 256
}

func buildOverpassQuery(bounds types.BoundingBox) string {
	bbox := fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", bounds.MinLat, bounds.MinLon, bounds.MaxLat, bounds.MaxLon)
	return fmt.Sprintf(`
[out:json][timeout:60];
(
  way["natural"="water"](%s);
  way["natural"="coastline"](%s);
  way["waterway"](%s);
  relation["natural"="water"](%s);
  relation["waterway"](%s);
  way["leisure"="park"](%s);
  way["leisure"="garden"](%s);
  way["landuse"="forest"](%s);
  way["landuse"="grass"](%s);
  way["landuse"="meadow"](%s);
  relation["leisure"="park"](%s);
  way["highway"](%s);
  way["railway"~"rail|light_rail|subway|tram"](%s);
  way["building"](%s);
  way["amenity"="school"](%s);
  way["amenity"="hospital"](%s);
  way["amenity"="university"](%s);
);
out geom;
`, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox, bbox)
}

// watercolorOverpassQueryForTile returns the Overpass QL query needed to render the tile.
// JS fetches the query result via HTTPS and then calls watercolorRenderTileFromOverpassJSON.
func watercolorOverpassQueryForTile(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return map[string]any{"error": "missing arguments"}
	}

	var req GenerateTileRequest
	if err := json.Unmarshal([]byte(args[0].String()), &req); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to parse request: %v", err)}
	}

	if err := ensureTexturesLoaded(); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to load textures: %v", err)}
	}

	tileSize := tileSizeForRequest(req)
	params := watercolor.DefaultParams(tileSize, defaultSeed, embeddedTextures)
	padPx := watercolor.RequiredPaddingPx(params)
	if padPx > tileSize {
		padPx = tileSize
	}
	metatileSize := tileSize + 2*padPx

	tileCoord := types.TileCoordinate{Zoom: req.Zoom, X: req.X, Y: req.Y}
	b := types.TileToBounds(tileCoord)
	if padPx > 0 {
		padFrac := float64(padPx) / float64(tileSize)
		b = b.ExpandByFraction(padFrac)
	}

	return map[string]any{
		"query":        buildOverpassQuery(b),
		"tileSize":     tileSize,
		"padPx":        padPx,
		"metatileSize": metatileSize,
		"minLon":       b.MinLon,
		"minLat":       b.MinLat,
		"maxLon":       b.MaxLon,
		"maxLat":       b.MaxLat,
	}
}

// watercolorRenderTileFromOverpassJSON renders a PNG tile (base64) from Overpass JSON.
// Args: requestJson (GenerateTileRequest), overpassJson (string)
func watercolorRenderTileFromOverpassJSON(this js.Value, args []js.Value) interface{} {
	start := time.Now()
	if len(args) < 2 {
		return map[string]any{"error": "missing arguments"}
	}

	var req GenerateTileRequest
	if err := json.Unmarshal([]byte(args[0].String()), &req); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to parse request: %v", err)}
	}
	overpassJSON := args[1].String()
	if strings.TrimSpace(overpassJSON) == "" {
		return map[string]any{"error": "empty Overpass JSON"}
	}

	if err := ensureTexturesLoaded(); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to load textures: %v", err)}
	}

	tileSize := tileSizeForRequest(req)
	params := watercolor.DefaultParams(tileSize, defaultSeed, embeddedTextures)
	padPx := watercolor.RequiredPaddingPx(params)
	if padPx > tileSize {
		padPx = tileSize
	}

	metatileSize := tileSize + 2*padPx
	params.TileSize = metatileSize
	params.OffsetX = req.X*tileSize - padPx
	params.OffsetY = req.Y*tileSize - padPx

	// Generate Perlin noise once for all layers to avoid redundant allocations
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		params.TileSize, params.TileSize,
		params.NoiseScale, params.Seed,
		params.OffsetX, params.OffsetY,
	)

	result, err := datasource.UnmarshalOverpassJSON([]byte(overpassJSON))
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to parse Overpass JSON: %v", err)}
	}
	features := datasource.ExtractFeaturesFromOverpassResult(result)

	r := raster.NewRenderer(req.Zoom, tileSize, metatileSize, metatileSize, params.OffsetX, params.OffsetY)
	raw := r.RenderLayers(features)

	painted := make(map[geojson.LayerType]image.Image)

	waterImg := raw[geojson.LayerWater]
	roadsImg := raw[geojson.LayerRoads]
	highwaysImg := raw[geojson.LayerHighways]

	baseBounds := image.Rect(0, 0, params.TileSize, params.TileSize)
	waterMask := mask.NewEmptyMask(baseBounds)
	roadsMask := mask.NewEmptyMask(baseBounds)
	if waterImg != nil {
		waterMask = mask.ExtractAlphaMask(waterImg)
	}
	if roadsImg != nil {
		roadsMask = mask.ExtractAlphaMask(roadsImg)
	}

	nonLandBase := mask.MaxMask(waterMask, roadsMask)

	if waterImg != nil {
		waterPainted, err := watercolor.PaintLayer(waterImg, geojson.LayerWater, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint water: %v", err)}
		}
		painted[geojson.LayerWater] = waterPainted
	}

	landMask, err := func() (*image.Gray, error) {
		blurred := mask.BoxBlurSigma(nonLandBase, params.BlurSigma)
		noisy := blurred
		if params.NoiseStrength != 0 {
			noisy = mask.ApplyNoiseToMask(blurred, params.PerlinNoise, params.NoiseStrength)
		}
		finalMask := mask.ApplyThresholdWithAntialiasAndInvert(noisy, params.Threshold)
		return finalMask, nil
	}()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to process non-land mask: %v", err)}
	}

	paintedLand, err := watercolor.PaintLayerFromFinalMask(landMask, geojson.LayerLand, params)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to paint land: %v", err)}
	}
	painted[geojson.LayerLand] = paintedLand

	if roadsImg != nil {
		roadsPainted, err := watercolor.PaintLayer(roadsImg, geojson.LayerRoads, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint roads: %v", err)}
		}
		painted[geojson.LayerRoads] = roadsPainted
	}
	if railroadsImg := raw[geojson.LayerRailroads]; railroadsImg != nil {
		railroadsPainted, err := watercolor.PaintLayer(railroadsImg, geojson.LayerRailroads, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint railroads: %v", err)}
		}
		painted[geojson.LayerRailroads] = railroadsPainted
	}
	if highwaysImg != nil {
		highwaysPainted, err := watercolor.PaintLayer(highwaysImg, geojson.LayerHighways, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint highways: %v", err)}
		}
		painted[geojson.LayerHighways] = highwaysPainted
	}
	if parksImg := raw[geojson.LayerParks]; parksImg != nil {
		parksMask := mask.MinMask(mask.ExtractAlphaMask(parksImg), landMask)
		parksPainted, err := watercolor.PaintLayerFromMask(parksMask, geojson.LayerParks, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint parks: %v", err)}
		}
		painted[geojson.LayerParks] = parksPainted
	}
	if civicImg := raw[geojson.LayerCivic]; civicImg != nil {
		civicMask := mask.MinMask(mask.ExtractAlphaMask(civicImg), landMask)
		civicPainted, err := watercolor.PaintLayerFromMask(civicMask, geojson.LayerCivic, params)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("failed to paint civic: %v", err)}
		}
		painted[geojson.LayerCivic] = civicPainted
	}

	base := texture.TileTexture(embeddedTextures[geojson.LayerPaper], params.TileSize, params.OffsetX, params.OffsetY)
	composited, err := composite.CompositeLayersOverBase(
		base,
		painted,
		[]geojson.LayerType{
			geojson.LayerWater, geojson.LayerLand, geojson.LayerParks, geojson.LayerCivic,
			geojson.LayerRoads, geojson.LayerRailroads, geojson.LayerHighways,
		},
		params.TileSize,
	)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to composite layers: %v", err)}
	}

	final := composited
	if padPx > 0 {
		cropRect := image.Rect(padPx, padPx, padPx+tileSize, padPx+tileSize)
		final = cropNRGBA(composited, cropRect)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, final); err != nil {
		return map[string]any{"error": fmt.Sprintf("failed to encode PNG: %v", err)}
	}

	return map[string]any{
		"pngBase64": base64.StdEncoding.EncodeToString(buf.Bytes()),
		"mime":      "image/png",
		"ms":        time.Since(start).Milliseconds(),
	}
}

func cropNRGBA(src image.Image, rect image.Rectangle) *image.NRGBA {
	if src == nil {
		return nil
	}
	if rect.Empty() {
		return image.NewNRGBA(image.Rect(0, 0, 0, 0))
	}
	if !rect.In(src.Bounds()) {
		rect = rect.Intersect(src.Bounds())
	}

	dst := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := 0; y < rect.Dy(); y++ {
		for x := 0; x < rect.Dx(); x++ {
			dst.Set(x, y, src.At(rect.Min.X+x, rect.Min.Y+y))
		}
	}
	return dst
}

// initGame is called on page load to set up the WASM module
func initGame(this js.Value, args []js.Value) interface{} {
	fmt.Println("WaterColorMap WASM module initialized")
	return map[string]any{"status": "ready"}
}

func main() {
	c := make(chan struct{})

	js.Global().Set("watercolorGenerateTile", js.FuncOf(generateTile))
	js.Global().Set("watercolorOverpassQueryForTile", js.FuncOf(watercolorOverpassQueryForTile))
	js.Global().Set("watercolorRenderTileFromOverpassJSON", js.FuncOf(watercolorRenderTileFromOverpassJSON))
	js.Global().Set("watercolorGetConcurrency", js.FuncOf(getConcurrency))
	js.Global().Set("watercolorInit", js.FuncOf(initGame))

	fmt.Println("WaterColorMap WASM module loaded")
	<-c
}
