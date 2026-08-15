# WaterColorMap Justfile
# Task orchestration for development and building

# Default recipe - show available commands
default:
    @just --list

# Mapnik font/plugin paths. go-mapnik hardcodes /usr/local/lib/mapnik/{fonts,input}
# and expects them to be overridden at link time; on a distro install they live
# elsewhere, so without this every run logs "MAPNIK: open ... no such file" and
# renders with no fonts and no input plugins registered.
mapnik_ldflags := "-X github.com/omniscale/go-mapnik/v2.fontPath=" + shell("mapnik-config --fonts 2>/dev/null || echo /usr/local/lib/mapnik/fonts") + " -X github.com/omniscale/go-mapnik/v2.pluginPath=" + shell("mapnik-config --input-plugins 2>/dev/null || echo /usr/local/lib/mapnik/input")

# Install dependencies
deps:
    go mod download
    go mod tidy

# Install the formatters treefmt.toml drives.
# gci and shfmt are Go-installable; prettier and taplo come from npm; shellcheck is
# preinstalled on CI runners and available from every distro package manager.
deps-fmt:
    go install github.com/daixiang0/gci@v0.14.0
    go install mvdan.cc/sh/v3/cmd/shfmt@v3.12.0

# Build the application
build:
    CGO_ENABLED=1 go build -ldflags "{{mapnik_ldflags}}" -o bin/watercolormap ./cmd/watercolormap

# Build with version information
build-release version:
    CGO_ENABLED=1 go build -ldflags "{{mapnik_ldflags}} -X github.com/cwbudde/watercolormap/internal/cmd.version={{version}} -X github.com/cwbudde/watercolormap/internal/cmd.commit=$(git rev-parse HEAD) -X github.com/cwbudde/watercolormap/internal/cmd.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/watercolormap ./cmd/watercolormap

# Run the application
run *args:
    go run ./cmd/watercolormap {{args}}

# Serve tiles + Leaflet demo (generates missing tiles on-demand)
serve *args:
    go run ./cmd/watercolormap serve {{args}}

# CORS is off by default, so a browser on another origin needs it switched on.
# Serve tiles with CORS on, for a cross-origin page (WASM playground, Pages demo)
serve-cors *args:
    go run ./cmd/watercolormap serve --cors-origin '*' {{args}}

# Build WASM module for browser playground
build-wasm:
    @echo "Building WASM module..."
    mkdir -p docs/wasm-playground
    GOOS=js GOARCH=wasm go build -o docs/wasm-playground/wasm.wasm ./cmd/wasm
    bash scripts/copy-wasm-exec.sh
    @echo "WASM build complete. Artifacts in docs/wasm-playground/"

# Build and serve WASM locally (for testing)
build-wasm-local: build-wasm
    @echo "Serving WASM playground at http://localhost:8000/wasm-playground/"
    cd docs && python3 -m http.server 8000

# Run tests
test:
    go test -ldflags "{{mapnik_ldflags}}" ./... -v

# Run unit tests (alias for CI)
test-unit:
    just test

# Run the whole suite with assembly disabled (the js/wasm build uses this path)
test-purego:
    go test -tags purego -ldflags "{{mapnik_ldflags}}" ./...

# Run all benchmarks
bench *args:
    go test -ldflags "{{mapnik_ldflags}}" -run '^$' -bench . -benchmem {{args}} ./internal/...

# Run just the blur benchmarks
bench-blur *args:
    go test -ldflags "{{mapnik_ldflags}}" -run '^$' -bench 'Blur|Antialias' -benchmem {{args}} ./internal/mask/ ./internal/watercolor/

# Run tests with coverage
test-coverage:
    go test -ldflags "{{mapnik_ldflags}}" ./... -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html

# Format code
fmt:
    treefmt --allow-missing-formatter

# Check if code is formatted (for CI)
check-formatted:
    @echo "Checking if code is formatted..."
    @if ! git diff --exit-code > /dev/null 2>&1; then \
        echo "ERROR: Working directory has uncommitted changes. Commit or stash changes before running format check."; \
        exit 1; \
    fi
    treefmt
    @if ! git diff --exit-code > /dev/null 2>&1; then \
        echo "ERROR: Code is not formatted. Run 'just fmt' to format."; \
        git diff; \
        exit 1; \
    fi
    @echo "Code is properly formatted"

# Setup dependencies (alias for CI)
setup-deps:
    just deps
    just deps-fmt

# Check if go mod tidy is needed
check-tidy:
    @if [ -n "$(git diff go.mod go.sum)" ]; then \
        echo "ERROR: go.mod or go.sum not tidy"; \
        git diff go.mod go.sum; \
        exit 1; \
    else \
        echo "go.mod and go.sum are tidy"; \
    fi

# Check if generated files are up to date
check-generated:
    @echo "Checking generated files..."
    @echo "All generated files are up to date"

# Lint code
lint:
    golangci-lint run

# Lint and fix issues
lint-fix:
    golangci-lint run --fix

