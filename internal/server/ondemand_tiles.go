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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/safe"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/cwbudde/watercolormap/internal/watercolor"
)

type OnDemandTilesConfig struct {
	// Watercolor optionally overrides the watercolor parameters from config.
	// Nil keeps the renderer on the untouched DefaultParams path.
	Watercolor     *watercolor.Overrides
	TilesDir       string
	StylesDir      string
	TexturesDir    string
	PNGCompression string
	CacheControl   string
	// ImageFormat is the one format this server renders and serves. The zero
	// value is PNG. A request for any other extension is a 404 — see serveTile.
	ImageFormat tileformat.Format
	// Ocean points the ocean pass at the processed OSM water polygons.
	// The zero value disables it.
	Ocean renderer.OceanConfig
	// WebPEffort is nativewebp's compression level (0-6); zero means the
	// package default. Ignored unless ImageFormat is WebP.
	WebPEffort               int
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
	// MaxPendingGenerations caps how many requests may be in the generation
	// path at once (rendering, fetching, or waiting for either). Beyond this
	// the server sheds load with 503 rather than queueing goroutines without
	// bound. Default: max(32, MaxConcurrentGenerations*8).
	MaxPendingGenerations int
}

// Field order is dictated by govet's fieldalignment: pointer-bearing fields
// first, then the plain ones, so the GC scans as little of the struct as
// possible. Related fields are kept together within that constraint and the
// grouping comments travel with them.
type OnDemandTiles struct {
	ds          pipeline.DataSource
	fetchQueue  *datasource.FetchQueue
	logger      *slog.Logger
	sem         chan struct{}
	retryQueue  chan retryJob
	retryCancel context.CancelFunc
	// stopCh is closed by BeginShutdown to release long-lived handlers (SSE).
	stopCh   chan struct{}
	retryCtx context.Context
	// locks holds the per-tile locks, refcounted so entries can be dropped
	// once nobody holds or waits for them. See tileLock.
	locks map[string]*tileLock

	gens sync.Map
	// currentRenders tracks in-progress renders.
	currentRenders sync.Map // map[string]time.Time - tile coord string -> start time
	// queuedTiles tracks tiles waiting for the render semaphore.
	queuedTiles sync.Map // map[string]time.Time - tile coord string -> queue time

	cfg OnDemandTilesConfig

	// wg tracks background workers so Stop can wait for them instead of
	// killing them mid-render.
	wg        sync.WaitGroup
	stopOnce  sync.Once
	beginOnce sync.Once
	locksMu   sync.Mutex

	totalRendered atomic.Int64
	totalFailed   atomic.Int64
	// rejectedBusy counts requests shed because the backlog was full.
	rejectedBusy atomic.Int64

	// The 32-bit counters are kept adjacent so the struct carries no padding
	// between them.
	activeRenders  atomic.Int32
	pendingRetries atomic.Int32
	// inFlightGenerations is admission control: it counts every request past
	// the admission gate, including those blocked on the per-tile lock and in
	// the fetch phase.
	inFlightGenerations atomic.Int32
	queuedRenders       atomic.Int32
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
	CurrentTiles  []string `json:"current_tiles"`
	QueuedTiles   []string `json:"queued_tiles"`
	TotalRendered int64    `json:"total_rendered"`
	TotalFailed   int64    `json:"total_failed"`
	// RejectedBusy counts requests shed because the backlog was full.
	RejectedBusy  int64 `json:"rejected_busy"`
	ActiveRenders int   `json:"active_renders"`
	MaxConcurrent int   `json:"max_concurrent"`
	QueuedRenders int   `json:"queued_renders"`
	// PendingGenerations counts requests admitted to the generation path,
	// including those blocked on a per-tile lock or in the fetch phase.
	PendingGenerations int `json:"pending_generations"`
	MaxPending         int `json:"max_pending"`
}

// RetryStatus contains retry queue status.
type RetryStatus struct {
	PendingRetries int `json:"pending_retries"`
	QueueCapacity  int `json:"queue_capacity"`
}

