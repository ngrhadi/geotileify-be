# Dockerfile
FROM golang:1.24-alpine

WORKDIR /app

# Copy source code
COPY . .

# Download dependencies
RUN go mod download

# Build the application
RUN go build -o geotileify ./cmd/geotileify

# Expose port
EXPOSE 9090

# Run the application
CMD ["./geotileify"]
