# Multi-stage build for WaterColorMap tile generator
#
# Both stages are pinned to the SAME digest on purpose: the runtime stage relies
# on having the same Mapnik build as the builder (see the ldflags note below), and
# a floating :24.04 tag does not guarantee that -- the two FROMs could resolve to
# different point releases in a single build.
# Refresh with: docker buildx imagetools inspect ubuntu:24.04
FROM ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea AS builder

# Prevent interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install system dependencies:
# - Build tools: build-essential, pkg-config, git, ca-certificates, curl
# - Mapnik and GIS: libmapnik-dev, mapnik-utils, python3-mapnik
# - Image processing: libpng-dev, libjpeg-dev, libfreetype6-dev
# DL3008 (pin apt versions) is ignored throughout: Ubuntu drops superseded
# versions from the archive, so a pinned `pkg=1.2.3` turns into a hard build
# failure the first time a security update lands. The digest-pinned base image
# is what gives this build its reproducibility floor.
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
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

# Install Go. The checksum is the one published on https://go.dev/dl/ for this
# exact filename -- update GO_VERSION and GO_SHA256 together or the build stops
# here rather than silently running whatever the download returned.
# amd64-only by design: the tarball name hardcodes the arch, and the Go build
# below is CGO/Mapnik-linked, so this image is not multi-arch anyway.
ARG GO_VERSION=1.25.0
ARG GO_SHA256=2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613
RUN curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" && \
	echo "${GO_SHA256}  /tmp/go.tar.gz" > /tmp/go.sha256 && \
	sha256sum -c /tmp/go.sha256 && \
	tar -C /usr/local -xzf /tmp/go.tar.gz && \
	rm /tmp/go.tar.gz /tmp/go.sha256
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

# Runtime stage - smaller image. Same digest as the builder, see the note above.
FROM ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea

ENV DEBIAN_FRONTEND=noninteractive

# Install only runtime dependencies (not dev packages)
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
	libmapnik3.1t64 \
	mapnik-utils \
	ca-certificates && \
	rm -rf /var/lib/apt/lists/*

# Create non-root user.
# ubuntu:24.04 ships a default `ubuntu` account that already holds UID 1000, so a
# bare `useradd -u 1000` fails with exit 4 ("UID already in use") and the image
# does not build at all. Free the UID first. It has to stay 1000 specifically:
# `just docker-run` bind-mounts host tiles/ and cache/, which are owned by the
# host's first user, and any other UID makes those mounts unwritable.
RUN if id -u 1000 >/dev/null 2>&1; then \
	userdel -r "$(id -nu 1000)"; \
	fi && \
	useradd -m -u 1000 mapuser

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
