package cmd

import (
	"strings"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
)

// TestValidateBatchZoom pins the sentinel split.
//
// The case that matters is {0, 5}: zoom 0 is the single world tile and the whole
// low-zoom tier starts there, but 0 used to double as "flag not supplied", so
// `--zoom-min 0` was rejected with a message about a missing flag. Everything
// else here exists to make sure fixing that did not turn a real mistake — an
// omitted flag, a negative zoom, a zoom past the projection — into a silently
// accepted run.
func TestValidateBatchZoom(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
		zoomMin int
		zoomMax int
	}{
		{
			name:    "both unset",
			zoomMin: unsetZoom,
			zoomMax: unsetZoom,
			wantErr: "are required",
		},
		{
			name:    "min unset",
			zoomMin: unsetZoom,
			zoomMax: 5,
			wantErr: "are required",
		},
		{
			name:    "max unset",
			zoomMin: 0,
			zoomMax: unsetZoom,
			wantErr: "are required",
		},
		{
			name:    "world tier from zero",
			zoomMin: 0,
			zoomMax: 5,
		},
		{
			name:    "single world tile",
			zoomMin: 0,
			zoomMax: 0,
		},
		{
			name:    "ordinary regional range",
			zoomMin: 10,
			zoomMax: 14,
		},
		{
			name:    "max zoom accepted",
			zoomMin: tile.MaxZoom,
			zoomMax: tile.MaxZoom,
		},
		{
			name:    "negative below the sentinel",
			zoomMin: -2,
			zoomMax: 5,
			wantErr: "--zoom-min (-2) must be between 0 and",
		},
		{
			name:    "past the projection",
			zoomMin: 0,
			zoomMax: tile.MaxZoom + 1,
			wantErr: "--zoom-max",
		},
		{
			name:    "inverted range",
			zoomMin: 5,
			zoomMax: 2,
			wantErr: "must be <=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBatchZoom(tt.zoomMin, tt.zoomMax)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateBatchZoom(%d, %d) = %v, want nil", tt.zoomMin, tt.zoomMax, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("validateBatchZoom(%d, %d) = nil, want error containing %q",
					tt.zoomMin, tt.zoomMax, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateBatchZoom(%d, %d) = %q, want it to contain %q",
					tt.zoomMin, tt.zoomMax, err, tt.wantErr)
			}
		})
	}
}
