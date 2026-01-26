# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the local server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /local-server ./cmd/local

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Copy the binary
COPY --from=builder /local-server /app/local-server

# Create non-root user
RUN addgroup -g 1000 server && \
    adduser -u 1000 -G server -s /bin/sh -D server && \
    chown -R server:server /app

USER server

# Expose ports
EXPOSE 1738 1739

# Health check (port configurable via HEALTH_PORT env var)
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:${HEALTH_PORT:-1739}/health || exit 1

ENTRYPOINT ["/app/local-server"]
