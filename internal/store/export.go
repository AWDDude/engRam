package store

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/AWDDude/engRam/internal/config"
)

// csvHeader is the column order used by Export and validated by Import.
var csvHeader = []string{"id", "title", "content", "tags", "linked_ids", "created_at"}

// Export writes all memories to w as CSV (columns: id, title, content, tags,
// linked_ids, created_at; tags and linked_ids are joined with ";"). Returns
// the number of memories exported.
func Export(ctx context.Context, cfg config.Config, w io.Writer) (int, error) {
	st, cleanup, err := NewBoltStore(cfg)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return exportStore(ctx, st, w)
}

func exportStore(ctx context.Context, st Store, w io.Writer) (int, error) {
	// Search with an empty query does a tag-only scan (see Store.Search),
	// which with an empty tag filter and unlimited limit lists every memory
	// by id — going through the interface keeps Export usable against any
	// Store implementation rather than only boltStore.
	results, err := st.Search(ctx, "", "", 0)
	if err != nil {
		return 0, fmt.Errorf("listing memories: %w", err)
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return 0, fmt.Errorf("writing header: %w", err)
	}
	for _, r := range results {
		mem, err := st.GetByID(ctx, r.ID)
		if err != nil {
			return 0, fmt.Errorf("retrieving memory %s: %w", r.ID, err)
		}
		record := []string{mem.ID, mem.Title, mem.Content, strings.Join(mem.Tags, ";"), strings.Join(mem.LinkedIDs, ";"), mem.CreatedAt}
		if err := cw.Write(record); err != nil {
			return 0, fmt.Errorf("writing memory %s: %w", mem.ID, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, fmt.Errorf("flushing csv: %w", err)
	}
	return len(results), nil
}
