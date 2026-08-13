package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/server"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve tiles and demo UI (optionally generating missing tiles on-demand)",
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().String("addr", "127.0.0.1:8080", "Listen address (host:port)")
	serveCmd.Flags().String("tiles-dir", "", "Directory containing tiles (defaults to --output-dir)")
	serveCmd.Flags().String("demo-dir", filepath.Join("docs", "leaflet-demo"), "Directory for demo static files")
	serveCmd.Flags().String("mbtiles", "", "Path to MBTiles file (alternative to tiles-dir)")

	serveCmd.Flags().Bool("generate-missing", true, "Generate missing tiles on-demand and cache them to disk")
	serveCmd.Flags().Bool("disable-cache", false, "Always regenerate tiles (still writes to disk)")
	serveCmd.Flags().Int("max-concurrent-generations", runtime.NumCPU(), "Max concurrent tile generations (default: number of CPUs)")
	serveCmd.Flags().Duration("generation-timeout", 2*time.Minute, "Timeout per tile generation")
	serveCmd.Flags().String("cache-control", "no-store", "Cache-Control header for served tiles")

	serveCmd.Flags().Int("tile-size", 256, "Base tile size in pixels (256; @2x requests render 512)")
	serveCmd.Flags().String("png-compression", "default", "PNG compression (default, speed, best, none)")
	serveCmd.Flags().Int64("seed", 1337, "Deterministic seed for noise/texture alignment")
	serveCmd.Flags().Bool("keep-layers", false, "Keep intermediate rendered layer PNGs for debugging")
	serveCmd.Flags().Int("overpass-workers", 4, "Number of parallel Overpass API requests (2-4 recommended for public API)")
	serveCmd.Flags().Int("fetch-workers", 2, "Number of concurrent data fetch workers (separate from rendering)")
	serveCmd.Flags().Int64("data-size-warning-mb", 10, "Warn when tile data exceeds this size in MB")

	serveCmd.Flags().Int("max-pending-generations", 0, "Max requests in the generation path before returning 503 (0 = auto)")
	serveCmd.Flags().Float64("tile-rate-limit", server.DefaultRateLimitRPS, "Per-client tile requests per second (0 disables)")
	serveCmd.Flags().Int("tile-rate-burst", server.DefaultRateLimitBurst, "Per-client tile request burst")
	serveCmd.Flags().Duration("tile-rate-ttl", server.DefaultRateLimitTTL, "Idle time before a rate-limit entry is evicted")
	serveCmd.Flags().Int("tile-rate-max-entries", server.DefaultRateLimitMaxEntries, "Max tracked clients before falling back to a shared bucket")
	serveCmd.Flags().String("trusted-proxies", "", "Comma-separated CIDRs whose X-Forwarded-For is trusted (empty = ignore the header)")
	serveCmd.Flags().Duration("shutdown-timeout", 30*time.Second, "Grace period for draining connections on SIGINT/SIGTERM")

	mustBind := func(key string, name string) {
		if err := viper.BindPFlag(key, serveCmd.Flags().Lookup(name)); err != nil {
			panic(fmt.Sprintf("failed to bind flag: %v", err))
		}
	}

	mustBind("serve.addr", "addr")
	mustBind("serve.tiles_dir", "tiles-dir")
	mustBind("serve.demo_dir", "demo-dir")
	mustBind("serve.mbtiles", "mbtiles")
	mustBind("serve.generate_missing", "generate-missing")
	mustBind("serve.disable_cache", "disable-cache")
	mustBind("serve.max_concurrent_generations", "max-concurrent-generations")
	mustBind("serve.generation_timeout", "generation-timeout")
	mustBind("serve.cache_control", "cache-control")

	mustBind("serve.tile_size", "tile-size")
	mustBind("serve.png_compression", "png-compression")
	mustBind("serve.seed", "seed")
	mustBind("serve.keep_layers", "keep-layers")
	mustBind("serve.overpass_workers", "overpass-workers")
	mustBind("serve.fetch_workers", "fetch-workers")
	mustBind("serve.data_size_warning_mb", "data-size-warning-mb")

	mustBind("serve.max_pending_generations", "max-pending-generations")
	mustBind("serve.tile_rate_limit", "tile-rate-limit")
	mustBind("serve.tile_rate_burst", "tile-rate-burst")
	mustBind("serve.tile_rate_ttl", "tile-rate-ttl")
	mustBind("serve.tile_rate_max_entries", "tile-rate-max-entries")
	mustBind("serve.trusted_proxies", "trusted-proxies")
	mustBind("serve.shutdown_timeout", "shutdown-timeout")
}

