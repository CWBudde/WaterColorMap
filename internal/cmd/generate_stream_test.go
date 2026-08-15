package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/checkpoint"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
)

// streamTestBBox is small enough to enumerate in a test and large enough to
// span more than one tile per zoom.
var streamTestBBox = [4]float64{9.7, 52.3, 9.9, 52.4}

func streamTestOptions() *batchOptions {
	return &batchOptions{
		workers:     3,
		zoomMin:     12,
		zoomMax:     13,
		format:      "folder",
		imageFormat: tileformat.PNG,
	}
}

// TestStreamingRunRendersEveryTile: the streamed enumeration must cover exactly
// what TilesInBBox would have.
func TestStreamingRunRendersEveryTile(t *testing.T) {
	opts := streamTestOptions()
	gen := newRecordingGenerator()

	run := runStreamingTilePool(context.Background(), gen, streamTestBBox, opts, "")

	want := tile.TilesInBBox(streamTestBBox, opts.zoomMin, opts.zoomMax)
	if run.completed != len(want) {
		t.Fatalf("completed %d tiles, want %d", run.completed, len(want))
	}
	if run.failedCount() != 0 {
		t.Errorf("clean run reported %d failures", run.failedCount())
	}

	got := gen.renderedSet()
	for _, c := range want {
		if !got[c] {
			t.Errorf("tile %s was never rendered", c.String())
		}
	}
}

// TestStreamingRunResumesFromCheckpoint: the resume is arithmetic. The prefix
// the checkpoint covers is never emitted at all — no stat, no render — and
// everything after it is rendered.
func TestStreamingRunResumesFromCheckpoint(t *testing.T) {
	opts := streamTestOptions()
	all := tile.TilesInBBox(streamTestBBox, opts.zoomMin, opts.zoomMax)
	const skipped = 7
	if len(all) <= skipped {
		t.Fatalf("test bbox yields only %d tiles", len(all))
	}

	path := filepath.Join(t.TempDir(), checkpoint.FileName)
	key := checkpoint.RunKey{
		BBox: streamTestBBox, ZoomMin: opts.zoomMin, ZoomMax: opts.zoomMax,
		Format: opts.format, ImageFormat: opts.imageFormat.String(),
	}
	opts.checkpoint = checkpoint.NewTracker(path, key, &checkpoint.State{
		Schema: checkpoint.Schema, RunKey: key, Watermark: skipped, Completed: skipped,
	}, 1)

	gen := newRecordingGenerator()
	run := runStreamingTilePool(context.Background(), gen, streamTestBBox, opts, "")

	if run.completed != len(all)-skipped {
		t.Fatalf("rendered %d tiles, want the %d after the checkpoint", run.completed, len(all)-skipped)
	}

	got := gen.renderedSet()
	for i, c := range all {
		if i < skipped && got[c] {
			t.Errorf("tile %s (index %d) was re-rendered despite the checkpoint", c.String(), i)
		}
		if i >= skipped && !got[c] {
			t.Errorf("tile %s (index %d) was not rendered after the resume", c.String(), i)
		}
	}

	// The whole range finished cleanly, so the checkpoint is gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("checkpoint survived a clean run: %v", err)
	}
}

// TestStreamingRunKeepsCheckpointAfterFailure: a failed tile blocks the
// watermark, and the file stays so the next run re-attempts it.
func TestStreamingRunKeepsCheckpointAfterFailure(t *testing.T) {
	opts := streamTestOptions()
	opts.allowFailures = true
	all := tile.TilesInBBox(streamTestBBox, opts.zoomMin, opts.zoomMax)
	const failIdx = 4

	path := filepath.Join(t.TempDir(), checkpoint.FileName)
	key := checkpoint.RunKey{
		BBox: streamTestBBox, ZoomMin: opts.zoomMin, ZoomMax: opts.zoomMax,
		Format: opts.format, ImageFormat: opts.imageFormat.String(),
	}
	opts.checkpoint = checkpoint.NewTracker(path, key, nil, 1)

	gen := newRecordingGenerator()
	gen.failFor = map[string]bool{all[failIdx].String(): true}

	run := runStreamingTilePool(context.Background(), gen, streamTestBBox, opts, "")

	if run.failed != 1 || run.failedCount() != 1 {
		t.Fatalf("failed=%d total=%d, want exactly one failure", run.failed, run.failedCount())
	}
	if len(run.failures) != 1 || run.failures[0].Task.Coords != all[failIdx] {
		t.Errorf("retained failures = %+v, want just tile %s", run.failures, all[failIdx].String())
	}

	state, err := checkpoint.Load(path)
	if err != nil || state == nil {
		t.Fatalf("checkpoint missing after a failed run: state=%v err=%v", state, err)
	}
	if state.Watermark != failIdx {
		t.Errorf("watermark %d, want %d: the failed tile must block it", state.Watermark, failIdx)
	}
	if state.Failed != 1 {
		t.Errorf("checkpoint recorded %d failures, want 1", state.Failed)
	}
}

