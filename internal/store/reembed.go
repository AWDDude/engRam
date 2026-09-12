package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/AWDDude/engRam/internal/config"
)

// Reembed re-embeds all memories from the current model's database into the
// new model specified in cfg, updates db_meta.json, then removes the old
// database file. Progress is written to w. Returns the number of memories
// re-embedded.
func Reembed(ctx context.Context, cfg config.Config, w io.Writer) (int, error) {
	embFn, cleanup, err := newEmbeddingFunc(ctx, cfg.Model.Path, cfg.Model.EmbeddingModel)
	if err != nil {
		return 0, fmt.Errorf("initializing new model: %w", err)
	}
	defer cleanup()
	return reembedWithEmb(ctx, cfg, embFn, w)
}

// reembedWithEmb performs the re-embedding with an injectable embedding function.
// Exists to allow tests to run without a live model download.
func reembedWithEmb(ctx context.Context, cfg config.Config, embFn EmbeddingFunc, w io.Writer) (int, error) {
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
		return 0, fmt.Errorf("already using model %q, no re-embedding needed", cfg.Model.EmbeddingModel)
	}

	oldModel := meta.ActiveModel
	oldDBPath := modelDBPath(cfg.DB.Path, oldModel)

	memories, err := readAllMemories(oldDBPath)
	if err != nil {
		return 0, fmt.Errorf("loading old memories: %w", err)
	}

	fmt.Fprintf(w, "Re-embedding %d memories from %q to %q...\n", len(memories), oldModel, cfg.Model.EmbeddingModel)

	newStore, err := newBoltStoreWithEmb(cfg, embFn)
	if err != nil {
		return 0, fmt.Errorf("creating new store: %w", err)
	}
	defer func() { _ = newStore.db.Close() }()

	for i, mem := range memories {
		if err := newStore.addMemory(ctx, mem.ID, mem.Title, mem.Content, mem.Tags, mem.LinkedIDs, mem.CreatedAt); err != nil {
			return i, fmt.Errorf("re-embedding memory %s: %w", mem.ID, err)
		}
		fmt.Fprintf(w, "  [%d/%d] re-embedded %s\n", i+1, len(memories), mem.ID)
	}

	if err := saveDBMeta(cfg.DB.Path, dbMeta{ActiveModel: cfg.Model.EmbeddingModel}); err != nil {
		return len(memories), fmt.Errorf("updating db meta: %w", err)
	}

	if err := os.Remove(oldDBPath); err != nil && !os.IsNotExist(err) {
		return len(memories), fmt.Errorf("removing old database: %w", err)
	}

	return len(memories), nil
}

// readAllMemories opens a bolt database read-only and returns every memory
// record in it. Vectors are deliberately not read: the caller is re-embedding
// with a different model, so the old ones are useless.
func readAllMemories(path string) ([]Memory, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, annotateLockTimeout(err))
	}
	defer func() { _ = db.Close() }()

	var memories []Memory
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMemories)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var mem Memory
			if err := json.Unmarshal(v, &mem); err != nil {
				return fmt.Errorf("parsing memory %s: %w", k, err)
			}
			memories = append(memories, mem)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return memories, nil
}
