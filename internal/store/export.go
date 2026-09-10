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
	st, cleanup, err := NewChromemStore(cfg)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return exportStore(ctx, st, w)
}

func exportStore(_ context.Context, st Store, w io.Writer) (int, error) {
	cs, ok := st.(*chromemStore)
	if !ok {
		return 0, fmt.Errorf("export requires a chromem-backed store")
	}
	memories := cs.meta.list("", 0)

	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return 0, fmt.Errorf("writing header: %w", err)
	}
	for _, mem := range memories {
		record := []string{mem.ID, mem.Title, mem.Content, strings.Join(mem.Tags, ";"), strings.Join(mem.LinkedIDs, ";"), mem.CreatedAt}
		if err := cw.Write(record); err != nil {
			return 0, fmt.Errorf("writing memory %s: %w", mem.ID, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, fmt.Errorf("flushing csv: %w", err)
	}
	return len(memories), nil
}
