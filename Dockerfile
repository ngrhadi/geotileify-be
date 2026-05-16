FROM golang:1.24 AS builder

WORKDIR /app

COPY . .

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o geotileify ./cmd/geotileify


# =========================
# Runtime
# =========================
FROM debian:bookworm-slim

WORKDIR /app

# install only runtime deps
RUN apt-get update && apt-get install -y \
    gdal-bin \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# copy binary
COPY --from=builder /app/geotileify .

EXPOSE 9090

CMD ["./geotileify"]
