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

# Build the worker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /worker ./cmd/worker

# Runtime stage
FROM node:20-alpine

WORKDIR /app

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Copy the worker binary
COPY --from=builder /worker /app/worker
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Use existing node user (UID 1000) and fix ownership
RUN chown -R node:node /app

USER node

# Default health check port (overridden by HEALTH_PORT env var at runtime)
ENV HEALTH_PORT=1739
EXPOSE 1739

# Health check - ECS task definition handles this, but keeping for local Docker runs
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:${HEALTH_PORT}/health/live || exit 1

ENTRYPOINT ["/app/worker"]
