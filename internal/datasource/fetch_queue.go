package datasource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cwbudde/watercolormap/internal/safe"
	"github.com/cwbudde/watercolormap/internal/types"
)

// FetchJob represents a tile fetch request.
type FetchJob struct {
	Coordinate types.TileCoordinate
	Bounds     types.BoundingBox
	ResultChan chan FetchResult
}

// FetchResult contains the result of a tile fetch operation.
type FetchResult struct {
	Data     *types.TileData
	DataSize int64 // Size of the fetched data in bytes (estimated)
	Error    error
}

// FetchQueueStatus contains current status of the fetch queue.
type FetchQueueStatus struct {
	// ActiveFetches is the number of currently in-flight fetch operations
	ActiveFetches int `json:"active_fetches"`
	// QueuedFetches is the number of jobs waiting in the queue
	QueuedFetches int `json:"queued_fetches"`
	// TotalCompleted is the total number of completed fetches since start
	TotalCompleted int64 `json:"total_completed"`
	// TotalFailed is the total number of failed fetches since start
	TotalFailed int64 `json:"total_failed"`
	// TotalBytes is the total bytes fetched since start
	TotalBytes int64 `json:"total_bytes"`
	// CurrentTiles lists tiles currently being fetched
	CurrentTiles []string `json:"current_tiles"`
}

// FetchQueueConfig configures the fetch queue behavior.
type FetchQueueConfig struct {
	// Workers is the number of concurrent fetch workers (default: 2)
	Workers int
	// QueueSize is the maximum number of pending fetch jobs (default: 100)
	QueueSize int
	// DataSizeWarningThreshold warns when tile data exceeds this size in bytes (default: 10MB)
	DataSizeWarningThreshold int64
	// Logger for fetch operations
	Logger *slog.Logger
}

// DefaultFetchQueueConfig returns sensible defaults.
func DefaultFetchQueueConfig() FetchQueueConfig {
	return FetchQueueConfig{
		Workers:                  2,
		QueueSize:                100,
		DataSizeWarningThreshold: 10 * 1024 * 1024, // 10MB
		Logger:                   slog.Default(),
	}
}

// FetchQueue manages decoupled data fetching from rendering.
// It queues fetch jobs and processes them with a pool of workers.
type FetchQueue struct {
	ds        *OverpassDataSource
	jobs      chan FetchJob
	cfg       FetchQueueConfig
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once

	// stateMu guards stopped and is held across every enqueue attempt, so no
	// job can reach the channel once Stop has flipped the flag. Context
	// cancellation alone could not do this: with a done context the send and
	// the ctx.Done() case are both ready and the runtime may pick the send,
	// reporting success for a job no worker will ever run.
	stateMu sync.RWMutex
	stopped bool

	// Status tracking
	activeFetches  atomic.Int32
	totalCompleted atomic.Int64
	totalFailed    atomic.Int64
	totalBytes     atomic.Int64
	currentTiles   sync.Map // map[string]time.Time - tile coord string -> start time
}

