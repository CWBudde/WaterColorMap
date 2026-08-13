package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/safe"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

type OnDemandTilesConfig struct {
	TilesDir                 string
	StylesDir                string
	TexturesDir              string
	PNGCompression           string
	CacheControl             string
	BaseTileSize             int
	Seed                     int64
	MaxConcurrentGenerations int
	GenerationTimeout        time.Duration
	KeepLayers               bool
	GenerateMissing          bool
	DisableCache             bool
	// FetchWorkers is the number of concurrent Overpass API fetch workers (default: 2)
	FetchWorkers int
	// DataSizeWarningMB logs a warning when tile data exceeds this size (default: 10)
	DataSizeWarningMB int64
}

type OnDemandTiles struct {
	ds          pipeline.DataSource
	fetchQueue  *datasource.FetchQueue
	logger      *slog.Logger
	sem         chan struct{}
	gens        sync.Map
	cfg         OnDemandTilesConfig
	retryQueue  chan retryJob
	retryCtx    context.Context
	retryCancel context.CancelFunc

	// Status tracking for renders
	activeRenders  atomic.Int32
	totalRendered  atomic.Int64
	totalFailed    atomic.Int64
	currentRenders sync.Map // map[string]time.Time - tile coord string -> start time
	pendingRetries atomic.Int32

	// Queue tracking - tiles waiting for semaphore
	queuedRenders atomic.Int32
	queuedTiles   sync.Map // map[string]time.Time - tile coord string -> queue time

	// Per-tile locks, refcounted so entries can be dropped once nobody holds
	// or waits for them. See tileLock.
	locksMu sync.Mutex
	locks   map[string]*tileLock
}

// tileLock serializes generation of a single tile. refs counts holders plus
// waiters, so the entry can be removed from the map when it reaches zero.
type tileLock struct {
	mu   sync.Mutex
	refs int
}

// TileStatus represents the current status of the tile generation system.
type TileStatus struct {
	// Fetch status (from FetchQueue)
	Fetch *datasource.FetchQueueStatus `json:"fetch,omitempty"`

	// Render status
	Render RenderStatus `json:"render"`

	// Retry queue status
	Retry RetryStatus `json:"retry"`
}

// RenderStatus contains current render operation status.
type RenderStatus struct {
	ActiveRenders int      `json:"active_renders"`
	TotalRendered int64    `json:"total_rendered"`
	TotalFailed   int64    `json:"total_failed"`
	CurrentTiles  []string `json:"current_tiles"`
	MaxConcurrent int      `json:"max_concurrent"`
	QueuedRenders int      `json:"queued_renders"`
	QueuedTiles   []string `json:"queued_tiles"`
}

// RetryStatus contains retry queue status.
type RetryStatus struct {
	PendingRetries int `json:"pending_retries"`
	QueueCapacity  int `json:"queue_capacity"`
}

type retryJob struct {
	coords  tile.Coords
	suffix  string
	attempt int
	data    *types.TileData // Pre-fetched data for retry
}

