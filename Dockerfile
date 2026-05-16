FROM golang:1.24-alpine

WORKDIR /app

# ogr2ogr (GDAL) for Shapefile → GeoJSON conversion
RUN apk add --no-cache gdal-tools

# DuckDB CLI for GeoParquet → GeoJSON conversion.
# Static binary — no extra lib mounts needed.
# Pin the version to keep builds reproducible; update as needed.
ARG DUCKDB_VERSION=v1.2.2
RUN apk add --no-cache wget unzip \
    && wget -q -O /tmp/duckdb.zip \
       https://github.com/duckdb/duckdb/releases/download/${DUCKDB_VERSION}/duckdb_cli-linux-amd64.zip \
    && unzip /tmp/duckdb.zip -d /usr/local/bin \
    && rm /tmp/duckdb.zip \
    && chmod +x /usr/local/bin/duckdb \
    && duckdb -c "INSTALL spatial;"

COPY . .
RUN go mod download
RUN go build -o geotileify ./cmd/geotileify

EXPOSE 9090
CMD ["./geotileify"]
