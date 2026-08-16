#!/usr/bin/env bash
#
# Compare the benchmarks of the working tree against another revision, running
# the two alternately so both see the same machine load, and summarise the
# result with benchstat.
#
# Interleaving is the whole point. docs/performance/pixel-access-optimization.md
# records what happens without it on a busy machine: an unloaded baseline
# against a loaded run showed a fictitious +1900% "regression". Running A, then
# B, then A, then B does not make a shared runner quiet, but it does stop a slow
# five minutes from landing entirely on one side of the comparison.
#
# Usage:  scripts/bench-compare.sh [base-revision]
#
# Environment:
#   BENCH_PKGS    space-separated package list (default: the Mapnik-free set)
#   BENCH_ROUNDS  interleaved rounds per side (default 6; benchstat wants >= 5)
#   BENCH_TIME    -benchtime value per round (default 300ms)
#   BENCH_FILTER  -bench regexp (default '.')
#   BENCH_OUT     directory for base.txt / head.txt (default a temp dir)

set -euo pipefail

base_rev="${1:-main}"

# Only packages that build without cgo and Mapnik. Adding internal/renderer or
# internal/server here would drag the Mapnik apt install into the CI job, which
# costs several minutes for benchmarks that are dominated by I/O anyway.
default_pkgs="./internal/watercolor ./internal/mask ./internal/texture ./internal/composite ./internal/tile ./internal/tileformat"
bench_pkgs="${BENCH_PKGS:-$default_pkgs}"
rounds="${BENCH_ROUNDS:-6}"
benchtime="${BENCH_TIME:-300ms}"
filter="${BENCH_FILTER:-.}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

if ! base_sha="$(git rev-parse --verify --quiet "${base_rev}^{commit}")"; then
	echo "bench-compare: cannot resolve revision '${base_rev}'" >&2
	exit 1
fi

head_sha="$(git rev-parse --verify HEAD)"
if [ "$base_sha" = "$head_sha" ]; then
	echo "bench-compare: '${base_rev}' is the current HEAD; nothing to compare" >&2
	exit 1
fi

out_dir="${BENCH_OUT:-$(mktemp -d -t wcm-bench-XXXXXX)}"
mkdir -p "$out_dir"
base_txt="$out_dir/base.txt"
head_txt="$out_dir/head.txt"
: >"$base_txt"
: >"$head_txt"

# A detached worktree rather than a checkout: the working tree stays untouched,
# so an interrupted run cannot leave the repository on the wrong revision.
work_dir="$(mktemp -d -t wcm-bench-base-XXXXXX)"
cleanup() {
	git worktree remove --force "$work_dir" >/dev/null 2>&1 || true
	rm -rf "$work_dir"
}
trap cleanup EXIT

git worktree add --detach --quiet "$work_dir" "$base_sha"

run_side() {
	local dir="$1" out="$2"
	(
		cd "$dir"
		# shellcheck disable=SC2086 # bench_pkgs is a deliberate word list
		go test -run '^$' -bench "$filter" -benchmem \
			-benchtime "$benchtime" -count 1 $bench_pkgs
	) >>"$out"
}

echo "bench-compare: base ${base_sha:0:12} vs head ${head_sha:0:12}"
echo "bench-compare: ${rounds} interleaved rounds, -benchtime ${benchtime}"

# Warm both build caches before the first measured round, so compilation time
# never lands inside a benchmark's own wall clock.
run_side "$work_dir" /dev/null
run_side "$repo_root" /dev/null

for round in $(seq 1 "$rounds"); do
	echo "bench-compare: round ${round}/${rounds}"
	run_side "$work_dir" "$base_txt"
	run_side "$repo_root" "$head_txt"
done

echo
echo "base: $base_txt"
echo "head: $head_txt"
echo

if command -v benchstat >/dev/null 2>&1; then
	benchstat "$base_txt" "$head_txt"
else
	# Pinned rather than @latest: an unpinned tool would silently change how
	# the comparison is reported between runs, which is the one thing a
	# regression report must not do.
	go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260813145340-fd4a688df892 \
		"$base_txt" "$head_txt"
fi