func NewOnDemandTiles(ds pipeline.DataSource, cfg OnDemandTilesConfig, logger *slog.Logger) (*OnDemandTiles, error) {
	if cfg.TilesDir == "" {
		cfg.TilesDir = "./tiles"
	}
	if cfg.StylesDir == "" {
		cfg.StylesDir = filepath.Join("assets", "styles")
	}
	if cfg.TexturesDir == "" {
		cfg.TexturesDir = filepath.Join("assets", "textures")
	}
	if cfg.BaseTileSize <= 0 {
		cfg.BaseTileSize = 256
	}
	if cfg.MaxConcurrentGenerations <= 0 {
		cfg.MaxConcurrentGenerations = 1
	}
	if cfg.GenerationTimeout <= 0 {
		cfg.GenerationTimeout = 2 * time.Minute
	}
	if cfg.CacheControl == "" {
		cfg.CacheControl = "no-store"
	}
	if cfg.FetchWorkers <= 0 {
		cfg.FetchWorkers = 2
	}
	if cfg.DataSizeWarningMB <= 0 {
		cfg.DataSizeWarningMB = 10
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create fetch queue if datasource is OverpassDataSource
	var fetchQueue *datasource.FetchQueue
	if opDS, ok := ds.(*datasource.OverpassDataSource); ok {
		fetchQueue = datasource.NewFetchQueue(opDS, datasource.FetchQueueConfig{
			Workers:                  cfg.FetchWorkers,
			QueueSize:                100,
			DataSizeWarningThreshold: cfg.DataSizeWarningMB * 1024 * 1024,
			Logger:                   logger,
		})
		fetchQueue.Start()
		logger.Info("started fetch queue with workers", "workers", cfg.FetchWorkers)
	}

	t := &OnDemandTiles{
		ds:          ds,
		fetchQueue:  fetchQueue,
		cfg:         cfg,
		logger:      logger,
		sem:         make(chan struct{}, cfg.MaxConcurrentGenerations),
		retryQueue:  make(chan retryJob, 1000),
		retryCtx:    ctx,
		retryCancel: cancel,
	}

	// Start retry worker. safe.Go is a backstop for the loop itself; each
	// individual job is additionally recovered inside retryWorker.
	safe.Go(t.log(), "retry worker", t.retryWorker)

	return t, nil
}

// Stop gracefully shuts down the server.
func (t *OnDemandTiles) Stop() {
	t.retryCancel()
	if t.fetchQueue != nil {
		t.fetchQueue.Stop()
	}
}

// Status returns the current status of the tile generation system.
func (t *OnDemandTiles) Status() TileStatus {
	var currentRenders []string
	t.currentRenders.Range(func(key, _ any) bool {
		currentRenders = append(currentRenders, key.(string))
		return true
	})

	var queuedTiles []string
	t.queuedTiles.Range(func(key, _ any) bool {
		queuedTiles = append(queuedTiles, key.(string))
		return true
	})

	status := TileStatus{
		Render: RenderStatus{
			ActiveRenders: int(t.activeRenders.Load()),
			TotalRendered: t.totalRendered.Load(),
			TotalFailed:   t.totalFailed.Load(),
			CurrentTiles:  currentRenders,
			MaxConcurrent: t.cfg.MaxConcurrentGenerations,
			QueuedRenders: int(t.queuedRenders.Load()),
			QueuedTiles:   queuedTiles,
		},
		Retry: RetryStatus{
			PendingRetries: int(t.pendingRetries.Load()),
			QueueCapacity:  cap(t.retryQueue),
		},
	}

	if t.fetchQueue != nil {
		fetchStatus := t.fetchQueue.Status()
		status.Fetch = &fetchStatus
	}

	return status
}

// StatusHandler returns an HTTP handler for the status endpoint (JSON).
func (t *OnDemandTiles) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")

		status := t.Status()
		if err := json.NewEncoder(w).Encode(status); err != nil {
			t.log().Error("failed to encode status", "error", err)
			http.Error(w, "failed to encode status", http.StatusInternalServerError)
			return
		}
	})
}

// StatusStreamHandler returns an SSE handler for real-time status streaming.
// This uses Server-Sent Events to push status updates to the client,
// avoiding browser connection limits that block polling during tile loading.
func (t *OnDemandTiles) StatusStreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// Send status updates every 250ms
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		// Send initial status immediately
		t.sendStatusEvent(w, flusher)

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				t.sendStatusEvent(w, flusher)
			}
		}
	})
}

