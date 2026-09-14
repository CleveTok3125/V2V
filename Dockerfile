FROM golang:1.27-alpine AS server-builder
WORKDIR /build
# Dependencies are vendored into the repo, so no module downloads (or
# working DNS) are needed during the image build.
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
# Version is stamped on the host where .git lives and passed in as a build
# arg (see docker-compose.yml); the web target falls back to a unique dev
# stamp when it is empty, so the bundle never silently reports a stale hash.
ARG GIT_HASH=""
ENV GIT_HASH=${GIT_HASH}
# Two independent builder stages so BuildKit runs the Go server build and
# the wasm bundle (plus parallel compression) concurrently; the final
# image waits only for the slower one. GOCACHE mounts keep incremental
# builds across image rebuilds.
RUN --mount=type=cache,target=/tmp/gocache \
  apk add --no-cache make git && make server

FROM golang:1.27-alpine AS web-builder
WORKDIR /build
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
ARG GIT_HASH=""
ENV GIT_HASH=${GIT_HASH}
RUN --mount=type=cache,target=/tmp/gocache \
  apk add --no-cache make git gzip brotli && make web

FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add tzdata ca-certificates su-exec && \
    adduser -D -H -s /sbin/nologin app
COPY --from=server-builder /build/public/server.bin ./server.bin
COPY --from=web-builder /build/webterm ./webterm
COPY docker/entrypoint.sh ./entrypoint.sh
# No USER directive on purpose: the entrypoint starts as root to
# prepare /app/data, then drops to app via su-exec. The server
# process itself is never root.
ENTRYPOINT ["./entrypoint.sh"]
CMD ["./server.bin"]
