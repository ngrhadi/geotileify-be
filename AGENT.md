# AGENT.md

Operating guide for coding agents working on **Geotileify** — a Go API that turns
geospatial uploads into streaming PMTiles, plus an embedded static landing page.

Keep this file current when you change architecture, routes, env vars, or the
landing page.

---

## 1. What this project is

Geotileify accepts one geospatial file (≤ 50 MB), converts it to a single
`.pmtiles` archive, uploads it to S3-compatible storage (MinIO), and returns a
public ID with a 24-hour TTL. Clients stream the archive through HTTP range
requests (MapLibre / Leaflet / `pmtiles`).

The landing page in `web/` is embedded into the Go binary and served by the same
server.

**Stack:** Go 1.24 · Echo v4 · PostgreSQL (`lib/pq`) · Redis + Asynq · MinIO ·
Tylertoo · DuckDB Spatial.

---

## 2. Quick start

```bash
# 1. Install system dependencies (must be on PATH)
#    duckdb + spatial extension, tylertoo
#    Both are installed in the Docker image.
duckdb -c "INSTALL spatial;"
tylertoo --version

# 2. Configure environment
cp .env.example .env        # then fill in DB / MinIO / Redis values

# 3. Create the database table (no migration runner exists)
psql "$DB_URL" -f migrations/001_create_tiles.sql

# 4. Run
go run ./cmd/geotileify

# Build a binary
go build -o geotileify ./cmd/geotileify

# Docker (see Dockerfile + docker-compose.yml)
docker compose up --build
```

The server listens on `PORT` (default `8080`; `.env.example` uses `9090`).

### Checks / integration tests

There is no Makefile, but `internal/tile` has integration tests. They skip
automatically when `duckdb` or `tylertoo` is not installed.

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...                       # conversion + tiling integration tests
node --check web/static/js/app.js   # static JS syntax check
```

---

## 3. Repository map

```
cmd/geotileify/main.go        Entry point: env, DB pool, MinIO client, dependency check
internal/api/server.go        Echo server, middleware, routes, all HTTP handlers
internal/storage/minio.go     MinIO wrapper (upload, delete, bucket check, presign)
internal/tile/tylertoo.go     DuckDB normalisation + tylertoo PMTiles runner
internal/tile/shapefile.go    Shapefile ZIP validation/extraction + GeoParquet conversion
internal/tile/tylertoo_test.go Integration tests for every upload format
internal/tasks/jobs.go        Asynq cleanup task (runs every 15 minutes)
utils/generate-ulid.go        Monotonic ULID generator (26 chars)