func (t *OnDemandTiles) sendStatusEvent(w http.ResponseWriter, flusher http.Flusher) {
	status := t.Status()
	data, err := json.Marshal(status)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func (t *OnDemandTiles) Handler() http.Handler {
	return http.HandlerFunc(t.serveTile)
}

func (t *OnDemandTiles) serveTile(w http.ResponseWriter, r *http.Request) {
	// Allow browser-based playgrounds (including GitHub Pages) to request tiles.
	// Note: HTTPS pages cannot fetch from HTTP backends due to mixed-content rules.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	coords, suffix, err := parseTilePath(r.URL.Path)
	if err != nil {
		writeTilePathError(w, r, t.log(), err)
		return
	}

	filename := coords.String() + suffix + ".png"
	fullPath := filepath.Join(t.cfg.TilesDir, filename)

	if !t.cfg.DisableCache {
		if fileExists(fullPath) {
			t.serveTileFile(w, r, fullPath)
			return
		}
	}

	if !t.cfg.GenerateMissing {
		writeTileError(w, "tile not found", http.StatusNotFound)
		return
	}

	unlock := t.lockTile(filename)
	defer unlock()

	if !t.cfg.DisableCache {
		if fileExists(fullPath) {
			t.serveTileFile(w, r, fullPath)
			return
		}
	}

	// Track tile as queued (waiting for semaphore)
	queueKey := coords.String() + suffix
	t.queuedRenders.Add(1)
	t.queuedTiles.Store(queueKey, time.Now())

	select {
	case t.sem <- struct{}{}:
		// Got semaphore - remove from queue
		t.queuedRenders.Add(-1)
		t.queuedTiles.Delete(queueKey)
		defer func() { <-t.sem }()
	case <-r.Context().Done():
		// Request cancelled - remove from queue
		t.queuedRenders.Add(-1)
		t.queuedTiles.Delete(queueKey)
		writeTileError(w, "request cancelled", http.StatusRequestTimeout)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), t.cfg.GenerationTimeout)
	defer cancel()

	force := t.cfg.DisableCache
	tileSize := tileSizeForSuffix(t.cfg.BaseTileSize, suffix)
	gen, err := t.getGenerator(tileSize)
	if err != nil {
		t.log().Error("failed to init generator", "error", err)
		writeTileError(w, "failed to init generator", http.StatusInternalServerError)
		return
	}

	start := time.Now()

	// Phase 1: Fetch data (decoupled from rendering)
	// The go-overpass library handles retries internally with exponential backoff
	var tileData *types.TileData
	if t.fetchQueue != nil {
		tileCoord := types.TileCoordinate{
			Zoom: int(coords.Z),
			X:    int(coords.X),
			Y:    int(coords.Y),
		}
		bounds := gen.CalculateFetchBounds(coords)

		fetchResult, fetchErr := t.fetchQueue.SubmitAndWait(ctx, tileCoord, bounds)
		if fetchErr != nil {
			t.log().Error("fetch queue error", "coords", coords.String(), "error", fetchErr)
			writeTileError(w, "upstream data fetch failed", http.StatusBadGateway)
			return
		}
		if fetchResult.Error != nil {
			// Fetch failed - queue for retry if transient
			if isTransientError(fetchResult.Error) {
				t.log().Warn("transient fetch error, queuing retry", "coords", coords.String(), "suffix", suffix, "error", fetchResult.Error)
				t.queueRetry(coords, suffix, 0, nil)
			} else {
				t.log().Error("failed to fetch tile data", "coords", coords.String(), "suffix", suffix, "error", fetchResult.Error)
			}
			writeTileError(w, "upstream data fetch failed", http.StatusBadGateway)
			return
		}
		tileData = fetchResult.Data
		t.log().Info("fetch completed", "coords", coords.String(), "data_size_mb", fmt.Sprintf("%.2f", float64(fetchResult.DataSize)/(1024*1024)))
	}

	// Phase 2: Render with pre-fetched data (or fetch during render if no queue)
	tileKey := coords.String() + suffix
	t.activeRenders.Add(1)
	t.currentRenders.Store(tileKey, time.Now())

	_, _, err = gen.GenerateWithData(ctx, coords, force, suffix, nil, tileData)

	t.activeRenders.Add(-1)
	t.currentRenders.Delete(tileKey)

	if err != nil {
		t.totalFailed.Add(1)
		// Rendering error - only queue retry if it's a fetch-related transient error
		// and we didn't already have pre-fetched data
		if tileData == nil && isTransientError(err) {
			t.log().Warn("transient error during generation, queuing retry", "coords", coords.String(), "suffix", suffix, "error", err)
			t.queueRetry(coords, suffix, 0, nil)
		} else {
			t.log().Error("failed to generate tile", "coords", coords.String(), "suffix", suffix, "error", err)
		}

		writeTileError(w, "tile generation failed", http.StatusBadGateway)
		return
	}
	t.totalRendered.Add(1)
	t.log().Info("tile generated on-demand", "coords", coords.String(), "suffix", suffix, "ms", time.Since(start).Milliseconds())

	if !fileExists(fullPath) {
		t.log().Error("tile generation reported success but no file on disk", "path", fullPath)
		writeTileError(w, "tile generation failed", http.StatusInternalServerError)
		return
	}

	t.serveTileFile(w, r, fullPath)
}

