BINARY  := bin/deepseek-search-mcp
PACKAGE := ./cmd/deepseek-search-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test race cover lint fmt vet clean

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PACKAGE)

install:
	go install -trimpath -ldflags "$(LDFLAGS)" $(PACKAGE)

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fmt:
	gofmt -w .

vet:
	go vet ./...

lint:
	gofmt -l .
	go vet ./...

clean:
	rm -rf bin coverage.out