func runServe(cmd *cobra.Command, args []string) error {
	if logger == nil {
		initLogging()
	}

	addr := viper.GetString("serve.addr")
	tilesDir := viper.GetString("serve.tiles_dir")
	if tilesDir == "" {
		tilesDir = viper.GetString("output-dir")
	}
	demoDir := viper.GetString("serve.demo_dir")
	mbtilesPath := viper.GetString("serve.mbtiles")
	generateMissing := viper.GetBool("serve.generate_missing")
	disableCache := viper.GetBool("serve.disable_cache")
	maxConc := viper.GetInt("serve.max_concurrent_generations")
	genTimeout := viper.GetDuration("serve.generation_timeout")
	cacheControl := viper.GetString("serve.cache_control")

	baseTileSize := viper.GetInt("serve.tile_size")
	pngCompression := viper.GetString("serve.png_compression")
	seed := viper.GetInt64("serve.seed")
	keepLayers := viper.GetBool("serve.keep_layers")
	overpassWorkers := viper.GetInt("serve.overpass_workers")
	fetchWorkers := viper.GetInt("serve.fetch_workers")
	dataSizeWarningMB := viper.GetInt64("serve.data_size_warning_mb")
	maxPending := viper.GetInt("serve.max_pending_generations")
	shutdownTimeout := viper.GetDuration("serve.shutdown_timeout")

	proxyPolicy, err := server.ParseProxyPolicy(viper.GetString("serve.trusted_proxies"))
	if err != nil {
		return fmt.Errorf("invalid --trusted-proxies: %w", err)
	}

	rateLimiter := server.NewIPRateLimiter(server.RateLimitConfig{
		RPS:        viper.GetFloat64("serve.tile_rate_limit"),
		Burst:      viper.GetInt("serve.tile_rate_burst"),
		TTL:        viper.GetDuration("serve.tile_rate_ttl"),
		MaxEntries: viper.GetInt("serve.tile_rate_max_entries"),
		Proxy:      proxyPolicy,
		Logger:     logger,
	})
	defer rateLimiter.Close()

	// Released at the start of the drain so long-lived handlers stop holding
	// connections open; set below when the on-demand backend is in use.
	onShutdown := func() {}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/demo/", http.StatusFound)
	})

	// Demo UI
	fs := http.FileServer(http.Dir(demoDir))
	mux.Handle("/demo/", http.StripPrefix("/demo/", fs))

	// Tiles handler - use MBTiles if specified, otherwise folder-based with on-demand generation
	if mbtilesPath != "" {
		logger.Info("Using MBTiles for tile serving", "path", mbtilesPath)
		mbHandler, err := server.NewMBTilesHandler(server.MBTilesConfig{
			MBTilesPath:  mbtilesPath,
			CacheControl: cacheControl,
		}, logger)
		if err != nil {
			return fmt.Errorf("failed to create MBTiles handler: %w", err)
		}
		defer mbHandler.Close()

		mux.Handle("/tiles/", withCORS(rateLimiter.Middleware(mbHandler.Handler())))
	} else {
		logger.Info("Using folder-based tile serving with on-demand generation", "tiles_dir", tilesDir)
		dataSourceName := viper.GetString("data-source")
		var ds pipeline.DataSource
		switch dataSourceName {
		case "overpass":
			ds = createOverpassDataSource(overpassWorkers, logger)
		default:
			return fmt.Errorf("unsupported data source: %s", dataSourceName)
		}

		od, err := server.NewOnDemandTiles(ds, server.OnDemandTilesConfig{
			TilesDir:                 tilesDir,
			StylesDir:                filepath.Join("assets", "styles"),
			TexturesDir:              filepath.Join("assets", "textures"),
			BaseTileSize:             baseTileSize,
			Seed:                     seed,
			KeepLayers:               keepLayers,
			PNGCompression:           pngCompression,
			GenerateMissing:          generateMissing,
			DisableCache:             disableCache,
			MaxConcurrentGenerations: maxConc,
			GenerationTimeout:        genTimeout,
			CacheControl:             cacheControl,
			FetchWorkers:             fetchWorkers,
			DataSizeWarningMB:        dataSizeWarningMB,
			MaxPendingGenerations:    maxPending,
		}, logger)
		if err != nil {
			return err
		}
		defer od.Stop()
		onShutdown = od.BeginShutdown

		// The status endpoints are deliberately not rate limited: the SSE
		// stream reconnects after any error, so throttling it produces a
		// reconnect storm. /healthz and /demo/ are likewise exempt.
		mux.Handle("/tiles/status", withCORS(od.StatusHandler()))
		mux.Handle("/tiles/status/stream", withCORS(od.StatusStreamHandler()))
		mux.Handle("/tiles/", withCORS(rateLimiter.Middleware(od.Handler())))
	}

	logger.Info("demo server listening",
		"addr", addr,
		"tiles_dir", tilesDir,
		"demo_dir", demoDir,
		"generate_missing", generateMissing,
		"max_concurrent_generations", maxConc,
		"overpass_workers", overpassWorkers,
		"fetch_workers", fetchWorkers,
		"data_size_warning_mb", dataSizeWarningMB,
	)

	// Print the URL directly for easy access
	fmt.Printf("\n  → http://%s/demo/\n\n", addr)

	srv := newHTTPServer(addr, mux, logger)
	srv.RegisterOnShutdown(onShutdown)

	return runHTTPServer(srv, shutdownTimeout, logger)
}

