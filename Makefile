BINARY := engram

.PHONY: build test clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) ./cmd/engram/

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
