# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server-builder
WORKDIR /build
# Dependencies are vendored into the repo, so no module downloads (or
# working DNS) are needed during the image build.
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
# GIT_HASH stamps both the server and the wasm bundle (Makefile prefers
# it over git describe) so the image never reports a stale version. When
# empty the build falls back to git describe/short hash.
ARG GIT_HASH=""
ENV GIT_HASH=${GIT_HASH}
# Pin the cache to the BuildKit cache-mount target (see --mount below).
ENV GOCACHE=/tmp/gocache
# Cross-compile the server for the target platform on the host-platform
# builder: GOOS/GOARCH come from buildx, so no QEMU emulation of the Go
# toolchain is needed. The wasm bundle is platform-independent.
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/tmp/gocache \
  apk add --no-cache make git && GOOS=${TARGETOS} GOARCH=${TARGETARCH} make server

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS web-builder
WORKDIR /build
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
ARG GIT_HASH=""
ENV GIT_HASH=${GIT_HASH}
ENV GOCACHE=/tmp/gocache
RUN --mount=type=cache,target=/tmp/gocache \
  apk add --no-cache make git gzip brotli && make web

FROM alpine:3.21
WORKDIR /app
RUN apk --no-cache add tzdata ca-certificates su-exec && \
    adduser -D -H -s /sbin/nologin app
COPY --from=server-builder /build/public/server.bin ./server.bin
COPY --from=web-builder /build/webterm ./webterm
COPY docker/entrypoint.sh ./entrypoint.sh
# No USER directive on purpose: the entrypoint starts as root to
# prepare /app/data, then drops to app via su-exec. The server
# process itself is never root.
ARG VERSION=""
ARG REVISION=""
LABEL org.opencontainers.image.title="V2V" \
      org.opencontainers.image.description="Verifiable Anonymous Chat" \
      org.opencontainers.image.source="https://github.com/CleveTok3125/V2V" \
      org.opencontainers.image.url="https://github.com/CleveTok3125/V2V" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
# Health probe port. The server reads PORT from the mounted .env, which
# the image cannot see, so compose passes the same value here.
ENV V2V_HEALTH_PORT=10000
# /api/version answers over plain HTTP and is not behind the TLS/proxy
# gate, so it is a safe liveness probe. busybox wget ships with alpine.
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${V2V_HEALTH_PORT}/api/version" >/dev/null 2>&1 || exit 1
STOPSIGNAL SIGTERM
ENTRYPOINT ["./entrypoint.sh"]
CMD ["./server.bin"]