// serveTileFile serves a rendered tile with the configured cache policy.
func (t *OnDemandTiles) serveTileFile(w http.ResponseWriter, r *http.Request, fullPath string) {
	w.Header().Set("Cache-Control", t.cfg.CacheControl)
	http.ServeFile(w, r, fullPath)
}

// writeTileError responds with a generic message and, critically, forbids
// caching it. Error bodies used to inherit the tile Cache-Control header, so a
// cacheable failure could pin a tile to "broken" in browsers and proxies.
//
// Messages here are deliberately generic: the detail (Overpass endpoint names,
// filesystem paths, Mapnik XML errors) goes to the log, not to the client.
func writeTileError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, code)
}

func (t *OnDemandTiles) getGenerator(tileSize int) (*pipeline.Generator, error) {
	if v, ok := t.gens.Load(tileSize); ok {
		return v.(*pipeline.Generator), nil
	}

	g, err := pipeline.NewGenerator(
		t.ds,
		t.cfg.StylesDir,
		t.cfg.TexturesDir,
		t.cfg.TilesDir,
		tileSize,
		t.cfg.Seed,
		t.cfg.KeepLayers,
		t.logger,
		pipeline.GeneratorOptions{PNGCompression: t.cfg.PNGCompression},
	)
	if err != nil {
		return nil, err
	}

	actual, _ := t.gens.LoadOrStore(tileSize, g)
	return actual.(*pipeline.Generator), nil
}

// lockTile serializes generation of one tile, returning the function that
// releases it.
//
// The previous implementation stored a mutex per tile in a sync.Map and never
// removed it, so a crawler walking the z18 grid leaked a mutex per tile for the
// lifetime of the process. Entries are now refcounted and dropped once the last
// holder or waiter is gone, which keeps steady-state memory proportional to
// concurrent requests rather than to distinct tiles ever requested.
func (t *OnDemandTiles) lockTile(key string) func() {
	t.locksMu.Lock()
	if t.locks == nil {
		t.locks = make(map[string]*tileLock)
	}
	l, ok := t.locks[key]
	if !ok {
		l = &tileLock{}
		t.locks[key] = l
	}
	// Counted before releasing locksMu so the entry cannot be evicted by a
	// concurrent release while this caller is still waiting for it.
	l.refs++
	t.locksMu.Unlock()

	l.mu.Lock()

	return func() {
		l.mu.Unlock()

		t.locksMu.Lock()
		defer t.locksMu.Unlock()
		l.refs--
		if l.refs == 0 {
			delete(t.locks, key)
		}
	}
}

// lockCount reports how many per-tile locks are currently retained. Test hook
// for the eviction behaviour.
func (t *OnDemandTiles) lockCount() int {
	t.locksMu.Lock()
	defer t.locksMu.Unlock()
	return len(t.locks)
}

func (t *OnDemandTiles) log() *slog.Logger {
	if t.logger != nil {
		return t.logger
	}
	return slog.Default()
}

