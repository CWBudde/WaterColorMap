package renderer

import (
	"os"
	"sync"
	"testing"
)

// TestMultiPassRendererTempDirsAreUnique guards against a regression where every
// renderer shared os.TempDir()/watercolormap. Because tile.Coords.String() carries
// no tile size, the base (256px) and @2x (512px) renders of the same tile wrote and
// removed identical GeoJSON files, racing each other.
func TestMultiPassRendererTempDirsAreUnique(t *testing.T) {
	const renderers = 8

	stylesDir := "../../assets/styles"
	outputDir := t.TempDir()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		dirs  []string
		errs  []error
		close []func() error
	)

	for range renderers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			dirs = append(dirs, r.TempDir())
			close = append(close, r.Close)
		}()
	}
	wg.Wait()

	defer func() {
		for _, c := range close {
			c() // nolint:errcheck // Best-effort cleanup on failure paths
		}
	}()

	for _, err := range errs {
		t.Fatalf("Failed to create renderer: %v", err)
	}

	seen := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			t.Fatal("Renderer has an empty temp directory")
		}
		if seen[dir] {
			t.Errorf("Temp directory %s is shared between renderers", dir)
		}
		seen[dir] = true

		if _, err := os.Stat(dir); err != nil {
			t.Errorf("Temp directory %s not created: %v", dir, err)
		}
	}
}

// TestMultiPassRendererCloseRemovesTempDir verifies Close() cleans up the private
// temp directory, including any orphaned GeoJSON files left inside it.
func TestMultiPassRendererCloseRemovesTempDir(t *testing.T) {
	r, err := NewMultiPassRenderer("../../assets/styles", t.TempDir(), 256, 0)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}

	tempDir := r.TempDir()
	if tempDir == "" {
		t.Fatal("Renderer has an empty temp directory")
	}

	orphan := tempDir + "/z0_x0_y0_water.geojson"
	if err := os.WriteFile(orphan, []byte(`{"type":"FeatureCollection","features":[]}`), 0o600); err != nil {
		t.Fatalf("Failed to write orphan GeoJSON: %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Errorf("Temp directory %s still exists after Close (stat err: %v)", tempDir, err)
	}

	// Close must stay idempotent: the pipeline defers it.
	if err := r.Close(); err != nil {
		t.Errorf("Second Close failed: %v", err)
	}
}
