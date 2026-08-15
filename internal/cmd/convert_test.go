package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tileformat"
)

// TestScanTilesDirectoryDetectsFormat: the folder is the authority on what its
// bytes are. A flag could be wrong, and a wrong one would produce an MBTiles
// file whose metadata lies about its own contents — served with the wrong
// Content-Type, with nothing to notice it.
func TestScanTilesDirectoryDetectsFormat(t *testing.T) {
	tests := []struct {
		name       string
		wantFormat tileformat.Format
		wantErr    string
		files      []string
		wantCount  int
	}{
		{
			name:       "png only",
			files:      []string{"z13_x1_y2.png", "z14_x3_y4.png"},
			wantFormat: tileformat.PNG,
			wantCount:  2,
		},
		{
			name:       "webp only",
			files:      []string{"z13_x1_y2.webp", "z14_x3_y4.webp"},
			wantFormat: tileformat.WebP,
			wantCount:  2,
		},
		{
			name:       "@2x tiles are included",
			files:      []string{"z13_x1_y2.webp", "z13_x1_y2@2x.webp"},
			wantFormat: tileformat.WebP,
			wantCount:  2,
		},
		{
			name:  "mixed formats are refused",
			files: []string{"z13_x1_y2.png", "z13_x1_y2.webp"},
			// One MBTiles file records exactly one format.
			wantErr: "more than one image format",
		},
		{
			name:       "unrelated files are ignored",
			files:      []string{"z13_x1_y2.png", "tilejson.json", "notes.txt", "z13_x1_y2.jpg"},
			wantFormat: tileformat.PNG,
			wantCount:  1,
		},
		{
			// The nested layout is what --folder-structure=nested writes, and
			// the scan used to miss it entirely.
			name:       "nested layout",
			files:      []string{"13/1/2.png", "13/1/2@2x.png", "14/3/4.png"},
			wantFormat: tileformat.PNG,
			wantCount:  3,
		},
		{
			// Only {z}/{x}/{y} counts. A number-named file at another depth is
			// not a tile of this tileset, whatever it is.
			name:       "nested layout ignores other depths",
			files:      []string{"13/1/2.png", "5.png", "a/b/c/6.png"},
			wantFormat: tileformat.PNG,
			wantCount:  1,
		},
		{
			name:       "empty directory defaults to png",
			files:      nil,
			wantFormat: tileformat.PNG,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tt.files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir for %s: %v", name, err)
				}
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			tiles, _, _, format, err := scanTilesDirectory(dir)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error mentioning %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("scanTilesDirectory: %v", err)
			}
			if len(tiles) != tt.wantCount {
				t.Errorf("found %d tiles, want %d", len(tiles), tt.wantCount)
			}
			if format != tt.wantFormat {
				t.Errorf("format = %v, want %v", format, tt.wantFormat)
			}
		})
	}
}
