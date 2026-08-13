package texture

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	_ "image/png" // Register PNG decoder
)

func TestLoadPNGTextures(t *testing.T) {
	texturesDir := "../../assets/textures"
	textures := []string{
		"land.png",
		"water.png",
		"green.png",
		"gray.png",
		"railroad.png",
		"civic.png",
		"urban.png",
		"white.png",
		"yellow.png",
	}

	for _, textureName := range textures {
		t.Run(textureName, func(t *testing.T) {
			path := filepath.Join(texturesDir, textureName)

			// Open the file
			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("Failed to open texture %s: %v", textureName, err)
			}
			defer file.Close()

			// Decode PNG
			img, format, err := image.Decode(file)
			if err != nil {
				t.Fatalf("Failed to decode texture %s: %v", textureName, err)
			}

			// Verify format
			if format != "png" {
				t.Errorf("Expected PNG format, got %s", format)
			}

			// Verify dimensions
			bounds := img.Bounds()
			width := bounds.Dx()
			height := bounds.Dy()

			t.Logf("Texture %s: %dx%d, format: %s", textureName, width, height, format)

			// Check that texture is square and 1024x1024 (typical for seamless textures)
			if width != height {
				t.Errorf("Texture %s is not square: %dx%d", textureName, width, height)
			}

			if width != 1024 {
				t.Logf("Warning: Texture %s is not 1024x1024 (%dx%d), but this may be intentional", textureName, width, height)
			}
		})
	}
}
