## Commands

```bash
# Run locally
go run ./cmd/geotileify

# Build binary
go build -o geotileify ./cmd/geotileify

# Tidy dependencies
go mod tidy

# Docker (full stack with tippecanoe + GDAL mounts)
docker-compose up
docker build -t geotileify .
```

No Makefile or test suite exists in this project.

## Architecture

Geotileify is a Go REST API that accepts geospatial file uploads, converts them to PMTiles format, stores them in MinIO, and auto-expires them after 24 hours.

**Request flow for `POST /generate`:**
1. Upload a supported geospatial file (≤50 MB)
2. If `.zip` (Shapefile bundle): validate safety, extract, convert `.shp` → GeoJSON via `ogr2ogr`
3. If `.parquet`: convert to GeoJSON via `duckdb` (not `ogr2ogr` — GDAL usually lacks Arrow driver)
4. Generate PMTiles via `tippecanoe` — output to system temp dir
5. Upload result to MinIO S3 bucket
6. Write metadata (ULID, file path, URL, expiry) to PostgreSQL `tiles` table
7. Background scheduler cleans up expired tiles every 15 minutes via Asynq + Redis

**Packages:**
- `cmd/geotileify/` — entry point; wires server, scheduler, and worker
- `internal/api/` — Echo HTTP server; middleware and route registration split into `registerMiddleware` / `registerRoutes`; each handler is a named method on `Server`
- `internal/storage/` — MinIO client wrapper
- `internal/tile/` — wrappers around `tippecanoe`, `ogr2ogr`, and `duckdb`; shapefile zip validation logic lives here
- `internal/tasks/` — Asynq task definition and cleanup handler (runs every 15 min, 10 workers)
- `utils/` — ULID generation

**Endpoints:**
- `GET /` — redirects to `/health`
- `GET /health` — service status
- `POST /generate` — upload + convert + tile generation
- `GET /download/:id` — presigned MinIO URL (triggers file download)
- `GET /tiles/:id` — presigned MinIO URL with range request support for PMTiles streaming

## Supported file formats

| Extension | Format | Conversion |
|-----------|--------|------------|
| `.geojson`, `.json` | GeoJSON | Direct to tippecanoe |
| `.geojsons`, `.geojsonl` | GeoJSON Sequences / Lines | Direct to tippecanoe |
| `.fgb` | FlatGeobuf | Direct to tippecanoe |
| `.parquet` | GeoParquet | DuckDB → GeoJSON → tippecanoe |
| `.zip` | Shapefile bundle | ogr2ogr → GeoJSON → tippecanoe |

**Shapefile zip validation** (in `internal/tile/tippecanoe.go`):
- Whitelist of allowed extensions inside zip (`.shp`, `.dbf`, `.shx`, `.prj`, `.cpg`, etc.) — rejects executables and unknown files
- Max 20 files, max 200 MB uncompressed (zip bomb protection)
- Zip-slip path traversal guard
- Requires `.shp` + `.dbf` + `.shx` to be present

## Environment

Copy `.env` and populate before running (`.env` is gitignored in production but committed with defaults here):

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

## Database

Single table `tiles` (see `migrations/001_create_tiles.sql`). `public_id` is a ULID (26-char). Index on `expires_at` for efficient cleanup queries.

## System dependencies

Four external binaries must be installed and on `PATH` (or bind-mounted in Docker):
- **`tippecanoe`** — converts GeoJSON/GeoJSONSeq/FlatGeobuf → PMTiles
- **`ogr2ogr`** (GDAL) — converts Shapefile → GeoJSON
- **`duckdb`** — converts GeoParquet → GeoJSON (preferred over ogr2ogr for Parquet because GDAL is typically not compiled with the Arrow driver)

DuckDB requires the `spatial` extension installed once:
```bash
duckdb -c "INSTALL spatial;"
```
