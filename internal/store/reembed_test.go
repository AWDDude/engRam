package store

import (
	"context"
	"io"
	"os"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/AWDDude/engRam/internal/config"
)

func TestReembedWithEmb_NoExistingDB(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}

	_, err := reembedWithEmb(context.Background(), cfg, chromem.EmbeddingFunc(testEmbedFunc), io.Discard)
	if err == nil {
		t.Error("expected error when no db_meta.json found")
	}
}

func TestReembedWithEmb_SameModel(t *testing.T) {
	dir := t.TempDir()
	if err := saveDBMeta(dir, dbMeta{ActiveModel: "same-model"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "same-model"},
	}

	_, err := reembedWithEmb(context.Background(), cfg, chromem.EmbeddingFunc(testEmbedFunc), io.Discard)
	if err == nil {
		t.Error("expected error when model is already the same")
	}
}

func TestReembedWithEmb_HappyPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Populate the old model's collection with two memories
	oldStore := testNewStoreWithModel(t, dir, "old-model")
	id1, err := oldStore.Add(ctx, "memory one", "fact", []string{"tag1"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := oldStore.Add(ctx, "memory two", "preference", []string{"tag2"})
	if err != nil {
		t.Fatal(err)
	}

	if err := saveDBMeta(dir, dbMeta{ActiveModel: "old-model"}); err != nil {
		t.Fatal(err)
	}

	// Run re-embedding
	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}
	n, err := reembedWithEmb(ctx, cfg, chromem.EmbeddingFunc(testEmbedFunc), io.Discard)
	if err != nil {
		t.Fatalf("reembedWithEmb: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 memories re-embedded, got %d", n)
	}

	// db_meta.json points to new model
	meta, err := loadDBMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ActiveModel != "new-model" {
		t.Errorf("expected active model 'new-model', got %q", meta.ActiveModel)
	}

	// Old collection directory is removed
	oldCollPath := modelCollectionPath(dir, "old-model")
	if _, err := os.Stat(oldCollPath); !os.IsNotExist(err) {
		t.Error("expected old collection directory to be removed")
	}

	// New collection has both memories with original IDs preserved
	newStore := testNewStoreWithModel(t, dir, "new-model")
	for _, id := range []string{id1, id2} {
		if _, err := newStore.GetByID(ctx, id); err != nil {
			t.Errorf("memory %s not found in new store: %v", id, err)
		}
	}
	mems, err := newStore.List(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Errorf("expected 2 memories in new store, got %d", len(mems))
	}
}

func TestReembedWithEmb_PreservesMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	oldStore := testNewStoreWithModel(t, dir, "old-model")
	id, err := oldStore.Add(ctx, "tagged content", "task", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := oldStore.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := saveDBMeta(dir, dbMeta{ActiveModel: "old-model"}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}
	if _, err := reembedWithEmb(ctx, cfg, chromem.EmbeddingFunc(testEmbedFunc), io.Discard); err != nil {
		t.Fatal(err)
	}

	newStore := testNewStoreWithModel(t, dir, "new-model")
	migrated, err := newStore.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after migration: %v", err)
	}
	if migrated.Content != original.Content {
		t.Errorf("content mismatch: got %q, want %q", migrated.Content, original.Content)
	}
	if migrated.Type != original.Type {
		t.Errorf("type mismatch: got %q, want %q", migrated.Type, original.Type)
	}
	if migrated.CreatedAt != original.CreatedAt {
		t.Errorf("created_at mismatch: got %q, want %q", migrated.CreatedAt, original.CreatedAt)
	}
	if len(migrated.Tags) != len(original.Tags) {
		t.Errorf("tags mismatch: got %v, want %v", migrated.Tags, original.Tags)
	}
}
