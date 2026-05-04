package main

import (
	"fmt"
	"os"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/server"
	"github.com/AWDDude/engRam/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: config error: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "--import" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: engram --import <export.json>")
			os.Exit(1)
		}
		if err := runImport(cfg, os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "engram: import error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	st, cleanup, err := store.NewChromemStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: store error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	s := mcpserver.NewMCPServer("engram", "1.0.0")
	server.RegisterTools(s, server.NewApp(st))

	if err := mcpserver.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "engram: server error: %v\n", err)
		os.Exit(1)
	}
}
