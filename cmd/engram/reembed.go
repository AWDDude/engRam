package main

import (
	"context"
	"fmt"
	"os"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/store"
)

func runReembed() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram reembed: config error: %v\n", err)
		os.Exit(1)
	}

	n, err := store.Reembed(context.Background(), cfg, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram reembed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Reembedding complete: %d memories re-embedded.\n", n)
}
