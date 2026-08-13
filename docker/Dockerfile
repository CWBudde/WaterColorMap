# Multi-stage build for WaterColorMap tile generator
FROM ubuntu:24.04 AS builder

# Prevent interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install system dependencies:
# - Build tools: build-essential, pkg-config, git, ca-certificates, curl
# - Mapnik and GIS: libmapnik-dev, mapnik-utils, python3-mapnik
# - Image processing: libpng-dev, libjpeg-dev, libfreetype6-dev
RUN apt-get update && apt-get install -y \
	build-essential \
	pkg-config \
	git \
	ca-certificates \
	curl \
	libmapnik-dev \
	mapnik-utils \
	python3-mapnik \
	libpng-dev \
	libjpeg-dev \
	libfreetype6-dev && \
	rm -rf /var/lib/apt/lists/*

# Install Go 1.25
RUN curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application.
# go-mapnik hardcodes /usr/local/lib/mapnik/{fonts,input} and expects a link-time
# override; on this image Mapnik lives elsewhere, so without these flags nothing is
# registered and rendering runs with no fonts and no input plugins. The runtime
# stage is the same Ubuntu release with the same Mapnik, so the builder's paths hold.
RUN CGO_ENABLED=1 GOOS=linux go build \
	-ldflags "-X github.com/omniscale/go-mapnik/v2.fontPath=$(mapnik-config --fonts) -X github.com/omniscale/go-mapnik/v2.pluginPath=$(mapnik-config --input-plugins)" \
	-o watercolormap ./cmd/watercolormap

# Runtime stage - smaller image
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Install only runtime dependencies (not dev packages)
RUN apt-get update && apt-get install -y \
	libmapnik3.1t64 \
	mapnik-utils \
	ca-certificates && \
	rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -m -u 1000 mapuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/watercolormap /app/watercolormap

# Copy assets and configuration
COPY --from=builder /app/assets /app/assets
COPY --from=builder /app/config.example.yaml /app/config.yaml

# Create directories for tiles and cache
RUN mkdir -p /app/tiles /app/cache && \
	chown -R mapuser:mapuser /app

USER mapuser

# Expose port for tile server (if we add HTTP server later)
EXPOSE 8080

# Default command
ENTRYPOINT ["/app/watercolormap"]
CMD ["--help"]
