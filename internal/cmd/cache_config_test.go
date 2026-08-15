package cmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/datasource"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCacheConfigDefaultsToDisabled(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cache, err := overpassCacheConfig(quietLogger())
	if err != nil {
		t.Fatalf("overpassCacheConfig: %v", err)
	}
	if cache != nil {
		t.Error("response caching must stay off with no config: it changes freshness semantics and costs gigabytes")
	}
}

func TestCacheConfigDisabledCreatesNothing(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	dir := filepath.Join(t.TempDir(), "cache")
	viper.Set(cacheEnabledKey, false)
	viper.Set(cacheDirKey, dir)

	cache, err := overpassCacheConfig(quietLogger())
	if err != nil {
		t.Fatalf("overpassCacheConfig: %v", err)
	}
	if cache != nil {
		t.Fatal("expected caching to be disabled")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a disabled cache must not create %s", dir)
	}
}

func TestCacheConfigReadsBlock(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	dir := filepath.Join(t.TempDir(), "overpass")
	viper.Set(cacheEnabledKey, true)
	viper.Set(cacheDirKey, dir)
	viper.Set(cacheTTLKey, "24h")
	viper.Set(cacheMaxSizeKey, "512MB")
	viper.Set(cacheStoreEmptyKey, true)

	cache, err := overpassCacheConfig(quietLogger())
	if err != nil {
		t.Fatalf("overpassCacheConfig: %v", err)
	}
	if cache == nil {
		t.Fatal("expected an enabled cache")
	}
	if cache.Dir() != dir {
		t.Errorf("dir = %q, want %q", cache.Dir(), dir)
	}
	if cache.TTL() != 24*time.Hour {
		t.Errorf("ttl = %v, want 24h", cache.TTL())
	}
	if want := int64(512 << 20); cache.MaxBytes() != want {
		t.Errorf("max-size = %d, want %d", cache.MaxBytes(), want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("an enabled cache must create its directory: %v", err)
	}
}

func TestCacheConfigUsesDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// A bare `enabled: true` must produce the documented defaults, not zero
	// values — a zero TTL or budget would mean "never expire, never evict".
	t.Chdir(t.TempDir())
	viper.Set(cacheEnabledKey, true)

	cache, err := overpassCacheConfig(quietLogger())
	if err != nil {
		t.Fatalf("overpassCacheConfig: %v", err)
	}
	if cache.Dir() != datasource.DefaultCacheDir {
		t.Errorf("dir = %q, want %q", cache.Dir(), datasource.DefaultCacheDir)
	}
	if cache.TTL() != datasource.DefaultCacheTTL {
		t.Errorf("ttl = %v, want %v", cache.TTL(), datasource.DefaultCacheTTL)
	}
	if cache.MaxBytes() != datasource.DefaultCacheMaxBytes {
		t.Errorf("max-size = %d, want %d", cache.MaxBytes(), datasource.DefaultCacheMaxBytes)
	}
}

// TestCacheConfigRejectsBadValues: a mistyped key must stop the run with an
// error that names the key, not silently disable the cache the user asked for.
func TestCacheConfigRejectsBadValues(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		wantKey string
	}{
		{
			name: "bad ttl",
			setup: func(t *testing.T) {
				viper.Set(cacheDirKey, t.TempDir())
				viper.Set(cacheTTLKey, "one week")
			},
			wantKey: cacheTTLKey,
		},
		{
			name: "negative ttl",
			setup: func(t *testing.T) {
				viper.Set(cacheDirKey, t.TempDir())
				viper.Set(cacheTTLKey, "-1h")
			},
			wantKey: cacheTTLKey,
		},
		{
			name: "bad size",
			setup: func(t *testing.T) {
				viper.Set(cacheDirKey, t.TempDir())
				viper.Set(cacheMaxSizeKey, "five gigs")
			},
			wantKey: cacheMaxSizeKey,
		},
		{
			name: "uncreatable dir",
			setup: func(t *testing.T) {
				// A file where the directory should be: MkdirAll cannot win.
				blocker := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatalf("write blocker: %v", err)
				}
				viper.Set(cacheDirKey, filepath.Join(blocker, "overpass"))
			},
			wantKey: cacheDirKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)

			viper.Set(cacheEnabledKey, true)
			tc.setup(t)

			cache, err := overpassCacheConfig(quietLogger())
			if err == nil {
				t.Fatalf("expected an error naming %s, got cache %v", tc.wantKey, cache)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name %s", err, tc.wantKey)
			}
		})
	}
}
