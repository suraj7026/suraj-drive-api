FROM golang:1.25.13-alpine AS builder
WORKDIR /app

# libvips (with libheif transitively) is required by govips for HEIC -> JPEG
# preview conversion. build-base supplies the C toolchain CGO needs.
RUN apk --no-cache add build-base vips-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=1 GOOS=linux go build -o migrate ./cmd/migrate

FROM alpine:3.19
# Runtime: libvips itself (not -dev), plus certs/tz for normal HTTP/clock work.
RUN apk --no-cache add ca-certificates tzdata vips
WORKDIR /app

COPY --from=builder /app/server ./server
COPY --from=builder /app/migrate ./migrate
COPY config.yaml ./config.yaml

EXPOSE 4001
CMD ["./server"]
