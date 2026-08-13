package renderer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// MultiPassRenderer renders tiles in multiple passes, one per layer
type MultiPassRenderer struct {
	mapnikRenderer *MapnikRenderer
	stylesDir      string
	outputDir      string
	tempDir        string
	baseTileSize   int
	padPx          int
}

// LayerRenderResult contains the result of rendering a single layer
type LayerRenderResult struct {
	Error      error
	Layer      geojson.LayerType
	OutputPath string
}

// TileRenderResult contains the results of rendering all layers for a tile
type TileRenderResult struct {
	Layers     map[geojson.LayerType]*LayerRenderResult
	TotalTime  float64
	TileCoords tile.Coords
}

// NewMultiPassRenderer creates a new multi-pass renderer.
//
// padPx renders a larger "metatile" (tileSize + 2*padPx) with expanded bounds.
// This provides real pixels outside the final tile area, which is important for
// post-processing blurs (watercolor masks, edge halos) to avoid seams.
func NewMultiPassRenderer(stylesDir, outputDir string, tileSize int, padPx int) (*MultiPassRenderer, error) {
	if tileSize <= 0 {
		return nil, fmt.Errorf("tile size must be positive")
	}
	if padPx < 0 {
		padPx = 0
	}
	renderSize := tileSize + 2*padPx

	// Create Mapnik renderer (empty style file, requested tile size)
	mapnikRenderer, err := NewMapnikRenderer("", renderSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mapnik renderer: %w", err)
	}

	// Set buffer size to ensure features near the render bounds aren't clipped.
	// When padPx is used we keep the buffer at least as large as the pad.
	buf := 128
	if padPx > buf {
		buf = padPx
	}
	mapnikRenderer.SetBufferSize(buf)

	// Create a private temp directory for GeoJSON files.
	// It must be unique per renderer: the same tile coordinates are rendered
	// concurrently at different tile sizes (base and @2x), and tile.Coords.String()
	// carries no size, so a shared directory would make those renders overwrite and
	// delete each other's GeoJSON files.
	tempDir, err := os.MkdirTemp("", "watercolormap-geojson-*")
	if err != nil {
		mapnikRenderer.Close() // nolint:errcheck // Best-effort cleanup
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		mapnikRenderer.Close() // nolint:errcheck // Best-effort cleanup
		os.RemoveAll(tempDir)  // nolint:errcheck // Best-effort cleanup
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	return &MultiPassRenderer{
		mapnikRenderer: mapnikRenderer,
		stylesDir:      stylesDir,
		outputDir:      outputDir,
		tempDir:        tempDir,
		baseTileSize:   tileSize,
		padPx:          padPx,
	}, nil
}

// Close cleans up resources, including the renderer's private GeoJSON temp directory.
func (r *MultiPassRenderer) Close() error {
	err := r.mapnikRenderer.Close()
	if r.tempDir != "" {
		if rmErr := os.RemoveAll(r.tempDir); rmErr != nil {
			// Keep the path so a later Close can retry the cleanup and still report the failure.
			if err == nil {
				err = fmt.Errorf("failed to remove temp directory: %w", rmErr)
			}
		} else {
			r.tempDir = ""
		}
	}
	return err
}

// TempDir returns the renderer's private directory for intermediate GeoJSON files.
func (r *MultiPassRenderer) TempDir() string {
	return r.tempDir
}

// RenderTile renders all layers for a single tile
func (r *MultiPassRenderer) RenderTile(coords tile.Coords, data *types.TileData) (*TileRenderResult, error) {
	result := &TileRenderResult{
		TileCoords: coords,
		Layers:     make(map[geojson.LayerType]*LayerRenderResult),
	}

	// Define the layers to render in order
	layers := []geojson.LayerType{
		geojson.LayerLand,      // Background layer (just background color)
		geojson.LayerWater,     // Water bodies
		geojson.LayerRivers,    // Rivers and streams (linear waterways)
		geojson.LayerParks,     // Parks and green spaces
		geojson.LayerUrban,     // Urban landuse areas (residential/commercial/industrial/retail)
		geojson.LayerCivic,     // Civic areas (schools, hospitals, universities, libraries, town halls)
		geojson.LayerBuildings, // Individual building footprints
		geojson.LayerRoads,     // All roads (white mask; used for cutouts)
		geojson.LayerRailroads, // Railway lines (rail, light_rail, subway, tram)
		geojson.LayerHighways,  // Major roads/highways (yellow)
	}

	// Get bounds for the tile and expand when rendering a metatile.
	bounds := coords.BoundsMercator()
	if r.padPx > 0 {
		w := bounds[2] - bounds[0]
		h := bounds[3] - bounds[1]
		padX := w * float64(r.padPx) / float64(r.baseTileSize)
		padY := h * float64(r.padPx) / float64(r.baseTileSize)
		bounds = [4]float64{bounds[0] - padX, bounds[1] - padY, bounds[2] + padX, bounds[3] + padY}
	}

	// Render each layer
	for _, layer := range layers {
		layerResult := r.renderLayer(coords, layer, data, bounds)
		result.Layers[layer] = layerResult

		if layerResult.Error != nil {
			// Log error but continue with other layers
			fmt.Printf("Warning: Failed to render layer %s for tile %s: %v\n",
				layer, coords.String(), layerResult.Error)
		}
	}

	return result, nil
}

// renderLayer renders a single layer
func (r *MultiPassRenderer) renderLayer(
	coords tile.Coords,
	layer geojson.LayerType,
	data *types.TileData,
	bounds [4]float64,
) *LayerRenderResult {
	result := &LayerRenderResult{
		Layer: layer,
	}

	// Get style file path
	stylePath := filepath.Join(r.stylesDir, "layers", fmt.Sprintf("%s.xml", layer))
	if _, err := os.Stat(stylePath); err != nil {
		result.Error = fmt.Errorf("style file not found: %s", stylePath)
		return result
	}

	// Special case: land layer (no features, just background)
	if layer == geojson.LayerLand {
		return r.renderLandLayer(coords, stylePath, bounds)
	}

	// Get features for this layer
	features := geojson.GetLayerFeatures(data.Features, layer)
	if len(features) == 0 {
		// No features for this layer - skip rendering
		result.OutputPath = ""
		return result
	}

	// Convert features to GeoJSON
	geoJSONBytes, err := geojson.ToGeoJSONBytes(features)
	if err != nil {
		result.Error = fmt.Errorf("failed to convert to GeoJSON: %w", err)
		return result
	}

	// Write GeoJSON to temporary file
	geoJSONPath := filepath.Join(r.tempDir, fmt.Sprintf("%s_%s.geojson", coords.String(), layer))
	if err := os.WriteFile(geoJSONPath, geoJSONBytes, 0o644); err != nil {
		result.Error = fmt.Errorf("failed to write GeoJSON: %w", err)
		return result
	}
	defer func() {
		os.Remove(geoJSONPath) // nolint:errcheck // Best-effort cleanup
	}()

	// Load style XML and replace datasource placeholder
	styleXML, err := os.ReadFile(stylePath)
	if err != nil {
		result.Error = fmt.Errorf("failed to read style file: %w", err)
		return result
	}

	// Replace DATASOURCE_PLACEHOLDER with actual GeoJSON path
	modifiedStyleXML := strings.ReplaceAll(string(styleXML), "DATASOURCE_PLACEHOLDER", geoJSONPath)
	geoJSONLayerName := strings.TrimSuffix(filepath.Base(geoJSONPath), filepath.Ext(geoJSONPath))
	modifiedStyleXML = strings.ReplaceAll(modifiedStyleXML, "LAYER_PLACEHOLDER", geoJSONLayerName)

	// Load style into Mapnik
	if err := r.mapnikRenderer.LoadXML(modifiedStyleXML); err != nil {
		result.Error = fmt.Errorf("failed to load style: %w", err)
		return result
	}

	// Set map bounds
	if err := r.mapnikRenderer.SetBounds(bounds[0], bounds[1], bounds[2], bounds[3]); err != nil {
		result.Error = fmt.Errorf("failed to set bounds: %w", err)
		return result
	}

	// Render to file
	outputPath := filepath.Join(r.outputDir, fmt.Sprintf("%s_%s.png", coords.String(), layer))
	if err := r.mapnikRenderer.RenderCurrentToFile(outputPath); err != nil {
		result.Error = fmt.Errorf("failed to render: %w", err)
		return result
	}

	result.OutputPath = outputPath
	return result
}

// renderLandLayer renders the land layer (just background color, no features)
func (r *MultiPassRenderer) renderLandLayer(
	coords tile.Coords,
	stylePath string,
	bounds [4]float64,
) *LayerRenderResult {
	result := &LayerRenderResult{
		Layer: geojson.LayerLand,
	}

	// Load style XML (land style has background color, no datasource)
	styleXML, err := os.ReadFile(stylePath)
	if err != nil {
		result.Error = fmt.Errorf("failed to read land style: %w", err)
		return result
	}

	// Load style into Mapnik
	if err := r.mapnikRenderer.LoadXML(string(styleXML)); err != nil {
		result.Error = fmt.Errorf("failed to load land style: %w", err)
		return result
	}

	// Set map bounds
	if err := r.mapnikRenderer.SetBounds(bounds[0], bounds[1], bounds[2], bounds[3]); err != nil {
		result.Error = fmt.Errorf("failed to set bounds: %w", err)
		return result
	}

	// Render to file using the current bounds.
	outputPath := filepath.Join(r.outputDir, fmt.Sprintf("%s_%s.png", coords.String(), geojson.LayerLand))
	if err := r.mapnikRenderer.RenderCurrentToFile(outputPath); err != nil {
		result.Error = fmt.Errorf("failed to render land layer: %w", err)
		return result
	}

	result.OutputPath = outputPath
	return result
}

// GetLayerPath returns the expected output path for a layer
func GetLayerPath(outputDir string, coords tile.Coords, layer geojson.LayerType) string {
	return filepath.Join(outputDir, fmt.Sprintf("%s_%s.png", coords.String(), layer))
}

// LayerExists checks if a layer file has already been rendered
func LayerExists(outputDir string, coords tile.Coords, layer geojson.LayerType) bool {
	path := GetLayerPath(outputDir, coords, layer)
	_, err := os.Stat(path)
	return err == nil
}
