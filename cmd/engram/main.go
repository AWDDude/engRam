package main

import (
	"fmt"
	"os"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/server"
	"github.com/AWDDude/engRam/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reembed":
			runReembed()
			return
		case "export":
			runExport()
			return
		case "import":
			runImport()
			return
		case "version", "--version", "-v":
			runVersion(os.Stdout)
			return
		case "help", "--help", "-h":
			runUsage(os.Stdout)
			return
		}
		// Anything else that looks like a flag is a mistake, not a memory
		// server invocation. Falling through would silently start the MCP
		// server and appear to hang, which is how `engram --version` used to
		// behave before it was a real command.
		if strings.HasPrefix(os.Args[1], "-") {
			fmt.Fprintf(os.Stderr, "engram: unknown option %q\n\n", os.Args[1])
			runUsage(os.Stderr)
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: config error: %v\n", err)
		os.Exit(1)
	}

	st, cleanup, err := store.NewBoltStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	// RegisterTools below registers a fixed set of tools once at startup and
	// never changes it afterward; declaring listChanged: false here (instead
	// of leaving it to mcp-go's implicit listChanged: true default) avoids
	// misleading clients into subscribing to tool-list-change notifications
	// that will never come.
	s := mcpserver.NewMCPServer("engram", version, mcpserver.WithToolCapabilities(false))
	server.RegisterTools(s, server.NewApp(st, cfg.DefaultLimit, cfg.MaxContentChars))

	if err := mcpserver.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "engram: server error: %v\n", err)
		os.Exit(1)
	}
}
