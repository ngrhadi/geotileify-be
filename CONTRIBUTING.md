# Contributing to Geotileify

Thank you for your interest in contributing! This guide covers everything you need to get the project running locally and submit your changes.

## Prerequisites

Install these tools before starting:

| Tool | Purpose | Install |
|------|---------|---------|
| Go 1.24+ | Runtime | https://go.dev/dl |
| Docker & Docker Compose | Local stack | https://docs.docker.com/get-docker |
| tippecanoe | GeoJSON → PMTiles | `brew install tippecanoe` (macOS) |
| GDAL (`ogr2ogr`) | Shapefile → GeoJSON | `brew install gdal` (macOS) |
| duckdb | GeoParquet → GeoJSON | `brew install duckdb` (macOS) |

After installing duckdb, install the spatial extension once:

```bash
duckdb -c "INSTALL spatial;"
```

## Local Setup

```bash
# 1. Clone the repo
git clone https://github.com/ngrhadi/geotileify-be.git
cd geotileify-be

# 2. Copy environment file
cp .env.example .env
# Edit .env and fill in your values

# 3. Start dependencies (PostgreSQL, Redis, MinIO)
docker-compose up -d

# 4. Run database migration
psql $DB_URL -f migrations/001_create_tiles.sql

# 5. Run the server
go run ./cmd/geotileify
```

The API will be available at `http://localhost:9090`.

## Project Structure

```
cmd/geotileify/     Entry point — wires server, scheduler, and worker
internal/api/       Echo HTTP handlers and middleware
internal/storage/   MinIO client wrapper
internal/tile/      tippecanoe, ogr2ogr, and duckdb wrappers
internal/tasks/     Asynq background job for cleanup
migrations/         SQL migration files
utils/              ULID generation helper
```

## Making Changes

### Branching Convention

Branch off from `master` using one of these prefixes:

| Prefix | Use for |
|--------|---------|
| `feat/` | New features |
| `fix/` | Bug fixes |
| `docs/` | Documentation only |
| `refactor/` | Code refactoring |
| `chore/` | Maintenance tasks |

```bash
git checkout master
git pull origin master
git checkout -b feat/your-feature-name
```

### Code Style

Run `gofmt` before committing:

```bash
gofmt -w .
```

No test suite exists yet — manual testing via the API endpoints is the current approach.

## Submitting a Pull Request

1. Push your branch to GitHub:
   ```bash
   git push origin feat/your-feature-name
   ```
2. Open a Pull Request against `master` on GitHub.
3. Fill in a clear description of **what** changed and **why**.
4. Wait for review — a maintainer will respond within a few days.

## Adding a New File Format

The conversion pipeline lives in `internal/tile/tippecanoe.go`. To support a new format:

1. Add the file extension to the format detection logic.
2. Implement the conversion function (follow the pattern of existing ones like `convertParquet` or `convertShapefile`).
3. Call the new function from the main `Generate` handler in `internal/api/server.go`.
4. Update the supported formats table in `README.md`.

## Adding a Database Migration

Create a new numbered SQL file under `migrations/`:

```
migrations/002_your_migration_name.sql
```

Migrations are applied manually with `psql`. Keep them idempotent using `IF NOT EXISTS` / `IF EXISTS` where possible.

## Questions

Open a [GitHub Issue](https://github.com/ngrhadi/geotileify-be/issues) for bugs, feature requests, or questions.
