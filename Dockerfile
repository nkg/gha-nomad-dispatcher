FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY main.go ./
COPY internal/ ./internal/

# Static, position-independent, stripped + reproducible.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags='-s -w' \
    -o gha-nomad-dispatcher .

# ─── Runtime ──────────────────────────────────────────────────
FROM alpine:3.22

LABEL org.opencontainers.image.title="gha-nomad-dispatcher"
LABEL org.opencontainers.image.description="GitHub workflow_job webhook → Nomad job dispatcher"
LABEL org.opencontainers.image.source="https://github.com/nkg/gha-nomad-dispatcher"
LABEL org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -h /app appuser

WORKDIR /app
COPY --from=builder /build/gha-nomad-dispatcher .

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=5s --retries=3 \
    CMD ["wget", "--spider", "-q", "http://localhost:8080/healthz"]

ENTRYPOINT ["./gha-nomad-dispatcher"]