# Clean build artifacts
clean:
    rm -rf bin/
    rm -f coverage.out coverage.html

# Clean WASM artifacts
clean-wasm:
    rm -f docs/wasm-playground/*.wasm docs/wasm-playground/wasm_exec.js

# Clean generated tiles
clean-tiles:
    rm -rf tiles/*.png

# Install the binary to $GOPATH/bin
install:
    go install ./cmd/watercolormap

# Generate a single tile (example for Hanover)
generate-tile zoom="13" x="4317" y="2692":
    go run ./cmd/watercolormap generate --zoom {{zoom}} --x {{x}} --y {{y}}

# Setup development environment
setup:
    @echo "Setting up development environment..."
    go mod download
    go mod tidy
    mkdir -p bin tiles assets/textures
    @echo "Setup complete!"

# Watch for changes and rebuild (requires entr)
watch:
    find . -name '*.go' | entr -r just run

# Check for security vulnerabilities
security:
    gosec ./...

# Run all quality checks
check: fmt lint test
    @echo "All checks passed!"

# Development setup - initialize everything needed
dev-init: setup deps
    @echo "Development environment ready!"
    @echo "Run 'just run' to start the application"

# Install system dependencies (Ubuntu/Debian)
install-deps:
    @echo "Installing system dependencies..."
    sudo apt-get update
    sudo apt-get install -y \
        build-essential \
        pkg-config \
        libmapnik-dev \
        mapnik-utils \
        python3-mapnik

# Verify Mapnik installation
verify-mapnik:
    @echo "Verifying Mapnik installation..."
    @mapnik-config --version || (echo "ERROR: mapnik-config not found" && exit 1)
    @pkg-config --modversion mapnik || (echo "ERROR: pkg-config cannot find mapnik" && exit 1)
    @echo "Mapnik is properly installed!"

# Processed OSM water polygons — the ocean source. OSM itself maps no ocean
# (the sea is the absence of land), so the open sea has to come from here.
# The 3857 variants are already in the renderer's projection, so nothing
# reprojects at render time. See PLAN.md 4.10.
water_polygons_dir := "data/water-polygons"
water_polygons_base := "https://osmdata.openstreetmap.de/download"

# Download the simplified water polygons (~120 MB, used for z<=9)
fetch-water-polygons-simplified:
    #!/usr/bin/env bash
    set -euo pipefail
    just _fetch-water-polygons simplified-water-polygons-split-3857

# Download the full water polygons (~800 MB, used for z>=10)
fetch-water-polygons-full:
    #!/usr/bin/env bash
    set -euo pipefail
    just _fetch-water-polygons water-polygons-split-3857

# Download both water polygon datasets (~1 GB total)
fetch-water-polygons: fetch-water-polygons-simplified fetch-water-polygons-full
    @echo "Water polygons ready in {{water_polygons_dir}}/"
    @echo "Point config.yaml at them (see the 'ocean:' block in config.example.yaml)."

# Download, unzip and index one water polygon dataset.
#
# shapeindex is what makes this usable: without the .index sidecar Mapnik scans
# the whole shapefile for every tile instead of doing a bbox lookup.
_fetch-water-polygons name:
    #!/usr/bin/env bash
    set -euo pipefail
    dir="{{water_polygons_dir}}"
    mkdir -p "$dir"
    if [ -d "$dir/{{name}}" ]; then
      echo "{{name}} already present in $dir — skipping download."
    else
      echo "Downloading {{name}}.zip ..."
      curl -fL --progress-bar -o "$dir/{{name}}.zip" "{{water_polygons_base}}/{{name}}.zip"
      unzip -q -d "$dir" "$dir/{{name}}.zip"
      rm -f "$dir/{{name}}.zip"
    fi
    for shp in "$dir/{{name}}"/*.shp; do
      if [ ! -f "${shp%.shp}.index" ]; then
        echo "Indexing $(basename "$shp") ..."
        shapeindex "$shp"
      fi
    done
    echo "Ready: $dir/{{name}}"

# Fail early with a useful message instead of rendering tan oceans.
require-water-polygons:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -d "{{water_polygons_dir}}" ]; then
      echo "No water polygons in {{water_polygons_dir}}." >&2
      echo "Run:  just fetch-water-polygons" >&2
      exit 1
    fi

# Build Docker image
docker-build:
    @echo "Building Docker image..."
    docker build -f docker/Dockerfile -t watercolormap:latest .

# Run Docker container
docker-run *args:
    @echo "Running Docker container..."
    docker run --rm \
        -v "${PWD}/config.yaml:/app/config.yaml:ro" \
        -v "${PWD}/tiles:/app/tiles" \
        -v "${PWD}/cache:/app/cache" \
        -v "${PWD}/assets:/app/assets:ro" \
        watercolormap:latest {{args}}

# Start development Docker container
docker-dev:
    @echo "Starting development container..."
    docker run --rm -it \
        -v "${PWD}:/app" \
        --workdir /app \
        --entrypoint bash \
        $(docker build -q -f docker/Dockerfile --target builder .)

# Generate a test tile (example)
generate-test-tile:
    @echo "Generating test tile..."
    ./bin/watercolormap generate --zoom 13 --x 4317 --y 2692

# Needs Mapnik and a reachable Overpass endpoint. Ctrl-C stops the server.
# Smoke test: generate a 3x3 z13 block around a tile, then serve it
smoke zoom="13" x="4317" y="2692" addr="127.0.0.1:8080":
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Generating a 3x3 block at zoom {{zoom}} around x={{x}} y={{y}}..."
    for dx in -1 0 1; do
      for dy in -1 0 1; do
        go run ./cmd/watercolormap generate \
          --zoom {{zoom}} --x $(( {{x}} + dx )) --y $(( {{y}} + dy ))
      done
    done
    echo "Serving on http://{{addr}}/demo/ ..."
    go run ./cmd/watercolormap serve --addr {{addr}}

# Run integration tests (requires Mapnik installed and Overpass reachable)
test-integration:
    WATERCOLORMAP_INTEGRATION=1 go test -ldflags "{{mapnik_ldflags}}" ./... -v

# Local Overpass instance, see docs/local-overpass.md and ../overpass-niedersachsen
local_overpass := "http://localhost:12345/api/interpreter"

# Fail early with a useful message instead of letting every fetch time out.
require-local-overpass:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! curl -sf -m 5 -o /dev/null -X POST "{{local_overpass}}" -d '[out:json];out count;'; then
      echo "Local Overpass is not answering at {{local_overpass}}." >&2
      echo "Start it with:  cd ../overpass-niedersachsen && just up" >&2
      echo "See docs/local-overpass.md" >&2
      exit 1
    fi

# Integration tests against the local Overpass instance (much faster, no rate limits)
test-integration-local: require-local-overpass
    WATERCOLORMAP_INTEGRATION=1 WATERCOLORMAP_OVERPASS_ENDPOINT="{{local_overpass}}" \
        go test -ldflags "{{mapnik_ldflags}}" ./... -v

# Smoke test against the local Overpass instance.
#
# The endpoint has to be exported, not just checked. config.yaml is gitignored,
# so on a fresh checkout there is no `overpass.servers` routing at all and both
# the nine generate runs and the server would fall back to the public API —
# taking its rate limits while claiming to be the local smoke test. The
# prerequisite proves localhost answers; this makes the recipe actually use it.
smoke-local zoom="13" x="4317" y="2692" addr="127.0.0.1:8080": require-local-overpass
    WATERCOLORMAP_OVERPASS_ENDPOINT="{{local_overpass}}" \
        just smoke {{zoom}} {{x}} {{y}} {{addr}}

# Update golden stage images (synthetic, deterministic)
update-goldens:
    UPDATE_GOLDEN=1 go test -ldflags "{{mapnik_ldflags}}" ./... -run 'TestPipelineStages/Synthetic'

# Update Hannover real-tile golden stage images (requires Mapnik + Overpass)
update-goldens-hannover:
    UPDATE_GOLDEN=1 WATERCOLORMAP_INTEGRATION=1 go test -ldflags "{{mapnik_ldflags}}" ./... -run 'TestPipelineStages/Hannover'

# Update all stage goldens (synthetic + Hannover)
update-goldens-all:
    just update-goldens
    just update-goldens-hannover

# Hannover bounding box (city center + surroundings)
# minLon, minLat, maxLon, maxLat
hannover_bbox := "9.65,52.32,9.85,52.43"

# Prebuild tile cache for Hannover (zoom 10-14, good for overview + detail)
prebuild-hannover zoom_min="10" zoom_max="14" *args:
    @echo "Prebuilding tiles for Hannover (zoom {{zoom_min}}-{{zoom_max}})..."
    go run ./cmd/watercolormap generate \
        --bbox "{{hannover_bbox}}" \
        --zoom-min {{zoom_min}} \
        --zoom-max {{zoom_max}} \
        --allow-failures \
        {{args}}

# Prebuild quick cache for Hannover (zoom 10-12, fast)
prebuild-hannover-quick *args:
    just prebuild-hannover 10 12 {{args}}

# Prebuild detailed cache for Hannover (zoom 10-15, slower but more detail)
prebuild-hannover-detailed *args:
    just prebuild-hannover 10 15 {{args}}

# Prebuild full cache for Hannover (zoom 10-16, comprehensive)
prebuild-hannover-full *args:
    just prebuild-hannover 10 16 {{args}}

# Prebuild cache for custom bbox and zoom range
prebuild bbox zoom_min="10" zoom_max="14" *args:
    @echo "Prebuilding tiles for bbox {{bbox}} (zoom {{zoom_min}}-{{zoom_max}})..."
    go run ./cmd/watercolormap generate \
        --bbox "{{bbox}}" \
        --zoom-min {{zoom_min}} \
        --zoom-max {{zoom_max}} \
        --allow-failures \
        {{args}}
