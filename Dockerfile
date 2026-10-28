# Build stage. CGO is disabled, so the binary is static and can be built on
# any Linux host. The API image targets linux/amd64 because the released
# DuckDB CLI and tylertoo binaries used below are amd64 builds.
FROM golang:1.24-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /out/geotileify ./cmd/geotileify

# Runtime — amd64 (see above).
FROM --platform=linux/amd64 debian:bookworm-slim

WORKDIR /app

ARG TYLERTOO_VERSION=0.8.0
ARG DUCKDB_VERSION=1.4.3

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        wget \
        unzip \
        libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

# DuckDB CLI — normalises GeoJSON / FlatGeobuf / Shapefile uploads to
# GeoParquet, and reprojects non-WGS84 GeoParquet before tiling.
RUN wget -q \
        "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb_cli-linux-amd64.zip" \
        -O /tmp/duckdb.zip \
    && unzip -q /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm -f /tmp/duckdb.zip

# DuckDB Spatial extension (ST_Read, ST_Transform, GeoParquet writer).
RUN duckdb -c "INSTALL spatial;"

# Tylertoo — GeoParquet → PMTiles (static musl x86_64 release).
RUN wget -q \
        "https://github.com/geoparquet-io/tylertoo/releases/download/v${TYLERTOO_VERSION}/tylertoo-${TYLERTOO_VERSION}-x86_64-unknown-linux-musl.tar.gz" \
        -O /tmp/tylertoo.tar.gz \
    && mkdir -p /tmp/tylertoo \
    && tar -xzf /tmp/tylertoo.tar.gz -C /tmp/tylertoo --strip-components=1 \
    && install -m 0755 /tmp/tylertoo/tylertoo /usr/local/bin/tylertoo \
    && rm -rf /tmp/tylertoo /tmp/tylertoo.tar.gz

COPY --from=build /out/geotileify /app/geotileify
COPY migrations /app/migrations

EXPOSE 9090

CMD ["/app/geotileify"]
