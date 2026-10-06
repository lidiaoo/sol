VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/lidiaoo/sol/internal/buildinfo.version=$(VERSION)
GO ?= go

# The module needs this Go version (see go.mod). Older toolchains - gccgo 1.18 is one of them -
# fail deep inside the standard library with "package log/slog is not in GOROOT", which says
# nothing about the cause. Name the requirement instead, and let GO name a different binary.
MIN_GO := 1.26

.PHONY: *

check-go:
	@$(GO) env GOVERSION 2>/dev/null | sed 's/^go//' | awk -F. '{ exit !($$1*1000+$$2 >= 1026) }' || \
		{ echo "$$($(GO) env GOVERSION 2>/dev/null || echo 'no go found'): this module needs Go $(MIN_GO) or newer"; \
		  echo "point make at the right toolchain, for example: make GO=/path/to/go1.26/bin/go build"; exit 1; }

build: check-go
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o sol .

# Same as build, but the shape the release pipeline produces: static, stripped, no paths.
build-static: check-go
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o sol-static .

test: check-go
	$(GO) test -tags mock -race -cover ./...

lint: check-go
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4 run --color always ${args}

lint-fix:
	make lint args=--fix
