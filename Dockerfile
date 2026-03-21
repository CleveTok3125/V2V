FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN chmod +x build_server.sh && CGO_ENABLED=0 GOOS=linux sh ./build_server.sh

FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add tzdata ca-certificates
COPY --from=builder /build/public/server.bin ./server.bin
RUN touch .env roles.json
CMD ["./server.bin"]