// parseTilePath extracts tile coordinates and the optional "@2x" suffix from a
// request path. It returns tile.ErrCoordsFormat for anything that is not a tile
// URL at all, and tile.ErrCoordsOutOfRange for a well-formed but impossible
// tile — callers map those to 404 and 400 respectively.
func parseTilePath(requestPath string) (tile.Coords, string, error) {
	// Expect: /tiles/z13_x4317_y2692.png or /tiles/z13_x4317_y2692@2x.png
	if !strings.HasPrefix(requestPath, "/tiles/") {
		return tile.Coords{}, "", fmt.Errorf("%w: %s", tile.ErrCoordsFormat, requestPath)
	}
	base := path.Base(requestPath)
	if !strings.HasSuffix(base, ".png") {
		return tile.Coords{}, "", fmt.Errorf("%w: %s", tile.ErrCoordsFormat, base)
	}
	name := strings.TrimSuffix(base, ".png")
	suffix := ""
	if strings.HasSuffix(name, "@2x") {
		suffix = "@2x"
		name = strings.TrimSuffix(name, "@2x")
	}

	coords, err := tile.ParseCoords(name)
	if err != nil {
		return tile.Coords{}, "", err
	}
	return coords, suffix, nil
}

// writeTilePathError maps a parseTilePath error to a response. An impossible
// coordinate is a client error (400) and must be rejected before any fetch or
// render is queued; anything else is simply not a tile URL (404).
func writeTilePathError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if errors.Is(err, tile.ErrCoordsOutOfRange) {
		logger.Debug("rejected out-of-range tile request", "path", r.URL.Path, "error", err)
		writeTileError(w, "tile coordinate out of range", http.StatusBadRequest)
		return
	}
	logger.Debug("rejected malformed tile request", "path", r.URL.Path, "error", err)
	http.NotFound(w, r)
}

func tileSizeForSuffix(base int, suffix string) int {
	if suffix == "@2x" {
		return base * 2
	}
	return base
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

// isTransientError checks if an error is likely transient and worth retrying
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "Gateway Timeout") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "overpass") ||
		strings.Contains(errStr, "empty response") ||
		strings.Contains(errStr, "max retries exceeded")
}

func (t *OnDemandTiles) queueRetry(coords tile.Coords, suffix string, attempt int, data *types.TileData) {
	select {
	case t.retryQueue <- retryJob{coords: coords, suffix: suffix, attempt: attempt, data: data}:
		t.pendingRetries.Add(1)
		t.log().Info("queued tile for retry", "coords", coords.String(), "suffix", suffix, "attempt", attempt+1)
	default:
		t.log().Warn("retry queue full, dropping tile", "coords", coords.String(), "suffix", suffix)
	}
}

const maxRetries = 3

func (t *OnDemandTiles) retryWorker() {
	for {
		select {
		case <-t.retryCtx.Done():
			return
		case job := <-t.retryQueue:
			t.pendingRetries.Add(-1)

			// Recover per job. A panic here used to kill the only retry
			// worker and permanently leak the semaphore slot it held.
			var keepGoing bool
			if err := safe.Do(t.log(), "tile retry", func() {
				keepGoing = t.runRetryJob(job)
			}); err != nil {
				t.totalFailed.Add(1)
				keepGoing = true
			}
			if !keepGoing {
				return
			}
		}
	}
}

// retryDelay returns the backoff before a retry attempt. Low zoom levels cover
// far more ground per tile, so their Overpass queries are heavier and hit rate
// limits harder — they wait longer before trying again.
func retryDelay(zoom uint32, attempt int) time.Duration {
	var baseDelay time.Duration
	switch {
	case zoom <= 7:
		baseDelay = 30 * time.Second
	case zoom <= 10:
		baseDelay = 15 * time.Second
	default:
		baseDelay = 5 * time.Second
	}
	return baseDelay * time.Duration(1<<attempt)
}

