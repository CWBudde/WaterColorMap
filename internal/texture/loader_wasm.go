//go:build js && wasm

package texture

import (
	"bytes"
	"fmt"
	"image"

	"github.com/cwbudde/watercolormap/assets"
	"github.com/cwbudde/watercolormap/internal/geojson"

	_ "image/png" // Register PNG decoder
)

// LoadEmbeddedDefaultTextures loads the default watercolor textures from the repo's
// assets directory (embedded into the WASM binary at build time).
func LoadEmbeddedDefaultTextures() (map[geojson.LayerType]image.Image, error) {
	textures := make(map[geojson.LayerType]image.Image)
	for layer, filename := range DefaultLayerTextures {
		b, err := assets.TexturesFS.ReadFile("textures/" + filename)
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded texture %s: %w", filename, err)
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("failed to decode embedded texture %s: %w", filename, err)
		}
		textures[layer] = img
	}
	return textures, nil
}
