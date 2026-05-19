package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Model.Path == "" {
		t.Error("expected non-empty default Model.Path")
	}
	if cfg.Model.EmbeddingModel == "" {
		t.Error("expected non-empty default Model.EmbeddingModel")
	}
	if cfg.DB.Path == "" {
		t.Error("expected non-empty default DB.Path")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Setenv("ENGRAM_CONFIG_PATH", "/nonexistent/path/config.json")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error for missing config file, got %v", err)
	}
	if cfg.Model.Path == "" {
		t.Error("expected non-empty default Model.Path")
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/custom/models", "embedding_model": "custom/model"},
		"db":    map[string]string{"path": "/custom/db"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path != "/custom/models" {
		t.Errorf("expected custom model path, got %s", cfg.Model.Path)
	}
	if cfg.Model.EmbeddingModel != "custom/model" {
		t.Errorf("expected custom embedding model, got %s", cfg.Model.EmbeddingModel)
	}
	if cfg.DB.Path != "/custom/db" {
		t.Errorf("expected custom db path, got %s", cfg.DB.Path)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestLoad_PartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/custom/models"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path != "/custom/models" {
		t.Errorf("expected custom model path, got %s", cfg.Model.Path)
	}
	if cfg.Model.EmbeddingModel == "" {
		t.Error("EmbeddingModel should keep its default when not overridden")
	}
	if cfg.DB.Path == "" {
		t.Error("DB.Path should keep its default when db section is omitted")
	}
}
