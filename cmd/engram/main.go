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
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: config error: %v\n", err)
		os.Exit(1)
	}

	st, cleanup, err := store.NewChromemStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	s := mcpserver.NewMCPServer("engram", "1.0.0")
	server.RegisterTools(s, server.NewApp(st, float32(cfg.DefaultMinScore)))

	if err := mcpserver.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "engram: server error: %v\n", err)
		os.Exit(1)
	}
}
