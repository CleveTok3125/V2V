APP_VERSION ?= $(shell git describe --tags --always 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo unknown)
VERSION ?= $(if $(GIT_HASH),$(GIT_HASH),$(shell git rev-parse --short HEAD 2>/dev/null || echo "dev-$$(date -u +%Y%m%d%H%M)"))
LDFLAGS := -s -w -X 'main.Version=$(APP_VERSION)'
WEB_LDFLAGS := -s -w -X 'main.Version=$(VERSION)'
GOCACHE ?= /tmp/gocache

PLATFORMS := windows/amd64 windows/arm64 linux/amd64 linux/arm64 android/arm64 darwin/amd64 darwin/arm64
HOST_GOOS ?= $(shell go env GOOS)
HOST_GOARCH ?= $(shell go env GOARCH)

.PHONY: all server web client v2vctl vet test clean help

all: server web client v2vctl

server:
	mkdir -p public
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -tags netgo -trimpath -ldflags '-s -w' -o public/server.bin ./server

web:
	mkdir -p webterm
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" webterm/wasm_exec.js
	printf 'window.V2V_VERSION = "%s";\n' "$(VERSION)" > webterm/version.js
	GOCACHE=$(GOCACHE) GOOS=js GOARCH=wasm go build -trimpath -ldflags "$(WEB_LDFLAGS)" -o webterm/app.wasm ./client
	@if command -v gzip >/dev/null 2>&1; then gzip -9 -kf webterm/app.wasm; echo "   gzip: $$(wc -c < webterm/app.wasm.gz) bytes"; fi
	@if command -v brotli >/dev/null 2>&1; then brotli -q 11 -k -f webterm/app.wasm; echo "   brotli: $$(wc -c < webterm/app.wasm.br) bytes"; fi
	@echo "webterm built (version $(VERSION), $$(wc -c < webterm/app.wasm) bytes)"

client:
	mkdir -p public
ifeq ($(ALL),1)
	@for p in $(PLATFORMS); do \
		GOOS=$${p%/*}; GOARCH=$${p#*/}; \
		OUT="public/V2V-$${GOOS}-$${GOARCH}"; \
		[ "$$GOOS" = "android" ] && [ "$$GOARCH" = "arm64" ] && OUT="public/V2V-android-aarch64"; \
		[ "$$GOOS" = "windows" ] && OUT="$$OUT.exe"; \
		echo "Building client: $$GOOS/$$GOARCH..."; \
		GOCACHE=$(GOCACHE) CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -trimpath -ldflags "$(LDFLAGS)" -o $$OUT ./client || exit 1; \
	done
	@echo "Done!"
else
	@echo "Building client for $(HOST_GOOS)/$(HOST_GOARCH)..."
	@OUT="public/V2V-$(HOST_GOOS)-$(HOST_GOARCH)"; \
	 [ "$(HOST_GOOS)" = "android" ] && [ "$(HOST_GOARCH)" = "arm64" ] && OUT="public/V2V-android-aarch64"; \
	 [ "$(HOST_GOOS)" = "windows" ] && OUT="$$OUT.exe"; \
	 GOCACHE=$(GOCACHE) CGO_ENABLED=0 GOOS=$(HOST_GOOS) GOARCH=$(HOST_GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $$OUT ./client && echo "Done! -> $$OUT"
endif

v2vctl:
	mkdir -p public
ifeq ($(ALL),1)
	@for p in $(PLATFORMS); do \
		GOOS=$${p%/*}; GOARCH=$${p#*/}; \
		OUT="public/V2Vctl-$${GOOS}-$${GOARCH}"; \
		[ "$$GOOS" = "android" ] && [ "$$GOARCH" = "arm64" ] && OUT="public/V2Vctl-android-aarch64"; \
		[ "$$GOOS" = "windows" ] && OUT="$$OUT.exe"; \
		echo "Building v2vctl: $$GOOS/$$GOARCH..."; \
		GOCACHE=$(GOCACHE) CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -trimpath -ldflags "$(LDFLAGS)" -o $$OUT ./cmd/v2vctl || exit 1; \
	done
	@echo "Done!"
else
	@echo "Building v2vctl for $(HOST_GOOS)/$(HOST_GOARCH)..."
	@OUT="public/V2Vctl-$(HOST_GOOS)-$(HOST_GOARCH)"; \
	 [ "$(HOST_GOOS)" = "android" ] && [ "$(HOST_GOARCH)" = "arm64" ] && OUT="public/V2Vctl-android-aarch64"; \
	 [ "$(HOST_GOOS)" = "windows" ] && OUT="$$OUT.exe"; \
	 GOCACHE=$(GOCACHE) CGO_ENABLED=0 GOOS=$(HOST_GOOS) GOARCH=$(HOST_GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $$OUT ./cmd/v2vctl && echo "Done! -> $$OUT"
endif

vet:
	GOCACHE=$(GOCACHE) go vet ./...

test:
	GOCACHE=$(GOCACHE) go test ./... -count=1

check: vet test
	@echo "check done (vet+test)"

clean:
	rm -rf public webterm/app.wasm webterm/app.wasm.gz webterm/app.wasm.br webterm/version.js webterm/wasm_exec.js

help:
	@echo "Targets:"
	@echo "  make all      - build server, web, client, v2vctl (parallel with -j)"
	@echo "    default: client/v2vctl build for host OS ($(HOST_GOOS)/$(HOST_GOARCH))"
	@echo "    ALL=1:   build full matrix for CI (e.g. make all ALL=1 -j4)"
	@echo "  make server   - build public/server.bin"
	@echo "  make web      - build webterm/app.wasm"
	@echo "  make client   - build public/V2V-* (host only, or all with ALL=1)"
	@echo "  make v2vctl   - build public/V2Vctl-* (host only, or all with ALL=1)"
	@echo "  make vet      - go vet"
	@echo "  make test     - go test"
	@echo "  make check    - vet+test"
	@echo "  make clean    - remove build artifacts"
