package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/store"
)

func runExport() {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	file := fs.String("f", "", "CSV file to export memories to (required)")
	fs.Parse(os.Args[2:])

	if *file == "" {
		fmt.Fprintln(os.Stderr, "engram export: -f flag is required")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram export: config error: %v\n", err)
		os.Exit(1)
	}

	stopDaemonForDirectAccess("export", cfg)

	f, err := os.Create(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram export: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	n, err := store.Export(context.Background(), cfg, f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram export: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Exported %d memories to %s.\n", n, *file)
}
