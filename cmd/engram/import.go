package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/store"
)

// importRecord is the format exported by the Python LanceDB migration script.
// Tags come in as a JSON-encoded string (e.g. `["k8s","infra"]`) matching how
// they were stored in LanceDB metadata.
type importRecord struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Type      string `json:"type"`
	Tags      string `json:"tags"`
	CreatedAt string `json:"created_at"`
}

func runImport(cfg config.Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading export file: %w", err)
	}

	var records []importRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parsing export file: %w", err)
	}

	st, cleanup, err := store.NewChromemStore(cfg)
	if err != nil {
		return fmt.Errorf("creating store: %w", err)
	}
	defer cleanup()

	ctx := context.Background()
	var imported, failed int

	for _, rec := range records {
		var tags []string
		_ = json.Unmarshal([]byte(rec.Tags), &tags)

		err := st.Import(ctx, store.Memory{
			ID:        rec.ID,
			Content:   rec.Content,
			Type:      rec.Type,
			Tags:      tags,
			CreatedAt: rec.CreatedAt,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", rec.ID, err)
			failed++
			continue
		}
		imported++
		fmt.Printf("  [%d/%d] imported %s\n", imported+failed, len(records), rec.ID)
	}

	fmt.Printf("Done: %d imported, %d failed.\n", imported, failed)
	if failed > 0 {
		return fmt.Errorf("%d memories failed to import", failed)
	}
	return nil
}
