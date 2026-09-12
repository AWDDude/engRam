package store

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/AWDDude/engRam/internal/config"
)

// Import reads memories from r in the CSV format produced by Export and adds
// them to the store, preserving IDs, tags, and creation timestamps. A blank
// ID is assigned a new one. Returns the number of memories imported.
func Import(ctx context.Context, cfg config.Config, r io.Reader) (int, error) {
	st, cleanup, err := NewBoltStore(cfg)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return importStore(ctx, st, r)
}

func importStore(ctx context.Context, st Store, r io.Reader) (int, error) {
	cs, ok := st.(rawAdder)
	if !ok {
		return 0, fmt.Errorf("import requires a store supporting raw adds")
	}

	cr := csv.NewReader(r)
	header, err := cr.Read()
	if err != nil {
		return 0, fmt.Errorf("reading header: %w", err)
	}
	if len(header) != len(csvHeader) {
		return 0, fmt.Errorf("unexpected csv header: %v", header)
	}
	for i, col := range csvHeader {
		if header[i] != col {
			return 0, fmt.Errorf("unexpected csv header: %v", header)
		}
	}

	var n int
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, fmt.Errorf("reading record %d: %w", n+1, err)
		}
		if len(record) != len(csvHeader) {
			return n, fmt.Errorf("record %d: expected %d fields, got %d", n+1, len(csvHeader), len(record))
		}

		id, title, content, tagsField, linkedIDsField, createdAt := record[0], record[1], record[2], record[3], record[4], record[5]
		if id == "" {
			id = uuid.NewString()
		}
		var tags []string
		if tagsField != "" {
			tags = strings.Split(tagsField, ";")
		}
		var linkedIDs []string
		if linkedIDsField != "" {
			linkedIDs = strings.Split(linkedIDsField, ";")
		}

		// Raw path: linked_ids are restored verbatim, with no existence
		// validation or bidirectional sync. CSV row order doesn't guarantee a
		// link's target precedes it, and the export that produced this file
		// was already internally consistent — see addMemory's doc comment.
		if err := cs.addMemory(ctx, id, title, content, tags, linkedIDs, createdAt); err != nil {
			return n, fmt.Errorf("importing memory %s: %w", id, err)
		}
		n++
	}
	return n, nil
}
