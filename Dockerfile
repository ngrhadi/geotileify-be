FROM golang:1.24-alpine

WORKDIR /app

RUN apk add --no-cache \
    gdal-tools \
    libc6-compat

COPY . .

RUN go mod download
RUN go build -o geotileify ./cmd/geotileify

EXPOSE 9090

CMD ["./geotileify"]
