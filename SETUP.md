# Setup Guide

Complete setup instructions for WaterColorMap development environment.

## System Requirements

- **OS**: Ubuntu 24.04 LTS (or compatible Linux)
- **Go**: 1.25.0 or higher
- **RAM**: 2GB minimum (4GB recommended)
- **Disk**: 500MB for dependencies + space for tiles

## Installation Methods

### Method 1: Native Installation (Recommended for Development)

#### 1. Install System Dependencies

```bash
# Update package list
sudo apt update

# Install Mapnik and build tools
sudo apt install -y \
    libmapnik-dev \
    mapnik-utils \
    python3-mapnik \
    build-essential \
    pkg-config \
    git
```

#### 2. Verify Mapnik Installation

```bash
# Check Mapnik version (should be 3.1.0+)
mapnik-config --version

# Verify pkg-config can find Mapnik libraries
mapnik-config --libs
```

#### 3. Clone Repository

```bash
git clone https://github.com/cwbudde/WaterColorMap.git
cd watercolormap
```

#### 4. Build Application

```bash
# Using Justfile (recommended)
just build

# Or manually
CGO_ENABLED=1 go build -o bin/watercolormap ./cmd/watercolormap
```

#### 5. Verify Installation

```bash
# Check build succeeded
./watercolormap version

# Run tests
just test
```

### Method 2: Docker (Recommended for Production)

#### 1. Install Docker

```bash
# Docker
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh
```

#### 2. Clone Repository

```bash
git clone https://github.com/cwbudde/WaterColorMap.git
cd watercolormap
```

#### 3. Build Docker Image

```bash
# Using Justfile
just docker-build
```

#### 4. Run in Container

```bash
# Show help
docker run --rm watercolormap:latest --help

# Generate a tile
docker run --rm \
   -v "$PWD/config.yaml:/app/config.yaml:ro" \
   -v "$PWD/tiles:/app/tiles" \
   -v "$PWD/cache:/app/cache" \
   -v "$PWD/assets:/app/assets:ro" \
   -e WATERCOLORMAP_CONFIG=/app/config.yaml \
   watercolormap:latest generate --tile z13_x4297_y2754
```

## Development Setup

### Prerequisites for Development

```bash
# Install additional development tools
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### Configuration

1. Copy example configuration:

   ```bash
   cp config.example.yaml config.yaml
   ```

2. Edit `config.yaml` to customize settings (optional)

3. Create output directories:

   ```bash
   mkdir -p tiles cache testdata/output
   ```

4. Optional — download the ocean data:

   ```bash
   just fetch-water-polygons
   ```

   OSM contains no ocean polygons, so without this the open sea renders as land
   and coastal tiles come out inverted. The recipe downloads the processed water
   polygons from [osmdata.openstreetmap.de](https://osmdata.openstreetmap.de/data/water-polygons.html)
   into the gitignored `./data` directory (~1 GB; `just fetch-water-polygons-simplified`
   fetches only the ~120 MB low-zoom set) and builds the `.index` sidecars that
   let Mapnik do a bbox lookup instead of scanning the whole shapefile.

   Then uncomment the `ocean:` block in `config.yaml`. Ocean rendering stays off
   until it is configured.

5. Optional — download the low-zoom (z0-5) data:

   ```bash
   just fetch-natural-earth
   ```

   Below z6, OSM is both too detailed and missing the generalised coastlines a
   world view needs, and one z2 tile would ask Overpass for a quarter of the
   planet. The recipe downloads the generalised ocean, lakes and river datasets
   from [naturalearthdata.com](https://www.naturalearthdata.com/) into the same
   gitignored `./data` directory (~10 MB) and builds their `.index` sidecars.

   Then uncomment the `natural-earth:` block in `config.yaml`. Tiles at or below
   `max-zoom` then render entirely from these shapefiles and make no Overpass
   request at all; z6 and above are unaffected either way.

### Running Tests

```bash
# All tests (including integration tests)
just test

# Unit tests only
go test -short ./...

# With coverage
just test-coverage
open coverage.html  # View in browser
```

### Development Workflow

```bash
# Format code
just fmt

# Run linter
just lint

# Run all checks (fmt + lint + test)
just check

# Build binary
just build

# Clean artifacts
just clean
```

## Troubleshooting

### Issue: "mapnik-config not found"

**Solution**:

```bash
# Ensure libmapnik-dev is installed
sudo apt install libmapnik-dev

# Check if mapnik-config is in PATH
which mapnik-config

# If not found, it may be in /usr/bin
ls -la /usr/bin/mapnik-config
```

### Issue: CGO compilation errors

**Solution**:

```bash
# Ensure build-essential is installed
sudo apt install build-essential pkg-config

# Verify C++ compiler works
g++ --version

# Check Mapnik flags are correct
mapnik-config --cflags
mapnik-config --libs
```

### Issue: "undefined reference to mapnik::" during linking

**Solution**:

```bash
# This should be handled automatically by the CGO directives in mapnik.go
# If still failing, ensure libmapnik library is installed:
dpkg -l | grep libmapnik

# Should show libmapnik3.1 package
```

### Issue: Docker build fails

**Solution**:

```bash
# Clean Docker build cache
docker system prune -a

# Rebuild with no cache
docker build --no-cache -f docker/Dockerfile -t watercolormap:latest .
```

### Issue: "permission denied" when running Docker

**Solution**:

```bash
# Add user to docker group
sudo usermod -aG docker $USER

# Log out and back in, or run:
newgrp docker
```

## Platform-Specific Notes

### Ubuntu 22.04 LTS

Mapnik 3.0.x is available - should work but 3.1.x recommended:

```bash
sudo apt install libmapnik3.0 libmapnik-dev
```

### Arch Linux

```bash
sudo pacman -S mapnik
```

### macOS (via Homebrew)

```bash
brew install mapnik
```

Note: On macOS, you may need to set CGO flags manually:

```bash
export CGO_LDFLAGS="$(mapnik-config --libs)"
export CGO_CXXFLAGS="$(mapnik-config --cxxflags)"
go build ./...
```

## Next Steps

After successful installation:

1. Read [docs/1.1-data-fetching-interface.md](docs/1.1-data-fetching-interface.md)
2. Read [docs/1.4-mapnik-installation.md](docs/1.4-mapnik-installation.md)
3. Try generating a test tile:
   ```bash
   ./watercolormap generate --tile z13_x4297_y2754
   ```
4. Review [PLAN.md](PLAN.md) for project roadmap

## Getting Help

- Check [docs/](docs/) for detailed documentation
- Review [PLAN.md](PLAN.md) for implementation details
- Open an issue on GitHub for bugs or questions

## References

- [Mapnik Installation Guide](https://github.com/mapnik/mapnik/wiki/UbuntuInstallation)
- [Go CGO Documentation](https://pkg.go.dev/cmd/cgo)
- [Docker Documentation](https://docs.docker.com/)
