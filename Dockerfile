FROM golang:1.24-alpine

WORKDIR /app

RUN apk add --no-cache \
    gdal-tools \
    libc6-compat \
    wget \
    unzip

# Install DuckDB CLI
RUN wget -q \
    https://github.com/duckdb/duckdb/releases/latest/download/duckdb_cli-linux-amd64.zip \
    -O /tmp/duckdb.zip \
    && unzip -q /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm -f /tmp/duckdb.zip

RUN duckdb -c "INSTALL spatial;"

COPY . .

RUN go mod download
RUN go build -o geotileify ./cmd/geotileify

EXPOSE 9090

CMD ["./geotileify"]