// NewFetchQueue creates a new fetch queue with the given datasource and config.
func NewFetchQueue(ds *OverpassDataSource, cfg FetchQueueConfig) *FetchQueue {
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 100
	}
	if cfg.DataSizeWarningThreshold <= 0 {
		cfg.DataSizeWarningThreshold = 10 * 1024 * 1024
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &FetchQueue{
		ds:     ds,
		jobs:   make(chan FetchJob, cfg.QueueSize),
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins processing fetch jobs with the configured number of workers.
func (fq *FetchQueue) Start() {
	fq.startOnce.Do(func() {
		fq.cfg.Logger.Info("starting fetch queue workers", "workers", fq.cfg.Workers)
		for i := 0; i < fq.cfg.Workers; i++ {
			fq.wg.Add(1)
			go fq.worker(i)
		}
	})
}

// Stop gracefully shuts down the fetch queue. It is safe to call more than
// once and safe to race with Submit.
//
// The jobs channel is deliberately not closed: workers already exit on
// ctx.Done(), and closing it raced with an in-flight Submit sending on the
// same channel, which panics. That was unreachable only because nothing ever
// called Stop; graceful shutdown makes it reachable.
func (fq *FetchQueue) Stop() {
	fq.stopOnce.Do(func() {
		// Close admission before cancelling: enqueue attempts never block
		// while holding the read lock, so this acquisition is immediate and
		// no submission can succeed from here on.
		fq.stateMu.Lock()
		fq.stopped = true
		fq.stateMu.Unlock()

		fq.cancel()
		fq.wg.Wait()
	})
}

// ErrQueueStopped is returned when a job is submitted to a queue that is
// shutting down or already stopped.
var ErrQueueStopped = errors.New("fetch queue is shutting down")

// ErrQueueFull is returned when the queue has no room for another job.
var ErrQueueFull = errors.New("fetch queue is full")

// enqueueRetryInterval is how long a blocking submission waits before trying a
// full queue again. Retrying instead of blocking on the send keeps stateMu free
// for Stop.
const enqueueRetryInterval = 10 * time.Millisecond

// tryEnqueue attempts one non-blocking send. It reports whether the job was
// queued, and errors only when the queue is stopped.
func (fq *FetchQueue) tryEnqueue(job FetchJob) (bool, error) {
	fq.stateMu.RLock()
	defer fq.stateMu.RUnlock()

	if fq.stopped {
		return false, ErrQueueStopped
	}
	select {
	case fq.jobs <- job:
		return true, nil
	default:
		return false, nil
	}
}

// enqueue waits for room in the queue, giving up when either context ends.
func (fq *FetchQueue) enqueue(ctx context.Context, job FetchJob) error {
	for {
		queued, err := fq.tryEnqueue(job)
		if err != nil {
			return err
		}
		if queued {
			return nil
		}

		timer := time.NewTimer(enqueueRetryInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-fq.ctx.Done():
			timer.Stop()
			return ErrQueueStopped
		}
	}
}

// Submit adds a fetch job to the queue and returns immediately.
// The result will be sent to the job's ResultChan when complete.
func (fq *FetchQueue) Submit(job FetchJob) error {
	queued, err := fq.tryEnqueue(job)
	if err != nil {
		return err
	}
	if !queued {
		return ErrQueueFull
	}
	return nil
}

// SubmitAndWait submits a fetch job and blocks until the result is available.
func (fq *FetchQueue) SubmitAndWait(ctx context.Context, coord types.TileCoordinate, bounds types.BoundingBox) (FetchResult, error) {
	resultChan := make(chan FetchResult, 1)
	job := FetchJob{
		Coordinate: coord,
		Bounds:     bounds,
		ResultChan: resultChan,
	}

	if err := fq.enqueue(ctx, job); err != nil {
		return FetchResult{}, err
	}

	select {
	case result := <-resultChan:
		return result, nil
	case <-ctx.Done():
		return FetchResult{}, ctx.Err()
	case <-fq.ctx.Done():
		// Without this the caller would keep waiting for a worker that has
		// already exited, until its own context expires.
		return FetchResult{}, ErrQueueStopped
	}
}

// FetchSync performs a synchronous fetch, bypassing the queue.
// Use this when you need immediate results without queuing.
func (fq *FetchQueue) FetchSync(ctx context.Context, coord types.TileCoordinate, bounds types.BoundingBox) FetchResult {
	return fq.doFetch(ctx, coord, bounds)
}

// Status returns the current status of the fetch queue.
func (fq *FetchQueue) Status() FetchQueueStatus {
	var currentTiles []string
	fq.currentTiles.Range(func(key, _ any) bool {
		currentTiles = append(currentTiles, key.(string))
		return true
	})

	return FetchQueueStatus{
		ActiveFetches:  int(fq.activeFetches.Load()),
		QueuedFetches:  len(fq.jobs),
		TotalCompleted: fq.totalCompleted.Load(),
		TotalFailed:    fq.totalFailed.Load(),
		TotalBytes:     fq.totalBytes.Load(),
		CurrentTiles:   currentTiles,
	}
}

// runJob performs one fetch, recovering from a panic in the datasource or in
// Overpass response parsing.
//
// The recovery is deliberately per job, not per worker: recovering around the
// worker loop would leave the goroutine dead and silently shrink the pool. It
// is also essential that the result is delivered even when the job panicked —
// a caller blocked in SubmitAndWait only unblocks on a result or on its own
// context expiring, so a swallowed panic would strand it for the full request
// timeout.
func (fq *FetchQueue) runJob(log *slog.Logger, job FetchJob) {
	var result FetchResult
	if err := safe.Do(log, "overpass fetch", func() {
		result = fq.doFetch(fq.ctx, job.Coordinate, job.Bounds)
	}); err != nil {
		// doFetch's own deferred cleanup (activeFetches, currentTiles) has
		// already run as the panic unwound; only the failure count is missing.
		fq.totalFailed.Add(1)
		result = FetchResult{Error: fmt.Errorf("fetch panicked: %w", err)}
	}

	if job.ResultChan != nil {
		select {
		case job.ResultChan <- result:
		default:
			log.Warn("result channel full or closed", "tile", formatTileCoord(job.Coordinate))
		}
	}
}

func (fq *FetchQueue) worker(id int) {
	defer fq.wg.Done()
	log := fq.cfg.Logger.With("worker_id", id)
	log.Debug("fetch worker started")

	for {
		select {
		case <-fq.ctx.Done():
			log.Debug("fetch worker stopping")
			return
		case job, ok := <-fq.jobs:
			if !ok {
				log.Debug("fetch worker channel closed")
				return
			}
			fq.runJob(log, job)
		}
	}
}

func (fq *FetchQueue) doFetch(ctx context.Context, coord types.TileCoordinate, bounds types.BoundingBox) FetchResult {
	tileKey := formatTileCoord(coord)

	// Track fetch start
	fq.activeFetches.Add(1)
	fq.currentTiles.Store(tileKey, time.Now())
	defer func() {
		fq.activeFetches.Add(-1)
		fq.currentTiles.Delete(tileKey)
	}()

	start := time.Now()
	log := fq.cfg.Logger.With(
		"tile", tileKey,
		"zoom", coord.Zoom,
	)

	log.Info("fetching tile data from Overpass API")

	data, err := fq.ds.FetchTileDataWithBounds(ctx, coord, bounds)
	elapsed := time.Since(start)

	if err != nil {
		fq.totalFailed.Add(1)
		log.Error("fetch failed",
			"error", err,
			"duration_ms", elapsed.Milliseconds(),
		)
		return FetchResult{Error: err}
	}

	// Estimate data size from features
	dataSize := estimateDataSize(data)

	// Track successful completion
	fq.totalCompleted.Add(1)
	fq.totalBytes.Add(dataSize)

	log.Info("fetch completed",
		"duration_ms", elapsed.Milliseconds(),
		"data_size_bytes", dataSize,
		"data_size_mb", fmt.Sprintf("%.2f", float64(dataSize)/(1024*1024)),
		"water_features", len(data.Features.Water),
		"rivers_features", len(data.Features.Rivers),
		"roads_features", len(data.Features.Roads),
		"parks_features", len(data.Features.Parks),
		"buildings_features", len(data.Features.Buildings),
		"urban_features", len(data.Features.Urban),
		"civic_features", len(data.Features.Civic),
	)

	if dataSize > fq.cfg.DataSizeWarningThreshold {
		log.Warn("tile data exceeds size threshold - consider optimizing query",
			"threshold_mb", fq.cfg.DataSizeWarningThreshold/(1024*1024),
			"actual_mb", fmt.Sprintf("%.2f", float64(dataSize)/(1024*1024)),
		)
	}

	return FetchResult{
		Data:     data,
		DataSize: dataSize,
	}
}

// estimateDataSize estimates the memory size of tile data.
// This is an approximation based on the number of features and their complexity.
func estimateDataSize(data *types.TileData) int64 {
	if data == nil {
		return 0
	}

	var size int64

	// Estimate per-feature overhead (coordinates, properties, etc.)
	// Each coordinate pair is ~16 bytes, assume average 50 coords per feature
	const avgCoordsPerFeature = 50
	const bytesPerCoord = 16
	const metadataPerFeature = 200 // Properties, tags, etc.
	const bytesPerFeature = avgCoordsPerFeature*bytesPerCoord + metadataPerFeature

	featureCount := len(data.Features.Water) +
		len(data.Features.Rivers) +
		len(data.Features.Roads) +
		len(data.Features.Parks) +
		len(data.Features.Buildings) +
		len(data.Features.Urban) +
		len(data.Features.Civic)

	size = int64(featureCount * bytesPerFeature)

	// Add overhead for the raw Overpass result if stored
	if data.OverpassResult != nil {
		// Rough estimate for JSON overhead
		size += 1024 * 1024 // 1MB baseline for raw response
	}

	return size
}

func formatTileCoord(c types.TileCoordinate) string {
	return fmt.Sprintf("z%d_x%d_y%d", c.Zoom, c.X, c.Y)
}
