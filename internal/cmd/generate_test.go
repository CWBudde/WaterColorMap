package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestBatchRejectsHiDPI pins the behaviour that replaced the second full render
// pass. Ignoring the flag would be worse than refusing it: a user who scripted
// `--bbox --hidpi` would get a run that looks entirely successful while
// producing half the tiles they expect, and would find out when a @2x request
// 404s.
func TestBatchRejectsHiDPI(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("generate.bbox", "9.7,52.3,9.9,52.4")
	viper.Set("generate.zoom_min", 13)
	viper.Set("generate.zoom_max", 13)
	viper.Set("generate.hidpi", true)
	viper.Set("generate.format", "folder")
	viper.Set("generate.folder_structure", "flat")

	err := runGenerate(nil, nil)
	if err == nil {
		t.Fatal("batch generation with --hidpi should be rejected")
	}
	// The message has to point somewhere useful, or the error is just a wall.
	for _, want := range []string{"--hidpi", "serve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message should mention %q, got: %v", want, err)
		}
	}
}

// TestBatchWithoutHiDPIPassesValidation is the counterpart: the same batch
// config without --hidpi must get past validation. It fails later, on the
// Overpass fetch or the asset paths, which is fine — what matters is that it
// is not rejected up front.
func TestBatchWithoutHiDPIPassesValidation(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("generate.bbox", "9.7,52.3,9.9,52.4")
	viper.Set("generate.zoom_min", 13)
	viper.Set("generate.zoom_max", 13)
	viper.Set("generate.hidpi", false)
	viper.Set("generate.format", "mbtiles")
	viper.Set("generate.folder_structure", "flat")
	// Deliberately absent --output-file, so validation stops the run early and
	// nothing renders. If --hidpi were still being checked, we would see its
	// message instead of this one.
	err := runGenerate(nil, nil)
	if err == nil {
		t.Fatal("expected the missing --output-file to be reported")
	}
	if !strings.Contains(err.Error(), "--output-file") {
		t.Errorf("expected the --output-file error, got: %v", err)
	}
}

func TestParseBBox(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    [4]float64
		wantErr bool
	}{
		{
			name:    "valid bbox",
			input:   "9.7,52.3,9.9,52.4",
			want:    [4]float64{9.7, 52.3, 9.9, 52.4},
			wantErr: false,
		},
		{
			name:    "valid bbox with spaces",
			input:   "9.7, 52.3, 9.9, 52.4",
			want:    [4]float64{9.7, 52.3, 9.9, 52.4},
			wantErr: false,
		},
		{
			name:    "negative coordinates",
			input:   "-122.5,37.7,-122.3,37.9",
			want:    [4]float64{-122.5, 37.7, -122.3, 37.9},
			wantErr: false,
		},
		{
			name:    "too few values",
			input:   "9.7,52.3,9.9",
			wantErr: true,
		},
		{
			name:    "too many values",
			input:   "9.7,52.3,9.9,52.4,10.0",
			wantErr: true,
		},
		{
			name:    "invalid number",
			input:   "abc,52.3,9.9,52.4",
			wantErr: true,
		},
		{
			name:    "minLon >= maxLon",
			input:   "10.0,52.3,9.9,52.4",
			wantErr: true,
		},
		{
			name:    "minLat >= maxLat",
			input:   "9.7,52.5,9.9,52.4",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBBox(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseBBox(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("parseBBox(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("parseBBox(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
