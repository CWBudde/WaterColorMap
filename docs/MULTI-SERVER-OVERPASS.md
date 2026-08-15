# Multi-Server Overpass API Configuration

WaterColorMap supports using multiple Overpass API servers with automatic geographic routing. This is useful when you have a local Overpass instance for a specific region and want to fall back to the public API for the rest of the world.

## Features

- **Geographic routing**: Automatically routes tile requests to the appropriate server based on tile location
- **Fast local queries**: Use a local Overpass instance for your region
- **Automatic fallback**: Falls back to public API for tiles outside configured regions
- **Zero code changes**: Drop-in replacement for the single-server configuration

## How It Works

The system checks tile coordinates against configured coverage areas in order and uses the first matching server. Coverage areas are defined as bounding boxes (latitude/longitude).

```
Tile Request → Check Coverage Areas → Route to Matching Server
                                          ↓ on failure
                                      Next Matching Server
```

### Failover

When a matching server fails, the **next** matching server is tried. Before this,
the first coverage match was terminal: one restarting local container failed
every tile inside its coverage box without ever trying the public fallback,
which is fatal to a long bulk run.

Two failures are deliberately **not** retried elsewhere, because another server
cannot help:

| Failure                                       | Retried elsewhere? | Why                                                      |
| --------------------------------------------- | ------------------ | -------------------------------------------------------- |
| Connection refused, 5xx, 429, HTML error page | ✅                 | A healthy second server answers it                       |
| Empty response at z8–13                       | ✅                 | This is what a silent upstream failure looks like        |
| Context cancelled / deadline exceeded         | ❌                 | The caller is gone; another attempt only delays shutdown |
| Response over the size cap                    | ❌                 | A property of the data and the cap, not of the server    |

Every failover is logged at `warn` with the failing server, the tile and the
error, so a run that degrades to the public API looks like a broken server
rather than an unexplained slowdown. If every candidate fails, the returned error
names them all.

## Configuration

### Basic Example

Create a `config.yaml` file (or copy from `config.multi-overpass.example.yaml`):

```yaml
data-source: "overpass"

overpass:
  servers:
    # Local Niedersachsen instance (fast, covers Lower Saxony, Germany)
    - name: "Niedersachsen"
      endpoint: "http://localhost:12345/api/interpreter"
      workers: 10 # Higher parallelism for local instance
      coverage:
        min_lat: 51.3 # Southern edge
        max_lat: 53.9 # Northern edge
        min_lon: 6.6 # Western edge
        max_lon: 11.6 # Eastern edge

    # Public fallback (covers everything else)
    - name: "Public"
      endpoint: "https://overpass-api.de/api/interpreter"
      workers: 2 # Conservative for public API
      # No coverage = matches everything
```

### Configuration Fields

#### Server Configuration

- **`name`**: Human-readable name for logging (e.g., "Niedersachsen", "Berlin", "Public")
- **`endpoint`**: Overpass API URL (e.g., `http://localhost:12345/api/interpreter`)
- **`workers`**: Number of parallel requests (10+ for local, 2-4 for public)
- **`coverage`** (optional): Geographic bounding box this server covers
  - `min_lat`: Southern edge in degrees
  - `max_lat`: Northern edge in degrees
  - `min_lon`: Western edge in degrees
  - `max_lon`: Eastern edge in degrees

#### Coverage Notes

- Servers are checked **in order**
- The first matching coverage area is tried first; on failure the next match is tried
- `nil` coverage (omit the field) = matches everything (use for fallback)
- Coverage areas can overlap
- Always include at least one server with no coverage as a fallback, **last**
- A coverage box fully inside an earlier server's box can never be selected
  first. That is almost always a mistake, so startup logs a warning naming both
  servers; put the most specific box first.

## Example Configurations

### Single Region with Fallback

```yaml
overpass:
  servers:
    # Local Hannover instance
    - name: "Hannover"
      endpoint: "http://localhost:12345/api/interpreter"
      workers: 10
      coverage:
        min_lat: 52.0
        max_lat: 53.0
        min_lon: 9.0
        max_lon: 10.5

    # Public fallback
    - name: "Public"
      endpoint: "https://overpass-api.de/api/interpreter"
      workers: 2
```

### Multiple Regions

```yaml
overpass:
  servers:
    # Berlin instance
    - name: "Berlin"
      endpoint: "http://berlin-overpass:12345/api/interpreter"
      workers: 10
      coverage:
        min_lat: 52.3
        max_lat: 52.7
        min_lon: 13.0
        max_lon: 13.8

    # Bayern instance
    - name: "Bayern"
      endpoint: "http://bayern-overpass:12345/api/interpreter"
      workers: 10
      coverage:
        min_lat: 47.3
        max_lat: 50.6
        min_lon: 8.9
        max_lon: 13.9

    # Public fallback
    - name: "Public"
      endpoint: "https://overpass-api.de/api/interpreter"
      workers: 2
```

### High-Performance Setup

```yaml
overpass:
  servers:
    # Local instance with aggressive parallelism
    - name: "Local"
      endpoint: "http://localhost:12345/api/interpreter"
      workers: 20 # Local can handle more
      coverage:
        min_lat: 50.0
        max_lat: 55.0
        min_lon: 6.0
        max_lon: 15.0

    # Alternative public instance as fallback
    - name: "Kumi"
      endpoint: "https://overpass.kumi.systems/api/interpreter"
      workers: 4
```

