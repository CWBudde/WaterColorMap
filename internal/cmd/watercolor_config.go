package cmd

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/watercolor"
)

// loadWatercolorOverrides reads the optional `watercolor:` block from the
// config file.
//
// It returns nil when the block is absent, and nil is the value that keeps the
// renderer on the untouched DefaultParams path — so "no config" and "empty
// config" mean the same thing, which is what the goldens rely on.
func loadWatercolorOverrides() (*watercolor.Overrides, error) {
	raw := viper.Get("watercolor")
	if raw == nil {
		return nil, nil
	}

	var overrides watercolor.Overrides

	// ErrorUnused turns a misspelled key into a startup failure instead of a
	// setting that silently does nothing. It is scoped to this subtree on
	// purpose: applied to the whole config it would immediately reject the
	// existing unused keys elsewhere in config.example.yaml, and this decoder
	// has no business policing them.
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           &overrides,
		ErrorUnused:      true,
		WeaklyTypedInput: true, // YAML gives ints where we want floats
	})
	if err != nil {
		return nil, fmt.Errorf("watercolor: %w", err)
	}
	if err := decoder.Decode(raw); err != nil {
		return nil, fmt.Errorf("watercolor: %w", err)
	}

	// Validate here as well as in NewGenerator so a `serve` run that builds its
	// generators lazily still rejects a bad file at startup.
	if err := overrides.Validate(); err != nil {
		return nil, err
	}

	return &overrides, nil
}
