package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/daemon"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			runDaemon()
			return
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

	// No store and no model is built here any more. bbolt locks its file for
	// the lifetime of a process and boltStore caches every record in memory,
	// so one process per MCP session could neither share the database nor see
	// another session's writes. Instead the daemon owns both, and this process
	// is a byte pipe carrying MCP frames to it — started on demand, so nothing
	// has to be installed or supervised for this to work.
	if err := daemon.Proxy(cfg, version, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "engram: %v\n", err)
		os.Exit(1)
	}
}
