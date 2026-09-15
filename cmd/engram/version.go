package main

import (
	"fmt"
	"io"
	"runtime"
)

// version is the engram release version.
//
// Release builds override it via goreleaser's ldflags (-X main.version=<tag>),
// so the tag is authoritative for anything published. The constant here is
// what a local `go build` or `make build` reports, and should name the next
// intended release.
var version = "2.1.0"

// commands lists every subcommand main dispatches on, so the usage text and
// the dispatch table can be checked against each other.
var commands = []string{"export", "import", "reembed", "version", "help"}

func runVersion(w io.Writer) {
	fmt.Fprintf(w, "engram %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

const usageText = `engram — MCP server for long-term semantic memory

Usage:
  engram                    start the MCP server on stdio (default)
  engram export -f <file>   write all memories to a CSV file
  engram import -f <file>   read memories from a CSV file
  engram reembed            re-embed all memories into a newly configured model
  engram version            print the version
  engram help               print this message

Config is read from $ENGRAM_CONFIG_PATH, or ~/.config/engram/config.json.
Omitted config fields fall back to their defaults.
`

func runUsage(w io.Writer) {
	fmt.Fprint(w, usageText)
}
