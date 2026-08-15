package checkpoint

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey() RunKey {
	return RunKey{
		BBox:        [4]float64{9.7, 52.3, 9.9, 52.4},
		ZoomMin:     0,
		ZoomMax:     14,
		Format:      "mbtiles",
		ImageFormat: "png",
	}
}

// TestWatermarkAdvancesOverOutOfOrderSuccesses: workers finish in whatever
// order they finish, and the watermark still has to mean "everything below this
// succeeded".
func TestWatermarkAdvancesOverOutOfOrderSuccesses(t *testing.T) {
	tr := NewTracker(filepath.Join(t.TempDir(), FileName), testKey(), nil, 1_000_000)

	// Deliberately jumbled, and 0 arrives last.
	for _, i := range []int{3, 1, 4, 2, 0} {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}

	if got := tr.Watermark(); got != 5 {
		t.Errorf("watermark %d, want 5: every index 0-4 succeeded", got)
	}
}

// TestFailureBlocksWatermark is the rule that keeps a resume from skipping a
// tile that is not there: a failure blocks the watermark, and successes behind
// it do not carry it past.
func TestFailureBlocksWatermark(t *testing.T) {
	tr := NewTracker(filepath.Join(t.TempDir(), FileName), testKey(), nil, 1_000_000)

	outcomes := map[int]bool{0: true, 1: true, 2: false, 3: true, 4: true, 5: true}
	for _, i := range []int{5, 2, 0, 4, 1, 3} {
		if err := tr.Complete(i, outcomes[i]); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}

	if got := tr.Watermark(); got != 2 {
		t.Errorf("watermark %d, want 2: index 2 failed, so a resume must re-attempt it", got)
	}
	state := tr.State()
	if state.Completed != 6 || state.Failed != 1 {
		t.Errorf("completed=%d failed=%d, want 6 and 1", state.Completed, state.Failed)
	}
}

// TestWatermarkMatchesLongestSuccessfulPrefix cross-checks the incremental
// frontier against the definition, over random completion orders and random
// failures.
func TestWatermarkMatchesLongestSuccessfulPrefix(t *testing.T) {
	const n = 200
	rng := rand.New(rand.NewSource(1337)) //nolint:gosec // deterministic test input

	for trial := 0; trial < 20; trial++ {
		ok := make([]bool, n)
		for i := range ok {
			ok[i] = rng.Intn(10) != 0
		}
		order := rng.Perm(n)

		tr := NewTracker(filepath.Join(t.TempDir(), FileName), testKey(), nil, 1_000_000)
		for _, i := range order {
			if err := tr.Complete(i, ok[i]); err != nil {
				t.Fatalf("Complete: %v", err)
			}
		}

		want := 0
		for want < n && ok[want] {
			want++
		}
		if got := tr.Watermark(); got != want {
			t.Fatalf("trial %d: watermark %d, want %d", trial, got, want)
		}
	}
}

// TestFrontierStaysBoundedAfterAFailure: an early permanent failure pins the
// watermark for the rest of the run, and every later success used to be kept in
// the frontier forever — O(tiles) memory, which is exactly what the streaming
// path exists to avoid. Successes past the failure can be dropped, because a
// resume re-attempts the whole suffix anyway.
func TestFrontierStaysBoundedAfterAFailure(t *testing.T) {
	tr := NewTracker(filepath.Join(t.TempDir(), FileName), testKey(), nil, 1_000_000)

	if err := tr.Complete(4, false); err != nil {
		t.Fatalf("Complete(4): %v", err)
	}
	for i := 5; i < 5000; i++ {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}

	if got := tr.Watermark(); got != 0 {
		t.Errorf("watermark %d, want 0: index 4 failed and 0-3 never completed", got)
	}
	if got := len(tr.frontier); got != 0 {
		t.Errorf("frontier holds %d entries after a failure at index 4, want none", got)
	}

	// Indices below the failure still count: they can still carry the watermark.
	for i := 0; i < 4; i++ {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}
	if got := tr.Watermark(); got != 4 {
		t.Errorf("watermark %d, want 4: 0-3 succeeded, 4 failed", got)
	}
}

// TestFrontierDropsSuccessesPastALaterFailure: failures arrive out of order too,
// so a lower one has to evict what a higher index already banked.
func TestFrontierDropsSuccessesPastALaterFailure(t *testing.T) {
	tr := NewTracker(filepath.Join(t.TempDir(), FileName), testKey(), nil, 1_000_000)

	for _, i := range []int{7, 8, 9} {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}
	if got := len(tr.frontier); got != 3 {
		t.Fatalf("frontier holds %d entries, want the 3 banked successes", got)
	}

	if err := tr.Complete(6, false); err != nil {
		t.Fatalf("Complete(6): %v", err)
	}
	if got := len(tr.frontier); got != 0 {
		t.Errorf("frontier holds %d entries after the failure at 6, want none", got)
	}
}

