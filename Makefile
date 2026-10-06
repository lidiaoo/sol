VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/lidiaoo/sol/internal/buildinfo.version=$(VERSION)

.PHONY: *

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o sol .

# Same as build, but the shape the release pipeline produces: static, stripped, no paths.
build-static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o sol-static .

test:
	go test -tags mock -race -cover ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4 run --color always ${args}

lint-fix:
	make lint args=--fix
