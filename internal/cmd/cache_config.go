package cmd

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/datasource"
)

// Config keys for the shared Overpass response cache. Like `ocean:`, this block
// is shared by every command, so its keys are hyphenated rather than following
// the underscore convention of the per-command sections.
const (
	cacheEnabledKey    = "cache.enabled"
	cacheDirKey        = "cache.dir"
	cacheTTLKey        = "cache.ttl"
	cacheMaxSizeKey    = "cache.max-size"
	cacheStoreEmptyKey = "cache.store-empty"
)

// overpassCacheConfig reads the cache block from viper and opens the cache.
//
// Caching is opt-in and returns (nil, nil) when disabled, which is also what an
// absent block yields. Off is the right default twice over: the cache changes
// freshness semantics — a re-run can silently render week-old OSM data — and it
// spends gigabytes of disk, neither of which should happen without consent.
//
// Everything is validated here rather than at first use, so a mistyped duration
// or size stops the run before the first Overpass request instead of quietly
// disabling the cache the user asked for.
func overpassCacheConfig(logger *slog.Logger) (*datasource.ResponseCache, error) {
	if !viper.GetBool(cacheEnabledKey) {
		return nil, nil
	}

	dir := viper.GetString(cacheDirKey)
	if dir == "" {
		dir = datasource.DefaultCacheDir
	}

	ttl := datasource.DefaultCacheTTL
	if raw := viper.GetString(cacheTTLKey); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", cacheTTLKey, raw, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("invalid %s %q: must not be negative", cacheTTLKey, raw)
		}
		ttl = parsed
	}

	maxBytes := datasource.DefaultCacheMaxBytes
	if raw := viper.GetString(cacheMaxSizeKey); raw != "" {
		parsed, err := datasource.ParseByteSize(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", cacheMaxSizeKey, err)
		}
		maxBytes = parsed
	}

	if logger == nil {
		logger = slog.Default()
	}

	cache, err := datasource.NewResponseCache(datasource.CacheConfig{
		Logger:     logger,
		Dir:        dir,
		TTL:        ttl,
		MaxBytes:   maxBytes,
		StoreEmpty: viper.GetBool(cacheStoreEmptyKey),
	})
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", cacheDirKey, err)
	}

	logger.Info("Overpass response cache enabled",
		"dir", dir, "ttl", ttl, "max_size_bytes", maxBytes)

	return cache, nil
}
