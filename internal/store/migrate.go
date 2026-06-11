package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	chromem "github.com/philippgille/chromem-go"

	"github.com/AWDDude/engRam/internal/config"
)

// Migrate re-embeds all memories from the current model's collection into the
// new model specified in cfg, updates db_meta.json, then removes the old
// collection directory. Progress is written to w. Returns the number of
// memories migrated.
func Migrate(ctx context.Context, cfg config.Config, w io.Writer) (int, error) {
	embFn, cleanup, err := newEmbeddingFunc(ctx, cfg.Model.Path, cfg.Model.EmbeddingModel)
	if err != nil {
		return 0, fmt.Errorf("initializing new model: %w", err)
	}
	defer cleanup()
	return migrateWithEmb(ctx, cfg, embFn, w)
}

// migrateWithEmb performs the migration with an injectable embedding function.
// Exists to allow tests to run without a live model download.
func migrateWithEmb(ctx context.Context, cfg config.Config, embFn chromem.EmbeddingFunc, w io.Writer) (int, error) {
	if err := os.MkdirAll(cfg.DB.Path, 0700); err != nil {
		return 0, fmt.Errorf("creating db dir: %w", err)
	}

	meta, err := loadDBMeta(cfg.DB.Path)
	if err != nil {
		return 0, err
	}
	if meta.ActiveModel == "" {
		return 0, fmt.Errorf("no existing database found at %s", cfg.DB.Path)
	}
	if meta.ActiveModel == cfg.Model.EmbeddingModel {
		return 0, fmt.Errorf("already using model %q, no migration needed", cfg.Model.EmbeddingModel)
	}

	oldModel := meta.ActiveModel
	oldCollPath := modelCollectionPath(cfg.DB.Path, oldModel)

	oldIdx, err := newMetaIndex(filepath.Join(oldCollPath, "meta.json"))
	if err != nil {
		return 0, fmt.Errorf("loading old memories: %w", err)
	}
	memories := oldIdx.list("", "", 0)

	fmt.Fprintf(w, "Migrating %d memories from %q to %q...\n", len(memories), oldModel, cfg.Model.EmbeddingModel)

	newStore, err := newChromemStoreWithEmb(cfg, embFn)
	if err != nil {
		return 0, fmt.Errorf("creating new store: %w", err)
	}

	for i, mem := range memories {
		if err := newStore.(*chromemStore).addMemory(ctx, mem.ID, mem.Content, mem.Type, mem.Tags, mem.CreatedAt); err != nil {
			return i, fmt.Errorf("migrating memory %s: %w", mem.ID, err)
		}
		fmt.Fprintf(w, "  [%d/%d] migrated %s\n", i+1, len(memories), mem.ID)
	}

	if err := saveDBMeta(cfg.DB.Path, dbMeta{ActiveModel: cfg.Model.EmbeddingModel}); err != nil {
		return len(memories), fmt.Errorf("updating db meta: %w", err)
	}

	if err := os.RemoveAll(oldCollPath); err != nil {
		return len(memories), fmt.Errorf("removing old collection: %w", err)
	}

	return len(memories), nil
}
