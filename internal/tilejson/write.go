package tilejson

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the conventional file name of a TileJSON document.
const FileName = "tilejson.json"

// WriteFile writes doc as tilejson.json into dir and returns the path written.
// The directory is created when missing.
func WriteFile(dir string, doc TileJSON) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal tilejson: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write tilejson: %w", err)
	}

	return path, nil
}
