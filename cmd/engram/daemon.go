package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/daemon"
	"github.com/AWDDude/engRam/internal/server"
	"github.com/AWDDude/engRam/internal/store"
)

// runDaemon dispatches `engram daemon` and its subcommands. The bare form runs
// the daemon in the foreground; it is what a client spawns in the background
// when it finds nobody listening, and is also useful to run by hand when
// debugging, which is why its log goes to stdout rather than only to a file.
func runDaemon() {
	action := ""
	if len(os.Args) > 2 {
		action = os.Args[2]
	}
	switch action {
	case "":
		runDaemonServe()
	case "stop":
		runDaemonStop()
	case "status":
		runDaemonStatus()
	default:
		fmt.Fprintf(os.Stderr, "engram daemon: unknown subcommand %q (want stop or status)\n", action)
		os.Exit(2)
	}
}

func runDaemonServe() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram daemon: config error: %v\n", err)
		os.Exit(1)
	}

	// Timestamps matter here in a way they don't for a stdio server: the log
	// is a file read after the fact, usually to explain why a client's dial
	// timed out.
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("engram-daemon: ")

	// The signals a `daemon stop`, a retirement after an upgrade, and an
	// export all send. Cancelling the context closes the listener and lets
	// attached sessions end, so the socket and pid file are cleaned up.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	newMCP := func() (*mcpserver.MCPServer, func(), error) {
		st, cleanup, err := store.NewBoltStore(cfg)
		if err != nil {
			return nil, nil, err
		}
		// RegisterTools registers a fixed set of tools once at startup and
		// never changes it afterward; declaring listChanged: false here
		// (instead of leaving it to mcp-go's implicit listChanged: true
		// default) avoids misleading clients into subscribing to
		// tool-list-change notifications that will never come.
		s := mcpserver.NewMCPServer("engram", version, mcpserver.WithToolCapabilities(false))
		server.RegisterTools(s, server.NewApp(st, cfg.DefaultLimit, cfg.MaxContentChars))
		return s, cleanup, nil
	}

	if err := daemon.Serve(ctx, cfg, version, newMCP, daemon.DefaultIdleTimeout); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func runDaemonStop() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram daemon stop: config error: %v\n", err)
		os.Exit(1)
	}
	wasRunning, err := daemon.Stop(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram daemon stop: %v\n", err)
		os.Exit(1)
	}
	if !wasRunning {
		fmt.Println("No engram daemon was running.")
		return
	}
	fmt.Println("Stopped the engram daemon.")
}

func runDaemonStatus() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram daemon status: config error: %v\n", err)
		os.Exit(1)
	}
	st := daemon.Query(cfg)
	if !st.Running {
		fmt.Printf("No engram daemon is running for %s.\n", cfg.DB.Path)
		return
	}
	fmt.Printf("engram daemon %s running on %s", st.Version, st.Socket)
	if st.PID != 0 {
		fmt.Printf(" (pid %d)", st.PID)
	}
	fmt.Println()
}

// stopDaemonForDirectAccess shuts down any running daemon so a CLI command can
// open the database itself.
//
// export, import and reembed all go straight to the bolt file, which the
// daemon holds locked. Taking it from the daemon is only reasonable because
// spawning is automatic: the next tool call starts a fresh one. The cost is
// that a tool call racing one of these commands errors, which is the right
// trade for rare manual maintenance and far cheaper than a quiesce protocol,
// or than exposing reembed as a tool the model could call.
func stopDaemonForDirectAccess(cmd string, cfg config.Config) {
	if err := daemon.StopIfRunning(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "engram %s: could not stop the running daemon: %v\n", cmd, err)
		os.Exit(1)
	}
}
