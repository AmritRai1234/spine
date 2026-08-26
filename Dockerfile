# ─── Build Stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS builder

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Build the binary (sqlite_fts5 enables full-text search support)
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -tags sqlite_fts5 -ldflags="-s -w" -o /spine ./cmd/spine/

# ─── Runtime Stage ────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /spine /usr/local/bin/spine

# Create app directory for manifests and data
WORKDIR /app

# Default port
EXPOSE 8080

# Health check (curl is installed in this stage)
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["spine"]

# Mount your manifest at /app/app.spine and set SPINE_API_KEY in the
# environment — `spine serve` refuses to start without authentication.
CMD ["serve"]
