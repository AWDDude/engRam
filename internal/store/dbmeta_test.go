package store

import (
	"errors"
	"strings"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/AWDDude/engRam/internal/config"
)

func TestModelCollectionPath_Sanitizes(t *testing.T) {
	got := modelCollectionPath("/db", "KnightsAnalytics/all-MiniLM-L6-v2")
	want := "/db/KnightsAnalytics_all-MiniLM-L6-v2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestModelCollectionPath_NoSlash(t *testing.T) {
	got := modelCollectionPath("/db", "simple-model")
	want := "/db/simple-model"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSaveAndLoadDBMeta(t *testing.T) {
	dir := t.TempDir()
	m := dbMeta{ActiveModel: "KnightsAnalytics/all-MiniLM-L6-v2"}

	if err := saveDBMeta(dir, m); err != nil {
		t.Fatalf("saveDBMeta: %v", err)
	}
	got, err := loadDBMeta(dir)
	if err != nil {
		t.Fatalf("loadDBMeta: %v", err)
	}
	if got.ActiveModel != m.ActiveModel {
		t.Errorf("got %q, want %q", got.ActiveModel, m.ActiveModel)
	}
}

func TestLoadDBMeta_MissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := loadDBMeta(dir)
	if err != nil {
		t.Fatalf("expected no error for missing db_meta.json, got %v", err)
	}
	if m.ActiveModel != "" {
		t.Errorf("expected empty ActiveModel for missing file, got %q", m.ActiveModel)
	}
}

func TestModelChangedError_Message(t *testing.T) {
	err := &ModelChangedError{OldModel: "old/model", NewModel: "new/model"}
	msg := err.Error()
	if !strings.Contains(msg, "old/model") {
		t.Errorf("expected error to contain old model name, got: %s", msg)
	}
	if !strings.Contains(msg, "new/model") {
		t.Errorf("expected error to contain new model name, got: %s", msg)
	}
	if !strings.Contains(msg, "engram migrate") {
		t.Errorf("expected error to mention 'engram migrate', got: %s", msg)
	}
}


func TestNewChromemStore_ModelMismatch(t *testing.T) {
	dir := t.TempDir()

	if err := saveDBMeta(dir, dbMeta{ActiveModel: "model-a"}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "model-b"},
	}

	_, _, err := NewChromemStore(cfg)
	if err == nil {
		t.Fatal("expected ModelChangedError, got nil")
	}

	var modelErr *ModelChangedError
	if !errors.As(err, &modelErr) {
		t.Fatalf("expected ModelChangedError, got %T: %v", err, err)
	}
	if modelErr.OldModel != "model-a" {
		t.Errorf("expected OldModel 'model-a', got %q", modelErr.OldModel)
	}
	if modelErr.NewModel != "model-b" {
		t.Errorf("expected NewModel 'model-b', got %q", modelErr.NewModel)
	}
}

func TestNewChromemStore_FirstRun_WritesDBMeta(t *testing.T) {
	dir := t.TempDir()

	cfg := config.Config{
		DB:    config.DBConfig{Path: dir},
		Model: config.ModelConfig{EmbeddingModel: "test-model"},
	}

	// NewChromemStore will fail at the embedding step (no model downloaded),
	// but db_meta.json is written before that — verify the side effect.
	_, _, _ = NewChromemStore(cfg)

	meta, err := loadDBMeta(dir)
	if err != nil {
		t.Fatalf("loadDBMeta after first run: %v", err)
	}
	if meta.ActiveModel != "test-model" {
		t.Errorf("expected db_meta.json to record 'test-model', got %q", meta.ActiveModel)
	}
}


// Ensure ModelChangedError satisfies the error interface for errors.As.
func TestModelChangedError_As(t *testing.T) {
	original := &ModelChangedError{OldModel: "a", NewModel: "b"}
	wrapped := errors.New("wrapped: " + original.Error())
	_ = wrapped // just confirm it compiles; errors.As is tested in TestNewChromemStore_ModelMismatch

	var target *ModelChangedError
	if !errors.As(original, &target) {
		t.Error("errors.As should match ModelChangedError directly")
	}
}

// testNewStoreWithModel creates a test store using a specific model name.
func testNewStoreWithModel(t *testing.T, dbPath, model string) Store {
	t.Helper()
	s, err := newChromemStoreWithEmb(
		config.Config{
			DB:    config.DBConfig{Path: dbPath},
			Model: config.ModelConfig{EmbeddingModel: model},
		},
		chromem.EmbeddingFunc(testEmbedFunc),
	)
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	return s
}
