package store

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/AWDDude/engRam/internal/config"
)

// Import reads memories from r in the CSV format produced by Export and adds
// them to the store, preserving IDs, tags, and created/updated timestamps. The
// pre-updated_at column order is also accepted, so a backup from an older
// engram still restores. A blank ID is assigned a new one. Returns the number
// of memories imported.
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
	cols, err := matchCSVHeader(header)
	if err != nil {
		return 0, err
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
		if len(record) != cols {
			return n, fmt.Errorf("record %d: expected %d fields, got %d", n+1, cols, len(record))
		}

		id, title, content, tagsField, linkedIDsField, createdAt := record[0], record[1], record[2], record[3], record[4], record[5]
		// A legacy row has no updated_at column; addMemory defaults the empty
		// value to createdAt, the same as a legacy record already in the db.
		var updatedAt string
		if cols == len(csvHeader) {
			updatedAt = record[6]
		}
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
		if err := cs.addMemory(ctx, id, title, content, tags, linkedIDs, createdAt, updatedAt); err != nil {
			return n, fmt.Errorf("importing memory %s: %w", id, err)
		}
		n++
	}
	return n, nil
}

// matchCSVHeader checks header against the current and legacy column orders,
// returning the number of fields each record must then carry.
func matchCSVHeader(header []string) (int, error) {
	for _, want := range [][]string{csvHeader, legacyCSVHeader} {
		if slices.Equal(header, want) {
			return len(want), nil
		}
	}
	return 0, fmt.Errorf("unexpected csv header: %v", header)
}
