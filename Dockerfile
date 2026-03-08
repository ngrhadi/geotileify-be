# Dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o geotileify ./cmd/geotileify

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create necessary directories
RUN mkdir -p /app/tmp /app/migrations

# Copy binary from builder
COPY --from=builder /app/geotileify .
COPY --from=builder /app/migrations ./migrations
COPY --from=builder /app/utils ./utils
COPY --from=builder /app/internal ./internal
COPY --from=builder /app/.env ./

# Copy tmp files if needed (optional)
COPY --from=builder /app/tmp ./tmp

# Expose port
EXPOSE 9090

# Run the application
CMD ["./geotileify"]