// runRetryJob performs one retry attempt. It reports whether the worker should
// keep running; false means the retry context was cancelled mid-job.
//
// Every exit path releases the semaphore and cancels the context via defer.
// These used to be released by hand on each branch, so a panic — or a future
// early return — leaked a generation slot for the lifetime of the process.
func (t *OnDemandTiles) runRetryJob(job retryJob) bool {
	delay := retryDelay(job.coords.Z, job.attempt)
	t.log().Info("waiting before retry", "coords", job.coords.String(), "suffix", job.suffix, "delay", delay)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-t.retryCtx.Done():
		return false
	case <-timer.C:
	}

	select {
	case t.sem <- struct{}{}:
	case <-t.retryCtx.Done():
		return false
	}
	defer func() { <-t.sem }()

	ctx, cancel := context.WithTimeout(t.retryCtx, t.cfg.GenerationTimeout)
	defer cancel()

	tileSize := tileSizeForSuffix(t.cfg.BaseTileSize, job.suffix)
	gen, err := t.getGenerator(tileSize)
	if err != nil {
		t.log().Error("retry: failed to init generator", "error", err)
		return true
	}

	tileData, ok := t.retryFetchData(ctx, job, gen)
	if !ok {
		return true
	}

	start := time.Now()
	tileKey := job.coords.String() + job.suffix
	t.activeRenders.Add(1)
	t.currentRenders.Store(tileKey, time.Now())
	// Deferred, not unwound by hand: a panic inside GenerateWithData is
	// recovered further up the stack, so a manual decrement here would be
	// skipped and permanently inflate active_renders while leaving a phantom
	// entry in current_tiles.
	defer func() {
		t.activeRenders.Add(-1)
		t.currentRenders.Delete(tileKey)
	}()

	_, _, err = gen.GenerateWithData(ctx, job.coords, false, job.suffix, nil, tileData)

	if err != nil {
		t.totalFailed.Add(1)
		t.log().Error("retry: failed to generate tile", "coords", job.coords.String(), "suffix", job.suffix, "attempt", job.attempt+1, "error", err)
		// Only retry if we didn't have pre-fetched data (fetch-related error)
		if tileData == nil && isTransientError(err) && job.attempt+1 < maxRetries {
			t.queueRetry(job.coords, job.suffix, job.attempt+1, nil)
		}
		return true
	}

	t.totalRendered.Add(1)
	t.log().Info("retry: tile generated successfully", "coords", job.coords.String(), "suffix", job.suffix, "attempt", job.attempt+1, "ms", time.Since(start).Milliseconds())
	return true
}

// retryFetchData resolves the tile data for a retry, fetching it when the job
// carries none. It reports false when the fetch failed and the job should be
// abandoned for this attempt.
func (t *OnDemandTiles) retryFetchData(ctx context.Context, job retryJob, gen *pipeline.Generator) (*types.TileData, bool) {
	if job.data != nil || t.fetchQueue == nil {
		return job.data, true
	}

	tileCoord := types.TileCoordinate{
		Zoom: int(job.coords.Z),
		X:    int(job.coords.X),
		Y:    int(job.coords.Y),
	}
	bounds := gen.CalculateFetchBounds(job.coords)

	fetchResult, fetchErr := t.fetchQueue.SubmitAndWait(ctx, tileCoord, bounds)
	if fetchErr != nil || fetchResult.Error != nil {
		fetchError := fetchErr
		if fetchError == nil {
			fetchError = fetchResult.Error
		}
		t.log().Error("retry: failed to fetch tile data", "coords", job.coords.String(), "suffix", job.suffix, "attempt", job.attempt+1, "error", fetchError)
		if isTransientError(fetchError) && job.attempt+1 < maxRetries {
			t.queueRetry(job.coords, job.suffix, job.attempt+1, nil)
		}
		return nil, false
	}

	t.log().Info("retry: fetch completed", "coords", job.coords.String(), "data_size_mb", fmt.Sprintf("%.2f", float64(fetchResult.DataSize)/(1024*1024)))
	return fetchResult.Data, true
}
