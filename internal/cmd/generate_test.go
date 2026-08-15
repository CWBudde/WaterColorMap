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
// config without --hidpi must reach runBatchGenerate rather than being rejected
// up front.
//
// The stopping point has to be inside the batch path, or the test proves
// nothing — the earlier format checks all return before the bbox branch is even
// entered. An unsupported data source is the first failure past that branch
// (runBatchGenerate resolves it after parsing the bbox and the zoom range, and
// before any tile is rendered), so seeing its message means the batch path ran
// and nothing was rendered getting there.
func TestBatchWithoutHiDPIPassesValidation(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("generate.bbox", "9.7,52.3,9.9,52.4")
	viper.Set("generate.zoom_min", 13)
	viper.Set("generate.zoom_max", 13)
	viper.Set("generate.hidpi", false)
	viper.Set("generate.format", "folder")
	viper.Set("generate.folder_structure", "flat")
	viper.Set("data-source", "definitely-not-a-data-source")

	err := runGenerate(nil, nil)
	if err == nil {
		t.Fatal("expected the unsupported data source to be reported")
	}
	if !strings.Contains(err.Error(), "unsupported data source") {
		t.Errorf("expected the data source error from inside the batch path, got: %v", err)
	}
	// And specifically not the --hidpi rejection, which is what this test exists
	// to rule out.
	if strings.Contains(err.Error(), "--hidpi") {
		t.Errorf("batch generation without --hidpi should not be rejected for it: %v", err)
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
		{
			name:    "entirely east of the world",
			input:   "181,-1,182,1",
			wantErr: true,
		},
		{
			name:    "entirely west of the world",
			input:   "-181,-10,-180.5,10",
			wantErr: true,
		},
		{
			name:    "latitude beyond the pole",
			input:   "9.7,-91,9.9,52.4",
			wantErr: true,
		},
		{
			name:  "whole world",
			input: "-180,-85,180,85",
			want:  [4]float64{-180, -85, 180, 85},
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
