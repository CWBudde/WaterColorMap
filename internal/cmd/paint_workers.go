package cmd

import (
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/pipeline"
)

// resolvePaintWorkers turns a configured paint-worker count into the value the
// pipeline is given.
//
// An explicit setting wins outright, including one that oversubscribes the machine:
// the operator knows their workload better than a division does, and the pipeline
// clamps to the size of the independent layer wave anyway. Zero — the default —
// divides the process-wide CPU budget over the tiles the caller has in flight, which
// on a saturated batch run or a busy tile server comes out as the serial pipeline.
func resolvePaintWorkers(key string, tilesInFlight int) int {
	if n := viper.GetInt(key); n > 0 {
		return n
	}

	return pipeline.AutoPaintWorkers(tilesInFlight)
}