## Running the Server

Once configured, simply start the server:

```bash
# Using config.yaml in current directory
watercolormap serve --config config.yaml

# Or specify config file
watercolormap serve --config /path/to/config.yaml
```

The server will log which Overpass servers are configured:

```
INFO Configured regional Overpass server name=Niedersachsen endpoint=http://localhost:12345/api/interpreter workers=10 coverage=51.30,6.60 to 53.90,11.60
INFO Configured fallback Overpass server name=Public endpoint=https://overpass-api.de/api/interpreter workers=2
```

## Setting Up a Local Overpass Instance

### Docker (Recommended)

```bash
# Pull official Overpass image
docker pull wiktorn/overpass-api

# Run with region extract (e.g., Niedersachsen)
docker run -d \
  -p 12345:80 \
  -v /data/overpass:/db \
  -e OVERPASS_PLANET_URL=https://download.geofabrik.de/europe/germany/niedersachsen-latest.osm.bz2 \
  wiktorn/overpass-api
```

### Manual Installation

1. Download region extract from [Geofabrik](https://download.geofabrik.de/)
2. Install Overpass API (see [official docs](https://wiki.openstreetmap.org/wiki/Overpass_API/Installation))
3. Initialize database with region extract
4. Start Overpass server on desired port

## Performance Tuning

### Local Instance

- **Workers**: 10-20 (limited by CPU, not rate limits)
- **Timeout**: Can be lower (5-10s) since network is fast
- **Retries**: Fewer needed for reliable local network

### Public API

- **Workers**: 2-4 (respect rate limits)
- **Timeout**: 30-60s (allow for queue time)
- **Retries**: More aggressive (network/rate limit issues)

## Troubleshooting

### "No overpass server configured for tile"

- **Cause**: Tile doesn't match any coverage area
- **Fix**: Add a fallback server with no coverage field

### Tiles outside region are slow

- **Cause**: Fallback server is overloaded or rate-limited
- **Fix**: Add more regional servers or use alternative public instances

### Local server connection refused

- **Cause**: Overpass instance not running
- **Check**: `curl http://localhost:12345/api/status`
- **Fix**: Start your local Overpass instance

### Wrong server being used

- **Cause**: Coverage areas checked in order, earlier match tried first
- **Debug**: Check server logs for "Configured ... Overpass server" messages, and
  for the startup warning "coverage is shadowed by an earlier server"
- **Fix**: Reorder servers or adjust coverage boundaries

### Run is unexpectedly slow, or hitting public rate limits

- **Cause**: The regional server is failing and every tile is falling over to the
  public fallback
- **Debug**: Look for `Overpass server failed; trying the next one` at `warn`, and
  `Overpass request succeeded on a fallback server` at `info`
- **Fix**: Repair the regional server. Failover keeps the run alive, but it does
  not make it fast

## Geographic Coverage Examples

### Germany Regions

```yaml
# Niedersachsen (Lower Saxony)
coverage: {min_lat: 51.3, max_lat: 53.9, min_lon: 6.6, max_lon: 11.6}

# Bayern (Bavaria)
coverage: {min_lat: 47.3, max_lat: 50.6, min_lon: 8.9, max_lon: 13.9}

# Berlin
coverage: {min_lat: 52.3, max_lat: 52.7, min_lon: 13.0, max_lon: 13.8}

# Hamburg
coverage: {min_lat: 53.4, max_lat: 53.7, min_lon: 9.7, max_lon: 10.3}
```

### Other Regions

```yaml
# Greater London
coverage: {min_lat: 51.3, max_lat: 51.7, min_lon: -0.5, max_lon: 0.3}

# San Francisco Bay Area
coverage: {min_lat: 37.2, max_lat: 38.0, min_lon: -122.6, max_lon: -121.8}

# Tokyo Metropolitan Area
coverage: {min_lat: 35.5, max_lat: 35.9, min_lon: 139.4, max_lon: 140.0}
```

## Migration from Single-Server

The new multi-server configuration is **backward compatible**. Existing single-server configs still work:

```yaml
# Old format (still works)
overpass:
  endpoint: "https://overpass-api.de/api/interpreter"
```

To migrate:

1. Move your endpoint to the servers array
2. Add coverage areas for regional instances
3. Keep public API as fallback

## Advanced Usage

### Custom Retry Configuration

```yaml
overpass:
  servers:
    - name: "Local"
      endpoint: "http://localhost:12345/api/interpreter"
      workers: 10
      # Retry config handled automatically per server
      coverage:
        min_lat: 51.0
        max_lat: 54.0
        min_lon: 6.0
        max_lon: 12.0
```

### Monitoring

The server logs every tile request with which Overpass server was selected:

```
DEBUG Fetching tile data tile=z13_x4299_y2740 server=Niedersachsen
DEBUG Fetching tile data tile=z10_x524_y340 server=Public
```

## See Also

- [Overpass API Documentation](https://wiki.openstreetmap.org/wiki/Overpass_API)
- [Geofabrik Downloads](https://download.geofabrik.de/) (regional OSM extracts)
- [WaterColorMap Configuration Guide](../README.md)
