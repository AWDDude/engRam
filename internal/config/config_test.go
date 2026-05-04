package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.ModelDir == "" {
		t.Error("expected non-empty default ModelDir")
	}
	if cfg.DBPath == "" {
		t.Error("expected non-empty default DBPath")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Setenv("ENGRAM_CONFIG_PATH", "/nonexistent/path/config.json")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error for missing config file, got %v", err)
	}
	if cfg.ModelDir == "" {
		t.Error("expected non-empty default ModelDir")
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(Config{
		ModelDir: "/custom/models",
		DBPath:   "/custom/db",
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ModelDir != "/custom/models" {
		t.Errorf("expected custom model dir, got %s", cfg.ModelDir)
	}
	if cfg.DBPath != "/custom/db" {
		t.Errorf("expected custom db path, got %s", cfg.DBPath)
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
	data, _ := json.Marshal(map[string]string{"model_dir": "/custom/models"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ModelDir != "/custom/models" {
		t.Errorf("expected custom model dir, got %s", cfg.ModelDir)
	}
	if cfg.DBPath == "" {
		t.Error("DBPath should keep its default when not overridden")
	}
}
