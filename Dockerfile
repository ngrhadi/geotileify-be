FROM golang:1.24-bookworm

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        gdal-bin \
        wget \
        unzip \
    && rm -rf /var/lib/apt/lists/*

# DuckDB CLI
RUN wget -q \
    https://github.com/duckdb/duckdb/releases/latest/download/duckdb_cli-linux-amd64.zip \
    -O /tmp/duckdb.zip \
    && unzip -q /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm -f /tmp/duckdb.zip

# Install DuckDB spatial extension during image build
RUN duckdb -c "INSTALL spatial;"

COPY . .

RUN go mod download
RUN go build -o geotileify ./cmd/geotileify

EXPOSE 9090

CMD ["./geotileify"]
