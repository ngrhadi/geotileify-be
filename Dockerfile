FROM golang:1.24-bookworm

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        gdal-bin \
        wget \
        unzip \
        git \
        build-essential \
        zlib1g-dev \
        libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*

# DuckDB
RUN wget -q \
    https://github.com/duckdb/duckdb/releases/latest/download/duckdb_cli-linux-amd64.zip \
    -O /tmp/duckdb.zip \
    && unzip -q /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm -f /tmp/duckdb.zip

# DuckDB Spatial extension
RUN duckdb -c "INSTALL spatial;"

# Tippecanoe
RUN git clone --depth 1 https://github.com/felt/tippecanoe.git /tmp/tippecanoe \
    && make -C /tmp/tippecanoe -j"$(nproc)" \
    && make -C /tmp/tippecanoe install \
    && rm -rf /tmp/tippecanoe

COPY . .

RUN go mod download
RUN go build -o geotileify ./cmd/geotileify

EXPOSE 9090

CMD ["./geotileify"]
