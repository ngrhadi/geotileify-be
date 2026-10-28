# Geotileify

A Go REST API that accepts geospatial file uploads, converts them to [PMTiles](https://protomaps.com/docs/pmtiles) using [tylertoo](https://geoparquet-io.github.io/tylertoo/), stores them in MinIO, and auto-expires them after 24 hours.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Chrome Web Store](https://img.shields.io/badge/Chrome%20Extension-Install-blue?logo=googlechrome&logoColor=white)](https://chromewebstore.google.com/detail/geotileify/ljoijfpcddgiadohecobajkkbbemmaio)

→ [Contributing Guide](CONTRIBUTING.md) · [Code of Conduct](CODE_OF_CONDUCT.md) · [Agent Guide](AGENT.md) · [Privacy Policy](https://geotileify.idn-guessr.com/privacy)

---

## Commands

```bash
# Run locally
go run ./cmd/geotileify

# Build binary
go build -o geotileify ./cmd/geotileify

# Tidy dependencies
go mod tidy

# Integration tests (skipped automatically if duckdb/tylertoo are missing)
go test ./...

# Docker
docker compose up --build
docker build -t geotileify .
```

## Architecture

Geotileify accepts geospatial uploads, normalises every input to **GeoParquet**
with DuckDB Spatial, then generates PMTiles with **tylertoo**. There is no fallback tiler.

**Request flow for `POST /generate`:**

1. Upload a supported geospatial file (≤ 50 MB).
2. Normalise to GeoParquet:
   - `.geojson`, `.json`, `.geojsons`, `.geojsonl`, `.fgb` → `duckdb ST_Read(...)` → GeoParquet.
   - `.zip` Shapefile bundle → validate + extract → `duckdb ST_Read(.shp)` → GeoParquet.
   - `.parquet` GeoParquet → used directly when it is WGS84; otherwise reprojected to WGS84 by DuckDB.
3. Generate PMTiles with `tylertoo` (`--min-zoom`, `--max-zoom`,
   `--simplify-factor`, and property include/exclude flags). The popup sends the
   selected values.
4. Inject `name`, `description`, and `attribution` into the PMTiles JSON
   metadata (tylertoo 0.8.0 does not expose those flags on `tiles`).
5. Upload the archive to MinIO as `tiles/<ULID>.pmtiles`.
6. Write metadata (ULID, object path, URL, expiry) to the PostgreSQL `tiles` table.
7. A background scheduler cleans up expired tiles every 15 minutes via Asynq + Redis.

**Packages:**

- `cmd/geotileify/` — entry point; wires server, scheduler, and worker
- `internal/api/` — Echo HTTP server; middleware and routes split into `registerMiddleware` / `registerRoutes`; each handler is a named method on `Server`
- `internal/storage/` — MinIO client wrapper
- `internal/tile/` — DuckDB/tylertoo wrappers, CRS normalisation, and shapefile zip safety
- `internal/tasks/` — Asynq cleanup task (every 15 min, 10 workers)
- `utils/` — ULID generation

**Endpoints:**

- `GET /` — serves the embedded landing page (`web/static/index.html`)
- `GET /privacy` — serves the embedded privacy policy (`web/static/privacy.html`)
- `GET /health` — service status
- `POST /generate` — upload + normalise + tile generation
- `GET /download/:id` — presigned MinIO URL (triggers file download)
- `GET /tiles/:id` — presigned MinIO URL with range request support for PMTiles streaming
- `GET /tiles/:id/proxy` — CORS-safe range proxy used by the extension map preview (auto-displays generated tiles)

## Supported file formats

| Extension | Format | Conversion |
|---|---|---|
| `.geojson`, `.json` | GeoJSON | DuckDB `ST_Read` → GeoParquet → tylertoo |
| `.geojsons`, `.geojsonl` | GeoJSON Sequences / Lines | DuckDB `ST_Read` → GeoParquet → tylertoo |
| `.fgb` | FlatGeobuf | DuckDB `ST_Read` → GeoParquet → tylertoo |
| `.parquet` | GeoParquet | Direct when WGS84, otherwise DuckDB reproject → GeoParquet → tylertoo |
| `.zip` | Shapefile bundle | Validate + extract → DuckDB `ST_Read(.shp)` → GeoParquet → tylertoo |

**Shapefile zip validation** (in `internal/tile/shapefile.go`):

- whitelist of allowed extensions (`.shp`, `.dbf`, `.shx`, `.prj`, `.cpg`, indexes, `.xml`) — rejects executables and unknown files
- max 20 files, max 200 MB uncompressed (zip-bomb protection)
- zip-slip path traversal guard
- requires `.shp` + `.dbf` + `.shx`

## CRS and tiling behaviour

- Tylertoo consumes GeoParquet in WGS84 or Web Mercator.
- Every non-WGS84 input is reprojected to **EPSG:4326** before tiling.
- If a file has no declared CRS and its coordinates look projected, the API
  returns **400** instead of tiling misplaced data. Include a `.prj`/CRS or
  reproject the source.
- `min_zoom` / `max_zoom` control the generated zoom range:
  - defaults: **0** and **14**
  - allowed: **0–16**, with `min_zoom <= max_zoom`
  - the popup extension sends both fields with every upload
- `simplify_factor` controls tiling simplification: default **1.0**; the popup
  offers 0.1, 0.5, 1.0, 2.0, and 4.0.
- `exclude_all_properties=false` keeps every property by default. Set it to
  `true` for geometry-only tiles, or pass `include_properties` (comma-separated)
  to keep only those fields (include overrides exclude-all).
- `name`, `description`, and `attribution` are merged into the PMTiles JSON
  metadata after tiling by the backend (`SetPMTilesMetadata`), because the
  installed tylertoo 0.8.0 CLI does not expose those archive metadata flags.
- Tylertoo writes PMTiles v3 with `vector_layers` metadata, ready for MapLibre/Leaflet.

### `POST /generate` form fields

| Field | Required | Default | Notes |
|---|---|---|---|
| `file` | yes | — | Geospatial upload, max 50 MB |
| `min_zoom` | no | `0` | Integer 0–16 |
| `max_zoom` | no | `14` | Integer 0–16, `>= min_zoom` |
| `simplify_factor` | no | `1.0` | Positive number 0.1–10 (popup: 0.1–4) |
| `exclude_all_properties` | no | `false` | `true` writes geometry-only tiles |
| `include_properties` | no | — | Comma-separated fields; overrides exclude-all |
| `name` | no | — | PMTiles metadata name (null if empty) |
| `description` | no | — | PMTiles metadata description |
| `attribution` | no | — | PMTiles metadata attribution |

## Environment

Copy `.env.example` and populate before running:

```
PORT=9090
PUBLIC_BASE_URL=http://localhost:9090

MINIO_ENDPOINT=...
MINIO_ACCESS_KEY=...
MINIO_SECRET_KEY=...
MINIO_BUCKET=tiles

DB_URL=postgres://user:pass@localhost:5432/geotileify?sslmode=disable
REDIS_URL=redis://localhost:6379
```

Optional binary overrides: `DUCKDB_BIN` and `TYLERTOO_BIN` (defaults: `duckdb`,
`tylertoo`).

## Database

Single table `tiles` (see `migrations/001_create_tiles.sql`). `public_id` is a
ULID (26 chars). Index on `expires_at` for efficient cleanup queries. New objects
use `tiles/<public_id>.pmtiles`, so uploads with the same filename do not
overwrite each other.

## System dependencies

Two external binaries must be installed and on `PATH` (both are installed in the
Docker image):

- **`duckdb`** — reads GeoJSON/FlatGeobuf/Shapefile and writes GeoParquet;
  also reprojects non-WGS84 GeoParquet.
- **`tylertoo`** — converts GeoParquet to PMTiles.

DuckDB needs the spatial extension once:

```bash
duckdb -c "INSTALL spatial;"
```

The server calls `tile.CheckDependencies()` at startup and exits with a clear
error if either binary is missing.

### Docker platform

The provided image is **linux/amd64** because DuckDB's pinned CLI and tylertoo
publish x86_64 Linux binaries. Run it on a Linux x86_64 VPS (Apple Silicon can
use Docker emulation for local testing).