// newHTTPServer builds the tile server with timeouts on every phase of a
// connection. Only ReadHeaderTimeout was set before, leaving the server open to
// slow-loris bodies and to clients that request a tile and never read the
// response — pinning a goroutine, an admission slot and possibly a render.
//
// WriteTimeout is deliberately shorter than a tile render. It is correct for
// /healthz, /demo/, /tiles/status and cache hits; the two routes that need
// longer extend the deadline per request via http.ResponseController.
func newHTTPServer(addr string, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// runHTTPServer serves until a signal arrives or the listener fails, then
// drains in-flight connections within shutdownTimeout.
func runHTTPServer(srv *http.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Request contexts derive from connCtx, not from the signal context. If
	// they derived from the signal, SIGTERM would cancel every in-flight
	// request at once and Shutdown would have nothing left to drain --
	// throwing away a tile that was nearly rendered. connCtx is cancelled
	// only after the drain, to abort whatever is still stuck.
	connCtx, cancelConns := context.WithCancel(context.Background())
	defer cancelConns()
	srv.BaseContext = func(net.Listener) context.Context { return connCtx }

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections", "timeout", shutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err := srv.Shutdown(shutdownCtx)
	cancelConns()
	if err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("server stopped")
	return nil
}

// createOverpassDataSource creates an Overpass datasource from configuration.
// Supports both single-server and multi-server (geographic routing) configurations.
func createOverpassDataSource(overpassWorkers int, logger *slog.Logger) pipeline.DataSource {
	// Check for multi-server configuration
	if viper.IsSet("overpass.servers") {
		var configs []map[string]interface{}
		if err := viper.UnmarshalKey("overpass.servers", &configs); err == nil && len(configs) > 0 {
			return createMultiServerDataSource(configs, logger)
		}
	}

	// Fall back to single-server configuration
	endpoint := viper.GetString("overpass.endpoint")
	if endpoint == "" {
		endpoint = "https://overpass-api.de/api/interpreter"
	}

	logger.Info("Using single Overpass server", "endpoint", endpoint, "workers", overpassWorkers)
	return datasource.NewOverpassDataSourceWithWorkers(endpoint, overpassWorkers)
}

// createMultiServerDataSource creates a multi-server routing datasource from config.
func createMultiServerDataSource(configs []map[string]interface{}, logger *slog.Logger) pipeline.DataSource {
	var serverConfigs []datasource.ServerConfig

	for i, cfg := range configs {
		endpoint := getStringOrDefault(cfg, "endpoint", "https://overpass-api.de/api/interpreter")
		workers := getIntOrDefault(cfg, "workers", 2)
		name := getStringOrDefault(cfg, "name", fmt.Sprintf("Server-%d", i+1))

		sc := datasource.ServerConfig{
			Endpoint: endpoint,
			Workers:  workers,
			Name:     name,
		}

		// Parse coverage area if specified
		if coverageMap, ok := cfg["coverage"].(map[string]interface{}); ok {
			minLat := getFloat64OrDefault(coverageMap, "min_lat", 0)
			maxLat := getFloat64OrDefault(coverageMap, "max_lat", 0)
			minLon := getFloat64OrDefault(coverageMap, "min_lon", 0)
			maxLon := getFloat64OrDefault(coverageMap, "max_lon", 0)

			if minLat != 0 || maxLat != 0 || minLon != 0 || maxLon != 0 {
				sc.Coverage = &types.BoundingBox{
					MinLat: minLat,
					MaxLat: maxLat,
					MinLon: minLon,
					MaxLon: maxLon,
				}
				logger.Info("Configured regional Overpass server",
					"name", name,
					"endpoint", endpoint,
					"workers", workers,
					"coverage", fmt.Sprintf("%.2f,%.2f to %.2f,%.2f", minLat, minLon, maxLat, maxLon))
			} else {
				logger.Info("Configured fallback Overpass server",
					"name", name,
					"endpoint", endpoint,
					"workers", workers)
			}
		} else {
			logger.Info("Configured fallback Overpass server",
				"name", name,
				"endpoint", endpoint,
				"workers", workers)
		}

		serverConfigs = append(serverConfigs, sc)
	}

	return datasource.NewMultiOverpassDataSource(serverConfigs...)
}

// Helper functions for config parsing
func getStringOrDefault(m map[string]interface{}, key, defaultVal string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return defaultVal
}

func getIntOrDefault(m map[string]interface{}, key string, defaultVal int) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return defaultVal
}

func getFloat64OrDefault(m map[string]interface{}, key string, defaultVal float64) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	if v, ok := m[key].(int); ok {
		return float64(v)
	}
	return defaultVal
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
