package store

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/AWDDude/engRam/internal/config"
)

func TestReembedWithEmb_NoExistingDB(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}

	_, err := reembedWithEmb(context.Background(), cfg, EmbeddingFunc(testEmbedFunc), io.Discard)
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

	_, err := reembedWithEmb(context.Background(), cfg, EmbeddingFunc(testEmbedFunc), io.Discard)
	if err == nil {
		t.Error("expected error when model is already the same")
	}
}

func TestReembedWithEmb_HappyPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Populate the old model's collection with two linked memories
	oldStore := testNewStoreWithModel(t, dir, "old-model")
	id1, err := oldStore.Add(ctx, "Memory one", "memory one", []string{"tag1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := oldStore.Add(ctx, "Memory two", "memory two", []string{"tag2"}, []string{id1})
	if err != nil {
		t.Fatal(err)
	}

	if err := saveDBMeta(dir, dbMeta{ActiveModel: "old-model"}); err != nil {
		t.Fatal(err)
	}

	// Release the exclusive bolt file lock: reembed reopens this database,
	// and in production it runs as its own CLI invocation with nothing else
	// holding the file.
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}

	// Run re-embedding
	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}
	n, err := reembedWithEmb(ctx, cfg, EmbeddingFunc(testEmbedFunc), io.Discard)
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

	// Old model's database file is removed
	oldDBPath := modelDBPath(dir, "old-model")
	if _, err := os.Stat(oldDBPath); !os.IsNotExist(err) {
		t.Error("expected old model's database file to be removed")
	}

	// New collection has both memories with original IDs preserved
	newStore := testNewStoreWithModel(t, dir, "new-model")
	for _, id := range []string{id1, id2} {
		if _, err := newStore.GetByID(ctx, id); err != nil {
			t.Errorf("memory %s not found in new store: %v", id, err)
		}
	}
	results, err := newStore.Search(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 memories in new store, got %d", len(results))
	}

	// Links preserved without re-validation across the reembed.
	migrated2, err := newStore.GetByID(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(migrated2.LinkedIDs, id1) {
		t.Errorf("expected memory two's link to memory one preserved, got %v", migrated2.LinkedIDs)
	}
}

func TestReembedWithEmb_PreservesMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	oldStore := testNewStoreWithModel(t, dir, "old-model")
	id, err := oldStore.Add(ctx, "Tagged title", "tagged content", []string{"a", "b"}, nil)
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

	// Release the exclusive bolt file lock: reembed reopens this database,
	// and in production it runs as its own CLI invocation with nothing else
	// holding the file.
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "new-model"},
	}
	if _, err := reembedWithEmb(ctx, cfg, EmbeddingFunc(testEmbedFunc), io.Discard); err != nil {
		t.Fatal(err)
	}

	newStore := testNewStoreWithModel(t, dir, "new-model")
	migrated, err := newStore.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after migration: %v", err)
	}
	if migrated.Title != original.Title {
		t.Errorf("title mismatch: got %q, want %q", migrated.Title, original.Title)
	}
	if migrated.Content != original.Content {
		t.Errorf("content mismatch: got %q, want %q", migrated.Content, original.Content)
	}
	if migrated.CreatedAt != original.CreatedAt {
		t.Errorf("created_at mismatch: got %q, want %q", migrated.CreatedAt, original.CreatedAt)
	}
	if len(migrated.Tags) != len(original.Tags) {
		t.Errorf("tags mismatch: got %v, want %v", migrated.Tags, original.Tags)
	}
}
