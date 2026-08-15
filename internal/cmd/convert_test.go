package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/tileformat"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
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

// Conversion must carry the provenance of the tiles it copies. Without it the
// converted tileset answers "unknown" for every tile, so `purge --data-before`
// would select nothing in it and `generate --stale-*` would re-render all of it.
func TestConvertCarriesTileStamps(t *testing.T) {
	dir := seedTilesDir(t, []string{"z13_x1_y2.png", "z13_x1_y3.png"})

	src, err := tilestamp.OpenFolder(dir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	want := tilestamp.Stamp{
		Z: 13, X: 1, Y: 2,
		OSMBase:     time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		RenderedAt:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Source:      "https://overpass.example/api/interpreter",
		RendererRev: "v1.2.3+abc1234",
	}
	if err := src.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := filepath.Join(t.TempDir(), "tiles.mbtiles")
	withCommandFlags(t, map[string]any{
		"convert.input_dir": dir,
		"convert.output":    out,
		"convert.name":      "test",
		"convert.bounds":    "",
	})

	if err := runConvert(convertCmd, nil); err != nil {
		t.Fatalf("runConvert: %v", err)
	}

	dst, err := tilestamp.OpenMBTilesReadOnly(out)
	if err != nil {
		t.Fatalf("OpenMBTilesReadOnly: %v", err)
	}
	defer dst.Close() // nolint:errcheck

	got, ok, err := dst.Get(13, 1, 2, "")
	if err != nil || !ok {
		t.Fatalf("Get(13/1/2) = ok:%v err:%v, want the copied stamp", ok, err)
	}
	if !got.OSMBase.Equal(want.OSMBase) || !got.RenderedAt.Equal(want.RenderedAt) ||
		got.Source != want.Source || got.RendererRev != want.RendererRev {
		t.Errorf("copied stamp = %+v, want %+v", got, want)
	}

	// The unstamped tile stays unstamped: conversion copies provenance, it does
	// not invent it.
	if _, ok, err := dst.Get(13, 1, 3, ""); err != nil || ok {
		t.Errorf("Get(13/1/3) = ok:%v err:%v, want no stamp", ok, err)
	}
}

// A tile folder from before stamps existed converts exactly as it did: nothing
// to copy is not a failure.
func TestConvertWithoutSourceStamps(t *testing.T) {
	dir := seedTilesDir(t, []string{"z13_x1_y2.png"})
	out := filepath.Join(t.TempDir(), "tiles.mbtiles")

	withCommandFlags(t, map[string]any{
		"convert.input_dir": dir,
		"convert.output":    out,
		"convert.name":      "test",
		"convert.bounds":    "",
	})

	if err := runConvert(convertCmd, nil); err != nil {
		t.Fatalf("runConvert: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat output: %v", err)
	}
}
