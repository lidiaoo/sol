VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# "Dirty" means tracked edits only, exactly like `git describe --dirty` and scripts/release.sh do it.
# The toolchain's own vcs.modified also counts untracked files, and building creates some
# (dist/stage/...), so a clean tree would otherwise still come out -dirty. Stamp it ourselves;
# `make DIRTY=1 build` forces the dirty branch.
DIRTY := $(shell git status --porcelain --untracked-files=no 2>/dev/null | head -1 | sed 's/.*/true/')
LDFLAGS := -X github.com/lidiaoo/sol/internal/buildinfo.version=$(VERSION) \
	-X github.com/lidiaoo/sol/internal/buildinfo.dirty=$(if $(DIRTY),true,false)
GO ?= go

# Where the development binaries land. Release archives stay in dist/ (scripts/release.sh).
EXT := $(if $(filter windows,$(shell $(GO) env GOOS 2>/dev/null)),.exe,)
BIN ?= build/sol$(EXT)
BINSTATIC ?= build/sol-static$(EXT)

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
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

# Same as build, but the shape the release pipeline produces: static, stripped, no paths.
build-static: check-go
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(BINSTATIC) .

test: check-go
	$(GO) test -tags mock -race -cover ./...

lint: check-go
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4 run --color always ${args}

lint-fix:
	make lint args=--fix