web/embed.go                  //go:embed static — embeds the landing page
web/static/index.html         Landing page (no template engine)
web/static/css/style.css      All page styles + design tokens
web/static/js/app.js          Health probe, nav, scroll-spy, hero canvas, reveal
web/static/icons/*.svg        Lucide icons + favicon, served as separate files

migrations/001_create_tiles.sql   `tiles` table + expires_at index
Dockerfile                        Go + DuckDB + Tylertoo image
.env.example                      Local configuration template
docker-compose.yml                Dev stack (self-contained toolchain in image)
DESIGN.md                         Design-language analysis / reference
scrollcraft/                      Scroll-design skill assets (not compiled)
```

The Go module path is `github.com/ngrhadi/geotileify-be`.

---

## 4. Request flow — `POST /generate`

1. Check that the MinIO bucket exists.
2. Read `multipart/form-data` field **`file`**; reject > 50 MB.
3. Validate the extension and parse tiling options: `min_zoom` (default 0),
   `max_zoom` (default 14), `simplify_factor` (default 1.0), `exclude_all_properties`
   (default false), `include_properties` (comma-separated), and the optional
   `name`, `description`, and `attribution` metadata fields.
4. Create an isolated work directory and write the upload there.
5. Normalise to WGS84 GeoParquet:
   - `.zip` → validate safety, extract, `duckdb ST_Read(.shp)` → GeoParquet.
   - `.parquet` → pass through when already WGS84; otherwise `duckdb ST_Transform` → GeoParquet.
   - everything else → `duckdb ST_Read(...)` → GeoParquet.
6. `tylertoo <input.parquet> <out.pmtiles> --min-zoom <min> --max-zoom <max> \
   --simplify-factor <f> [--include-property <name> ... | --exclude-all-properties] \
   --layer-name <safe-name> --tile-buffer 5 --force`.
7. Merge `name`/`description`/`attribution` into the PMTiles JSON metadata.
8. Upload the `.pmtiles` object to MinIO as `tiles/<public-id>.pmtiles`.
9. Insert a row into `tiles` with a new ULID and `expires_at = NOW() + 24h`.
10. Remove the work directory and return JSON.
11. A background Asynq job sweeps expired rows/files every 15 minutes.

No fallback tiler exists. If DuckDB or tylertoo fails, the request fails
with the tool's error in the JSON response and in the server log.

Tylertoo 0.8.0 does not expose `--name`, `--description`, or `--attribution`
on the `tiles` command. The backend injects those fields into the PMTiles JSON
metadata after tiling (`internal/tile/pmtiles_metadata.go`).

---

## 5. HTTP API

| Method | Path            | Purpose |
| ------ | --------------- | ------- |
| GET    | `/`             | Embedded landing page (`no-store`). |
| GET    | `/static/*`     | Embedded CSS/JS/icons. Long cache for CSS/JS. |
| GET    | `/health`       | Liveness probe: `{"status":"ok", ...}`. |
| POST   | `/generate`     | Upload + convert + store. Fields: `file`, `min_zoom`, `max_zoom`, `simplify_factor`, `exclude_all_properties`, `include_properties`, `name`, `description`, `attribution`. |
| GET    | `/download/:id` | 302 to a 5-minute presigned URL (`attachment`). |
| GET    | `/tiles/:id`    | 302 to a 5-minute presigned URL (range-capable). |
| GET    | `/tiles/:id/proxy` | CORS-safe byte-range proxy for the extension preview. |

```bash
# Upload
curl -F "file=@buildings.geojson" -F "min_zoom=0" -F "max_zoom=14" \
  -F "simplify_factor=1.0" -F "exclude_all_properties=false" \
  -F "name=Jakarta buildings" -F "attribution=© GeoTeam" \
  http://localhost:9090/generate

# Response
{
  "id": "01J...",
  "download_url": "http://localhost:9090/download/01J...",
  "expires_at": "2026-05-17T10:25:00Z",
  "min_zoom": 0,
  "max_zoom": 14
}

# Stream (follow the redirect with the PMTiles client of your choice)
curl -I http://localhost:9090/tiles/01J...
```

Expired IDs return **403**; unknown IDs return **404**. `download_url` is built
from `PUBLIC_BASE_URL` — if it is empty the value becomes a relative path.

---

## 6. Supported input formats

| Extension | Format | Pipeline |
| --------- | ------ | -------- |
| `.geojson` | GeoJSON | `ST_Read` → GeoParquet → tylertoo |
| `.json` | GeoJSON (alt extension) | `ST_Read` → GeoParquet → tylertoo |
| `.geojsons` | GeoJSON Sequence | `ST_Read` → GeoParquet → tylertoo |
| `.geojsonl` | GeoJSON Lines | `ST_Read` → GeoParquet → tylertoo |
| `.fgb` | FlatGeobuf | `ST_Read` → GeoParquet → tylertoo |
| `.parquet` | GeoParquet | direct if WGS84; otherwise `ST_Transform` → GeoParquet → tylertoo |
| `.zip` | Shapefile bundle | validate/extract → `ST_Read(.shp)` → GeoParquet → tylertoo |

All non-WGS84 inputs are reprojected to EPSG:4326 before tiling. If the CRS is
missing and the coordinates look projected, conversion fails with HTTP 400.

Shapefile ZIP guardrails in `internal/tile/shapefile.go`:

- extension allowlist (`.shp`, `.dbf`, `.shx`, `.prj`, `.cpg`, indexes, `.xml`);
- max 20 entries, max 200 MB uncompressed (zip-bomb guard);
- zip-slip path traversal guard;
- requires `.shp` + `.dbf` + `.shx`.

When adding a format, update the handler allowlist in `internal/api/server.go`,
the DuckDB conversion path in `internal/tile/tylertoo.go`, the integration tests
in `internal/tile/tylertoo_test.go`, and both README/AGENT tables.

---

## 7. Configuration

Loaded by `godotenv.Load()` from the process working directory; real environment
variables win if already set.

| Variable | Notes |
| -------- | ----- |
| `PORT` | Listen port. `8080` if unset. |
| `PUBLIC_BASE_URL` | Base for `download_url`, e.g. `http://localhost:9090`. |
| `MINIO_ENDPOINT` | Host[:port] used by the SDK **and** URL construction. |
| `MINIO_ACCESS_KEY` | Static V4 credentials. |
| `MINIO_SECRET_KEY` | Static V4 credentials. |
| `MINIO_BUCKET` | Must already exist. |
| `DB_URL` | `postgres://…?sslmode=disable`-style URL for `lib/pq`. |
| `REDIS_URL` | `redis://host:6379`; scheme is stripped for Asynq. |
| `TYLERTOO_BIN` | Optional binary path override (default `tylertoo`). |
| `DUCKDB_BIN` | Optional binary path override (default `duckdb`). |

MinIO `Secure` is hard-coded to `true` in `cmd/geotileify/main.go`. Change that
only with a matching endpoint/proxy setup.

**Never commit real credentials in docs, examples, or logs.**

---

## 8. Data model

```sql
tiles (
  id BIGSERIAL PRIMARY KEY,
  public_id CHAR(26) UNIQUE NOT NULL,  -- ULID exposed to clients
  file_path TEXT NOT NULL,             -- MinIO object key
  url TEXT NOT NULL,                   -- stored canonical URL
  generated_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
)
```

Index: `idx_tiles_expires_at` for the cleanup sweep.

Cleanup behavior (`internal/tasks/jobs.go`): select expired rows → delete the
MinIO object → delete the row. A storage deletion failure skips that row so it is
retried on the next sweep.

---

## 9. Static landing page (frontend)

No bundler, no framework, no build step. Edit the files in `web/static/`
directly; `web/embed.go` embeds them at compile time.

### Serving contract

- `internal/api/server.go` serves `index.html` at `/` with `Cache-Control: no-store`.
- `/static/*` reads from the embedded FS, sets MIME by extension, blocks `..`,
  and caches CSS/JS for 24 h, other assets for 1 h.
- Because CSS/JS are cached for a day, bump a query string / asset path when
  testing changes in a deployed environment, or hard-reload.

### Visual system

Design tokens live at the top of `web/static/css/style.css`:

- brand `#8ED2D4` + deep/mist variants;
- ink `#171717`, body `#4d4d4d`, mute `#888`, hairline `#ebebeb`;
- canvas `#fff` / soft `#fafafa`;
- Geist + Geist Mono loaded from Google Fonts, system fallbacks;
- page content rail: `.container` max-width **1024px**;
- displayed UI version: **v2.0.0** — keep announce bar, brand pill, and footer in sync;
- breakpoints: **900px** (nav → hamburger, grids → 1 col) and **640px**
  (mobile type/spacing overrides). JS closes the menu above 900px.

### Hero contract (important)

The hero is deliberately **full-bleed**:

```
.hero                → no container wrapper, no padding
.hero-card           → width:100%, edge-to-edge, no radius, min-height 520–740px
.hero-inner          → max-width:1024px, margin:auto — realigns copy to page rail
.hero-copy           → left column max 640px
.hero-scrim          → darkens behind copy, fades to transparent before the right side
.hero-rail           → decorative 3-step pipeline on desktop (hidden ≤900px)
canvas#tileCanvas    → uniform tile field behind everything
```

There is **no hero bar** (the old app-window chrome was removed). Keep it that
way unless the design is intentionally changed.

The canvas (`web/static/js/app.js`) paints a sparse tile field that is uniform
left→right. Do not reintroduce horizontal damping/fading of the tiles. The copy
stays readable via a left-side scrim in `.hero-scrim` (plus text shadows); that
scrim must fade fully out before the right edge so the right side stays clean.
On small screens the scrim becomes a full-width vertical wash.

The animated accents are **light-blue pulse tiles** (not white), concentrated
toward the **right side** and phased as a right→left wave so the hero animation
is visible outside the copy scrim. Keep that placement when tuning the canvas.

The animation is a slow ~12 fps breathe (not a blink). It renders one static
frame for `prefers-reduced-motion` and pauses on hidden tabs.

If you add content to the hero, keep it inside `.hero-copy` and do not
reintroduce a `.container` around `.hero-card`.

### Sections & JS hooks

- Section IDs used by scroll-spy: `#features`, `#endpoints`, `#formats`.
- Nav links that should highlight use `data-spy="<section-id>"`.
- Scroll-spy is **line-based** (the last section whose top crosses ~112px), not
  an IntersectionObserver middle band. `#formats` is too short for a middle
  band and would incorrectly leave **Features** highlighted — keep the
  line-based logic when editing nav behavior.
- Scroll reveal is applied by JS to `.band`, `.strip`, `.card`, `.endpoint`,
  `.footer-top`; without JS content is visible normally.
- Health: one `fetch("/health")` updates the nav pill (`#healthBadge`) and
  footer (`#footerHealth`). In a static-only preview this intentionally shows
  “Unreachable”.
- Mobile menu: `#menuBtn` + `#mobileMenu` with `aria-expanded`.

### Icons

Lucide SVGs live in `web/static/icons/` and are referenced as `<img>` so they
**do not inherit `color`**. To whiten icons on dark surfaces the CSS uses
`filter:brightness(0) invert(1)` (`.icon-on-dark`, announce/endpoint/footer
rules); the brand CTA overrides the filter back to none. Add missing icons to
`web/static/icons/ICONS.md` and the HTML together. The JS hides any broken
`.icon` image as a fallback.

`web/static/icons/favicon.svg` is the 4-tile brand mark used as the favicon.
Keep its colors/shape in sync with `.brand-mark` in the header/footer.

### Adding a section

1. Add markup in `index.html` between existing bands, using `.container` +
   `.band` conventions.
2. Reuse existing tokens; avoid introducing new colors.
3. If it needs nav/scroll-spy, add the id + `data-spy` link.
4. Keep motion subtle and respect `prefers-reduced-motion`.
5. Check 1440, 1024, 768, 390 widths and no horizontal scroll.

### Chrome extension (companion project)

The extension lives at `../geotileify` (Vite + Preact + Tailwind). Its theme
mirrors the landing page in `src/style.css` via `@theme` tokens
(`--color-brand`, `--color-ink`, `--color-hairline`, etc.). Shared logo assets:
`public/assets/logo.svg` + generated `icon16/48/128.png` and
`public/assets/icon_home.png`. Rebuild with `npm run build`; package the zip with
`npm run serve`.

Extension UI surfaces to keep on-theme: the toolbar popup
(`src/popup/popup.jsx`), the map overlay (`src/map/map.jsx`), and the bundled
landing page (`src/index.jsx`).

The map page auto-loads a generated tile from `?id=<public-id>` through
`/tiles/:id/proxy` and fits the map to the archive bounds. It also decodes one
or more real MVT tiles (via `@mapbox/vector-tile` + `pbf`) to detect each
layer's geometry type before choosing fill/line/circle layers. Do not replace
this with layer-name heuristics; that is what used to render polygons as points.
Downloads in the popup and map overlay use `chrome.downloads` (manifest
permission `downloads`), with an anchor fallback for plain-browser previews.

The popup has an Advanced panel for PMTiles metadata (`name`, `description`,
`attribution`), `simplify_factor` (0.1/0.5/1/2/4), and property inclusion
(default include-all). Metadata is auto-filled on file selection for
GeoJSON/JSON files (top-level or `metadata` object); other formats fall back to
the filename for the name and leave description/attribution null. The generated
metadata is shown in the map metadata drawer.

Layer min/max zoom is applied on the MapLibre **style layers** (`minzoom`, and
`maxzoom` as the exclusive bound, so `layer.maxzoom + 1`). The map's own
`minZoom` is intentionally left at 0, so the layer disappears outside its range
without limiting how far the base map can zoom out. The initial fit is clamped
inside the layer's `[minZoom, maxZoom]` range; an explicit URL view is respected.
Map controls include zoom in/out, a compass reset, a clickable rounded zoom
value, and an Inspect toggle. Inspect uses `queryRenderedFeatures` on the active
style layers and opens a right-side drawer; a Metadata button opens the same
drawer with the PMTiles header and JSON metadata. Inspect also has a throttled
hover highlight: a small GeoJSON hover source is updated only when the hovered
feature changes (~90 ms throttled query), keeping rendering smooth. Hover
layers are geometry-filtered: a bright red fill for polygons, a bright red
stroke for lines, and a yellow circle for points only, so a polygon/line never
shows node circles and hover is clearly distinct from the active layer. The hover
handler queries the loaded PMTiles source and re-joins matching tile fragments
by source layer/type/properties, so the whole visible feature is highlighted
rather than one clipped tile fragment. Polygon fragments are unioned with
`polygon-clipping` before rendering, which removes translucent overlap seams
between adjacent tiles. Polygon layers use a single fill style layer with
`fill-outline-color`; line layers keep their line stroke. This avoids
the previous duplicate fill+line layer pair while preserving outlines.
The URL is kept in sync as `?id=…&z=…&x=<lng>&y=<lat>` via `replaceState`; opening
that URL restores the view and skips the initial fit.

---

## 10. Middleware, limits, security

Registered in `internal/api/server.go`:

- **CORS** allowlist: two Chrome extension IDs + `https://geotileify.idn-guessr.com`.
- Request logger.
- **Rate limit**: 10 rps, burst 30, per `RealIP`, 3-minute window. Skipped for
  `/`, `/health`, `/static/*`, and `/tiles/*` (map range requests).
- **Body limit**: 50 MB (`middleware.BodyLimit`), plus a handler-level size check.
- Static handler: `path.Clean` + `..` rejection + `X-Content-Type-Options: nosniff`.
- Zip ingestion: allowlist, size caps, zip-slip guard.
- Presigned URLs expire after 5 minutes; public IDs expire after 24 hours.
- The `/tiles/:id/proxy` route allows `Range` and exposes `Content-Range`,
  `Content-Length`, `Accept-Ranges`, `ETag`, and cache headers so the extension
  can stream PMTiles without MinIO CORS support.

---

## 11. Known quirks / gotchas

- **No fallback tiler:** the tiling path is DuckDB → GeoParquet →
  tylertoo. A missing binary fails at startup (`tile.CheckDependencies`); a
  conversion/tiling error returns the tool's error in the JSON response.
- **CRS:** all inputs are normalised to EPSG:4326. Tylertoo also accepts
  EPSG:3857, but the API reprojects it for consistency. If a source has no CRS
  and its bounds fall outside lon/lat ranges, the request is rejected (HTTP 400)
  rather than silently producing shifted tiles. DuckDB's `ST_Transform` must be
  called with `always_xy := true` in this project.
- **Zoom range** comes from `min_zoom`/`max_zoom` (defaults 0/14,
  allowed 0-16, `min <= max`). Both are sent by the extension popup and forwarded
  to `tylertoo --min-zoom/--max-zoom`.
- **Object keys** are `tiles/<public-id>.pmtiles`, so re-uploading the same
  filename no longer overwrites an earlier archive.
- **Temp workspaces** are per-request `os.MkdirTemp` directories removed with
  `defer os.RemoveAll` on every exit path.
- **No migration runner:** apply SQL manually (or add a runner deliberately).
- **`GET /` is the landing page**, not a redirect to `/health` (README may be stale).
- **Cache:** CSS/JS are cached 24 h; HTML is `no-store`.
- **MinIO URL field:** `url` is built as `https://<endpoint>/<bucket>/<object>`;
  this is informational and may not match a custom reverse proxy.
- **Docker compose** no longer mounts host binaries or glibc libraries; DuckDB
  and the static tylertoo binary are installed in the image. The image is
  **linux/amd64** because tylertoo currently ships an x86_64 Linux binary;
  Apple Silicon runs it through Rosetta/QEMU.
- **`scrollcraft/` and `DESIGN.md`** are design references/assets, not part of
  the Go build.
- **Secrets:** `.env` is present locally and gitignored; never copy its values
  into docs, tests, or committed code.

---

## 12. Common tasks

```bash
# Format & static checks
gofmt -w .
go vet ./...
go build ./...
go test ./...
node --check web/static/js/app.js

# Add a new endpoint
#   1. handler method on *api.Server in internal/api/server.go
#   2. register in registerRoutes
#   3. document here + README

# Add a supported format
#   1. handler allowlist + switch in internal/api/server.go
#   2. DuckDB ST_Read / geo->GeoParquet path in internal/tile/tylertoo.go
#   3. integration fixture in internal/tile/tylertoo_test.go
#   4. hero format strip / README / AGENT tables

# Reset local tile data
psql "$DB_URL" -c "DELETE FROM tiles;"
# (also remove objects from the MinIO bucket)
```

### Frontend verification checklist

- Hero spans the viewport at every width; copy aligns with the 1024px rail.
- No `.hero-bar` in DOM; no right-side tile fade; scrim only behind copy.
- Headline/lede/CTA remain legible over the canvas.
- No horizontal scroll at 1440 / 1024 / 768 / 390.
- Nav scroll-spy, mobile menu, health badge, footer status work.
- Missing icons do not render broken-image glyphs.
- `prefers-reduced-motion` disables breathing/reveal animations.
