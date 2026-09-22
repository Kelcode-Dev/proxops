GO      ?= go
BIN     := bin
DIST    := dist
# Embedded build version. Derived from SemVer-style git tags only (--match
# 'v[0-9]*'): a checkout AT tag vX.Y.Z reports vX.Y.Z; a commit after the tag
# carries describe distance metadata (vX.Y.Z-N-gSHA); a tree with no matching
# tag falls back to the commit hash. Release CI overrides VERSION with the
# exact tag name so release binaries are deterministic.
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
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
