// Package checkpoint records how far a batch tile run got, so an interrupted
// run can resume without re-examining what it already produced.
//
// The unit of progress is a position in the deterministic tile enumeration
// (tile.TilesInBBoxSeq), not a tile identity: resuming is then arithmetic —
// skip the first N of the sequence — rather than hundreds of thousands of
// filesystem or SQLite probes. Skip-existing still guards every tile that does
// get emitted, so the checkpoint can only ever save work, never cause a tile to
// be written twice or wrongly.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the checkpoint's default name inside the output directory.
const FileName = ".watercolormap-checkpoint.json"

// Schema is the version of the on-disk format. A file written by a different
// schema is ignored, exactly like a run-key mismatch.
//
// Bumped to 2 when Target and FolderStructure joined RunKey: a schema-1 file
// carries neither, so resuming it would be resuming a run whose destination is
// unknown.
const Schema = 2

// DefaultInterval is how many completed tiles pass between saves. Small enough
// that an interrupted run loses seconds of work, large enough that the write is
// noise next to ~0.3 renders/s.
const DefaultInterval = 2000

// RunKey identifies the run a checkpoint belongs to. Anything that changes
// which tiles the run produces, or what it produces for them, belongs in here:
// resuming across a change of any of these would skip tiles that were never
// rendered under the new settings.
type RunKey struct {
	Format string `json:"format"`
	// Target is the absolute path the run writes to: the MBTiles file, or the
	// output directory of a folder run. Without it, a watermark taken against
	// one destination would be replayed against another — an empty database or
	// a fresh directory — and the skipped prefix would never be rendered,
	// because skipped tiles are never emitted for the existence check.
	Target      string `json:"target"`
	ImageFormat string `json:"image_format"`
	// FolderStructure is the on-disk layout of a folder run ("flat" or
	// "nested"), and empty for MBTiles, where it has no effect. Switching
	// layouts changes every tile's path, so it changes which files a resumed
	// run would be assuming exist.
	FolderStructure string     `json:"folder_structure"`
	Suffix          string     `json:"suffix"`
	BBox            [4]float64 `json:"bbox"`
	ZoomMin         int        `json:"zoom_min"`
	ZoomMax         int        `json:"zoom_max"`
}

