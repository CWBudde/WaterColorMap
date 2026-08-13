package texture

import (
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/cwbudde/watercolormap/internal/geojson"

	_ "image/png" // Register PNG decoder
)

// LoadDefaultTextures loads the default textures for all watercolor layers from the given directory.
func LoadDefaultTextures(dir string) (map[geojson.LayerType]image.Image, error) {
	textures := make(map[geojson.LayerType]image.Image)

	for layer, filename := range DefaultLayerTextures {
		path := filepath.Join(dir, filename)

		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open texture %s: %w", path, err)
		}

		img, _, err := image.Decode(file)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decode texture %s: %w", path, err)
		}

		textures[layer] = img
	}

	return textures, nil
}