// TestFlushRunsBeforeTheWatermarkIsPublished: for a buffering backend a
// successful render is not yet a durable tile, so nothing may be written that
// claims otherwise.
func TestFlushRunsBeforeTheWatermarkIsPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	tr := NewTracker(path, testKey(), nil, 1)

	flushed := 0
	tr.SetFlush(func() error {
		if _, err := os.Stat(path); err == nil {
			t.Error("checkpoint was written before the output was flushed")
		}
		flushed++
		return nil
	})

	if err := tr.Complete(0, true); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if flushed != 1 {
		t.Errorf("flush ran %d times, want once per save", flushed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("no checkpoint after a successful flush: %v", err)
	}
}

// TestFailedFlushSuppressesTheCheckpoint: an output that could not be committed
// must not get a watermark claiming it was.
func TestFailedFlushSuppressesTheCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	tr := NewTracker(path, testKey(), nil, 1)
	tr.SetFlush(func() error { return errors.New("disk on fire") })

	if err := tr.Complete(0, true); err == nil {
		t.Fatal("Complete reported success although the flush failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a checkpoint was written despite the failed flush: %v", err)
	}
}

// TestRoundTrip: what a run writes is what the next run reads.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	tr := NewTracker(path, testKey(), nil, 1_000_000)
	for i := 0; i < 10; i++ {
		if err := tr.Complete(i, i != 7); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	state, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state == nil {
		t.Fatal("Load returned no state for a file that was just written")
	}
	if state.Schema != Schema || state.RunKey != testKey() {
		t.Errorf("state = %+v, want schema %d and key %+v", state, Schema, testKey())
	}
	if state.Watermark != 7 || state.Completed != 10 || state.Failed != 1 {
		t.Errorf("watermark=%d completed=%d failed=%d, want 7, 10, 1",
			state.Watermark, state.Completed, state.Failed)
	}

	if ok, why := state.Resumable(testKey(), 100); !ok {
		t.Errorf("state should be resumable for its own key: %s", why)
	}

	// Resuming carries the counters forward rather than restarting them.
	resumed := NewTracker(path, testKey(), state, 1_000_000)
	if got := resumed.Watermark(); got != 7 {
		t.Errorf("resumed watermark %d, want 7", got)
	}
}

// TestLoadMissingFileIsNotAnError: the first run of a range has no checkpoint,
// and that is not a failure.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	state, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if state != nil {
		t.Fatalf("Load of a missing file returned %+v, want nil", state)
	}
}

// TestRunKeyMismatchIsNotResumable: never silently resume a different run.
func TestRunKeyMismatchIsNotResumable(t *testing.T) {
	base := testKey()
	state := &State{Schema: Schema, RunKey: base, Watermark: 50}

	other := base
	other.ZoomMax = 13

	bbox := base
	bbox.BBox = [4]float64{1, 2, 3, 4}

	format := base
	format.ImageFormat = "webp"

	tests := []struct {
		name  string
		state *State
		key   RunKey
		total int
	}{
		{"zoom range", state, other, 1000},
		{"bbox", state, bbox, 1000},
		{"image format", state, format, 1000},
		{"schema", &State{Schema: Schema + 1, RunKey: base, Watermark: 50}, base, 1000},
		{"watermark past the end", &State{Schema: Schema, RunKey: base, Watermark: 5000}, base, 1000},
		{"negative watermark", &State{Schema: Schema, RunKey: base, Watermark: -1}, base, 1000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := tc.state.Resumable(tc.key, tc.total)
			if ok {
				t.Fatal("mismatched checkpoint reported resumable")
			}
			if why == "" {
				t.Error("a refused checkpoint must say why; the operator has to see it")
			}
		})
	}
}

// TestAtomicWriteLeavesNoPartialFile: the interrupt a checkpoint exists to
// survive must not be able to leave a half-written one behind.
func TestAtomicWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	tr := NewTracker(path, testKey(), nil, 2)
	for i := 0; i < 20; i++ {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want just the checkpoint", len(entries))
	}
	if _, err := Load(path); err != nil {
		t.Errorf("checkpoint written by the interval saves does not parse: %v", err)
	}
}

// TestWriteFailureKeepsThePreviousCheckpoint: a save that cannot complete must
// leave the last good checkpoint in place rather than destroying it.
func TestWriteFailureKeepsThePreviousCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	tr := NewTracker(path, testKey(), nil, 1_000_000)
	for i := 0; i < 5; i++ {
		if err := tr.Complete(i, true); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A read-only directory fails the temp create, i.e. before anything could
	// touch the existing file.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("cannot make the directory read-only: %v", err)
	}
	defer os.Chmod(dir, 0o755) //nolint:errcheck // test cleanup

	if err := tr.Save(); err == nil {
		t.Skip("read-only directory still accepted a write (running as root?)")
	}

	state, err := Load(path)
	if err != nil || state == nil {
		t.Fatalf("previous checkpoint lost after a failed save: state=%v err=%v", state, err)
	}
	if state.Watermark != 5 {
		t.Errorf("watermark %d after a failed save, want the last good 5", state.Watermark)
	}
}

// TestRemove deletes on clean completion and tolerates a missing file.
func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	tr := NewTracker(path, testKey(), nil, 0)
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := tr.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("checkpoint still present after Remove: %v", err)
	}
	if err := tr.Remove(); err != nil {
		t.Errorf("Remove of a missing checkpoint: %v", err)
	}
}