type retryJob struct {
	data    *types.TileData // Pre-fetched data for retry
	suffix  string
	coords  tile.Coords
	attempt int
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
	if cfg.MaxPendingGenerations <= 0 {
		// Deep enough that a normal viewport load is never shed, shallow
		// enough that the queue drains faster than a browser gives up.
		cfg.MaxPendingGenerations = max(32, cfg.MaxConcurrentGenerations*8)
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
		stopCh:      make(chan struct{}),
	}

	// Start retry worker. safe.Go is a backstop for the loop itself; each
	// individual job is additionally recovered inside retryWorker. The
	// WaitGroup lets Stop wait for it rather than killing it mid-render.
	t.wg.Add(1)
	safe.Go(t.log(), "retry worker", func() {
		defer t.wg.Done()
		t.retryWorker()
	})

	return t, nil
}

// stopWaitTimeout bounds how long Stop waits for background workers.
//
// Mapnik rendering happens in cgo and may not observe context cancellation, so
// an unbounded wait could turn a graceful shutdown into a multi-minute hang.
const stopWaitTimeout = 10 * time.Second

// sseWriteTimeout bounds a single status-stream write. The stream itself is
// unbounded in duration; only an individual stalled write is capped.
const sseWriteTimeout = 10 * time.Second

// tileWriteGrace is added to GenerationTimeout when extending the socket write
// deadline for a tile that has to be generated, covering the PNG write itself.
const tileWriteGrace = 30 * time.Second

// busyRetryAfterSeconds is the Retry-After hint returned when the render
// backlog is full.
const busyRetryAfterSeconds = 5

// BeginShutdown releases long-lived handlers so they stop holding connections
// open. It does not stop background work; call Stop for that.
//
// http.Server.Shutdown waits for active connections, and the SSE status stream
// only ends when its request context is cancelled -- which Shutdown does not
// do. Without this, shutting down with a demo tab open always burned the full
// shutdown timeout. Wire it up via srv.RegisterOnShutdown.
func (t *OnDemandTiles) BeginShutdown() {
	t.beginOnce.Do(func() {
		if t.stopCh != nil {
			close(t.stopCh)
		}
	})
}

// Stop gracefully shuts down the server. It is idempotent.
//
// Order matters: cancel first, then wait, then stop the fetch queue. Stopping
// the fetch queue before the retry worker has exited would let the worker call
// SubmitAndWait on an already-stopped queue.
//
// Waiting lets an in-flight retry render finish instead of being abandoned
// mid-tile. It is not what keeps a truncated PNG out of the cache -- the wait
// is bounded, so it cannot be: tiles are encoded to a temporary file and
// renamed into place (see pipeline.encodePNGAtomic), which is what makes a
// cached tile either absent or complete.
func (t *OnDemandTiles) Stop() {
	t.stopOnce.Do(func() {
		t.BeginShutdown()
		t.retryCancel()

		done := make(chan struct{})
		go func() {
			t.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(stopWaitTimeout):
			t.log().Warn("timed out waiting for background workers to stop",
				"timeout", stopWaitTimeout)
		}

		if t.fetchQueue != nil {
			t.fetchQueue.Stop()
		}
	})
}

// Status returns the current status of the tile generation system.
func (t *OnDemandTiles) Status() TileStatus {
	var currentRenders []string
	t.currentRenders.Range(func(key, _ any) bool {
		if s, ok := key.(string); ok {
			currentRenders = append(currentRenders, s)
		}
		return true
	})

	var queuedTiles []string
	t.queuedTiles.Range(func(key, _ any) bool {
		if s, ok := key.(string); ok {
			queuedTiles = append(queuedTiles, s)
		}
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
			// Surfaced so the demo can show backpressure rather than tiles
			// silently failing.
			PendingGenerations: int(t.inFlightGenerations.Load()),
			MaxPending:         t.cfg.MaxPendingGenerations,
			RejectedBusy:       t.rejectedBusy.Load(),
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

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// The server-wide WriteTimeout would kill this long-lived response,
		// so the deadline is re-armed per event instead of cleared outright:
		// the stream may live indefinitely, but a single stalled write is
		// still bounded.
		rc := http.NewResponseController(w)

		// Send status updates every 250ms
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		// Send initial status immediately
		if err := t.sendStatusEvent(w, flusher, rc); err != nil {
			return
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.stopCh:
				// Released on shutdown; otherwise http.Server.Shutdown would
				// wait on this connection for the whole drain timeout.
				return
			case <-ticker.C:
				if err := t.sendStatusEvent(w, flusher, rc); err != nil {
					t.log().Debug("status stream ended", "error", err)
					return
				}
			}
		}
	})
}

