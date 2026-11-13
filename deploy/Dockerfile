# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS web-builder
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml tsconfig*.json ./
COPY packages ./packages
COPY apps ./apps
COPY contracts ./contracts
RUN pnpm install --frozen-lockfile && pnpm --filter @tuskrag/web build

FROM golang:1.25-bookworm AS go-builder
WORKDIR /src/services/core
COPY services/core/go.mod services/core/go.sum ./
RUN go mod download
COPY services/core ./
COPY --from=web-builder /src/apps/web/dist ./internal/httpapi/web
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/tuskrag ./cmd/tuskrag

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates bubblewrap python3 python3-venv curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system tuskrag \
    && useradd --system --gid tuskrag --create-home --home-dir /var/lib/tuskrag tuskrag

WORKDIR /opt/tuskrag
COPY --from=go-builder /out/tuskrag /usr/local/bin/tuskrag
COPY services/parser /opt/tuskrag/parser

RUN python3 -m venv /opt/tuskrag/parser/.venv \
    && /opt/tuskrag/parser/.venv/bin/pip install --no-cache-dir /opt/tuskrag/parser \
    && mkdir -p /var/lib/tuskrag/storage /var/lib/tuskrag/tmp /run/secrets \
    && chown -R tuskrag:tuskrag /var/lib/tuskrag /opt/tuskrag

USER tuskrag
ENV TUSKRAG_PARSER=/opt/tuskrag/parser/.venv/bin/tuskrag-parse \
    TUSKRAG_PARSER_RUNTIME_DIR=/opt/tuskrag/parser \
    TUSKRAG_BUBBLEWRAP=/usr/bin/bwrap \
    TUSKRAG_STORAGE_DIR=/var/lib/tuskrag/storage \
    TUSKRAG_PARSER_TEMP_DIR=/var/lib/tuskrag/tmp \
    TUSKRAG_LISTEN=0.0.0.0:8080

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tuskrag"]
