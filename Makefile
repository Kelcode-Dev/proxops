GO      ?= go
BIN     := bin
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet cross clean

all: build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/proxops ./cmd/proxops

test:
	$(GO) test -count=1 ./...

vet:
	$(GO) vet ./...

# Static binary for a typical PVE host (no toolchain required there).
cross:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/proxops-linux-amd64 ./cmd/proxops

clean:
	rm -rf $(BIN) $(DIST)