// State is the checkpoint file's content.
type State struct {
	UpdatedAt time.Time `json:"updated_at"`
	RunKey    RunKey    `json:"run_key"`
	Schema    int       `json:"schema"`
	// Watermark is the number of leading tiles of the enumeration that all
	// SUCCEEDED. A failed tile blocks it, so a resumed run re-attempts that
	// tile and everything after it.
	Watermark int `json:"watermark"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// Load reads the checkpoint at path. A missing file is not an error: it returns
// (nil, nil), which is the ordinary case of a first run.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, by design
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkpoint %s: %w", path, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint %s: %w", path, err)
	}
	return &state, nil
}

// Resumable reports whether state may be resumed for key, and why not when it
// may not. A nil state is not resumable and needs no explanation.
//
// The rule is the same one the rest of this codebase applies to skipping work:
// anything uncertain means do the work again. A checkpoint from a different
// run, a different schema, or with a nonsensical watermark is ignored rather
// than reinterpreted.
func (s *State) Resumable(key RunKey, total int) (bool, string) {
	switch {
	case s == nil:
		return false, ""
	case s.Schema != Schema:
		return false, fmt.Sprintf("checkpoint schema %d, expected %d", s.Schema, Schema)
	case s.RunKey != key:
		return false, fmt.Sprintf("checkpoint describes a different run (%+v)", s.RunKey)
	case s.Watermark < 0 || s.Watermark > total:
		return false, fmt.Sprintf("checkpoint watermark %d is outside this run's %d tiles", s.Watermark, total)
	default:
		return true, ""
	}
}

// Tracker maintains the watermark of a running batch and persists it.
//
// It is safe for concurrent use, though in practice the worker pool calls
// Complete from a single collector goroutine. Both the in-memory state and the
// file are protected: mu guards the counters, saveMu serialises publication so
// two snapshots cannot reach disk out of the order they were taken. The lock
// order is always mu then saveMu; nothing takes them the other way round.
type Tracker struct {
	// frontier holds indices that succeeded ahead of the watermark. Its size is
	// bounded by how far out of order completions can be, i.e. by the number of
	// workers and any stalled tile — not by the length of the run. `blocked`
	// is what keeps that true once something fails.
	frontier map[int]struct{}
	// flush, when set, makes the run's output durable before a watermark that
	// covers it is written. It is set once, before the run starts, and read
	// without holding mu.
	flush     func() error
	path      string
	key       RunKey
	interval  int
	watermark int
	// blocked is the lowest index known to have failed, or noFailure while
	// nothing has. The watermark can never move past it in this run.
	blocked   int
	completed int
	failed    int
	sinceSave int
	mu        sync.Mutex
	saveMu    sync.Mutex
}

// noFailure is `blocked` while nothing has failed: every index is below it.
const noFailure = math.MaxInt

// NewTracker returns a tracker writing to path, resuming from state when state
// is non-nil (the caller decides that with State.Resumable). interval <= 0
// means DefaultInterval.
func NewTracker(path string, key RunKey, state *State, interval int) *Tracker {
	if interval <= 0 {
		interval = DefaultInterval
	}

	t := &Tracker{
		path:     path,
		key:      key,
		interval: interval,
		frontier: make(map[int]struct{}),
		blocked:  noFailure,
	}
	if state != nil {
		t.watermark = state.Watermark
		t.completed = state.Completed
		t.failed = state.Failed
	}
	return t
}

// Path returns the file the tracker writes to.
func (t *Tracker) Path() string { return t.path }

// SetFlush installs a function that makes the run's output durable, and must be
// called before the run starts.
//
// A watermark is a promise that the tiles below it exist in the output. For a
// backend that buffers — mbtiles.Writer batches rows and commits them in one
// transaction — a successful render is not yet a written tile, so publishing a
// watermark over buffered rows would let a crash leave a durable checkpoint
// covering tiles that were lost. A resumed run skips that prefix outright, so
// those rows would be missing for good. With flush set, nothing is published
// before the output it describes is committed.
func (t *Tracker) SetFlush(flush func() error) { t.flush = flush }

// Watermark returns the number of leading tiles known to have succeeded, i.e.
// how many entries a resuming run skips.
func (t *Tracker) Watermark() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.watermark
}

// Complete records the outcome of the task at index in the enumeration, and
// saves when the interval has elapsed. Save errors are returned; the caller
// decides whether to warn or abort (warning is the sane choice — a run that
// cannot checkpoint is still a run that renders tiles).
//
// A failure never advances the watermark, not even past indices that already
// succeeded behind it: the whole point is that a resumed run re-attempts the
// failed tile.
func (t *Tracker) Complete(index int, ok bool) error {
	t.mu.Lock()
	t.completed++
	if !ok {
		t.failed++
		t.block(index)
	} else {
		t.record(index)
	}

	t.sinceSave++
	if t.sinceSave < t.interval {
		t.mu.Unlock()
		return nil
	}
	t.sinceSave = 0
	state := t.stateLocked()
	// Taken while mu is still held, so snapshots are published in the order
	// they were taken and a later watermark can never be overwritten by an
	// earlier one.
	t.saveMu.Lock()
	t.mu.Unlock()
	defer t.saveMu.Unlock()

	return t.publish(state)
}

// block records that index failed.
//
// The watermark can never move past a failed index in this run, so every
// success at or beyond it is dead weight: a resume re-attempts the whole suffix
// anyway. Dropping those entries is what keeps the frontier bounded when an
// early tile fails — without it, a failure at index 4 of a planet-sized run
// would leave every later success in the map, which is exactly the O(tiles)
// allocation the streaming path exists to remove.
func (t *Tracker) block(index int) {
	if index >= t.blocked {
		return
	}
	t.blocked = index
	for i := range t.frontier {
		if i >= t.blocked {
			delete(t.frontier, i)
		}
	}
}

// record marks index as succeeded and advances the watermark over any
// contiguous run of successes it now completes.
func (t *Tracker) record(index int) {
	if index < t.watermark || index >= t.blocked {
		return
	}
	t.frontier[index] = struct{}{}
	for {
		if _, ok := t.frontier[t.watermark]; !ok {
			return
		}
		delete(t.frontier, t.watermark)
		t.watermark++
	}
}

// Save persists the current state. Called on shutdown, and by Complete every
// interval tiles.
func (t *Tracker) Save() error {
	t.mu.Lock()
	t.sinceSave = 0
	state := t.stateLocked()
	t.saveMu.Lock()
	t.mu.Unlock()
	defer t.saveMu.Unlock()

	return t.publish(state)
}

// publish flushes the run's output and writes state. Callers hold saveMu, so a
// snapshot cannot be overtaken on its way to disk, and Remove cannot land
// between a save's flush and its rename.
func (t *Tracker) publish(state State) error {
	if t.flush != nil {
		if err := t.flush(); err != nil {
			return fmt.Errorf("failed to flush output before checkpoint: %w", err)
		}
	}
	return writeAtomic(t.path, state)
}

// Remove deletes the checkpoint file, for a run that finished the whole range.
// A missing file is success.
func (t *Tracker) Remove() error {
	t.saveMu.Lock()
	defer t.saveMu.Unlock()

	if err := os.Remove(t.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove checkpoint %s: %w", t.path, err)
	}
	return nil
}

// State returns a snapshot, for logging and tests.
func (t *Tracker) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked()
}

func (t *Tracker) stateLocked() State {
	return State{
		Schema:    Schema,
		RunKey:    t.key,
		Watermark: t.watermark,
		Completed: t.completed,
		Failed:    t.failed,
		UpdatedAt: time.Now().UTC(),
	}
}

// writeAtomic writes state to path through a temp file in the same directory,
// fsynced and renamed into place — the discipline encodeTileAtomic uses for
// tiles, and for the same reason: a checkpoint truncated by the very interrupt
// it exists to survive would be worse than no checkpoint at all, because a
// resuming run would read it.
func writeAtomic(path string, state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode checkpoint: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("failed to create checkpoint file: %w", err)
	}
	tmpName := tmp.Name()

	// Best effort cleanup of the failure paths; on success the file is already
	// closed and renamed away, so both calls are no-ops.
	defer func() {
		tmp.Close()        //nolint:errcheck // failure paths only
		os.Remove(tmpName) //nolint:errcheck // failure paths only
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync checkpoint: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("failed to set checkpoint file mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close checkpoint: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to publish checkpoint: %w", err)
	}
	return nil
}
