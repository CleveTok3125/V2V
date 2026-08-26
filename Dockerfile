FROM golang:1.25-alpine AS builder
WORKDIR /build
# Dependencies are vendored into the repo, so no module downloads (or
# working DNS) are needed during the image build.
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
# Version is stamped on the host where .git lives and passed in as a build
# arg (see docker-compose.yml); build_web.sh falls back to a unique dev
# stamp when it is empty, so the bundle never silently reports a stale hash.
ARG GIT_HASH=""
ENV GIT_HASH=${GIT_HASH}
RUN chmod +x build_server.sh && CGO_ENABLED=0 GOOS=linux sh ./build_server.sh

FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add tzdata ca-certificates
COPY --from=builder /build/public/server.bin ./server.bin
COPY --from=builder /build/webterm ./webterm
RUN touch .env roles.json
CMD ["./server.bin"]
