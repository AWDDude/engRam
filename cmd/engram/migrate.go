package main

import (
	"context"
	"fmt"
	"os"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/store"
)

func runMigrate() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram migrate: config error: %v\n", err)
		os.Exit(1)
	}

	n, err := store.Migrate(context.Background(), cfg, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram migrate: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Migration complete: %d memories migrated.\n", n)
}
