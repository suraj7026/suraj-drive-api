FROM golang:1.25.13-alpine3.23 AS builder
WORKDIR /app

# libvips (with libheif transitively) is required by govips for HEIC -> JPEG
# preview conversion. build-base supplies the C toolchain CGO needs.
RUN apk --no-cache add build-base vips-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=1 GOOS=linux go build -o migrate ./cmd/migrate

FROM alpine:3.23
# Runtime: libvips itself (not -dev), plus certs/tz for normal HTTP/clock work.
RUN apk --no-cache add ca-certificates tzdata vips \
 && addgroup -S -g 10001 drive \
 && adduser -S -D -H -u 10001 -G drive drive
WORKDIR /app

COPY --from=builder --chown=drive:drive /app/server ./server
COPY --from=builder --chown=drive:drive /app/migrate ./migrate
COPY --chown=drive:drive config.yaml ./config.yaml

USER drive
EXPOSE 4001
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -q -T 3 -O /dev/null http://127.0.0.1:4001/api/ready || exit 1
CMD ["./server"]
