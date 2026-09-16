BINARY := engram
GIT_COMMIT := $(shell git rev-parse --short HEAD)

.PHONY: build test clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(GIT_COMMIT)" -o $(BINARY) ./cmd/engram/

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
