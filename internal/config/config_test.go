package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_AutoCreate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ENGRAM_CONFIG_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path == "" {
		t.Error("expected non-empty default Model.Path")
	}

	createdPath := filepath.Join(dir, "engram", "config.json")
	data, err := os.ReadFile(createdPath)
	if err != nil {
		t.Fatalf("expected config file to be created at %s: %v", createdPath, err)
	}
	var written Config
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("created config is not valid JSON: %v", err)
	}
	if written.Model.EmbeddingModel != cfg.Model.EmbeddingModel {
		t.Errorf("written embedding model %q does not match default %q", written.Model.EmbeddingModel, cfg.Model.EmbeddingModel)
	}
}

func TestLoad_AutoCreate_ViaEnvPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom", "config.json")
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected config file to be created at %s: %v", path, err)
	}
	var written Config
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("created config is not valid JSON: %v", err)
	}
	if written.Model.EmbeddingModel != cfg.Model.EmbeddingModel {
		t.Errorf("written embedding model %q does not match default %q", written.Model.EmbeddingModel, cfg.Model.EmbeddingModel)
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":             map[string]string{"path": "/custom/models", "embedding_model": "custom/model"},
		"db":                map[string]string{"path": "/custom/db"},
		"default_min_score": 0.5,
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

func TestDefault_XDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg/data")
	cfg := Default()
	if !strings.HasPrefix(cfg.Model.Path, "/custom/xdg/data") {
		t.Errorf("XDG_DATA_HOME not honored for model path, got: %s", cfg.Model.Path)
	}
	if !strings.HasPrefix(cfg.DB.Path, "/custom/xdg/data") {
		t.Errorf("XDG_DATA_HOME not honored for db path, got: %s", cfg.DB.Path)
	}
}

func TestLoad_XDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ENGRAM_CONFIG_PATH", "")

	cfgDir := filepath.Join(dir, "engram")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"model":             map[string]string{"path": "/xdg/config/models", "embedding_model": "custom/model"},
		"db":                map[string]string{"path": "/xdg/config/db"},
		"default_min_score": 0.5,
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path != "/xdg/config/models" {
		t.Errorf("XDG_CONFIG_HOME not honored for config lookup, got model path: %s", cfg.Model.Path)
	}
}

func TestLoad_MissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/custom/models"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing fields, got nil")
	}
	for _, field := range []string{"model.embedding_model", "db.path", "search.default_min_score"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("expected error to mention %q, got: %v", field, err)
		}
	}
}

func TestLoad_DefaultMinScore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":             map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":                map[string]string{"path": "/db"},
		"default_min_score": 0.7,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultMinScore != 0.7 {
		t.Errorf("expected default_min_score 0.7, got %v", cfg.DefaultMinScore)
	}
}

func TestLoad_DefaultMinScore_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":    map[string]string{"path": "/db"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing default_min_score, got nil")
	}
	if !strings.Contains(err.Error(), "search.default_min_score") {
		t.Errorf("expected error to mention search.default_min_score, got: %v", err)
	}
}

func TestLoad_DefaultMinScore_OutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":             map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":                map[string]string{"path": "/db"},
		"default_min_score": 1.5,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for out-of-range default_min_score, got nil")
	}
	if !strings.Contains(err.Error(), "search.default_min_score") {
		t.Errorf("expected error to mention search.default_min_score, got: %v", err)
	}
}
