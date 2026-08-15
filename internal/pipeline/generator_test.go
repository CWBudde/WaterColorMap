package pipeline

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
)

// nonProbingWriter is a TileWriter that cannot answer existence questions —
// the shape every pre-existing writer and test fake has.
type nonProbingWriter struct{}

func (nonProbingWriter) WriteTile(int, int, int, []byte) error { return nil }

// probingWriter is a TileWriter that also implements TileProber.
type probingWriter struct {
	err    error
	exists bool
}

func (probingWriter) WriteTile(int, int, int, []byte) error { return nil }

func (p probingWriter) HasTile(int, int, int) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.exists, nil
}

// TestTileExists covers the skip decision for both backends. The invariant the
// table encodes: anything uncertain (no prober, failed probe) must answer false
// so the tile gets rendered, because a wrong skip leaves a permanent hole.
func TestTileExists(t *testing.T) {
	tests := []struct {
		writer     TileWriter
		name       string
		createFile bool
		want       bool
	}{
		{
			name:       "folder backend with existing file",
			writer:     nil,
			createFile: true,
			want:       true,
		},
		{
			name:       "folder backend with missing file",
			writer:     nil,
			createFile: false,
			want:       false,
		},
		{
			name:   "prober reports the tile present",
			writer: probingWriter{exists: true},
			want:   true,
		},
		{
			name:   "prober reports the tile absent",
			writer: probingWriter{exists: false},
			want:   false,
		},
		{
			// A failing probe must not be read as "already done".
			name:   "prober fails",
			writer: probingWriter{err: errors.New("database is locked")},
			want:   false,
		},
		{
			// The writer stays authoritative: never fall back to os.Stat on a
			// folder path that a writer-backed run does not populate.
			name:       "writer that cannot probe",
			writer:     nonProbingWriter{},
			createFile: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finalPath := filepath.Join(t.TempDir(), "z13_x100_y200.png")
			if tt.createFile {
				if err := os.WriteFile(finalPath, []byte("existing tile"), 0o600); err != nil {
					t.Fatalf("Failed to create tile file: %v", err)
				}
			}

			g := &Generator{
				logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
				options: GeneratorOptions{TileWriter: tt.writer},
			}

			got := g.tileExists(tile.Coords{Z: 13, X: 100, Y: 200}, finalPath)
			if got != tt.want {
				t.Errorf("tileExists = %v, want %v", got, tt.want)
			}
		})
	}
}