// TestStreamingRunReportsUnscheduledTilesAsFailures is the property
// reconcileBandResults guards on the banded path: an interrupted run must never
// report success for tiles it never attempted.
func TestStreamingRunReportsUnscheduledTilesAsFailures(t *testing.T) {
	opts := streamTestOptions()
	all := tile.TilesInBBox(streamTestBBox, opts.zoomMin, opts.zoomMax)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	run := runStreamingTilePool(ctx, newRecordingGenerator(), streamTestBBox, opts, "")

	if run.failedCount() != len(all) {
		t.Fatalf("cancelled run reported %d failures (%d failed, %d unscheduled) for %d tiles; "+
			"a cancelled run must not look like a success",
			run.failedCount(), run.failed, run.unscheduled, len(all))
	}
	if run.unscheduled > 0 && run.cause == nil {
		t.Error("unscheduled tiles were reported without a cause")
	}
}

// TestSetupCheckpointIgnoresMismatchedRunKey: never silently resume a different
// run.
func TestSetupCheckpointIgnoresMismatchedRunKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, checkpoint.FileName)

	// A checkpoint from a run over a different bbox.
	other := checkpoint.RunKey{
		BBox: [4]float64{1, 2, 3, 4}, ZoomMin: 12, ZoomMax: 13,
		Format: "folder", ImageFormat: "png",
	}
	if err := checkpoint.NewTracker(path, other, &checkpoint.State{
		Schema: checkpoint.Schema, RunKey: other, Watermark: 99,
	}, 0).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	viper.Set("generate.checkpoint", path)
	defer viper.Set("generate.checkpoint", "")

	opts := streamTestOptions()
	cp, err := setupCheckpoint(opts, streamTestBBox)
	if err != nil {
		t.Fatalf("setupCheckpoint: %v", err)
	}
	if cp.Watermark() != 0 {
		t.Errorf("watermark %d: a checkpoint for a different bbox was resumed", cp.Watermark())
	}
}

// TestSetupCheckpointIgnoresCheckpointUnderForce: force means render these
// again.
func TestSetupCheckpointIgnoresCheckpointUnderForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, checkpoint.FileName)

	opts := streamTestOptions()
	key := checkpoint.RunKey{
		BBox: streamTestBBox, ZoomMin: opts.zoomMin, ZoomMax: opts.zoomMax,
		Format: opts.format, ImageFormat: opts.imageFormat.String(),
	}
	if err := checkpoint.NewTracker(path, key, &checkpoint.State{
		Schema: checkpoint.Schema, RunKey: key, Watermark: 3,
	}, 0).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	viper.Set("generate.checkpoint", path)
	defer viper.Set("generate.checkpoint", "")

	// Without --force the same key resumes.
	cp, err := setupCheckpoint(opts, streamTestBBox)
	if err != nil {
		t.Fatalf("setupCheckpoint: %v", err)
	}
	if cp.Watermark() != 3 {
		t.Fatalf("watermark %d, want the checkpointed 3", cp.Watermark())
	}

	opts.force = true
	cp, err = setupCheckpoint(opts, streamTestBBox)
	if err != nil {
		t.Fatalf("setupCheckpoint: %v", err)
	}
	if cp.Watermark() != 0 {
		t.Errorf("watermark %d under --force, want 0", cp.Watermark())
	}
}

// TestCheckpointPathDefaultsIntoTheOutputDirectory covers the `--checkpoint`
// with no value form, and "off unless asked for".
func TestCheckpointPathDefaultsIntoTheOutputDirectory(t *testing.T) {
	opts := streamTestOptions()
	opts.outputDir = "/tmp/tiles"

	viper.Set("generate.checkpoint", "")
	if got := checkpointPath(opts); got != "" {
		t.Errorf("checkpointPath = %q with no configuration, want off", got)
	}

	viper.Set("generate.checkpoint", checkpointAuto)
	defer viper.Set("generate.checkpoint", "")
	want := filepath.Join(opts.outputDir, checkpoint.FileName)
	if got := checkpointPath(opts); got != want {
		t.Errorf("checkpointPath = %q, want %q", got, want)
	}

	viper.Set("generate.checkpoint", "/var/run/wcm.json")
	if got := checkpointPath(opts); got != "/var/run/wcm.json" {
		t.Errorf("checkpointPath = %q, want the explicit path", got)
	}
}

// TestSetupCheckpointRejectsBandFetch: banded runs render out of enumeration
// order, so an index watermark would mean nothing. Refusing beats resuming the
// wrong tiles.
func TestSetupCheckpointRejectsBandFetch(t *testing.T) {
	viper.Set("generate.checkpoint", filepath.Join(t.TempDir(), checkpoint.FileName))
	defer viper.Set("generate.checkpoint", "")

	opts := streamTestOptions()
	opts.bandFetch = true

	if _, err := setupCheckpoint(opts, streamTestBBox); err == nil {
		t.Error("--checkpoint with --band-fetch was accepted")
	}
}
