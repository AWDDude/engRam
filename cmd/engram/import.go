package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/store"
)

func runImport() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	file := fs.String("f", "", "CSV file to import memories from (required)")
	fs.Parse(os.Args[2:])

	if *file == "" {
		fmt.Fprintln(os.Stderr, "engram import: -f flag is required")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram import: config error: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram import: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	n, err := store.Import(context.Background(), cfg, f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram import: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Imported %d memories from %s.\n", n, *file)
}