// sendStatusEvent writes one SSE event, returning an error when the client is
// gone or the write stalls.
//
// The error must be propagated: once a write deadline is in play, an expired
// deadline makes every subsequent write fail instantly, and the 250ms loop
// would otherwise spin forever writing to a dead connection.
func (t *OnDemandTiles) sendStatusEvent(w http.ResponseWriter, flusher http.Flusher, rc *http.ResponseController) error {
	status := t.Status()
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}

	// Not supported over HTTP/2 or behind a ResponseWriter wrapper without
	// Unwrap; the stream still works, just without a per-write bound.
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}

	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}

	// The socket error usually surfaces when net/http's buffer is flushed, not
	// in the small Fprintf above. http.Flusher.Flush swallows it, so the loop
	// would keep writing to a dead connection; ResponseController.Flush
	// reports it. The plain Flusher stays as the fallback for writers that do
	// not support the controller.
	if err := rc.Flush(); err != nil {
		if !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		flusher.Flush()
	}
	return nil
}

func (t *OnDemandTiles) Handler() http.Handler {
	return http.HandlerFunc(t.serveTile)
}

// serveTile answers a tile request. CORS is deliberately absent here: it is
// owned entirely by the serve command's withCORS middleware, which also
// answers preflights, so the toggle there cannot be overridden from inside
// the handler.
func (t *OnDemandTiles) serveTile(w http.ResponseWriter, r *http.Request) {
	coords, suffix, format, err := parseTilePath(r.URL.Path)
	if err != nil {
		writeTilePathError(w, r, t.log(), err)
		return
	}

	// This server renders exactly one format. Serving WebP bytes at a .png URL
	// would be a lie that outlives the request in every cache downstream, and
	// rendering both formats doubles fetch, render and disk for a choice the
	// operator already made. So a mismatch is simply not found.
	if format != t.imageFormat() {
		t.log().Debug("rejected tile request for a format this server does not produce",
			"path", r.URL.Path, "requested", format, "configured", t.imageFormat())
		writeTileError(w, "tile not found", http.StatusNotFound)
		return
	}

	filename := coords.FileName(suffix, format.Ext())
	fullPath := filepath.Join(t.cfg.TilesDir, filename)

	if t.serveCachedTile(w, r, fullPath) {
		return
	}

	if !t.cfg.GenerateMissing {
		writeTileError(w, "tile not found", http.StatusNotFound)
		return
	}

	// Admit before taking the per-tile lock. That lock is acquired before the
	// semaphore and held across the whole fetch+render, so requests blocked on
	// it are the largest pool of stuck goroutines -- and they are invisible to
	// queuedRenders, which is only incremented once the lock is already held.
	if !t.admit() {
		t.writeBusy(w)
		return
	}
	defer t.release()

	// Generation legitimately outlives the server-wide WriteTimeout, so the
	// socket deadline is extended for this request only. The cache-hit path
	// above deliberately keeps the shorter default.
	//
	// The deadline is re-armed after every potentially long wait rather than
	// set once: the per-tile lock and the render semaphore can each hold a
	// request for minutes, and a deadline that already expired while queueing
	// would let the request render a tile it can no longer send.
	t.extendWriteDeadline(w)

	unlock := t.lockTile(filename)
	defer unlock()

	t.extendWriteDeadline(w)

	if t.serveCachedTile(w, r, fullPath) {
		return
	}

	releaseSlot, ok := t.acquireRenderSlot(w, r, coords.String()+suffix)
	if !ok {
		return
	}
	defer releaseSlot()

	// Re-armed once more so the deadline covers the generation that starts
	// here, not the semaphore wait that just ended.
	t.extendWriteDeadline(w)

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
	tileData, ok := t.fetchTileData(ctx, w, coords, suffix, gen)
	if !ok {
		return
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
			t.queueRetry(coords, suffix, 0)
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
//
// Content-Type is set explicitly rather than left to http.ServeFile's
// extension sniffing. Go's builtin table does map .webp, but the mime package
// also loads /etc/mime.types on Linux, which would make the header this server
// sends depend on the machine it happens to run on.
func (t *OnDemandTiles) serveTileFile(w http.ResponseWriter, r *http.Request, fullPath string) {
	w.Header().Set("Cache-Control", t.cfg.CacheControl)
	w.Header().Set("Content-Type", t.imageFormat().ContentType())
	http.ServeFile(w, r, fullPath)
}

// serveCachedTile serves an already-rendered tile from disk when caching is
// enabled and the file is there, reporting whether it answered the request.
func (t *OnDemandTiles) serveCachedTile(w http.ResponseWriter, r *http.Request, fullPath string) bool {
	if t.cfg.DisableCache || !fileExists(fullPath) {
		return false
	}
	t.serveTileFile(w, r, fullPath)
	return true
}

// acquireRenderSlot waits for a render semaphore slot, tracking the tile as
// queued for the duration of the wait. It returns the release function and
// reports false when the request was cancelled first -- in which case the
// response has already been written.
func (t *OnDemandTiles) acquireRenderSlot(w http.ResponseWriter, r *http.Request, queueKey string) (func(), bool) {
	// Track tile as queued (waiting for semaphore)
	t.queuedRenders.Add(1)
	t.queuedTiles.Store(queueKey, time.Now())

	select {
	case t.sem <- struct{}{}:
		// Got semaphore - remove from queue
		t.queuedRenders.Add(-1)
		t.queuedTiles.Delete(queueKey)
		return func() { <-t.sem }, true
	case <-r.Context().Done():
		// Request cancelled - remove from queue
		t.queuedRenders.Add(-1)
		t.queuedTiles.Delete(queueKey)
		writeTileError(w, "request cancelled", http.StatusRequestTimeout)
		return nil, false
	}
}

// fetchTileData resolves the tile's source data through the fetch queue before
// rendering. With no fetch queue configured it returns (nil, true) and the
// generator fetches during the render instead. It reports false once it has
// written the error response.
func (t *OnDemandTiles) fetchTileData(
	ctx context.Context,
	w http.ResponseWriter,
	coords tile.Coords,
	suffix string,
	gen *pipeline.Generator,
) (*types.TileData, bool) {
	if t.fetchQueue == nil {
		return nil, true
	}

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
		return nil, false
	}
	if fetchResult.Error != nil {
		// Fetch failed - queue for retry if transient
		if isTransientError(fetchResult.Error) {
			t.log().Warn("transient fetch error, queuing retry", "coords", coords.String(), "suffix", suffix, "error", fetchResult.Error)
			t.queueRetry(coords, suffix, 0)
		} else {
			t.log().Error("failed to fetch tile data", "coords", coords.String(), "suffix", suffix, "error", fetchResult.Error)
		}
		writeTileError(w, "upstream data fetch failed", http.StatusBadGateway)
		return nil, false
	}

	t.log().Info("fetch completed", "coords", coords.String(), "data_size_mb", fmt.Sprintf("%.2f", float64(fetchResult.DataSize)/(1024*1024)))
	return fetchResult.Data, true
}

// admit reserves a generation slot, reporting false when the backlog is full.
//
// Add-then-check is exact without a CAS loop: concurrent callers each observe a
// distinct value, so the limit is never exceeded.
func (t *OnDemandTiles) admit() bool {
	if n := t.inFlightGenerations.Add(1); n > int32(t.cfg.MaxPendingGenerations) {
		t.inFlightGenerations.Add(-1)
		t.rejectedBusy.Add(1)
		return false
	}
	return true
}

// release returns a generation slot reserved by admit.
func (t *OnDemandTiles) release() {
	t.inFlightGenerations.Add(-1)
}

// writeBusy rejects a request because the render backlog is full. Shedding
// here is the only real backpressure: per-IP rate limiting cannot protect the
// pipeline, because one legitimate browser is allowed to outpace it.
func (t *OnDemandTiles) writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(busyRetryAfterSeconds))
	writeTileError(w, "tile render queue full", http.StatusServiceUnavailable)
}

