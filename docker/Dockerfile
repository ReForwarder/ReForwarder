# UI build stage.
#
# Built on a glibc image, not alpine: Vite 8 bundles with rolldown, which needs a
# native binding, and `npm ci` on musl trips the npm optional-deps bug
# (npm/cli#4828) — it silently skips the musl binaries, so the build then fails
# on a missing package. glibc uses the well-supported -gnu bindings. This is a
# throwaway builder stage (only web/dist is copied out), so it has no effect on
# the size or base of the final image.
FROM node:24-bookworm-slim AS ui-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# Go build stage
FROM golang:1.27-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=2026-03-15

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# Stage the built dashboard where //go:embed reads it, so the binary is
# self-contained. Must come after `COPY internal/`, which would otherwise
# clobber it.
COPY --from=ui-builder /app/web/dist ./internal/web/dist

RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X 'github.com/goposta/posta/internal/config.Version=${VERSION}' -X 'github.com/goposta/posta/internal/config.CommitID=${COMMIT}' -X 'github.com/goposta/posta/internal/config.BuildDate=${BUILD_DATE}'" \
    -o /bin/posta ./cmd/posta

# Runtime stage
FROM alpine:3.21

ARG VERSION=dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.title="posta" \
      org.opencontainers.image.description="Posta is a self-hosted email delivery platform" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.authors="Jonas Kaninda" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/goposta/posta" \
      org.opencontainers.image.revision="${COMMIT}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S posta && adduser -S posta -G posta

# Single self-contained binary: the dashboard is embedded, so there are no
# static files to copy and no POSTA_WEB_DIR to set.
COPY --from=builder /bin/posta /bin/posta
COPY docker/healthcheck.sh /usr/local/bin/posta-healthcheck
RUN ln -s /bin/posta /posta && chmod +x /usr/local/bin/posta-healthcheck

USER posta
EXPOSE 9000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/posta-healthcheck"]

ENTRYPOINT ["/bin/posta"]
