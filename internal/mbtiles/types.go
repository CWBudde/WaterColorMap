// Package mbtiles provides MBTiles format support for reading and writing tile databases.
package mbtiles

import (
	"fmt"
	"strconv"
)

// Metadata contains MBTiles metadata fields.
type Metadata struct {
	// MinZoom/MaxZoom are pointers so that zoom level 0 — a perfectly valid
	// zoom — can be told apart from "not set". A nil pointer omits the key.
	// Use Zoom to build one from a literal. They lead the struct to satisfy
	// the fieldalignment linter.
	MinZoom     *int
	MaxZoom     *int
	Name        string // Human-readable tileset identifier
	Format      string // Tile data type (png, jpg, webp, pbf)
	Attribution string // Attribution text
	Description string // Human-readable description
	Type        string // "baselayer" or "overlay"
	Version     string // Version string
	Bounds      [4]float64
	Center      [3]float64
}

// Zoom returns a pointer to z, for use with Metadata.MinZoom/MaxZoom.
func Zoom(z int) *int {
	return &z
}

// ToMap converts Metadata to a map for database insertion.
func (m Metadata) ToMap() map[string]string {
	result := make(map[string]string)

	if m.Name != "" {
		result["name"] = m.Name
	}
	if m.Format != "" {
		result["format"] = m.Format
	}
	if m.MinZoom != nil {
		result["minzoom"] = strconv.Itoa(*m.MinZoom)
	}
	if m.MaxZoom != nil {
		result["maxzoom"] = strconv.Itoa(*m.MaxZoom)
	}
	if m.Bounds != [4]float64{} {
		result["bounds"] = fmt.Sprintf("%.6f,%.6f,%.6f,%.6f",
			m.Bounds[0], m.Bounds[1], m.Bounds[2], m.Bounds[3])
	}
	if m.Center != [3]float64{} {
		result["center"] = fmt.Sprintf("%.6f,%.6f,%d",
			m.Center[0], m.Center[1], int(m.Center[2]))
	}
	if m.Attribution != "" {
		result["attribution"] = m.Attribution
	}
	if m.Description != "" {
		result["description"] = m.Description
	}
	if m.Type != "" {
		result["type"] = m.Type
	}
	if m.Version != "" {
		result["version"] = m.Version
	}

	return result
}