// extendWriteDeadline lengthens the socket write deadline to cover a full
// generation. Failure is not fatal: the deadline is simply unsupported (HTTP/2,
// or a ResponseWriter wrapper without Unwrap), which only costs the bound.
func (t *OnDemandTiles) extendWriteDeadline(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(t.cfg.GenerationTimeout + tileWriteGrace)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		t.log().Warn("could not extend write deadline", "error", err)
	}
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

// imageFormat is the configured format, with the zero value meaning PNG.
func (t *OnDemandTiles) imageFormat() tileformat.Format {
	if t.cfg.ImageFormat == "" {
		return tileformat.PNG
	}
	return t.cfg.ImageFormat
}

func (t *OnDemandTiles) getGenerator(tileSize int) (*pipeline.Generator, error) {
	if v, ok := t.gens.Load(tileSize); ok {
		// The map only ever holds generators; the check keeps a corrupt entry
		// from panicking a request handler.
		if g, ok := v.(*pipeline.Generator); ok {
			return g, nil
		}
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
		pipeline.GeneratorOptions{
			PNGCompression: t.cfg.PNGCompression,
			ImageFormat:    t.imageFormat(),
			WebPEffort:     t.cfg.WebPEffort,
			Watercolor:     t.cfg.Watercolor,
			Ocean:          t.cfg.Ocean,
		},
	)
	if err != nil {
		return nil, err
	}

	actual, _ := t.gens.LoadOrStore(tileSize, g)
	if existing, ok := actual.(*pipeline.Generator); ok {
		return existing, nil
	}
	return g, nil
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

// parseTilePath extracts tile coordinates, the optional "@2x" suffix and the
// requested image format from a request path. It returns tile.ErrCoordsFormat
// for anything that is not a tile URL at all — including an extension this
// project does not produce — and tile.ErrCoordsOutOfRange for a well-formed but
// impossible tile. Callers map those to 404 and 400 respectively.
func parseTilePath(requestPath string) (tile.Coords, string, tileformat.Format, error) {
	// Expect: /tiles/z13_x4317_y2692.png, @2x, and the .webp equivalents.
	if !strings.HasPrefix(requestPath, "/tiles/") {
		return tile.Coords{}, "", "", fmt.Errorf("%w: %s", tile.ErrCoordsFormat, requestPath)
	}
	base := path.Base(requestPath)

	format, ok := tileformat.ParseExt(path.Ext(base))
	if !ok {
		return tile.Coords{}, "", "", fmt.Errorf("%w: %s", tile.ErrCoordsFormat, base)
	}

	name := strings.TrimSuffix(base, format.DotExt())
	suffix := ""
	if strings.HasSuffix(name, "@2x") {
		suffix = "@2x"
		name = strings.TrimSuffix(name, "@2x")
	}

	coords, err := tile.ParseCoords(name)
	if err != nil {
		return tile.Coords{}, "", "", err
	}
	return coords, suffix, format, nil
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

// queueRetry schedules another attempt at a tile whose data fetch failed
// transiently. The job carries no pre-fetched data: the retry re-fetches, since
// the previous fetch is exactly what failed.
func (t *OnDemandTiles) queueRetry(coords tile.Coords, suffix string, attempt int) {
	select {
	case t.retryQueue <- retryJob{coords: coords, suffix: suffix, attempt: attempt, data: nil}:
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
			t.queueRetry(job.coords, job.suffix, job.attempt+1)
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
			t.queueRetry(job.coords, job.suffix, job.attempt+1)
		}
		return nil, false
	}

	t.log().Info("retry: fetch completed", "coords", job.coords.String(), "data_size_mb", fmt.Sprintf("%.2f", float64(fetchResult.DataSize)/(1024*1024)))
	return fetchResult.Data, true
}
