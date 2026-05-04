BINARY    := engram
INSTALL   := $(HOME)/.claude/mcp-servers/engram

.PHONY: build install test clean

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) ./cmd/engram/

install: build
	mkdir -p $(INSTALL)
	cp $(BINARY) $(INSTALL)/$(BINARY)
	@echo "Installed to $(INSTALL)/$(BINARY)"

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
